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
| 4 | Kein `'unsafe-inline'`, auch nicht für `style-src` (`PARAMETER.md`) | `style-src` ist ohne, aber `style-src-attr 'unsafe-inline'` ist gesetzt. | HTML-Mail besteht aus `style`-Attributen. Sie läuft in einem `srcdoc`-Frame, der die CSP der Seite erbt, und Attribute können keine Nonce tragen; ohne die Ausnahme sähe fast jede HTML-Mail kaputt aus. Risiko: Ein eingeschleustes `style`-Attribut kann die Darstellung ändern, aber kein Skript ausführen und keine Stylesheets oder Verbindungen laden (`style-src-elem` bleibt streng, `connect-src`, `img-src` begrenzt). Der Frame ist `sandbox`, ohne Skripte, mit eigener CSP. Die Alternative, Mails über eine eigene Route mit eigener CSP auszuliefern, ist ein größerer Umbau von Mailansicht, Druck und Kurzansicht. |
| 5 | CSP mit Nonce je Anfrage | Android (Capacitor) und Desktop (Wails) behalten die CSP im `<meta>` mit `script-src 'self'`, ohne Nonce und mit `style-src 'unsafe-inline'`. | Die Dateien liegen lokal, kein Server rendert sie, eine Nonce wäre fest eingebaut und damit wertlos. Kein Inline-Skript ist erlaubt. |
| 6 | Testlauf mit `Report-Only` (`konzept/PORTIERUNG.md`, Risiken) | Die CSP gilt sofort. | `WEB.md` verlangt `Report-Only` nicht, und die Oberfläche wurde lokal im Browser ohne Verstoß geprüft. |
| 7 | Migrationen nur vorwärts (`BETRIEB.md`) | Es gibt zusätzlich den Notweg `tuxmail keys rollback`. | Einmaliger Rückweg für dieses Release, nur von Hand bei gestopptem Dienst. Beim Start läuft nichts rückwärts. Wiederherstellungscodes, die seit dem Upgrade erzeugt wurden, gehen dabei verloren; der Befehl nennt die Konten. |
| 8 | Wiederherstellungscodes per HMAC (`PARAMETER.md`) | Neue Codes ja. Bestehende liegen als unter dem Schlüsselring verschlüsselter SHA-256 vor (`enc-sha256`), bis sie verbraucht oder neu erzeugt sind. | Ohne den Code lässt sich kein HMAC bilden, und die Codes sollen gültig bleiben. Ein Datenbank-Leck allein reicht trotzdem nicht mehr für einen Offline-Angriff. |
| 9 | Pepper außerhalb der DB (`PARAMETER.md`) | Pepper und Schlüssel der Audit-Kette liegen in `server_keys`, verschlüsselt unter dem Schlüsselring. | Der Ring liegt außerhalb der DB, ein Leck der DB allein verrät sie nicht. So rotieren sie mit und überleben den Schlüsselwechsel. |
| 10 | Gerätetoken wie API-Schlüssel mit HMAC und Pepper (`PARAMETER.md`, API-Schlüssel) | SHA-256 des Tokens, wie bei Sitzungen. | 256 Bit Zufall, offline nicht zu erraten. Das Format `<präfix>_<kennung>_…` gilt für API-Schlüssel, die TuxMail nicht hat. |
| 11 | `session_version` (`AUTH.md` §3) | Passwortwechsel und „überall abmelden“ löschen die Sitzungszeilen. | Gleiche Wirkung, Pflicht-Negativtest besteht (`TestAuthSection6HTTP`). |
| 12 | Basis-Module verwenden (`CLAUDE.md`) | `internal/secretbox` und `internal/auditchain` sind Kopien, Recovery-HMAC und Sperrliste ebenso. | Das Basis-Repository ist privat, TuxMail baut auf öffentlicher CI. Die Testvektoren aus `spec/testvektoren.json` laufen in den Tests. |
| 13 | Leerlauf je Anfrage (`AUTH.md` §3) | Nur Anfragen mit `X-TuxMail-Activity: 1` zählen, das setzt der Webclient bis 60 s nach einer Eingabe. | Ein offener Tab fragt im Hintergrund nach und hält den Event-Stream. Das darf den Leerlauf nicht aufheben. Das Lesen eines Textes ohne Eingabe zählt also nicht als Aktivität. |

