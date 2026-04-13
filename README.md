# llmflow

`llmflow` is a configurable CLI tool written in Go for reading structured input data,
assembling prompts deterministically, sending requests to multiple AI APIs, and writing
results to various output destinations.

## Features

- Configuration via YAML or JSON
- Multi-provider support: `openai`, `gemini`, `ollama`, `lmstudio`, `anthropic`, `generic`
- Prompt building blocks: `system`, `pre_prompt`, `post_prompt`, optional Go templates
- Input formats: `csv`, `json`, `jsonl`, `xml`, `sqlite`, `mssql`
- Output formats: `csv`, `jsonl`, `sqlite`, `mssql`
- Secrets resolved from environment variables
- Structured JSON logs via `log/slog`
- Configurable concurrency, rate limiting, and retry count
- Web UI with optional Bearer-token authentication and graceful shutdown
- Live job monitoring with progress, logs, and intermediate result previews
- Optional sandboxed code execution tool via nanoGo
- Health endpoint (`GET /health`) for container orchestrators

## Quick start

```bash
llmflow validate --config examples/config.yaml
llmflow run     --config examples/config.yaml
```

### Web UI

```bash
# Optional: protect the API with a Bearer token
export LLMFLOW_WEB_TOKEN=mysecret

llmflow web --addr :8080
```

The web UI exposes:

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/health` | Liveness / readiness probe |
| `POST` | `/api/validate` | Validate a YAML config |
| `POST` | `/api/run` | Submit a job |
| `GET`  | `/api/jobs` | List recent jobs |
| `GET`  | `/api/jobs/{id}` | Get job status and logs |
| `POST` | `/api/upload` | Upload an input file |
| `GET`  | `/api/detect` | Detect local Ollama / LM Studio |

Notes:
- `AI Quick Setup` uses the same timeout as the quick form (`api.timeout`) and supports longer-running local models.
- You can set `LLMFLOW_WEB_SUGGEST_TIMEOUT` on the server to override the default suggestion timeout budget.

## Provider examples

```bash
llmflow run --config examples/config-openai.yaml
llmflow run --config examples/config-gemini.yaml
llmflow run --config examples/config-ollama.yaml
llmflow run --config examples/config-lmstudio.yaml
llmflow run --config examples/config-anthropic.yaml
llmflow run --config examples/config-generic.yaml
llmflow run --config examples/config-json-input.yaml
llmflow run --config examples/config-xml-input.yaml
```

## Configuration reference

```yaml
api:
  provider: openai          # openai | gemini | ollama | lmstudio | anthropic | generic
  base_url: https://api.openai.com/v1
  api_key_env: OPENAI_API_KEY   # name of the env var holding the API key
  model: gpt-4o-mini
  timeout: 60s
  max_output_tokens: 1000
  rate_limit_rpm: 60        # requests per minute (0 = unlimited)

prompt:
  system: "You are a precise data-transformation assistant."
  pre_prompt: "Analyse the following record."
  input_template: |
    Source: {{ .source }}
    Data: {{ toPrettyJSON .record }}
  post_prompt: "Return only a compact JSON object."

input:
  type: csv                 # csv | json | jsonl | xml | sqlite | mssql
  path: ./examples/input.csv
  csv:
    delimiter: ","
    has_header: true

output:
  type: jsonl               # csv | jsonl | sqlite | mssql
  path: ./examples/output.jsonl

processing:
  mode: per_record
  include_input_in_output: true
  response_field: llm_response
  continue_on_error: true
  workers: 2                # parallel workers
  max_retries: 3            # LLM call retries per record
  dry_run: false

tools:
  enabled: true
  web_fetch: true
  web_search: true
  web_extract_links: true
  code_execute: true
  sql_query: false
  max_rounds: 5
  code:
    timeout: 10s            # per snippet execution timeout
    max_source_bytes: 65536
    read_only_fs: false     # enables HostReadFile(path)
    read_whitelist: [examples, README.md, LICENSE]
    http_get: false         # enables HTTPGetText(url)
    http_timeout: 5s
    http_min_interval: 200ms
  sql:
    driver: sqlite          # sqlite | sqlserver
    dsn: ./lookup.db        # optional direct DSN
    dsn_env: DB_DSN         # optional env fallback
```

### Example files in `examples/`

| File | Description |
|------|-------------|
| `config.yaml` | Default OpenAI example |
| `config-openai.yaml` | OpenAI via Chat Completions interface |
| `config-gemini.yaml` | Google Gemini REST API |
| `config-ollama.yaml` | Local Ollama via `/api/generate` |
| `config-lmstudio.yaml` | Local LM Studio via OpenAI-compatible `/v1` |
| `config-anthropic.yaml` | Anthropic Messages API |
| `config-generic.yaml` | Any OpenAI-compatible endpoint |
| `config-json-input.yaml` | JSON input example |
| `config-xml-input.yaml` | XML input example |
| `input.csv`, `input.json`, `input.xml` | Matching sample input files |

## Build

```bash
make build        # produces bin/llmflow
make test         # run tests
make test-cover   # run tests with coverage report
make lint         # run golangci-lint
```

## Architecture

| Package | Responsibility |
|---------|---------------|
| `internal/config` | Load and validate configuration |
| `internal/input` | Readers for CSV / JSON / XML / SQLite / MSSQL |
| `internal/output` | Writers for CSV / JSONL / SQLite / MSSQL |
| `internal/prompt` | Prompt assembly and Go templating |
| `internal/llm` | Provider-specific LLM clients and routing |
| `internal/openai` | OpenAI Responses API client |
| `internal/app` | Orchestration (workers, retries, rate limiting) |
| `internal/web` | Embedded web UI with REST API |
