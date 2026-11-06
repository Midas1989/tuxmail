<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Entwicklung: Dev-Mailserver, Server- und Desktop-Modus, End-to-End-Tests

Diese Anleitung beschreibt, wie TuxMail lokal gegen **echte Mailprotokolle**
(IMAP, SMTP, Autoconfig) entwickelt und getestet wird – ohne Demo-Daten aus
`tuxmail demo seed`, sondern mit einem richtigen Konto, das über den
Kontoassistenten angelegt wird.

Voraussetzungen: Go 1.26, Node 22.12 oder neuer, für die E2E-Tests
Playwright mit Chromium (`npm i -g playwright && npx playwright install chromium`).

## 1. Frontend und Binary bauen

```sh
go generate ./web            # npm ci + vite build, kopiert frontend/dist nach web/dist
go build -o bin/tuxmail ./cmd/tuxmail
```

`web/dist` und Binaries werden nicht eingecheckt.

## 2. Dev-Mailserver (`tuxmail devserver`)

Der versteckte Befehl `tuxmail devserver` (Paket `internal/devserver`) startet
einen vollständigen, rein im Speicher laufenden Mailserver für Entwicklung und
CI – **nicht für den Produktivbetrieb**:

| Dienst | Standardadresse | Details |
|---|---|---|
| IMAP | `127.0.0.1:1143` | go-imap v2 `imapmemserver`, Klartext, IDLE, MOVE, UIDPLUS, beliebige Keywords |
| SMTP | `127.0.0.1:1587` | go-smtp; mit AUTH (PLAIN/LOGIN) Versand an alle Empfänger, ohne AUTH nur Zustellung an lokale Postfächer (wie ein MX) |
| HTTP | `127.0.0.1:1080` | Autoconfig/ISPDB-Dokument und JSON-Steuer-API |

```sh
bin/tuxmail devserver                 # Benutzer anna@ und ben@tuxmail.test, Passwort "tuxmail-dev"
bin/tuxmail devserver --user max@firma.test:geheim:"Max Muster" --no-seed
bin/tuxmail devserver --verbose       # Protokollfehler und Zustellungen loggen
bin/tuxmail devserver --profile german --bulk 2500   # großes Postfach, deutsche Dovecot-Ordner
```

**Große Postfächer:** `--bulk N` legt pro Benutzer N generierte Mails im
Posteingang an (realistische Größen: Text 1–8 KB, jede 7. ein HTML-Newsletter,
jede 23. mit PDF-Anhang, jede 5. eine Antwort; Papierkorb/Archive je N/4,
übrige Ordner N/20). `--profile german` erzeugt die Ordner eines deutschen
Dovecot-Postfachs (`Entwürfe`, `Gesendet`, `Spam`, `Papierkorb`, `Templates`,
`Archive` mit `\Archive` **und** ein Benutzerordner `Archiv` mit `2024`/`2025`)
und meldet SPECIAL-USE (RFC 6154). Messung „Zeit bis zu den ersten Zeilen“:
`TUXMAIL_LARGE_N=2500 go test ./internal/app -run TestLiveLargeMailbox -v`.

Beim Start werden realistische Mails eingespielt (`internal/devserver/seed.go`):
Threads mit Antworten über Posteingang/Gesendet/Archiv, eine weitergeleitete
Rechnung mit PDF, eine Antwort mit Bildanhang, ein HTML-Newsletter mit
externen Bildern, Tracking-Pixel und `List-Unsubscribe`, eine Kalendereinladung
(`text/calendar; method=REQUEST`) und eine ISO-8859-1-Mail mit Umlauten.

### Steuer-API (für Tests und Skripte)

Alle Postfachzugriffe laufen über eine echte IMAP-Sitzung:

