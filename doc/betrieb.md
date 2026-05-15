# llmflow für Betrieb, Administration und Security

Dieses Dokument beschreibt, wie llmflow betrieben, abgesichert und überwacht werden sollte.

## Betriebsmodell

llmflow kann in zwei Rollen betrieben werden:

- CLI-Jobrunner für manuelle oder geplante Läufe
- Webserver mit UI und API für interaktive Nutzung und Datei-basierte Automatisierung

## Startvarianten

### Webserver

```bash
./bin/llmflow web --addr :8080
```

### Einzeljob per CLI

```bash
./bin/llmflow run --config /pfad/zur/config.yaml
```

## Wichtige Verzeichnisse

Standardmäßig nutzt llmflow das Verzeichnis `data`.

Typische Struktur:

- `data/input`: Uploads und Input-Dateien
- `data/output`: Ergebnisdateien
- `data/jobs/jobs.json`: persistierte Job-Historie
- `data/jobs/watchers.json`: persistierte Standing Orders

Über `LLMFLOW_DATA_DIR` kann das Root-Verzeichnis geändert werden.

## Relevante Umgebungsvariablen

- `LLMFLOW_DATA_DIR`: alternatives Datenverzeichnis
- `LLMFLOW_LLM_PRESETS_FILE`: optionaler Pfad zu einer Admin-Preset-Datei für die Weboberfläche; Standard ist `data/llm-presets.yaml`
- `LLMFLOW_WEB_TOKEN`: optionaler Bearer-Token-Schutz für `/api/*`
- `LLMFLOW_WEB_SUGGEST_TIMEOUT`: separates Timeout-Budget für AI Quick Setup in der Weboberfläche
- `LLMFLOW_MAX_WORKERS`: systemweite Obergrenze für parallel aktive Worker im Webserver; Standard ist `8`
- Provider-spezifische API-Key-Variablen wie `OPENAI_API_KEY`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY`
- Datenbank-DSNs oder Secrets über `dsn_env`, `api_key_env`, `username_env`, `password_env`

## Admin-defined LLMs

Administratoren koennen in der Weboberflaeche gemeinsame LLM-Auswahlen bereitstellen. Dadurch muessen Anwender keine Provider-URLs, Deployment-Namen oder Secret-Namen manuell eintragen.

### Speicherort

Ohne weitere Konfiguration sucht der Webserver nach:

```text
data/llm-presets.yaml
```

Alternativ kann der Pfad explizit gesetzt werden:

```bash
export LLMFLOW_LLM_PRESETS_FILE=/pfad/zur/llm-presets.yaml
./bin/llmflow web --addr :8080
```

### Dateiformat

Empfohlen ist eine YAML-Datei mit einem `presets`-Array:

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
```

### Feldbedeutung

- `id`: technische, eindeutige Kennung des Presets
- `label`: Anzeigename in der Weboberflaeche
- `provider`: z. B. `openai`, `azure`, `gemini`, `anthropic`, `lmstudio`, `ollama`, `generic`
- `base_url`: Provider-Endpoint; fuer Azure die Resource-URL
- `api_version`: nur fuer Azure relevant
- `api_key_env`: Name der Umgebungsvariable, in der das Secret auf dem Server liegt
- `model`: Modellname; bei Azure der Deployment-Name

### Secrets richtig ablegen

Die Preset-Datei sollte keine echten API-Keys enthalten. Stattdessen wird nur der Name der Umgebungsvariable hinterlegt, zum Beispiel `AZURE_OPENAI_API_KEY`.

Beispiel:

```bash
export AZURE_OPENAI_API_KEY='your-real-key'
export OPENAI_API_KEY='your-real-key'
./bin/llmflow web --addr :8080
```

### Aktivierung und Nutzung

1. Preset-Datei anlegen, bevorzugt unter `data/llm-presets.yaml`.
2. Noetige Secrets als Umgebungsvariablen im Startkontext des Servers setzen.
3. Webserver starten oder nach Aenderungen neu starten.
4. In der Weboberflaeche im Feld `Admin-defined LLM` das gewuenschte Preset auswaehlen.

Die Auswahl fuellt Provider, Base URL, Modell und Secret-Referenz automatisch vor.

### Typische Fehler

- Datei liegt am falschen Ort und `LLMFLOW_LLM_PRESETS_FILE` ist nicht gesetzt.
- Die Umgebungsvariable aus `api_key_env` ist im Server-Prozess nicht gesetzt.
- Bei Azure wurde statt des Deployment-Namens der Basis-Modellname eingetragen.
- Die Preset-ID ist doppelt vergeben; spaetere Duplikate werden ignoriert.

## Sicherheitsempfehlungen

### API-Schutz

