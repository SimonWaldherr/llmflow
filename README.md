# llmflow

`llmflow` is a configurable CLI tool written in Go for reading structured input data,
assembling prompts deterministically, sending requests to multiple AI APIs, and writing
results to various output destinations.

## What llmflow is, and what it is not

`llmflow` is built for one specific job: take structured input files, run the same prompt
pipeline over each record, and write the results back to files in a repeatable way.
That makes it a batch processor for AI tasks, not a general automation platform and not a
chat-first agent app.

In practical terms:

- `n8n` is a general workflow automation tool. It connects services, APIs, and triggers,
  and is great when you want broad integrations and event-driven flows.
- `OpenClaw` is closer to an AI assistant / agent workspace. It is useful when the main
  interaction is conversational or agent-driven.
- `llmflow` is for structured, file-based processing. It reads CSV, JSON, JSONL, XML, or
  databases, applies a deterministic prompt pipeline per row, and writes durable output
  files.

The short version: use `n8n` to orchestrate systems, use `OpenClaw` for interactive AI
workflows, and use `llmflow` when you want reproducible, repeatable AI processing over
structured data.

## Features

- Configuration via YAML or JSON
- Multi-provider support: `openai`, `azure`, `gemini`, `ollama`, `lmstudio`, `anthropic`, `generic`
- Prompt building blocks: `system`, `pre_prompt`, `post_prompt`, optional Go templates
- Input formats: `csv`, `json`, `jsonl`, `xml`, `sqlite`, `mssql`
- Output formats: `csv`, `xlsx`, `jsonl`, `xml`, `sqlite`, `mssql`
- Input preview with column exclusion before a run starts
- Auto-detection of input format for uploaded files in the web UI
- Web UI stores run results as JSONL and exports them on download as CSV, XLSX, JSON, JSONL, or XML
- Optional non-agentic web enrichment step per record
- Optional key-column-only output mode for traceable slim outputs
- Standing orders / file watchers for folder-based automation
- Secrets resolved from environment variables
- Structured JSON logs via `log/slog`
- Configurable concurrency, system-wide worker cap, rate limiting, and retry count
- Web UI with optional Bearer-token authentication and graceful shutdown
- Live job monitoring with progress, logs, intermediate result previews, and re-run support for completed jobs
- Optional sandboxed code execution tool via nanoGo
- Health endpoint (`GET /health`) for container orchestrators

## Quick start

```bash
llmflow validate --config examples/config.yaml
llmflow run     --config examples/config.yaml
```

### CLI command overview

- `llmflow validate --config <file>`: parse and validate configuration only.
- `llmflow run --config <file>`: execute a full batch run.
- `llmflow web --addr :8080`: start the web UI/API server.
- `llmflow --version`: print build version.

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
| `POST` | `/api/preflight` | Validate a config and return run-readiness summary and warnings |
| `POST` | `/api/run` | Submit a job |
| `GET`  | `/api/jobs` | List recent jobs |
| `GET`  | `/api/jobs/{id}` | Get job status and logs |
| `POST` | `/api/upload` | Upload an input file |
| `GET`  | `/api/files` | List input and output files |
| `GET`  | `/api/files/preview/{dir}/{name}` | Preview the first rows of a CSV or JSONL file |
| `GET`  | `/api/detect-format` | Detect input format for an uploaded file |
| `GET`  | `/api/watchers` | List configured standing orders |
| `POST` | `/api/watchers` | Create a standing order |
| `POST` | `/api/watchers/{id}/toggle` | Pause or resume a standing order |
| `DELETE` | `/api/watchers/{id}` | Delete a standing order |
| `GET`  | `/openapi.json` | OpenAPI 3 specification |
| `GET`  | `/docs` | Swagger UI |
| `GET`  | `/api/detect` | Detect local Ollama / LM Studio |

Notes:
- `AI Quick Setup` uses the same timeout as the quick form (`api.timeout`) and supports longer-running local models.
- You can set `LLMFLOW_WEB_SUGGEST_TIMEOUT` on the server to override the default suggestion timeout budget.
- Admins can predefine selectable LLMs for the web UI with `LLMFLOW_LLM_PRESETS_FILE=/path/to/llm-presets.yaml`.

## Documentation

- General overview: `doc/README.md`
- User guide: `doc/anwender.md`
- Operations guide: `doc/betrieb.md`
- Developer guide: `doc/entwickler.md`

## Provider examples

