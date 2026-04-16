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
- `LLMFLOW_WEB_TOKEN`: optionaler Bearer-Token-Schutz für `/api/*`
- `LLMFLOW_WEB_SUGGEST_TIMEOUT`: separates Timeout-Budget für AI Quick Setup in der Weboberfläche
- Provider-spezifische API-Key-Variablen wie `OPENAI_API_KEY`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY`
- Datenbank-DSNs oder Secrets über `dsn_env`, `api_key_env`, `username_env`, `password_env`

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
- Zugriff auf Log-Ausgaben und Konfigurationsdateien einschränken.

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
