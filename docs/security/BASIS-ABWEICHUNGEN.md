# Abweichungen von der Basis-Spezifikation

Stand: 2026-11-07, Release 1.0.0. Bezug: Basis `spec/PARAMETER.md`, `spec/AUTH.md`, `spec/WEB.md`, `spec/KRYPTO.md`,
`spec/BETRIEB.md`. Hier steht, wo TuxMail bewusst anders handelt, und warum.

## Entschieden (Midas über die Koordination, 2026-11-07)

| # | Vorgabe | TuxMail | Grund |
|---|---|---|---|
| 1 | 10 Fehlversuche / 15 min je Konto (`PARAMETER.md`) | Je Konto **und Netz** 5 Versuche, danach Sperre von 1 min bis 1 h. Netzübergreifend ab 50 Fehlversuchen keine Sperre, sondern Druckmodus: nur bekannte Netze, eine Prüfung je Sekunde. | Eine harte Sperre je Konto bei 10 gäbe jedem Fremden die Macht, den Inhaber auszusperren (Befund NA-06). Aus einem Netz greift die Grenze früher als verlangt, der 11. Fehlversuch aus demselben Netz bekommt 429 (Test `TestLoginLimitsInDatabase`). Ein verteilter Angriff bleibt durch die Druckstufe gebremst. |
| 2 | Keine CSRF-Ausnahme per Pfad (`WEB.md`) | `/api/device/authorize`, `/api/device/token`, `/api/device/login` haben keine CSRF-Prüfung. | Diese Endpunkte lesen keine Sitzung und setzen kein Cookie, ein untergeschobener Aufruf kann also weder im Namen eines Opfers handeln noch es anmelden. Die Antwort steht nur im Body, und ohne CORS-Freigabe kann eine fremde Seite ihn nicht lesen. Native Clients senden kein `Origin`. Rate-Limits gelten. Gerätetoken (Bearer) sind aus demselben Grund frei: keine Ambient-Anmeldedaten. |
| 3 | Rotation mit Wiederverwendungs-Erkennung (`PARAMETER.md`, ohne Wert für eine Karenz) | Ein altes Token gilt 60 s nach dem ersten Gebrauch des neuen weiter (`DeviceTokenLeeway`). Es kann in dieser Zeit nicht rotieren. | Anfragen, die beim Tokenwechsel unterwegs sind, dürfen nicht wie ein gestohlenes Token aussehen. Sonst würde das Mobilnetz Geräte abmelden. |

## Technisch bedingt