```bash
llmflow run --config examples/config-openai.yaml
llmflow run --config examples/config-azure.yaml
llmflow run --config examples/config-gemini.yaml
llmflow run --config examples/config-ollama.yaml
llmflow run --config examples/config-lmstudio.yaml
llmflow run --config examples/config-anthropic.yaml
llmflow run --config examples/config-generic.yaml
llmflow run --config examples/config-json-input.yaml
llmflow run --config examples/config-xml-input.yaml
```

### Admin-defined LLM dropdown

The web UI can show a curated LLM dropdown so users do not need to know provider URLs,
deployment names, or API-key environment variables. Set:

```bash
export LLMFLOW_LLM_PRESETS_FILE=examples/llm-presets.yaml
llmflow web --addr :8080
```

If `LLMFLOW_LLM_PRESETS_FILE` is not set, the web server looks for:

```text
data/llm-presets.yaml
```

So there are two common ways to store admin-managed LLM definitions:

- Default location: `data/llm-presets.yaml`
- Custom location via `LLMFLOW_LLM_PRESETS_FILE=/path/to/llm-presets.yaml`

Recommended workflow:

1. Create a preset file, for example `data/llm-presets.yaml`.
2. Define one or more shared LLM entries.
3. Store API keys in server environment variables, not in the preset file.
4. Start or restart `llmflow web`.
5. In the UI, use the `Admin-defined LLM` dropdown.

Preset file format:

```yaml
presets:
  - id: azure-prod-gpt4o
    label: Azure Production GPT-4o
    provider: azure
    base_url: https://YOUR-RESOURCE.openai.azure.com
    api_version: 2024-10-21
    api_key_env: AZURE_OPENAI_API_KEY
    model: YOUR-DEPLOYMENT
```

`api_key_env` is the environment variable name resolved by the server at run time. Do not put raw API keys in preset files.

Example with multiple providers:

```yaml
presets:
  - id: azure-prod-gpt4o
    label: Azure Production GPT-4o
    provider: azure
    base_url: https://YOUR-RESOURCE.openai.azure.com
    api_version: 2024-10-21
    api_key_env: AZURE_OPENAI_API_KEY
    model: YOUR-DEPLOYMENT

  - id: openai-mini
    label: OpenAI GPT-5.4 Mini
    provider: openai
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
    model: gpt-5.4-mini

  - id: local-lmstudio
    label: Local LM Studio
    provider: lmstudio
    base_url: http://localhost:1234/v1
    model: local-model
```

Notes:

- `id` must be unique.
- `label` is what users see in the dropdown.
- `provider`, `model`, and usually `base_url` should match the target provider.
- For Azure, `model` is the deployment name.
- Duplicate preset IDs are ignored after the first entry.
- Invalid entries missing `id`, `provider`, or `model` are skipped.

Example for server-side secret setup on macOS/Linux:

```bash
export AZURE_OPENAI_API_KEY='your-real-key'
export OPENAI_API_KEY='your-real-key'
export LLMFLOW_LLM_PRESETS_FILE="$PWD/data/llm-presets.yaml"
llmflow web --addr :8080
```

When a user selects a preset, the UI fills provider, base URL, model, and `api_key_env` automatically. The actual secret is still read only on the server from the named environment variable.

## Configuration reference

```yaml
api:
  provider: openai          # openai | azure | gemini | ollama | lmstudio | anthropic | generic
  base_url: https://api.openai.com/v1
  api_version: 2024-10-21   # Azure OpenAI only
  api_key_env: OPENAI_API_KEY   # name of the env var holding the API key
  api_key: sk-...           # optional direct key; prefer api_key_env for production
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
  type: jsonl               # csv | xlsx | jsonl | xml | sqlite | mssql
  path: ./examples/output.jsonl

processing:
  mode: per_record
  include_input_in_output: true
  key_column: id
  response_field: llm_response
  parse_json_response: true
  continue_on_error: true
  workers: 2                # per-job workers; web server caps total workers with LLMFLOW_MAX_WORKERS
  max_retries: 3            # LLM call retries per record
  dry_run: false
  batch_size: 1
  output_fields: [id, sentiment, score]

enrich:
  enabled: true
  column: ean
  output_field: web_info
  max_chars: 2000

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
| `config-azure.yaml` | Azure OpenAI via deployment endpoint |
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
make ci           # run the same checks as CI locally
make act          # run the GitHub Actions workflow locally via act
make test-cover   # run tests with coverage report
make lint         # run golangci-lint
```

If `make lint` fails because `golangci-lint` is missing, install it first:

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
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