```sh
curl 'http://127.0.0.1:1080/api/folders?user=anna@tuxmail.test'
curl 'http://127.0.0.1:1080/api/messages?user=anna@tuxmail.test&folder=INBOX'   # UID, Flags, Header
curl 'http://127.0.0.1:1080/api/raw?user=ben@tuxmail.test&folder=INBOX&uid=2'    # RFC 5322
curl -d '{"user":"anna@tuxmail.test","folder":"INBOX","uid":3,"add":["\\Seen"]}' http://127.0.0.1:1080/api/flags
curl -d '{"from":"Maria <maria@example.org>","to":["anna@tuxmail.test"],"subject":"Hallo","text":"…"}' \
     http://127.0.0.1:1080/api/send                                               # eingehende Mail per SMTP
curl http://127.0.0.1:1080/api/deliveries                                         # vom SMTP-Server angenommene Mails
```

Weitere Endpunkte: `POST /api/move`, `POST /api/deliver?from=&to=` (Rohmail),
`GET /mail/config-v1.1.xml`, `GET /v1.1/{domain}`.

### Autoconfig

TuxMail fragt neben Thunderbirds ISPDB auch `autoconfig.<domain>` usw. ab
(KONZEPT 7.5). Mit der Umgebungsvariable **`TUXMAIL_ISPDB_URL`** wird die
ISPDB ersetzt – z. B. durch einen eigenen Spiegel oder den Dev-Server:

```sh
TUXMAIL_ISPDB_URL=http://127.0.0.1:1080/v1.1/ bin/tuxmail serve --config dev.yaml
```

Der Kontoassistent findet dann für `anna@tuxmail.test` die Server des
Dev-Servers („Einstellungen über Autoconfig gefunden“). Weil der Dev-Server
unverschlüsselt arbeitet, muss die Warnung „Unverschlüsselte Verbindung“ im
Assistenten bestätigt werden.

Autoconfig-Dokumente über unverschlüsseltes HTTP werden sonst **nicht** mehr
abgefragt (`autoconfig.Discoverer.AllowHTTP` ist standardmäßig aus; jeder im
Netz könnte sonst Server unterschieben). Ausnahme sind Loopback-Adressen
(`127.0.0.1`, `::1`, `localhost`) wie der Dev-Server oben. Ein Redirect von
https auf http wird abgelehnt, und die E-Mail-Adresse steht nur in
https-Anfragen im Query-String.

## 3. Echte Server: Dovecot + Postfix

Für realistische Tests (TLS, STARTTLS, Dovecot-Keywords, LMTP-Zustellung)
richtet `scripts/dovecot-dev.sh` einen Wegwerf-Mailserver ein (root, Pakete
`dovecot-imapd dovecot-lmtpd postfix openssl`):

```sh
sudo scripts/dovecot-dev.sh start     # /tmp/tuxmail-dovecot: IMAP 143/993, SMTP 25, Submission 587
sudo scripts/dovecot-dev.sh stop
```

- Benutzer `anna@tuxmail.test`, `ben@tuxmail.test`, Passwort `tuxmail-dev`.
- Selbst signiertes Zertifikat für `localhost`/`127.0.0.1`; die CA liegt in
  `/tmp/tuxmail-dovecot/ca.pem`. TuxMail prüft TLS immer – zum Testen wird die
  CA mit `SSL_CERT_FILE=/tmp/tuxmail-dovecot/ca.pem` vertraut.
- Postfix nimmt Mails für `tuxmail.test` auf Port 25 an und stellt sie per
  LMTP an Dovecot zu; Port 587 verlangt AUTH (SASL über Dovecot).
- Bei jedem Start sind die Postfächer leer.

Der Dev-Server kann sich vor den echten Server schalten: Er startet dann keinen
eigenen IMAP/SMTP-Server, sondern liefert nur Autoconfig (IMAPS 993 + Submission
587 STARTTLS), spielt die Beispielmails per IMAP APPEND ein und bietet die
Steuer-API gegen Dovecot/Postfix an:

```sh
bin/tuxmail devserver --external-imap 127.0.0.1:993 --external-ca /tmp/tuxmail-dovecot/ca.pem
# Keywords prüfen, z. B. nach dem Taggen in TuxMail:
doveadm -c /tmp/tuxmail-dovecot/dovecot.conf fetch -u anna@tuxmail.test 'flags hdr.subject' mailbox INBOX
```

