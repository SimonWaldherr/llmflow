# llmflow für Anwender

Dieses Dokument richtet sich an Menschen, die llmflow verwenden wollen, ohne dafür den Code verstehen zu müssen.

## Typische Anwendungsfälle

- Produktdaten klassifizieren oder ergänzen
- Support-Tickets priorisieren
- Stammdaten bereinigen oder normalisieren
- Inhalte extrahieren und als strukturierte Felder speichern
- CSV- oder JSON-Dateien mit LLM-Unterstützung anreichern

## Grundprinzip

llmflow verarbeitet strukturierte Eingaben in einem festen Ablauf:

1. Eingabedaten laden
2. Optional Spalten ausblenden oder Daten anreichern
3. Prompt pro Datensatz oder Datensatz-Gruppe erzeugen
4. Antwort des Modells speichern
5. Ergebnisse als Datei oder Datenbank-Output ablegen

## Einstieg über die Weboberfläche

Die Weboberfläche startet mit:

```bash
./bin/llmflow web
```

Danach ist sie standardmäßig unter `http://localhost:8080` erreichbar.

### Quick Start Workflow

Der Quick Start ist der empfohlene Einstieg für neue Jobs.

#### Schritt 1: Input File

- Datei hochladen
- Format wird automatisch erkannt
- Vorschau prüfen
- Nicht benötigte Spalten abwählen

Wichtig: Abgewählte Spalten werden weder an das LLM übergeben noch später in den Output übernommen.

#### Schritt 2: Aufgabe beschreiben

Im Feld `AI Quick Setup` beschreibst du in natürlicher Sprache, was das Modell pro Datensatz tun soll.

Beispiele:

- `Klassifiziere jede Produktbeschreibung nach Warengruppe und gib nur JSON zurück.`
- `Prüfe für jedes Ticket, ob es dringend ist, und begründe die Entscheidung kurz.`
- `Extrahiere Gewicht, Volumen und Gefahrgutstatus als JSON.`

#### Schritt 3: Provider und Prompts festlegen

Danach wählst du:

- LLM Provider
- Modell
- API-Key oder Umgebungsvariable
- optional Base URL
- System Prompt
- Pre Prompt
- Input Template
- Post Prompt

Faustregel:

- `system`: Rolle und dauerhaftes Verhalten
- `pre_prompt`: kurze Arbeitsanweisung vor dem Datensatz
- `input_template`: welche Daten das Modell wirklich sieht
- `post_prompt`: Ausgabeformat erzwingen, zum Beispiel JSON

#### Schritt 4: Output festlegen

Hier definierst du:

- Output-Typ
- Response Field Name
- Include Input in Output: `all`, `key`, `none`
- optional Key Column
- Workers
- optional Job Name

## Wichtige Optionen verständlich erklärt

### Response Field Name

Dieses Feld enthält die rohe LLM-Antwort, wenn du keine strukturierte JSON-Ausgabe weiter zerlegst.

### Response Fields

Wenn du mehrere Felder erzeugen willst, trägst du sie als kommaseparierte Liste ein.

Beispiel:

```text
weight, volume, needs_freight, short_expiry
```

Dann solltest du im Prompt klar verlangen, dass das Modell genau diese Felder als JSON zurückgibt.

### Parse JSON Response

Wenn aktiviert, versucht llmflow die JSON-Struktur aus der Modellantwort zu extrahieren und als einzelne Ausgabefelder zu speichern.

### Include Input in Output

- `all`: Alle Eingabefelder bleiben im Ergebnis erhalten.
- `key`: Nur eine Schlüsselspalte bleibt erhalten.
- `none`: Nur neu erzeugte Felder werden gespeichert.

`key` ist oft die beste Wahl für große Dateien, wenn man das Ergebnis einer eindeutigen ID zuordnen will, aber keine redundanten Rohdaten im Output braucht.

### Data Enrichment

Mit Data Enrichment kann vor dem eigentlichen LLM-Aufruf eine Web-Recherche auf Basis einer Eingabespalte durchgeführt werden.

Beispiel:

- Quelle: `ean`
- Ziel: `web_info`

Dann wird pro Datensatz mit dem Wert aus `ean` gesucht, und das Ergebnis als Textfeld in den Datensatz eingebaut.

Das ist kein agentischer Modus. Die Suchlogik ist fest und nicht vom Modell gesteuert.

### Agentic Tools

Optional kann das Modell Tools verwenden.

Geeignet für:

- Web-Inhalte nachladen
- Links extrahieren
- einfache Berechnungen ausführen
- Datenbank-Nachschlagewerte holen

Nicht jeder Job sollte diese Funktion aktivieren. Für standardisierte, reproduzierbare Extraktion ist der nicht-agentische Standard meist sauberer.

## YAML-Editor

Wenn du die Quick-Start-Maske konfiguriert hast, kannst du dir daraus YAML generieren lassen.

Der YAML-Editor unterstützt:

- Validate
- Run
- Dry Run
- Import einer YAML-Datei
- Export der aktuellen YAML-Datei

Die generierte YAML enthält auch den ursprünglichen Task aus `AI Quick Setup` als Kommentarzeile.

## Dateien und Vorschauen

Im Tab `Files` kannst du:

- Input-Dateien hochladen
- Input- und Output-Dateien sehen
- Output-Dateien herunterladen
- Output-Dateien vorab ansehen
- Dateien löschen
- Standing Orders verwalten

## Standing Orders

Standing Orders sind für wiederkehrende Dateiimporte gedacht.

Du definierst:

- Watch Directory
- Dateimuster, zum Beispiel `PRODUCTS_*.csv`
- YAML-Template für den Job
- optionalen Namen
- Aktiv-Status

Wichtig im YAML-Template:

- Verwende `{{.InputFile}}` als Platzhalter für die tatsächliche Datei.

Ablauf:

1. Neue Datei landet im Watch Directory
2. llmflow verschiebt sie nach `active/`
3. Job wird gestartet
4. Nach Abschluss wird die Datei nach `done/` verschoben

## CLI für Anwender

### Konfiguration validieren

```bash
./bin/llmflow validate --config examples/config.yaml
```

### Job ausführen

```bash
./bin/llmflow run --config examples/config.yaml
```

### Dry Run

```bash
./bin/llmflow run --config examples/config.yaml --dry-run
```

## Best Practices

- Fang mit kleinen Testdateien an.
- Verwende Dry Run, wenn du nur den Ablauf prüfen willst.
- Nutze Vorschau und Spaltenausschluss, bevor du große Jobs startest.
- Erzwinge strukturierte JSON-Ausgaben im Post Prompt, wenn du mehrere Felder brauchst.
- Verwende `key` statt `all`, wenn die Input-Datei groß ist und du nur Referenz plus neue Felder brauchst.
- Aktiviere Enrichment nur dann, wenn die zusätzliche Web-Recherche echten Mehrwert bringt.

## Häufige Fehler

### Das Modell liefert freien Text statt JSON

Dann ist der Prompt nicht streng genug. Im `post_prompt` sollte sehr klar stehen, dass ausschließlich ein JSON-Objekt mit genau den erwarteten Feldern zurückgegeben werden soll.

### Zu viele irrelevante Felder im Prompt

Nutze die Vorschau und deaktiviere Spalten, die das Modell nicht braucht.

### Output ist unnötig groß

Stelle `Include Input in Output` auf `key` oder `none`.

### Lokales Modell antwortet langsam

Erhöhe `api.timeout` und reduziere `workers`.
