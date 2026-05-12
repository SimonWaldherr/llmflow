package web

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/xuri/excelize/v2"
)

func TestNormalizeHostBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"localhost:11434":         "http://localhost:11434",
		"http://127.0.0.1:11434":  "http://127.0.0.1:11434",
		"https://example.org/foo": "https://example.org",
		"::invalid::":             "",
	}
	for in, want := range cases {
		if got := normalizeHostBaseURL(in); got != want {
			t.Fatalf("normalizeHostBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeLMStudioBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"localhost:1234":      "http://localhost:1234/v1",
		"http://127.0.0.1:80": "http://127.0.0.1:80/v1",
	}
	for in, want := range cases {
		if got := normalizeLMStudioBaseURL(in); got != want {
			t.Fatalf("normalizeLMStudioBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeModelNames(t *testing.T) {
	in := []string{" llama3", "mistral", "llama3", "", "  mistral:7b "}
	got := normalizeModelNames(in)
	want := []string{"llama3", "mistral", "mistral:7b"}
	if len(got) != len(want) {
		t.Fatalf("len(normalizeModelNames) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeModelNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildDetectCandidatesUsesEnvOverrides(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "192.168.1.20:11434")
	t.Setenv("LLMFLOW_LMSTUDIO_BASE_URL", "192.168.1.21:1234")

	candidates := buildDetectCandidates()

	var foundOllama, foundLMStudio bool
	for _, c := range candidates {
		if c.provider == config.ProviderOllama && c.baseURL == "http://192.168.1.20:11434" {
			foundOllama = true
		}
		if c.provider == config.ProviderLMStudio && c.baseURL == "http://192.168.1.21:1234/v1" {
			foundLMStudio = true
		}
	}
	if !foundOllama {
		t.Fatal("expected ollama candidate from OLLAMA_HOST")
	}
	if !foundLMStudio {
		t.Fatal("expected lmstudio candidate from LLMFLOW_LMSTUDIO_BASE_URL")
	}
}

func TestResolveQuickFormAPIKey(t *testing.T) {
	direct, env := resolveQuickFormAPIKey("sk-test-direct")
	if direct != "sk-test-direct" || env != "" {
		t.Fatalf("expected direct key, got direct=%q env=%q", direct, env)
	}

	direct, env = resolveQuickFormAPIKey("OPENAI_API_KEY")
	if direct != "" || env != "OPENAI_API_KEY" {
		t.Fatalf("expected env var name, got direct=%q env=%q", direct, env)
	}

	direct, env = resolveQuickFormAPIKey("   ")
	if direct != "" || env != "" {
		t.Fatalf("expected empty key config, got direct=%q env=%q", direct, env)
	}
}

func TestResolveSuggestTimeout_Default(t *testing.T) {
	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "")

	got, err := resolveSuggestTimeout("")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 30*time.Second {
		t.Fatalf("expected 30s default, got %s", got)
	}
}

func TestBuildSuggestUserMessage_DoesNotWrapConfigInMarkdown(t *testing.T) {
	got := buildSuggestUserMessage("Classify products", "api:\n  model: gpt-4o-mini")
	if !strings.Contains(got, "Current config YAML for reference only") {
		t.Fatalf("expected config guidance in message, got %q", got)
	}
	if strings.Contains(got, "```") {
		t.Fatalf("did not expect markdown fences in message: %q", got)
	}
}

func TestResolveSuggestTimeout_UsesRequestOverride(t *testing.T) {
	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "90s")

	got, err := resolveSuggestTimeout("5m")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 5*time.Minute {
		t.Fatalf("expected 5m from request override, got %s", got)
	}
}

func TestResolveSuggestTimeout_ClampsAndValidates(t *testing.T) {
	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "1s")

	got, err := resolveSuggestTimeout("")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 10*time.Second {
		t.Fatalf("expected lower clamp to 10s, got %s", got)
	}

	got, err = resolveSuggestTimeout("20m")
	if err != nil {
		t.Fatalf("resolveSuggestTimeout returned error: %v", err)
	}
	if got != 10*time.Minute {
		t.Fatalf("expected upper clamp to 10m, got %s", got)
	}

	if _, err := resolveSuggestTimeout("invalid"); err == nil {
		t.Fatal("expected invalid request timeout to fail")
	}

	t.Setenv("LLMFLOW_WEB_SUGGEST_TIMEOUT", "nope")
	if _, err := resolveSuggestTimeout(""); err == nil {
		t.Fatal("expected invalid env timeout to fail")
	}
}

func TestParseConfigAcceptsJSON(t *testing.T) {
	jsonConfig := `{
		"api": {"model": "gpt-test", "api_key": "sk-test-direct"},
		"prompt": {"input_template": "{{ .record }}"},
		"input": {"type": "csv"},
		"output": {"type": "jsonl"}
	}`

	cfg, err := parseConfig(jsonConfig)
	if err != nil {
		t.Fatalf("parseConfig rejected json: %v", err)
	}
	if cfg.API.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", cfg.API.Model)
	}
	if cfg.API.APIKeyDirect != "sk-test-direct" {
		t.Fatalf("api key direct = %q, want direct key", cfg.API.APIKeyDirect)
	}
}

func TestHandlePreflightReturnsSummaryAndWarnings(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "missing.csv")
	outputPath := filepath.Join(root, "out.jsonl")
	cfg := `api:
  provider: ollama
  model: llama3
prompt:
  input_template: "{{ toPrettyJSON .record }}"
input:
  type: csv
  path: ` + inputPath + `
output:
  type: jsonl
  path: ` + outputPath + `
processing:
  include_input_in_output: true
  response_field: result
  response_format: json
  workers: 12
`
	body, err := json.Marshal(map[string]string{"config": cfg})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/preflight", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	(&Server{}).handlePreflight(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected ok response, got %#v", resp)
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data preflightResponse
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Summary.Provider != config.ProviderOllama || data.Summary.Model != "llama3" {
		t.Fatalf("unexpected summary: %#v", data.Summary)
	}
	if data.Summary.SchemaFields != 0 {
		t.Fatalf("schema fields = %d, want 0", data.Summary.SchemaFields)
	}
	if len(data.Warnings) < 2 {
		t.Fatalf("expected warnings, got %#v", data.Warnings)
	}
	joined := strings.Join(data.Warnings, "\n")
	if !strings.Contains(joined, "input file does not exist") {
		t.Fatalf("expected missing input warning, got %#v", data.Warnings)
	}
	if !strings.Contains(joined, "structured output is enabled without a schema") {
		t.Fatalf("expected schema warning, got %#v", data.Warnings)
	}
}