## 4. Server-Modus (`tuxmail serve`)

Minimale Konfiguration für die Entwicklung:

```yaml
# dev.yaml
listen: 127.0.0.1:18080
public_url: http://127.0.0.1:18080
data_dir: /tmp/tuxmail-dev/data        # absoluter Pfad
log: {level: debug, format: text}
```

```sh
echo 'correct horse battery staple' | bin/tuxmail user add --config dev.yaml \
     --username anna --email anna@tuxmail.test --admin --password-stdin
TUXMAIL_ISPDB_URL=http://127.0.0.1:1080/v1.1/ bin/tuxmail serve --config dev.yaml
```

Dann `http://127.0.0.1:18080` öffnen, als `anna` anmelden und unter
**Einstellungen → Konten → Konto hinzufügen** das Mailkonto `anna@tuxmail.test`
mit Passwort `tuxmail-dev` einrichten. Die Synchronisation (IMAP IDLE) und der
Versand (SMTP) laufen pro Benutzer im Server; die Oberfläche erhält Änderungen
live über den WebSocket `/api/events`.

Für die Frontend-Entwicklung mit Hot-Reload: `cd frontend && npm run dev` (Mock-API)
oder die gebaute Oberfläche über `tuxmail serve`.

### Content-Security-Policy je Schale

`frontend/index.html` trägt eine CSP als `<meta>`; der Text lebt in `frontend/src/lib/csp.ts`, `vite.config.ts` schreibt
ihn je Build-Ziel in die Ausgabe. Nicht benötigte Quellen fehlen absichtlich.

| Schale | Wer setzt die CSP | Wirksame Richtlinie (Auszug) |
|---|---|---|
| Web (`tuxmail serve`) | HTTP-Header des Servers **und** Meta | beide strikt, das Meta ist nie lockerer als der Header: `connect-src 'self'`, `img-src 'self' data: blob:` |
| Wails-Desktop | nur Meta (gleicher Build wie Web: `npm run build`) | `connect-src 'self'`, `img-src 'self' data: blob:` – kein `https:`, kein `ws:` |
| Android (Capacitor) | nur Meta (`npm run build:capacitor` = `vite build --mode capacitor`) | `connect-src 'self' https: wss:` (Server vom Nutzer gewählt, kein Klartext-`ws:`), `img-src … https:` (Bildproxy des Servers); zur Laufzeit zusätzlich auf den Origin des aktiven Servers verengt (zweites Meta kann nur einschränken) |
| `npm run dev` | Meta | wie Web, plus `ws:`/`wss:` für Hot-Reload |

Der APK-Build **muss** `build:capacitor` verwenden (`mobile/scripts/build.sh`, `android.yml`, F-Droid-Rezept); mit dem
Web-Build blockiert die CSP alle Anfragen an den Server (die App meldet das beim Start in der Konsole).
Entfernte Quellen: Es gibt im Frontend keine `EventSource`/Fremd-`fetch`-Aufrufe; entfernte Mailbilder laufen nur über
`/api/proxy/image` des eigenen Servers, Avatare nur als `data:`/`blob:`/`/api/`-URL (`lib/avatar.ts`).

## Terminal-Modus (`tuxmail mail`, `tuxmail term`, Ansicht „Terminal“)

Drei Zugänge auf demselben Kern, erste Fassung nur E-Mail:

| Zugang | Wofür | Technik |
|---|---|---|
| `tuxmail mail …` | Skripte, Text oder `--json` | `internal/mailcmd` auf `internal/app` |
| `tuxmail term` | Vollbild im Terminal (Linux, Windows Terminal), liest ohne Synchronisation | `internal/termui` (bubbletea) auf `internal/mailcmd` |
| Ansicht „Terminal“ in der Oberfläche | Browser und Desktop-Fenster, auch mobil | normale Web-Oberfläche, normale API; kein Terminal, keine Shell, kein PTY |