| 14 | Herkunft nur aus `ALLOWED_ORIGINS` (`WEB.md`) | Neben `public_url` gilt auch das eigene `Host` der Anfrage (`originAllowed`). | Für den Direktzugriff ohne Proxy, etwa `http://127.0.0.1:8080` beim Einrichten. Eine fremde Seite kann ihr `Origin` nicht auf einen fremden `Host` passend setzen, außer über DNS-Rebinding; das `__Host-`-Cookie und das HMAC-Token gehören aber zum Host der echten Anmeldung. |
| 15 | Schlüssel nicht im Backup derselben Daten (`KRYPTO.md`) | Tägliche Einstellungs-Sicherung und Server-Sicherung enthalten `secretbox.json` und `master.key`. | Nur innerhalb der age-Verschlüsselung (vergleiche ADR 0007 Nr. 6): Ein Backup, das sich ohne getrennt gepflegte Schlüssel nicht einspielen lässt, ist im Ernstfall wertlos. Der private age-Schlüssel liegt nicht am Server. Unverschlüsselt nur nach ausdrücklicher Einstellung. |
| 16 | Pflicht-Checks (`BETRIEB.md`) | `mail` und `license` fehlen, `certificate` nur bei eigenem TLS. | TuxMail ist Mail-Client und verschickt keine eigene Systempost, Lizenzen gibt es nicht. Hinter nginx prüft das Zertifikat die Stelle, die es hält. |
| 17 | Schwellen der Health-Checks (ohne Wert in der Spezifikation) | Platz: `warn` unter 10 %, `fail` unter 2 %. Jobs: `fail` nach 1 h ohne Lauf. Zertifikat: `warn` 14 Tage vor Ablauf. | Eigene Werte, in `internal/server/server.go`. |
| 19 | Lokale 2FA gilt nicht für IdP-Konten, „wenn der IdP MFA nachweist (`amr`/`acr`)“ (`AUTH.md` §5) | Eine OIDC-Sitzung eines mit OIDC verknüpften Admins braucht kein TOTP, auch ohne `amr`/`acr`. | Viele IdP liefern die Angaben nicht oder uneinheitlich; eine Pflicht würde solche Admins beim Upgrade vor den Einrichtungsdialog stellen, obwohl die MFA am IdP liegt. Ein Passwort-Login desselben Admins ist nicht ausgenommen (NA-02). Der Betreiber verantwortet die MFA am IdP. Test: `TestOIDCAdminIsNotLockedOut`. |
| 18 | Reauth als `stepup_until` für 5 min (`AUTH.md` §3) | Jede sensible Aktion verlangt ihren eigenen Nachweis, ohne Zeitfenster. | Strenger als verlangt, und die vorhandenen Dialoge fragen ohnehin je Aktion. |

## Geräte-Token: Routen

Gerätetoken sind langlebig und liegen auf Geräten, die verloren gehen, und sie können sich nicht erneut ausweisen. Was
gesperrt bleibt, bietet die Android-App nicht als Knopf an, sondern als Hinweis „Geht aus Sicherheitsgründen nur im Browser“
mit Link zum Server (`BrowserOnlyHint`). `device_token_forbidden` ist in de und en übersetzt. Stand: Entscheidung vom 2026-11-07,
„Android ohne Einschränkung“, außer:

| Gruppe | Routen | Gerätetoken | Browser-Sitzung |
|---|---|---|---|
| Verwaltung | `/api/admin/*` | nie | Admin, 2FA-Pflicht |
| Kontoschutz | `/api/auth/password`, `/totp/*`, `/recovery-codes`, `/passkeys/*` (außer Liste), `/sessions/{id}`, `/sessions/revoke-others`, `/api/device/request`, `/api/device/approve` | nie | Reauth; `totp/confirm` folgt auf `totp/setup` mit Reauth, Passkey-Name ändern und das Ende der eigenen Sitzung brauchen keine |
| Schlüsselmaterial | PGP: `keys/{id}/secret`, `keys/{id}/passphrase`. PGP und S/MIME: Löschen eines Schlüssels mit privatem Teil | nie | wie bisher (PGP-Export verlangt die Passphrase) |
| OAuth-Clients | `PUT`/`DELETE /api/oauth/clients/{provider}` | nie | wie bisher |
| Team-Rechte | `PUT /api/team/{teamId}/grants` | nie | wie bisher |
| Geheimnisse im Archiv | `POST /api/backup`, `/backup/schedules` mit `secrets: true`, `/backup/restore` mit `secrets: true` oder `mode: replace` | nie, auch nicht mit Scope `provisioning` | Reauth |
| Versiegelte Geheimnisse | `GET /api/provisioning/secrets` | Scope `provisioning`, nur an den Schlüssel des Geräts | nein |
| Freigegeben | PGP und S/MIME: Vertrauen, `peers/accept`, `peers/verify`, `peers/delete`, Löschen fremder Schlüssel. Kontoeinstellungen samt Server unter der Passwort-Regel unten. `PUT /api/accounts/{id}/password`, PGP entsperren, Schlüssel erzeugen und einführen, Passwort eines eigenen Freigabelinks | ja | ja |