| # | Vorgabe | TuxMail | Grund |
|---|---|---|---|
| 4 | Kein `'unsafe-inline'`, auch nicht für `style-src` (`PARAMETER.md`) | `style-src` ist ohne, aber `style-src-attr 'unsafe-inline'` ist gesetzt. | HTML-Mail besteht aus `style`-Attributen. Sie läuft in einem `srcdoc`-Frame, der die CSP der Seite erbt, und Attribute können keine Nonce tragen. Der Frame ist `sandbox`, ohne Skripte, mit eigener CSP. Mails über eine eigene Route auszuliefern wäre ein größerer Umbau. |
| 5 | CSP mit Nonce je Anfrage | Android (Capacitor) und Desktop (Wails) behalten die CSP im `<meta>` mit `script-src 'self'`, ohne Nonce und mit `style-src 'unsafe-inline'`. | Die Dateien liegen lokal, kein Server rendert sie, eine Nonce wäre fest eingebaut und damit wertlos. Kein Inline-Skript ist erlaubt. |
| 6 | Testlauf mit `Report-Only` (`konzept/PORTIERUNG.md`, Risiken) | Die CSP gilt sofort. | `WEB.md` verlangt `Report-Only` nicht, und die Oberfläche wurde lokal im Browser ohne Verstoß geprüft. |
| 7 | Migrationen nur vorwärts (`BETRIEB.md`) | Es gibt zusätzlich den Notweg `tuxmail keys rollback`. | Einmaliger Rückweg für dieses Release, nur von Hand bei gestopptem Dienst. Beim Start läuft nichts rückwärts. Wiederherstellungscodes, die seit dem Upgrade erzeugt wurden, gehen dabei verloren; der Befehl nennt die Konten. |
| 8 | Wiederherstellungscodes per HMAC (`PARAMETER.md`) | Neue Codes ja. Bestehende liegen als unter dem Schlüsselring verschlüsselter SHA-256 vor (`enc-sha256`), bis sie verbraucht oder neu erzeugt sind. | Ohne den Code lässt sich kein HMAC bilden, und die Codes sollen gültig bleiben. Ein Datenbank-Leck allein reicht trotzdem nicht mehr für einen Offline-Angriff. |
| 9 | Pepper außerhalb der DB (`PARAMETER.md`) | Pepper und Schlüssel der Audit-Kette liegen in `server_keys`, verschlüsselt unter dem Schlüsselring. | Der Ring liegt außerhalb der DB, ein Leck der DB allein verrät sie nicht. So rotieren sie mit und überleben den Schlüsselwechsel. |
| 10 | Gerätetoken wie API-Schlüssel mit HMAC und Pepper (`PARAMETER.md`, API-Schlüssel) | SHA-256 des Tokens, wie bei Sitzungen. | 256 Bit Zufall, offline nicht zu erraten. Das Format `<präfix>_<kennung>_…` gilt für API-Schlüssel, die TuxMail nicht hat. |
| 11 | `session_version` (`AUTH.md` §3) | Passwortwechsel und „überall abmelden“ löschen die Sitzungszeilen. | Gleiche Wirkung, Pflicht-Negativtest besteht (`TestAuthSection6HTTP`). |
| 12 | Basis-Module verwenden (`CLAUDE.md`) | `internal/secretbox` und `internal/auditchain` sind Kopien, Recovery-HMAC und Sperrliste ebenso. | Das Basis-Repository ist privat, TuxMail baut auf öffentlicher CI. Die Testvektoren aus `spec/testvektoren.json` laufen in den Tests. |
| 13 | Leerlauf je Anfrage (`AUTH.md` §3) | Nur Anfragen mit `X-TuxMail-Activity: 1` zählen, das setzt der Webclient bis 60 s nach einer Eingabe. | Ein offener Tab fragt im Hintergrund nach und hält den Event-Stream. Das darf den Leerlauf nicht aufheben. Das Lesen eines Textes ohne Eingabe zählt also nicht als Aktivität. |

## Noch offen

| Punkt | Grund |
|---|---|
| 2FA für Admins immer (`PARAMETER.md`) | `auth.totp_required_for_admins` ist Einstellung, Ansible setzt sie an, der Code-Standard ist aus. Ein Wechsel des Standards betrifft bestehende Admins beim Upgrade. |
| Neue Sitzungs-ID bei Rechtewechsel und Step-up | Heute nur beim Login (vergleiche ADR 0007 Nr. 1). |
| Audit: Archivierung älterer Abschnitte, Zabbix-Item | Der neueste Hash geht täglich ins Log (`audit chain anchor`). Abschnitte und Monitoring-Vorlage fehlen. |
| `old_json`/`new_json` im Audit | Bestehende Felder liegen im Basis-Feld `new`. Alte Werte werden nicht erfasst. |
| Verschlüsselung der Mail-Datenbanken | Nicht in diesem Paket. |
| HIBP-Abfrage | Laut `PARAMETER.md` optional. |

## Sätze für die Basis-ADR

- Eine Konto-Sperre je Konto gilt nur je Netz (tuxmail: 5 Versuche); netzübergreifend tritt ab 50 Fehlversuchen statt einer Sperre eine Bremse in Kraft, damit Fremde den Inhaber nicht aussperren können.
- Native Anmelde-Endpunkte ohne Sitzungsbezug, die kein Cookie setzen und nur im Antwort-Body antworten (`/api/device/authorize|token|login`), sind von der CSRF-Prüfung ausgenommen.
- Bei der Rotation von Geräte-Token gilt das alte Token 60 s nach dem ersten Gebrauch des neuen weiter, ohne erneut rotieren zu dürfen; danach widerruft sein Gebrauch die Familie.
- `style-src-attr 'unsafe-inline'` ist erlaubt, wo fremdes HTML in einem sandboxed `srcdoc`-Frame angezeigt wird, der die CSP der Seite erbt.
