# llmflow Dokumentation

Dieses Verzeichnis bündelt die Dokumentation für unterschiedliche Zielgruppen. Der Anspruch ist nicht, jede Zielgruppe mit denselben Details zu belasten, sondern jede mit den Informationen zu versorgen, die sie wirklich für Entscheidungen oder Arbeit mit dem Tool braucht.

## Für wen welches Dokument gedacht ist

- [anwender.md](anwender.md): Für Fachanwender, Data-Ops, Analysten und alle, die Jobs über CLI oder Weboberfläche ausführen.
- [betrieb.md](betrieb.md): Für Betrieb, Administration, Security und Deployment.
- [entwickler.md](entwickler.md): Für Entwickler, die llmflow erweitern, integrieren oder debuggen.

## Was llmflow ist

llmflow ist ein batch-orientiertes Tool für die reproduzierbare Verarbeitung strukturierter Daten mit Sprachmodellen. Es liest strukturierte Eingaben wie CSV, JSON, JSONL, XML oder Datenbanktabellen, baut daraus deterministisch Prompts, ruft ein konfiguriertes LLM auf und schreibt die Ergebnisse in Dateien oder Datenbanken zurück.

Der Fokus liegt auf Wiederholbarkeit, Nachvollziehbarkeit und klarer Konfiguration. llmflow ist nicht als allgemeine Workflow-Plattform gedacht und auch nicht als Chat- oder Assistenzsystem für freie Interaktion.

## Kernfunktionen in einem Satz

- Strukturierte Inputs verarbeiten
- Prompts pro Datensatz oder Batch erzeugen
- Mehrere LLM-Provider ansprechen
- Ergebnisse robust und reproduzierbar speichern
- Jobs über Web-UI oder CLI steuern
- Optional mit Tools, Enrichment und Standing Orders erweitern

## Funktionsüberblick

### Eingabe

- CSV, XLSX, JSON, JSONL, XML
- SQLite und MSSQL
- Datei-Upload über Weboberfläche
- Dateivorschau vor dem Run
- Ausschluss einzelner Spalten vor der Übergabe ans LLM
- Format-Erkennung für Uploads in der Weboberfläche

### Verarbeitung

- Deterministische Prompt-Pipeline mit `system`, `pre_prompt`, `input_template`, `post_prompt`
- Einzelverarbeitung pro Record oder Batch-Verarbeitung mit `batch_size`
- Konfigurierbare Parallelität, Timeouts, Retries und Rate-Limits
- Optionaler Dry Run ohne echte LLM-Aufrufe
- Optionales JSON-Parsing strukturierter LLM-Antworten
- Optionaler Key-Column-Modus für schlanke Outputs
- Optionales nicht-agentisches Web-Enrichment pro Datensatz
- Optionaler agentischer Tool-Einsatz für Web, Code und SQL

### Ausgabe

- CSV, XLSX, JSON, JSONL, XML, SQLite, MSSQL
- Kompletter Input, nur Schlüsselspalte oder gar kein Input im Output
- Auswahl konkreter Output-Felder
- Dateivorschau im Output-Bereich der Weboberfläche

### Betrieb und UI

- CLI für `validate`, `run`, `web`
- Weboberfläche mit Quick Start, YAML-Editor, Jobs und Files
- Job-Historie mit Logs, Status und Preview
- Standing Orders für überwachte Verzeichnisse
- Optionale Bearer-Token-Absicherung der API

## Begriffe

- Record: Ein Eingabedatensatz, zum Beispiel eine CSV-Zeile oder ein JSON-Objekt.
- Prompt-Pipeline: Die feste Zusammensetzung aus System Prompt, Pre Prompt, Input Template und Post Prompt.
- Dry Run: Ein Simulationslauf ohne echte LLM-Requests.
- Key Column: Eine einzelne Eingabespalte, die zusammen mit neuen Feldern im Output erhalten bleibt.
- Standing Order / Watcher: Eine Konfiguration, die neue Dateien in einem Verzeichnis automatisch verarbeitet.