**Befehlssyntax an einer Stelle:** `internal/mailcmd/commands.json`. Go (`parse.go`) und die Oberfläche
(`frontend/src/lib/terminal/parse.ts`, kleine Portierung) lesen dieselbe Tabelle; `internal/mailcmd/testdata/vectors.json`
legt die Ergebnisse für beide Sprachen fest (auch die Entfernung von Steuerzeichen). Neuer Befehl: Tabelle und
Vektoren ändern, dann `service.go`, `termui` und `exec.ts`.

### Befehle

```text
tuxmail mail folders [--account N]
tuxmail mail list    [FOLDER] [--limit N] [--offset N] [--account N]
tuxmail mail read    ID
tuxmail mail reply   ID [--all] [--identity N] [--text TEXT] [--confirm CHECK,..]
tuxmail mail send    --to ADDR,.. [--cc ..] [--bcc ..] [--subject TEXT] [--identity N] [--text TEXT] [--confirm CHECK,..]
tuxmail mail search  QUERY.. [--folder FOLDER] [--limit N] [--offset N] [--account N]
tuxmail mail mark    ID,.. read|unread|flagged|unflagged
tuxmail mail move    ID,.. FOLDER
```

- Zusätzlich überall: `--json`, `--data-dir DIR` oder `--portable` (lokale Installation, Standard: das normale
  Datenverzeichnis), `--user NAME [--config FILE]` (Server), `--vault-passphrase-file FILE` (vorhandener Passwort-Tresor).
  Passwörter stehen nie in Argumenten.
- `send` und `reply` lesen den Text von der Standardeingabe, wenn `--text` fehlt.
- `FOLDER`: Nummer, Rolle (`inbox`, `sent`, `drafts`, `archive`, `junk`, `trash`, `templates`), `unread`, `flagged` oder
  Name/Pfad. Rollen in `list` gelten über alle Konten; in der Oberfläche ist bei mehreren Konten die Nummer nötig.
- Ausgabe als Text: Zeilen mit Tabulatoren. `list`/`search`: `ID Datum Flags Von Betreff`, Flags aus `U`/`F`/`A` oder `-`.
  `folders`: `ID Konto Rolle Ungelesen Gesamt Pfad`. Fehler stehen auf stderr.
- Exit-Codes: `0` ok, `1` Fehler, `2` Aufruf falsch, `3` anderes TuxMail läuft (Änderung abgewiesen),
  `4` nicht gefunden, `5` Versandprüfung hat gestoppt (mit `--confirm CHECK` bestätigen).
- Die Befehlszeile in `term` und in der Oberfläche versteht dieselben Befehle ohne `tuxmail mail` und ohne die
  Instanz-Optionen, dazu `help` und `quit`; die Nummer darf fehlen, dann gilt die gewählte Nachricht. `send` und `reply`
  öffnen dort immer erst den Editor. Optionen ohne Gegenstück in der Ansicht werden mit Fehler abgelehnt, nie still verworfen:
  `term`: `--limit`/`--offset` (`list`, `search`), `--cc`, `--bcc`, `--identity`, `--confirm`; Oberfläche: `--limit`/`--offset`
  (`list`), `--bcc`, `--confirm`, `--identity` bei `reply`. In der Oberfläche prüft der Editor die Versandregeln selbst.

### Laufender Dienst: was passiert

Geprüft: Der Server hat keine lokale API ohne Anmeldung, das Desktop-Fenster gar keine HTTP-API. Deshalb arbeitet die
Befehlszeile direkt auf den Daten, mit dieser Regel:

- **Lesen** (`folders`, `list`, `read`, `search`, die ganze `tuxmail term`-Sitzung) öffnet die Datenbank nur lesend: keine
  Migration, kein Schreiben, keine Sperre, keine Synchronisation, kein Workflow. Ein Dienst startet daneben ohne Verzögerung.
  Nicht heruntergeladene Texte zeigen nur die Vorschau; `term` holt keine neue Post und markiert beim Öffnen nichts.