`POST /api/backup` mit `secrets: true` geht bewusst auch mit Scope `provisioning` nicht: Der Scope liefert Geheimnisse nur
versiegelt an den Schlüssel des Geräts, ein Archiv mit frei gewählter Passphrase dagegen jedem, der das Token hat.
`TestDeviceTokenSensitiveRoutesAreClassified` scheitert, wenn eine neue Route dieser Art nicht zugeordnet ist.

Restrisiko der Freigabe: Wer ein Gerätetoken stiehlt, kann Vertrauensentscheidungen ändern und fremde Schlüssel löschen, bis das
Token widerrufen ist. Er kommt so an keinen privaten Schlüssel und an kein Passwort.

## Regel für gespeicherte Zugänge

Ändert eine Anfrage Server, Port oder Benutzer eines gespeicherten Zugangs, oder schaltet sie auf unverschlüsselt, wird das
gespeicherte Passwort nicht verwendet; sie muss ein neues mitbringen (`password_required`, `internal/app/credentials.go`). Das gilt
für alle Clients und für Kontoeinstellungen, den Verbindungstest ungespeicherter Werte, ManageSieve, das WebDAV-Ziel geplanter
Sicherungen (Adresse, Benutzer) und den KI-Dienst (Anbieter, Basis-URL). OAuth-Konten können kein Passwort mitbringen: Ihr Token
geht nicht an einen anderen Server, sie werden neu angelegt. Ein eigener SMTP- oder ManageSieve-Server bekommt ein eigenes
Geheimnis, das Passwort des Eingangsservers bleibt. Zurück auf den Standard-ManageSieve-Server geht ohne Passwort, dessen Zugang
ist der des Kontos.

Nicht von der Regel erfasst: Eine Wiederherstellung mit `mode: replace` bringt Kontoeinstellungen aus dem Archiv neben die
gespeicherten Geheimnisse; sie ist daher für Gerätetoken gesperrt und verlangt im Browser Reauth. Wer sich dort ausweisen kann,
kennt das Konto.

## Noch offen

| Punkt | Grund |
|---|---|
| Admin-2FA nicht abschaltbar (`PARAMETER.md`: „Admins immer“) | Standard an (Entscheidung 2026-11-07), per `auth.totp_required_for_admins` abschaltbar. |
| Neue Sitzungs-ID bei Rechtewechsel | Bei Login und Step-up neu; beim Rechtewechsel erst mit der nächsten Anmeldung (vergleiche ADR 0007 Nr. 1). |
| Audit: Archivierung älterer Abschnitte, Zabbix-Item | Der neueste Hash geht täglich ins Log (`audit chain anchor`). Abschnitte und Monitoring-Vorlage fehlen. |
| `old_json`/`new_json` im Audit | Bestehende Felder liegen im Basis-Feld `new`. Alte Werte werden nicht erfasst. |
| Verschlüsselung der Mail-Datenbanken | Nicht in diesem Paket. |
| HIBP-Abfrage | Laut `PARAMETER.md` optional. |

## Sätze für die Basis-ADR

- Eine Konto-Sperre je Konto gilt nur je Netz (tuxmail: 5 Versuche); netzübergreifend tritt ab 50 Fehlversuchen statt einer Sperre eine Bremse in Kraft, damit Fremde den Inhaber nicht aussperren können.
- Native Anmelde-Endpunkte ohne Sitzungsbezug, die kein Cookie setzen und nur im Antwort-Body antworten (`/api/device/authorize|token|login`), sind von der CSRF-Prüfung ausgenommen.
- Bei der Rotation von Geräte-Token gilt das alte Token 60 s nach dem ersten Gebrauch des neuen weiter, ohne erneut rotieren zu dürfen; danach widerruft sein Gebrauch die Familie.
- Gespeicherte Passwörter gehen nur an den Server, Port und Benutzer, für den sie eingegeben wurden; wer das Ziel ändert, bringt ein neues Passwort mit.
- `style-src-attr 'unsafe-inline'` ist erlaubt, wo fremdes HTML in einem sandboxed `srcdoc`-Frame angezeigt wird, der die CSP der Seite erbt.

## Hinweis zum Zwischenstand

Commit `3ffbf7d` wurde nie veröffentlicht. Datenbanken von diesem Stand haben den Trigger `audit_log_no_null` nicht; `keys rollback`
kommt damit zurecht (`DROP TRIGGER IF EXISTS`), der Trigger selbst fehlt dort bis zu einer Neuinstallation.
