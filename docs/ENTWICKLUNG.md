<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Entwicklung: Dev-Mailserver, Server- und Desktop-Modus, End-to-End-Tests

Diese Anleitung beschreibt, wie TuxMail lokal gegen **echte Mailprotokolle**
(IMAP, SMTP, Autoconfig) entwickelt und getestet wird – ohne Demo-Daten aus
`tuxmail demo seed`, sondern mit einem richtigen Konto, das über den
Kontoassistenten angelegt wird.

Voraussetzungen: Go 1.24 (`GOTOOLCHAIN=local`), Node 22, für die E2E-Tests
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
```

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

Ohne Bildschirm (CI, Container) läuft das Fenster unter Xvfb:

```sh
mkdir /tmp/tm && cp bin/tuxmail-desktop /tmp/tm/tuxmail && touch /tmp/tm/portable
Xvfb :77 -screen 0 1440x900x24 & export DISPLAY=:77
WEBKIT_DISABLE_COMPOSITING_MODE=1 /tmp/tm/tuxmail &
xdotool search --name TuxMail | while read w; do xdotool windowsize $w 1440 900; done   # ohne Fenstermanager
import -window root /tmp/desktop.jpg
```

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