- **Serverdaten** (`--user` oder ein `users/<id>`-Verzeichnis als `--data-dir`): nur Lesen, im Servermodus. `send`, `reply`,
  `mark`, `move` brechen immer mit Exit-Code 3 ab, egal ob der Dienst gerade läuft; Änderungen gehören in die Oberfläche.
  Darum läuft dort nie eine zweite Synchronisation und nie ein Workflow unter dem Konto des Aufrufers, und auch `--stop`-
  Sicherungen (`deploy/systemd/tuxmail-backup.sh`) bekommen keinen Schreiber dazwischen.
- **Fremde Daten:** Gehört das Verzeichnis einem anderen Systembenutzer (etwa `sudo tuxmail term`), bricht der Befehl ab,
  bevor er dort eine Datei anlegt. Richtig ist `sudo -u tuxmail tuxmail …`.
- **Lokale Installation, ändern:** Jede Änderung (auch aus `term`) nimmt nur für ihre Dauer die Sperre `engine.lock`,
  startet die Synchronisation ohne Messenger und beendet sie wieder, höchstens 25 s. Ist die Sperre belegt (Desktop-Fenster
  offen): Exit-Code 3, bevor etwas geschrieben wird. Änderungen, die noch nicht beim Server sind, bleiben in der Warteschlange
  (Hinweis auf stderr).
- **Dienste und die Sperre:** `serve` wartet nie; das Desktop-Fenster wartet höchstens 35 s auf eine laufende Änderung. Wer die
  Sperre nicht bekommt, läuft mit Warnung weiter und holt sie alle 5 s nach.
- `tuxmail mail list` synchronisiert nicht; ohne laufenden Dienst zeigt es den zuletzt geholten Stand.

### Tasten

| Taste | `tuxmail term` | Ansicht „Terminal“ |
|---|---|---|
| `j`/`k`, Pfeile | wählen oder rollen | wählen (öffnet wie in „Klassisch“) |
| `Enter` | öffnen | in den Text springen |
| `Esc` | zurück, Suche löschen | zurück |
| `n`/`p` | nächste/vorige Nachricht | – |
| `g`/`G` | Anfang/Ende | – |
| `r`/`R` | antworten/allen antworten | gleich |
| `c` | verfassen (`Strg+S` sendet, `Tab` nächstes Feld) | verfassen |
| `f` | – | weiterleiten |
| `s`, `u` | Stern, gelesen/ungelesen | gleich |
| `e`, `#`/`Entf` | archivieren, Papierkorb | gleich |
| `/` | suchen | Befehlszeile mit `search` |
| `:` | Befehlszeile | Befehlszeile |
| `F`/`Tab` | Ordner | Seitenleiste |
| `?`, `q`, `Strg+C` | Hilfe, zurück/beenden, beenden | `help` in der Befehlszeile, `quit` wechselt zu „Klassisch“ |
| `Strg+Umschalt+V` | – | Ansicht wechseln |

### Mailinhalt ist nicht vertrauenswürdig

`mailcmd.CleanLine`/`CleanText` entfernen ganze Escape-Sequenzen (CSI, OSC, DCS, C1), alle Steuerzeichen und
Richtungs-Umschalter aus Betreff, Absender, Text, Dateinamen und Fehlermeldungen, auch in `--json`. HTML wird zu Text,
Remote-Inhalte werden nie geladen, Anhänge nur aufgelistet und nie geöffnet.

### Ausprobieren

```sh
bin/tuxmail demo seed --data-dir /tmp/tx-demo
bin/tuxmail mail list --data-dir /tmp/tx-demo
bin/tuxmail term --data-dir /tmp/tx-demo
```

Die Demo-Konten zeigen auf `.invalid`, Änderungen bleiben also in der Warteschlange. Echter Versand zum Testen:
`tuxmail devserver` und der Test `TestMailLiveAgainstDevserver` in `cmd/tuxmail/mail_live_test.go`.

## 5. Desktop-Modus

```sh
sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev     # einmalig
go generate ./web
go build -tags desktop,production,webkit2_41 -o bin/tuxmail-desktop ./cmd/tuxmail
bin/tuxmail-desktop                     # installiert: Daten in ~/.local/share/tuxmail, Passwörter im Schlüsselbund
```