Wenn die Weboberfläche nicht nur lokal erreichbar ist, sollte `LLMFLOW_WEB_TOKEN` gesetzt werden.

Dann müssen API-Requests einen Header enthalten:

```http
Authorization: Bearer <token>
```

### Netzwerkgrenzen

- Den Webserver nicht ungeschützt ins Internet hängen.
- Reverse Proxy mit TLS und Zugriffskontrolle verwenden.
- Für interne Nutzung IP-Restriktionen oder VPN bevorzugen.

### Secrets

- Secrets nicht im YAML im Klartext speichern, wenn vermeidbar.
- `api_key_env`, `dsn_env` und ähnliche Felder bevorzugen.
- Direkt eingetragene API-Keys werden für den aktuellen Lauf verwendet, aber nicht in der Job-Historie gespeichert.
- Zugriff auf Log-Ausgaben und Konfigurationsdateien einschränken.

### Parallelität begrenzen

`processing.workers` steuert die parallelen Worker eines einzelnen Jobs. Für den Produktiveinsatz sollte zusätzlich `LLMFLOW_MAX_WORKERS` gesetzt werden. Diese Obergrenze gilt im Webserver pro Prozess über alle gleichzeitig laufenden Jobs hinweg. Wenn mehrere Jobs parallel starten, warten zusätzliche Worker automatisch, statt Provider, lokale Modelle oder Ausgabedateien zu überlasten.

### Agentic Tools bewusst freischalten

Die Tool-Funktionen erweitern den Wirkbereich des Modells.

Empfehlung:

- Standardmäßig deaktiviert lassen
- Nur die konkret benötigten Tools aktivieren
- SQL-Zugriffe auf read-only Datenquellen beschränken
- Code-Ausführung nur mit restriktiver Konfiguration und Whitelist verwenden

### Standing Orders absichern

Watcher verarbeiten Dateien automatisch. Deshalb sollte das Watch Directory:

- klar abgegrenzt sein
- nicht von beliebigen Parteien beschreibbar sein
- nur die erwarteten Dateitypen enthalten
- im Monitoring sichtbar sein

## Deployment-Hinweise

### Systemdienst

Für produktiven Betrieb empfiehlt sich ein Systemdienst oder Container-Orchestrator.

Wichtig:

- Restart bei Absturz
- persistentes Datenverzeichnis
- definierte Umgebungsvariablen
- Log-Rotation oder zentrale Log-Aggregation

### Health-Check

llmflow stellt bereit:

- `GET /health`

Das ist geeignet für Liveness- und Readiness-Prüfungen.

## Backup und Recovery

Mindestens folgende Daten sollten gesichert werden:

- Konfigurationsdateien
- `data/output`
- `data/jobs/jobs.json`
- `data/jobs/watchers.json`
- bei Bedarf `data/input`

Nicht jede Installation braucht dieselbe Aufbewahrung. Für revisionsnahe Prozesse ist eine definierte Retention wichtig.

## Monitoring-Empfehlungen

Sinnvolle Betriebsindikatoren:

- Erreichbarkeit von `/health`
- Job-Durchsatz pro Zeitraum
- Fehlerquote pro Provider oder Modell
- durchschnittliche Laufzeit pro Job
- Anzahl wartender oder wiederkehrend fehlschlagender Watcher-Jobs
- Größe von `data/output` und `data/input`

## Troubleshooting

### Webserver startet, aber Jobs schlagen fehl

Typische Ursachen:

- API-Key fehlt
- falscher Provider oder falsches Modell
- Timeout zu niedrig
- lokales Modell nicht erreichbar

### Watcher reagiert nicht

Prüfen:

- Watcher ist aktiv
- Verzeichnis existiert
- Pattern passt wirklich zum Dateinamen
- Datei liegt direkt im Watch Directory und nicht schon in `active` oder `done`

### AI Quick Setup dauert zu lange

Dann das Timeout erhöhen, entweder in `api.timeout` oder über `LLMFLOW_WEB_SUGGEST_TIMEOUT`.

### Output wächst stark an

Maßnahmen:

- `Include Input in Output` auf `key` oder `none`
- `output_fields` explizit definieren
- Aufbewahrungskonzept für alte Dateien etablieren

## Betriebsentscheidungen

### Wann Webserver sinnvoll ist

- wenn mehrere Nutzer Jobs starten
- wenn Fachanwender ohne CLI arbeiten sollen
- wenn File-Uploads, Vorschau und Watcher gebraucht werden

### Wann CLI sinnvoller ist

- für geplante Cron- oder CI-Läufe
- für reproduzierbare Jobs aus Git-versionierten Configs
- für kontrollierte Serverprozesse ohne interaktive UI
