# llmflow

`llmflow` ist ein konfigurierbares CLI-Tool in Go, um strukturierte Inputs einzulesen,
Prompts deterministisch zusammenzubauen, Requests gegen mehrere AI-APIs zu senden
und Ergebnisse in verschiedene Ziele zu schreiben.

## Features

- Konfiguration per YAML oder JSON
- Multi-Provider-Support: `openai`, `gemini`, `ollama`, `lmstudio`, `anthropic`, `generic`
- Prompt-Bausteine: `system`, `pre_prompt`, `post_prompt`, optional Templates
- Inputs: `csv`, `json`, `jsonl`, `xml`, `sqlite`, `mssql`
- Outputs: `csv`, `jsonl`, `sqlite`, `mssql`
- Secrets per Environment-Variablen-Referenz
- Strukturierte Logs via `log/slog`
- Saubere Interfaces für Reader/Writer und Erweiterbarkeit

## Beispiel

```bash
llmflow validate --config examples/config.yaml
llmflow run --config examples/config.yaml
```

Weitere Beispiele:

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

Beispieldateien im Ordner `examples/`:

- `config.yaml`: allgemeines Standardbeispiel fuer OpenAI
- `config-openai.yaml`: OpenAI Responses ueber Chat-Completions-kompatibles Interface
- `config-gemini.yaml`: Google Gemini REST API
- `config-ollama.yaml`: lokales Ollama ueber `/api/generate`
- `config-lmstudio.yaml`: lokales LM Studio ueber OpenAI-kompatibles `/v1`
- `config-anthropic.yaml`: Anthropic Messages API
- `config-generic.yaml`: beliebiger OpenAI-kompatibler Endpoint
- `config-json-input.yaml`: JSON-Input-Beispiel
- `config-xml-input.yaml`: XML-Input-Beispiel
- `input.csv`, `input.json`, `input.xml`: passende Eingabedaten fuer die Beispiele

## Architektur

- `internal/config`: Laden und Validieren der Konfiguration
- `internal/input`: Reader für CSV/JSON/XML/SQLite/MSSQL
- `internal/output`: Writer für CSV/JSONL/SQLite/MSSQL
- `internal/prompt`: Prompt-Aufbau und Templating
- `internal/llm`: Provider-spezifische LLM-Clients und Routing
- `internal/openai`: bestehender OpenAI-Responses-Client
- `internal/app`: Orchestrierung