func TestHandleRunFailsWhenJobCannotBePersisted(t *testing.T) {
	root := t.TempDir()
	jobsPath := filepath.Join(root, "jobs.json")
	if err := os.Mkdir(jobsPath, 0o750); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "input.csv")
	if err := os.WriteFile(inputPath, []byte("name\nAlice\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "out.jsonl")
	runConfig := `api:
  provider: ollama
  model: llama3
prompt:
  input_template: "{{ .name }}"
input:
  type: csv
  path: ` + inputPath + `
output:
  type: jsonl
  path: ` + outputPath + `
processing:
  dry_run: true
`
	body, err := json.Marshal(map[string]any{"config": runConfig, "dry_run": true})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{dataDir: root, jobsFile: jobsPath}
	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleRun(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if len(srv.jobs) != 0 {
		t.Fatalf("expected failed enqueue to roll back in-memory job, got %d job(s)", len(srv.jobs))
	}
}

func TestLoadJobsFallsBackToBackup(t *testing.T) {
	root := t.TempDir()
	jobsDir := filepath.Join(root, "jobs")
	if err := os.MkdirAll(jobsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	jobsPath := filepath.Join(jobsDir, "jobs.json")
	if err := os.WriteFile(jobsPath, []byte("{not-json"), 0o640); err != nil {
		t.Fatal(err)
	}
	backup := jobState{Jobs: []*JobStatus{{ID: 7, Status: "completed", StartedAt: time.Now(), Config: "api:\n  provider: ollama\n  model: llama3\n"}}}
	data, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobsPath+".bak", data, 0o640); err != nil {
		t.Fatal(err)
	}
	srv := &Server{dataDir: root, jobsFile: jobsPath}
	if err := srv.loadJobs(); err != nil {
		t.Fatalf("loadJobs returned error: %v", err)
	}
	if len(srv.jobs) != 1 || srv.jobs[0].ID != 7 {
		t.Fatalf("expected backup job #7, got %#v", srv.jobs)
	}
	if srv.jobIDSeq != 7 {
		t.Fatalf("jobIDSeq = %d, want 7", srv.jobIDSeq)
	}
}

func TestBuildPreviewColumnStats(t *testing.T) {
	columns := []string{"id", "active", "note"}
	records := []map[string]any{
		{"id": float64(1), "active": true, "note": ""},
		{"id": float64(2), "active": false, "note": "ready"},
	}
	stats := buildPreviewColumnStats(columns, records)
	if stats["id"].Type != "number" {
		t.Fatalf("id type = %q, want number", stats["id"].Type)
	}
	if stats["active"].Type != "bool" {
		t.Fatalf("active type = %q, want bool", stats["active"].Type)
	}
	if stats["note"].Empty != 1 {
		t.Fatalf("note empty = %d, want 1", stats["note"].Empty)
	}
	if len(stats["id"].Examples) == 0 {
		t.Fatal("expected id examples")
	}
}

func TestHandleModelsOpenAICompatible(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test-direct" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-4o"},
				{"id": "gpt-4o-mini"},
			},
		})
	}))
	t.Cleanup(providerServer.Close)

	reqBody := map[string]string{
		"provider": config.ProviderOpenAI,
		"base_url": providerServer.URL,
		"api_key":  "sk-test-direct",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	(&Server{}).handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected ok response, got %#v", resp)
	}
	models, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("expected models array, got %#v", resp.Data)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %#v", models)
	}
}

