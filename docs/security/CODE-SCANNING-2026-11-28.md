# Code Scanning: Bewertung der CodeQL-Meldungen (2026-11-28)

Stand: 43 offene CodeQL-Meldungen auf `main` (Commit `237aeb9`). Jede Stelle wurde im Code nachgelesen.

- **behoben:** im Code geändert (Commit „Code Scanning: …“).
- **ausgeschlossen:** Test-, Mock- oder Skriptdatei. Sie ist seit diesem Commit nicht mehr im Scan
  (`.github/codeql/codeql-config.yml`), die Meldung schließt sich beim nächsten Lauf selbst.
- **Fehlalarm:** Die Gefahr ist an anderer Stelle abgefangen, die CodeQL nicht verfolgt. Beim Schließen
  auf GitHub „Dismiss → False positive“ wählen und die Begründung aus der Tabelle übernehmen.

| # | Regel | Stelle | Ergebnis | Begründung |
| --- | --- | --- | --- | --- |
| 29 | SSRF (kritisch) | `internal/providers/eas/client.go:465` | Fehlalarm | Die Server-Adresse gibt der Nutzer selbst ein. Der Transport ist ein Klon von `http.DefaultTransport`, und dessen Verbindungsaufbau prüft `netguard` (`internal/netguard`, `dialDefault`) bei jeder Verbindung gegen die Richtlinie. Das gilt auch nach DNS-Auflösung und Weiterleitung, Weiterleitungen werden zudem nicht verfolgt. |
| 28 | SSRF (kritisch) | `internal/imageproxy/fetch.go:161` | Fehlalarm | `checkURL` (nur http/https, Ports, localhost, IP-Liste) und `control` am Dialer prüfen die tatsächlich gewählte IP-Adresse. Kein Umgebungs-Proxy, Weiterleitungen werden einzeln neu geprüft. |
| 27 | Klartext-Log | `internal/server/util.go:93` | Fehlalarm | Geloggt wird ein Fehlerwert. Die Fehler der Anmeldung enthalten keine Passwörter (alle `fmt.Errorf` in `internal/auth` geprüft). |
| 26 | Klartext-Log | `internal/server/api_auth.go:450` | Fehlalarm | wie #27, Fehler des OIDC-Rückrufs ohne Geheimnisse |
| 25 | Klartext-Log | `internal/providers/imap/serverids.go:52` | Fehlalarm | Verbindungsfehler von go-imap, das Passwort steht nicht darin |
| 24 | URL-Schema | `internal/mime/sanitize/text.go:211` | behoben | Der Text-Export lässt jetzt neben `javascript:` auch `vbscript:`- und `data:`-Links weg. Es wurde nie etwas ausgeführt (reiner Text). |
| 23 | Reflected XSS | `internal/app/remote_proxy.go:311` | Fehlalarm | Die Antwort ist JSON mit `Content-Type: application/json` |
| 22 | Reflected XSS | `internal/server/middleware.go:161` | Fehlalarm | Hülle um den ResponseWriter; alle Antworten tragen `nosniff` und eine CSP mit Nonce (`securityHeaders`) |
| 21 | Reflected XSS | `internal/server/api/team_routes.go:277` | Fehlalarm | wie #22 (JSON-Umschreiber) |
| 20 | Reflected XSS | `internal/server/api/idempotency.go:65` | Fehlalarm | wie #22 (Mitschnitt für Idempotenz) |
| 19 | Schwacher Hash | `internal/core/threading/key.go:52` | Fehlalarm | SHA-256 als Fingerabdruck einer Mail, kein Passwort-Hash |
| 18 | Schwacher Hash | `internal/store/blob/blob.go:87` | Fehlalarm | SHA-256 als Inhaltsadresse des Speichers, kein Passwort-Hash |
| 17 | Archiv mit Symlinks | `internal/messenger/signalcli/install.go:439` | Fehlalarm | Das Archiv ist per SHA-256 festgelegt. Symlinks werden nur angelegt, wenn sie im Archiv bleiben (`within`); `noSymlinkBelow` verhindert das Schreiben durch einen Link. |
| 15, 14 | Ganzzahl-Umwandlung | `internal/mailcmd/service.go:500` | behoben | `limit` auf 1 bis 1000 begrenzt, `offset` auf 0 bis 2³¹−1 |
| 16 | Cookie ohne HttpOnly | `internal/server/middleware.go:299` | Fehlalarm | Nur das CSRF-Cookie ist absichtlich lesbar (Double-Submit: das Frontend sendet es als Header). Sitzungs- und OIDC-Cookie sind HttpOnly. |
| 12, 11 | postMessage ohne Herkunft | `frontend/src/pwa/sw.js:138, :31` | behoben | Der doppelte Handler ist entfernt; Nachrichten werden nur von Seiten der eigenen Herkunft angenommen |
| 6 | Unvollständiges Escaping | `frontend/src/lib/search/engine.ts:383` | behoben (echter Fehler) | Ein Backslash im Wert (z. B. `C:\Mail\`) zerstörte die Suchanfrage; jetzt werden `\` und `"` maskiert, mit Test |
| 5 | Unvollständiges Escaping | `frontend/src/lib/search/query.ts:452` | behoben (echter Fehler) | wie #6 |
| 4 | Mehrzeichen-Bereinigung | `frontend/src/lib/signature.ts:125` | Fehlalarm | Der reguläre Ausdruck prüft nur, ob das bereits per Allow-List gereinigte HTML Text enthält; sein Ergebnis wird nie eingefügt |
| 10, 9, 8, 7 | Unsicherer Zufall | `frontend/src/lib/api/mock*.ts`, `comfortMock.ts` | ausgeschlossen | Demo-Daten (Beispiel-Passwörter und -Links im Demo-Modus) |
| 2 | URL-Teilstring | `frontend/src/lib/api/comfortMock.ts:268` | ausgeschlossen | Demo: simuliert eine fehlschlagende Zustellung |
| 13 | Regex ohne Anker | `frontend/scripts/e2e-mail.cjs:207` | ausgeschlossen | Hilfsskript der End-to-End-Tests |
| 3 | HTML-Filter-Regex | `frontend/src/lib/i18n/hardcoded.test.ts:24` | ausgeschlossen | Test |
| 1 | Ersetzen durch sich selbst | `frontend/src/lib/signature.test.ts:121` | ausgeschlossen | Test |
| 30–43 | Log aus Nutzereingaben | `middleware.go:137`, `sync/worker.go`, `sync/ingest.go`, `util.go`, `team_admin.go`, `api.go`, `send.go`, `eas/client.go`, `imageproxy/proxy.go`, `sender.go`, `pim_calendar.go`, `chatfeatures.go` | Fehlalarm | Es wird nur strukturiert mit `log/slog` protokolliert (JSON- oder Text-Handler). Beide maskieren Zeilenumbrüche und Steuerzeichen in Werten, eine Zeile kann keine zweite vortäuschen. Pfade werden zusätzlich über `logPath` gekürzt. |

Ergebnis: 2 echte Fehler (Maskierung in der Suche, kein Sicherheitsproblem, aber falsche Suchergebnisse) und
5 Härtungen behoben. 8 Meldungen betreffen Test-, Mock- oder Skriptdateien, 28 sind Fehlalarme.

## gosec

gosec meldet 543 Stellen. Sie sind „nur zur Info“ eingestellt, bremsen keinen Build und werden getrennt
abgearbeitet. Davon sind 359 nicht geprüfte Fehlerrückgaben (G104), meist `Close` und `Write` auf bereits
fertige Antworten, und 75 Dateipfade aus Variablen (G304), deren Pfade aus der Konfiguration stammen.
Mit dieser Bewertung angesehen: G124 (Cookie), G705 (Graph-Webhook, jetzt zusätzlich `nosniff`) und
G118 (Goroutines, Absicht: die Arbeit läuft nach der Anfrage weiter).
