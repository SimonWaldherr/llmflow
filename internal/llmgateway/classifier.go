package llmgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Classifier struct {
	backends *BackendManager
	client   *http.Client
}

func NewClassifier(backends *BackendManager) *Classifier {
	return &Classifier{backends: backends, client: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Classifier) Classify(ctx context.Context, route RouteSpec, in RouteInput) ([]string, string, error) {
	if route.Classifier == nil {
		return nil, "", nil
	}
	if route.Classifier.Backend == "" {
		return nil, "", nil
	}
	backend, ok := c.backends.Get(route.Classifier.Backend)
	if !ok || !backend.IsHealthy() {
		return nil, "", fmt.Errorf("classifier backend %q unavailable", route.Classifier.Backend)
	}
	labels := make([]string, 0, len(route.Classifier.Routes))
	for label := range route.Classifier.Routes {
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		return nil, "", nil
	}
	prompt := route.Classifier.Prompt
	if prompt == "" {
		prompt = "Classify this prompt into one label: {{labels}}. Return only the label. Prompt: {{prompt}}"
	}
	prompt = strings.ReplaceAll(prompt, "{{labels}}", strings.Join(labels, ", "))
	prompt = strings.ReplaceAll(prompt, "{{prompt}}", in.Prompt)

	label, err := c.queryLabel(ctx, backend, prompt)
	if err != nil {
		return nil, "", err
	}
	if refs, ok := route.Classifier.Routes[strings.ToLower(label)]; ok {
		return refs, strings.ToLower(label), nil
	}
	if refs, ok := route.Classifier.Routes[label]; ok {
		return refs, label, nil
	}
	if refs, ok := route.Classifier.Routes["default"]; ok {
		return refs, "default", nil
	}
	return nil, label, nil
}

func (c *Classifier) queryLabel(ctx context.Context, backend *Backend, prompt string) (string, error) {
	backendType := strings.ToLower(strings.TrimSpace(backend.Spec.Type))
	if backendType == "" {
		backendType = BackendTypeOpenAI
	}
	model := firstNonEmpty(backend.Spec.Models...)

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a routing classifier."},
			{"role": "user", "content": prompt},
		},
		"max_tokens": 16,
	}
	target := joinTargetURL(backend.URL, "/v1/chat/completions")
	switch backendType {
	case BackendTypeOllama:
		payload = map[string]any{
			"model":  model,
			"prompt": prompt,
			"stream": false,
		}
		target = joinTargetURL(backend.URL, "/api/generate")
	case BackendTypeAnthropic:
		payload = map[string]any{
			"model":      model,
			"max_tokens": 16,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
		}
		target = joinTargetURL(backend.URL, "/v1/messages")
	case BackendTypeGemini:
		payload = map[string]any{
			"contents": []map[string]any{
				{
					"role": "user",
					"parts": []map[string]string{
						{"text": prompt},
					},
				},
			},
			"generationConfig": map[string]any{
				"maxOutputTokens": 16,
				"temperature":     0,
			},
		}
		if strings.TrimSpace(model) == "" {
			return "", fmt.Errorf("classifier backend %q requires a model", backend.Spec.Name)
		}
		target = joinTargetURL(backend.URL, "/models/"+model+":generateContent")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range backend.Spec.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("classifier status %d", resp.StatusCode)
	}
	switch backendType {
	case BackendTypeOllama:
		var out struct {
			Response string `json:"response"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		return normalizeLabel(out.Response), nil
	case BackendTypeAnthropic:
		var out struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		for _, item := range out.Content {
			if strings.EqualFold(item.Type, "text") && strings.TrimSpace(item.Text) != "" {
				return normalizeLabel(item.Text), nil
			}
		}
		return "", fmt.Errorf("classifier returned no text content")
	case BackendTypeGemini:
		var out struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
			return "", fmt.Errorf("classifier returned no candidates")
		}
		return normalizeLabel(out.Candidates[0].Content.Parts[0].Text), nil
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("classifier returned no choices")
	}
	return normalizeLabel(out.Choices[0].Message.Content), nil
}

func normalizeLabel(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.Trim(s, "\"'`")
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	s = fields[0]
	return strings.TrimSuffix(s, ".")
}

func firstNonEmpty(items ...string) string {
	for _, it := range items {
		if strings.TrimSpace(it) != "" {
			return it
		}
	}
	return ""
}