**Portabler Modus:** Liegt neben dem Programm eine Datei `portable` (oder ein
Ordner `data`), bzw. mit `--portable`/`--data-dir`, landen alle Daten in `data/`
neben dem Programm und die Kontopasswörter in einem mit Master-Passwort
verschlüsselten Tresor (`data/secrets.vault`). Beim ersten Start fragt TuxMail
nach einem neuen Master-Passwort (mit Wiederholung, mindestens 8 Zeichen), bei
jedem weiteren Start nach dem Entsperren; ein falsches Passwort wird gemeldet.

**Wahl des Passwortspeichers (Sicherheitsprüfung NC-07):** Der installierte
Client (nicht portabel) entscheidet beim allerersten Start, ob der
Betriebssystem-Schlüsselbund oder ein Master-Passwort-Tresor genutzt wird, und
merkt sich die Wahl in `desktop.json` (`"secretStore": "keyring"` oder
`"vault"`). Ist der Schlüsselbund bei einem späteren Start vorübergehend nicht
erreichbar (z. B. Secret Service startet beim Login erst später), wechselt
TuxMail **nicht** still auf einen neuen Tresor: Es schreibt eine Warnung ins
Log, und Zugriffe auf gespeicherte Passwörter melden den Fehler „the OS keyring
is not available …“, bis der Schlüsselbund wieder läuft (es wird bei jedem
Zugriff neu versucht, ein Neustart ist nicht nötig). Wer bewusst wechseln will,
ändert den Wert in `desktop.json` (bei `"vault"` fragt TuxMail beim nächsten
Start nach dem Master-Passwort; bereits im Schlüsselbund gespeicherte
Kontopasswörter müssen dann neu eingegeben werden). Der portable Modus nutzt
immer den Tresor.

Ohne Bildschirm (CI, Container) läuft das Fenster unter Xvfb:

```sh
mkdir /tmp/tm && cp bin/tuxmail-desktop /tmp/tm/tuxmail && touch /tmp/tm/portable
Xvfb :77 -screen 0 1440x900x24 & export DISPLAY=:77
WEBKIT_DISABLE_COMPOSITING_MODE=1 /tmp/tm/tuxmail &
xdotool search --name TuxMail | while read w; do xdotool windowsize $w 1440 900; done   # ohne Fenstermanager
import -window root /tmp/desktop.jpg
```

**Logdatei:** Der Desktop-Client schreibt immer nach
`<data_dir>/logs/tuxmail.log` (rotierend, 5 MB, drei ältere Dateien
`tuxmail.log.1` … `.3`; `TUXMAIL_LOG_LEVEL=debug` für mehr Details) –
portabel also `data/logs/tuxmail.log` neben dem Programm, installiert
`%APPDATA%\TuxMail\logs\tuxmail.log` (Windows) bzw.
`~/.local/share/tuxmail/logs/tuxmail.log` (Linux). **Einstellungen → Info &
Diagnose** zeigt Version, Datenverzeichnis, Logdatei und den Sync-Status jedes
Kontos; „Diagnosedaten exportieren“ erstellt ein ZIP mit den Logs und der
Konfiguration ohne Passwörter, Tokens und Mailinhalte (E-Mail-Adressen
maskiert), auf dem Desktop zusätzlich in `<data_dir>/diagnostics/`.

**Fenster:** Unter Windows ist das Fenster rahmenlos mit eigener Titelleiste
(Ziehen, Doppelklick maximiert, Minimieren/Maximieren/Schließen; unter
Windows 11 22H2+ mit Mica-Hintergrund), unter Linux bleibt die
Systemtitelleiste Standard. Umschalten unter **Einstellungen → Info &
Diagnose → Fenster → „Systemtitelleiste verwenden“** (gespeichert in
`<data_dir>/desktop.json`, wirkt nach einem Neustart).

**Test-Hook:** Für automatisierte Tests des nativen Builds kann die Desktop-API
(dieselben Handler, die das Fenster nutzt) zusätzlich auf Loopback angeboten
werden – nur wenn beide Variablen gesetzt sind, jede Anfrage braucht den Token:

```sh
TUXMAIL_DESKTOP_TEST_LISTEN=127.0.0.1:18090 TUXMAIL_DESKTOP_TEST_TOKEN=<mind. 16 Zeichen> /tmp/tm/tuxmail
curl -H 'X-TuxMail-Test-Token: <token>' http://127.0.0.1:18090/api/vault/status
```

## 6. End-to-End-Tests

`scripts/e2e.sh` baut Frontend und Binary, startet `tuxmail devserver` und
`tuxmail serve` mit temporärer Konfiguration und führt
`frontend/scripts/e2e-mail.cjs` (Playwright/Chromium) aus. Das Skript braucht
weder root noch Dovecot und ist für GitHub Actions gedacht:

```sh
scripts/e2e.sh                                   # alles
E2E_SKIP_BUILD=1 E2E_BIN=bin/tuxmail scripts/e2e.sh
SCREENSHOTS=frontend/docs/screenshots scripts/e2e.sh   # Screenshots e2e-*.jpg (1440×900, deviceScaleFactor 1)
sudo E2E_DOVECOT=1 scripts/e2e.sh                # dieselbe Reise gegen Dovecot + Postfix
```

Variablen: `E2E_DIR` (Arbeitsverzeichnis mit Logs `serve.log`,
`devserver.log`), `E2E_HTTP_PORT` (18080), `E2E_DEV_PORT` (1080),
`E2E_IMAP_PORT`/`E2E_SMTP_PORT` (1143/1587), `NODE_PATH` (Playwright,
Standard `npm root -g`). Der Exit-Code ist die Zahl der fehlgeschlagenen
Schritte.

Geprüfte Reise (jeder Schritt wird als PASS/FAIL gemeldet; Prüfungen auf der
Serverseite über die Steuer-API, also per IMAP):

1. Anmeldung über `/login`.
2. Kontoassistent: E-Mail + Passwort → Autoconfig/ISPDB → Konto angelegt;
   das manuelle Serverformular ist erreichbar und vorbelegt.
3. Synchronisation: echte Ordner, Ungelesen-Zähler = Server.
4. Lesebereich: bereinigtes HTML, Hinweis „Externe Inhalte blockiert“, kein Script/Tracker.
5. Anhang herunterladen (Dateiname und Inhalt).
6. Konversation über Posteingang/Gesendet in der Hybrid-Ansicht, Blasen des aktuellen Ordners hervorgehoben.
7. Chat-Ansicht: Zitate und Signaturen eingeklappt.
8. Signatur in den Einstellungen anlegen und der Identität zuordnen.
9. Suche einfach und im Expertenmodus (`from:… AND has:attachment`) mit Facetten.
10. Tag vergeben → IMAP-Keyword auf dem Server.
11. Mail verschieben → auf dem Server im Archiv.
12. Gelesen/Ungelesen in beide Richtungen; Änderung eines anderen Clients erscheint live.
13. Antwort aus dem vollen Editor → beim zweiten Benutzer mit `In-Reply-To`/`References` und Signatur, im Gesendet-Ordner.
14. Versand-Prüfung „Anhang vergessen“ vor dem Senden, danach „Trotzdem senden“.
15. Antwort aus dem Chat-Eingabefeld → korrekt verkettete Mail mit Signatur; kein Entwurf bleibt zurück.
16. Workflow als Regel anlegen und aktivieren (`WHEN mail.received IF from:*@example.org THEN tag "Kunde"`).
17. Neue Mail per SMTP erscheint ohne Neuladen (IDLE + WebSocket) in der richtigen Unterhaltung; der Workflow setzt den Tag (Keyword auf dem Server).

`frontend/scripts/e2e-real.cjs` ist der ältere Screenshot-Lauf gegen
Demo-Daten (`tuxmail demo seed`).

Go-seitig decken `internal/devserver` (Dev-Server) und
`internal/app/devserver_test.go` (App mit Sync und Versand gegen den Dev-Server:
Versand-Prüfungen, Live-Ereignisse der Personen-Ansicht) dieselben Pfade ohne
Browser ab; sie laufen mit `go test ./...`.