func TestHandleModelsLMStudioWithoutAPIKey(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("unexpected auth header for local provider: %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "local-model-a"},
			},
		})
	}))
	t.Cleanup(providerServer.Close)

	reqBody := map[string]string{
		"provider": config.ProviderLMStudio,
		"base_url": providerServer.URL,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	(&Server{}).handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestFilesAPI(t *testing.T) {
	root := t.TempDir()
	inputDir := filepath.Join(root, "input")
	outputDir := filepath.Join(root, "output")
	if err := os.MkdirAll(inputDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "sample.csv"), []byte("a,b\n1,2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "result.jsonl"), []byte("{\"ok\":true}\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	srv := &Server{dataDir: root}

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	rec := httptest.NewRecorder()
	srv.handleListFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", rec.Code, http.StatusOK)
	}

	var listResp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(listResp.Data)
	if err != nil {
		t.Fatal(err)
	}
	var files []struct {
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}
	if err := json.Unmarshal(raw, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %#v", files)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/files/download/input/sample.csv", nil)
	req.SetPathValue("dir", "input")
	req.SetPathValue("name", "sample.csv")
	rec = httptest.NewRecorder()
	srv.handleDownloadFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "a,b\n1,2\n" {
		t.Fatalf("download body = %q, want %q", got, "a,b\n1,2\n")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/files/download/output/result.jsonl?format=csv", nil)
	req.SetPathValue("dir", "output")
	req.SetPathValue("name", "result.jsonl")
	rec = httptest.NewRecorder()
	srv.handleDownloadFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("csv export status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv export: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("csv rows = %d, want %d", len(rows), 2)
	}
	if got, want := rows[0], []string{"ok"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("csv header = %#v, want %#v", got, want)
	}
	if got, want := rows[1], []string{"true"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("csv row = %#v, want %#v", got, want)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/files/download/output/result.jsonl?format=tsv", nil)
	req.SetPathValue("dir", "output")
	req.SetPathValue("name", "result.jsonl")
	rec = httptest.NewRecorder()
	srv.handleDownloadFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tsv export status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	tsvReader := csv.NewReader(strings.NewReader(rec.Body.String()))
	tsvReader.Comma = '\t'
	rows, err = tsvReader.ReadAll()
	if err != nil {
		t.Fatalf("parse tsv export: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("tsv rows = %d, want %d", len(rows), 2)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/files/download/output/result.jsonl?format=xlsx", nil)
	req.SetPathValue("dir", "output")
	req.SetPathValue("name", "result.jsonl")
	rec = httptest.NewRecorder()
	srv.handleDownloadFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("xlsx export status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	xlsx, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("open xlsx export: %v", err)
	}
	defer xlsx.Close()
	rows, err = xlsx.GetRows("Output")
	if err != nil {
		t.Fatalf("read xlsx rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("xlsx rows = %d, want %d", len(rows), 2)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/files/output/result.jsonl", nil)
	req.SetPathValue("dir", "output")
	req.SetPathValue("name", "result.jsonl")
	rec = httptest.NewRecorder()
	srv.handleDeleteFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d", rec.Code, http.StatusOK)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "result.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected output file to be deleted, stat err = %v", err)
	}
}

func TestHandleRerunJob(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "input.csv")
	outputPath := filepath.Join(root, "output.jsonl")
	if err := os.WriteFile(inputPath, []byte("name\nAlice\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	srv := &Server{dataDir: root}
	runConfig := `api:
  provider: ollama
  model: llama3
prompt:
  input_template: "{{ .name }}"
input:
  type: csv
  path: ` + inputPath + `
  csv:
    delimiter: ","
    has_header: true
output:
  type: jsonl
  path: ` + outputPath + `
processing:
  include_input_in_output: true
  response_field: result
  dry_run: true
`

	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewReader([]byte(`{"config":`+strconv.Quote(runConfig)+`,"dry_run":true}`)))
	rec := httptest.NewRecorder()
	srv.handleRun(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	firstJob := decodeJobFromResponse(t, rec.Body.Bytes())
	waitForJobStatus(t, srv, firstJob.ID, "completed")

	rerunReq := httptest.NewRequest(http.MethodPost, "/api/jobs/1/rerun", nil)
	rerunReq.SetPathValue("id", "1")
	rerunRec := httptest.NewRecorder()
	srv.handleRerunJob(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d", rerunRec.Code, http.StatusAccepted)
	}

	secondJob := decodeJobFromResponse(t, rerunRec.Body.Bytes())
	if secondJob.ID == firstJob.ID {
		t.Fatal("expected rerun to create a new job")
	}
	if secondJob.Name == firstJob.Name {
		t.Fatalf("expected rerun job name to change, got %q", secondJob.Name)
	}
	if !secondJob.DryRun {
		t.Fatal("expected rerun to preserve dry_run state")
	}
	waitForJobStatus(t, srv, secondJob.ID, "completed")
}

func decodeJobFromResponse(t *testing.T, body []byte) JobStatus {
	t.Helper()
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatal(err)
	}
	var job JobStatus
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	return job
}

func waitForJobStatus(t *testing.T, srv *Server, id int, want string) JobStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+strconv.Itoa(id), nil)
		req.SetPathValue("id", strconv.Itoa(id))
		rec := httptest.NewRecorder()
		srv.handleGetJob(rec, req)
		if rec.Code == http.StatusOK {
			job := decodeJobFromResponse(t, rec.Body.Bytes())
			if job.Status == want {
				return job
			}
			if job.Status == "failed" || job.Status == "cancelled" {
				t.Fatalf("job #%d ended with status %q: %s", id, job.Status, job.Error)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job #%d did not reach status %q in time", id, want)
	return JobStatus{}
}
