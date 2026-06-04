package llmgateway

import (
"bytes"
"context"
"encoding/json"
"errors"
"fmt"
"io"
"net/http"
"net/url"
"strconv"
"strings"
"time"
)

type requestInfo struct {
Model      string
Prompt     string
TokenCount int
Metadata   map[string]string
Body       []byte
}

func parseRequestInfo(path string, body []byte) requestInfo {
out := requestInfo{Body: body, Metadata: map[string]string{"path": path}}
if len(body) == 0 {
return out
}
var payload map[string]any
if err := json.Unmarshal(body, &payload); err != nil {
return out
}
if model, ok := payload["model"].(string); ok {
out.Model = model
out.Metadata["model"] = model
}
prompt := extractPrompt(payload)
out.Prompt = prompt
out.TokenCount = estimateTokenCount(prompt)
out.Metadata["tokens"] = strconv.Itoa(out.TokenCount)
if stream, ok := payload["stream"].(bool); ok {
out.Metadata["stream"] = strconv.FormatBool(stream)
}
return out
}

func estimateTokenCount(s string) int {
s = strings.TrimSpace(s)
if s == "" {
return 0
}
runes := len([]rune(s))
if runes < 4 {
return 1
}
return (runes + 3) / 4
}

func extractPrompt(payload map[string]any) string {
var parts []string
if p, ok := payload["prompt"].(string); ok {
parts = append(parts, p)
}
if in, ok := payload["input"].(string); ok {
parts = append(parts, in)
}
if msgs, ok := payload["messages"].([]any); ok {
for _, raw := range msgs {
m, ok := raw.(map[string]any)
if !ok {
continue
}
switch content := m["content"].(type) {
case string:
parts = append(parts, content)
case []any:
for _, c := range content {
if chunk, ok := c.(map[string]any); ok {
if text, ok := chunk["text"].(string); ok {
parts = append(parts, text)
}
}
}
}
}
}
return strings.TrimSpace(strings.Join(parts, "\n"))
}

func proxyWithFailover(ctx context.Context, client *http.Client, req *http.Request, body []byte, candidates []*Backend) (*http.Response, *Backend, error) {
if len(candidates) == 0 {
return nil, nil, errors.New("no backend candidates")
}
var lastErr error
for _, backend := range candidates {
if !backend.IsHealthy() {
continue
}
resp, err := proxyOnce(ctx, client, req, body, backend)
if err == nil {
backend.MarkSuccess()
return resp, backend, nil
}
backend.MarkFailure(err)
lastErr = err
}
if lastErr == nil {
lastErr = errors.New("all candidates unavailable")
}
return nil, nil, lastErr
}

func proxyOnce(ctx context.Context, client *http.Client, req *http.Request, body []byte, backend *Backend) (*http.Response, error) {
backend.IncActive()
defer backend.DecActive()

proxyBody := remapModel(body, backend.Spec.ModelMap)
target := joinTargetURL(backend.URL, req.URL.Path)
proxyReq, err := http.NewRequestWithContext(ctx, req.Method, target, bytes.NewReader(proxyBody))
if err != nil {
return nil, err
}
proxyReq.URL.RawQuery = req.URL.RawQuery
copyHeaders(proxyReq.Header, req.Header)
for k, v := range backend.Spec.Headers {
proxyReq.Header.Set(k, v)
}
proxyReq.Host = backend.URL.Host
proxyReq.ContentLength = int64(len(proxyBody))
proxyReq.Header.Set("Content-Length", strconv.Itoa(len(proxyBody)))

start := time.Now()
resp, err := client.Do(proxyReq)
if err != nil {
return nil, err
}
if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
resp.Body.Close()
return nil, fmt.Errorf("status %d", resp.StatusCode)
}
_ = start
return resp, nil
}

func remapModel(body []byte, modelMap map[string]string) []byte {
if len(modelMap) == 0 || len(body) == 0 {
return body
}
var payload map[string]any
if err := json.Unmarshal(body, &payload); err != nil {
return body
}
model, _ := payload["model"].(string)
if mapped, ok := modelMap[model]; ok {
payload["model"] = mapped
encoded, err := json.Marshal(payload)
if err == nil {
return encoded
}
}
return body
}

func joinTargetURL(base *url.URL, requestPath string) string {
u := *base
basePath := strings.TrimRight(u.Path, "/")
if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(requestPath, "/v1/") {
u.Path = basePath + strings.TrimPrefix(requestPath, "/v1")
} else if strings.HasSuffix(basePath, "/api") && strings.HasPrefix(requestPath, "/api/") {
u.Path = basePath + strings.TrimPrefix(requestPath, "/api")
} else {
u.Path = strings.TrimRight(basePath, "/") + requestPath
}
if u.Path == "" {
u.Path = "/"
}
return u.String()
}

func copyHeaders(dst, src http.Header) {
for k, values := range src {
if strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Keep-Alive") || strings.EqualFold(k, "Transfer-Encoding") {
continue
}
for _, v := range values {
dst.Add(k, v)
}
}
}

func writeProxyResponse(w http.ResponseWriter, resp *http.Response) error {
defer resp.Body.Close()
for k, values := range resp.Header {
for _, v := range values {
w.Header().Add(k, v)
}
}
w.WriteHeader(resp.StatusCode)
_, err := io.Copy(w, resp.Body)
return err
}
