# llmflow für Entwickler

Dieses Dokument richtet sich an Entwickler, die llmflow verstehen, erweitern oder integrieren möchten.

## Architektur in Kurzform

llmflow ist in klar getrennte Pakete geschnitten:

- `cmd/llmflow`: CLI-Einstiegspunkt
- `internal/config`: Laden, Defaulting und Validierung der Konfiguration
- `internal/input`: Reader für CSV, XLSX, JSON, JSONL, XML, SQLite, MSSQL
- `internal/prompt`: deterministische Prompt-Erzeugung
- `internal/llm`: Provider-spezifische Generatoren
- `internal/tools`: optionale agentische Tools
- `internal/enrich`: nicht-agentisches Web-Enrichment
- `internal/output`: Writer für CSV, XLSX, JSON, JSONL, XML, SQLite, MSSQL
- `internal/app`: Orchestrierung, Worker, Retries, Rate-Limits, Record-Fluss
- `internal/web`: API und eingebettete Weboberfläche

## Control Flow eines Runs

1. Config laden und Defaults anwenden
2. Input Reader erstellen
3. Output Writer erstellen
4. Prompt Builder erstellen
5. optional Tools und Enricher vorbereiten
6. Records lesen
7. pro Record oder Batch Prompt bauen
8. LLM aufrufen oder Dry Run erzeugen
9. Antwort optional als JSON zerlegen
10. Output schreiben

## Wichtige Domänenkonzepte

### Prompt-Pipeline

Die Prompt-Pipeline wird aus vier Bausteinen zusammengesetzt:

- `system`
- `pre_prompt`
- `input_template`
- `post_prompt`

Diese Struktur ist bewusst deterministisch. llmflow soll keine implizite Magie hinzufügen, sondern die Konfiguration exakt umsetzen.

### Processing-Modi

- `per_record`: Standardmodus
- `batch_size > 1`: mehrere Records pro LLM-Request

Batching setzt voraus, dass das Modell eine JSON-Liste in derselben Reihenfolge zurückliefert.

### Output-Selektion

Der Output kann über drei Mechanismen gezielt verschlankt werden:

- `include_input_in_output: false`
- `key_column`
- `output_fields`

### Enrichment

Das Enrichment läuft vor dem Prompt-Building. Dadurch steht das neue Feld dem Template und dem Modell wie ein normales Input-Feld zur Verfügung.

## Weboberfläche und API

Die Weboberfläche ist in `internal/web/static/index.html` eingebettet. Der Server liefert sowohl die statischen Assets als auch die JSON-API aus `internal/web/server.go`.

Relevante API-Bereiche:

- Job-Lifecycle
- Datei-Upload und Dateiverwaltung
- File Preview
- Format Detection
- Watcher-Verwaltung
- Model Detection
- Config Suggestion
- Prompt Preview

## Entwicklungs-Setup

### Build

```bash
make build
```

### Tests

```bash
go test ./...
```

### Web-UI lokal starten

```bash
./bin/llmflow web
```

## Designentscheidungen

### Warum YAML statt Code-Konfiguration

Konfigurationen sollen versionierbar, reviewbar und außerhalb des Codes wiederverwendbar sein.

### Warum deterministische Prompt-Bausteine

Viele Datenprozesse müssen nachvollziehbar sein. Ein fixer Aufbau reduziert versteckte Änderungen zwischen Läufen.

### Warum nicht nur agentisch

Der agentische Modus ist flexibel, aber schwerer zu kontrollieren. Deshalb gibt es getrennt dazu einen nicht-agentischen Standardpfad und ein getrenntes Enrichment.

## Erweiterungspunkte

### Neuer Input-Typ

Typische Schritte:

1. Reader in `internal/input` ergänzen
2. Factory aktualisieren
3. Konfiguration validieren
4. Tests ergänzen
5. Web-UI nur dann anpassen, wenn der Typ dort auswählbar sein soll

### Neuer Output-Typ

Typische Schritte:

1. Writer in `internal/output` ergänzen
2. Factory aktualisieren
3. Konfigurationsvalidierung ergänzen
4. Tests ergänzen

### Neuer LLM-Provider

Typische Schritte:

1. Client in `internal/llm` ergänzen
2. Provider-Routing aktualisieren
3. Base-URL-Defaults und API-Key-Auflösung dokumentieren
4. Beispiel-Config anlegen

### Neues Tool

Agentische Tools gehören in `internal/tools`. Achte auf:

- minimale, klar definierte Oberfläche
- sichere Defaults
- aussagekräftige Tests
- klare Aktivierung über Config

### Web-UI erweitern

Die UI liegt weitgehend in einer Datei. Das hält das Embedding einfach, erhöht aber die Änderungsfläche.

Praktische Regeln:

- API zuerst sauber in `server.go` definieren
- UI-Feldnamen eng an Config-Felder anlehnen
- Quick Start und YAML-Editor konsistent halten
- Preview- und Statuspfade nach Änderungen manuell prüfen

## Teststrategie

Es gibt Unit-Tests in mehreren Paketen, unter anderem für:

- App-Orchestrierung
- Config-Laden und Validierung
- Input-Reader
- Output-Writer
- Prompt-Building
- Tools
- Webserver

Wichtige Besonderheit:

- Web-Job-Tests sollten `dry_run=true` verwenden, wenn sie ohne echten Provider laufen sollen.

## Technische Schulden und Verbesserungsmöglichkeiten

### Statische Web-UI

Die UI ist funktional, aber stark zentralisiert. Mittel- bis langfristig wäre eine Aufteilung in kleinere HTML-, CSS- und JS-Module wartbarer.

### Dokumentationsabgleich

Neue Web-Routen und neue Config-Felder sollten bei Erweiterungen sofort in README und `doc/` nachgezogen werden. Der Drift war bereits einmal sichtbar und wurde in diesem Stand bereinigt.

### Watcher-Robustheit

Der aktuelle Watcher basiert auf Polling. Das ist bewusst einfach und robust, aber nicht maximal effizient. Falls sehr viele Verzeichnisse oder hohe Dateifrequenzen relevant werden, wäre event-basiertes Watching ein denkbarer Ausbau.

## Beitragshinweise

Wenn du Änderungen machst:

- Defaults und Validierung konsistent halten
- neue Features in README und `doc/` dokumentieren
- CLI, Web-UI und YAML-Generierung zusammen denken
- bei Output-Änderungen immer auch Preview und Jobs-Ansicht prüfen
