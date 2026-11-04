# Exchange ActiveSync (EAS) in TuxMail

Dieses Dokument beschreibt die EAS-Implementierung (`internal/providers/eas`), ihre Grenzen und wie sie gegen echte Server getestet wird. Grundlage sind die offenen Microsoft-Spezifikationen [MS-ASHTTP], [MS-ASWBXML], [MS-ASCMD], [MS-ASAIRS], [MS-ASEMAIL], [MS-ASCAL], [MS-ASCNTC], [MS-ASTASK], [MS-ASPROV] und [MS-ASDTYPE] (KONZEPT 7.2). Server-Eigenheiten wurden mit dem dokumentierten Verhalten des MPL-2.0-Projekts EAS-4-TbSync abgeglichen; Code wurde nicht übernommen.

## Aufbau

| Paket | Inhalt |
|---|---|
| `eas/wbxml` | WBXML-1.3-Codec mit allen EAS-Codepages 0–24, Inline-Strings, String-Tabelle, Opaque-Daten; Golden-Vektoren und Fuzz-Tests |
| `eas` | HTTP-Schicht (`Client`), Kommandos, `Provider` (`domain.MailProvider`), `Transport` (`domain.Transport`), PIM-Synchronisation (`SyncPIM`) |
| `eas/easstore` | Adapter `eas.PIMStore` → Tabellen `calendars`/`cal_objects`/`addressbooks`/`contacts` (iCalendar/vCard) |
| `eas/easvacation` | `vacation.ServerVacationSync` über Settings/Oof |
| `eas/eastest` | In-Process-Fake-Server (httptest, WBXML) für Tests |
| `sync/eas_register.go` | Provider-Factory für Kontotyp `eas`, Zustandsspeicher, Remote-Wipe, `SyncEASPIM` |
| `send/eas.go` | `EASFactory` (SendMail) und `KindFactory` (Transport je Kontotyp) |
| `autoconfig/autodiscover.go` | Microsoft Autodiscover (POX, JSON v2, SRV, Exchange Online) |

## Protokoll

- **Versionen:** Aushandlung per `OPTIONS` (`MS-ASProtocolVersions`), Präferenz 16.1 → 14.1 → 16.0 → 14.0 → 12.1 → 12.0 → 2.5. Erzwingen mit Konto-Extra `eas_version`, begrenzen mit `eas_max_version` (z. B. `14.1` für Server mit fehlerhafter 16.x-Umsetzung). Voll unterstützt: 12.1, 14.x, 16.x; 2.5 nur eingeschränkt (Mail über MIMESupport, kein ItemOperations, keine Kategorien).
- **Anfrage:** `POST /Microsoft-Server-ActiveSync?Cmd=…&User=…&DeviceId=…&DeviceType=TuxMail`, Header `MS-ASProtocolVersion`, `X-MS-PolicyKey`; optional Base64-kodierte Query ([MS-ASHTTP] 2.2.1.1.1.1, `ClientConfig.Base64Query`).
- **Authentifizierung:** Basic (On-Premises, Z-Push, Kopano, Grommunio, Horde, Zimbra …) oder OAuth2 Bearer (`TokenSource`, Pflicht für Exchange Online).
- **Fehlerbehandlung:** 401 → `ErrAuth`; 403 → `ErrForbidden`; 449 bzw. Status 142/143/144 → automatisches Re-Provisioning und Wiederholung; 451 → neue URL aus `X-MS-Location` (wird gespeichert); 503 mit `Retry-After` ≤ 30 s → transparente Wiederholung, sonst `*HTTPError` (temporär); Redirects von HTTPS auf HTTP werden abgelehnt.
- **Geräte-ID:** stabil je Konto (`TuxMail` + Hash aus Konto-ID und Benutzer, Override `eas_device_id`). Eine neue ID bedeutet für den Server ein neues Gerät (neues Provisioning, Vollsynchronisation).

### Kommandos

| Kommando | Verwendung |
|---|---|
| Provision | zweiphasig, Richtlinien werden über `OnPolicy` gemeldet; Bestätigung mit Status `1` (konfigurierbar `PolicyAckStatus`); DeviceInformation bei 14.1+ |
| Remote-Wipe | `RemoteWipe`/`AccountOnlyRemoteWipe`: `OnRemoteWipe` löscht **nur die Daten dieses Kontos** (`sync.WipeEASAccountData`: Ordner/Nachrichten, Kalender, Adressbücher; Konto wird deaktiviert), erst danach wird quittiert; der Client stellt den Verkehr ein (`ErrRemoteWiped`). Ohne Handler wird nicht quittiert |
| FolderSync, FolderCreate/Update/Delete | Hierarchie-SyncKey und Ordnerliste werden im Kontozustand gehalten (Status 9 → Neusynchronisation) |
| Sync | SyncKey je Ordner, WindowSize, MoreAvailable, BodyPreference (MIME, gekürzt auf 16 KiB für die Threading-Header), Add/Change/Delete/SoftDelete, lokale Änderungen mit `GetChanges=0` |
| GetItemEstimate | `Client.GetItemEstimate` |
| ItemOperations | `FetchRaw` (vollständiges MIME), `FetchAttachment` |
| MoveItems | `Move` (neue ServerIds) |
| SendMail, SmartReply, SmartForward | `Transport` (14.0+ ComposeMail-WBXML, sonst `message/rfc822`); Bcc wird aus den Envelope-Empfängern ergänzt; der Server legt die Mail in „Gesendete Elemente“ ab |
| MeetingResponse | `Client.MeetingResponse` (zusagen/vorläufig/absagen) |
| Search | Postfach (`SearchMailbox`) und GAL (`SearchGAL`) |
| Ping | `Watch`: ein gemeinsamer Long-Poll für alle beobachteten Ordner, Heartbeat-Aushandlung (Status 5), MaxFolders (Status 6), FolderSync bei Status 7, Backoff bei Fehlern |
| Settings | OOF lesen/schreiben (`easvacation`), DeviceInformation, UserInformation |
| ResolveRecipients | `Client.ResolveRecipients` |

### Abbildung auf TuxMail

- **Ordner:** `RemoteID` = Pfad aus ServerIds (`"5/1a"`, Trenner `/`, `/` und `%` maskiert) – Umbenennen ändert die ID nicht. Rollen über den EAS-Ordnertyp (Posteingang, Entwürfe, Gelöschte Elemente, Gesendete Elemente), Junk/Archiv über den Namen. Kalender-, Kontakt-, Aufgaben- und Postausgangsordner erscheinen nicht in der Mailordnerliste.
- **Nachrichten:** Referenz = ServerId; Message-ID/In-Reply-To/References/Thread-Index aus dem gekürzten MIME-Kopf, `Email2:ConversationId` → `ServerThreadID`.
- **Flags:** `Read` ↔ `\Seen`, `Flag` ↔ `\Flagged`, `LastVerbExecuted` → `\Answered`/`$Forwarded` (nur lesend), `IsDraft` → `\Draft`. **Kategorien ↔ Tags** über die Keyword-Abbildung des Sync-Moduls (`store.TagFromOutlookCategory`/`KeywordMapper`), schreibend ab 14.0.
- **Löschen:** `Delete` löscht endgültig (`DeletesAsMoves=0`); das Verschieben in den Papierkorb übernimmt das Sync-Modul.
- **Zustand:** Der Ordnerzustand (`domain.SyncState`) enthält SyncKey und Flags je Element (EAS-Changes können unvollständig sein). Kontoweiter Zustand (Policy-Key, URL, Version, Hierarchie, SyncKeys nach eigenen Änderungen) liegt in `eas.StateStore`; in der App `sync.RegisterEAS(reg, sync.EASOptions{State: sync.EASStoreState{Store: st}, OnRemoteWipe: …})` verwenden. Ohne persistenten Zustand (`DefaultRegistry`) führt jeder Neustart zu FolderSync 0 und damit zu einer Vollsynchronisation.

### Kalender, Kontakte, Aufgaben

`Client.SyncPIM(ctx, pimStore, accountID, opts)` synchronisiert zweiseitig: zuerst lokale Änderungen (Sync Add/Change/Delete), dann Serveränderungen; bei Konflikten gewinnt der Server (KONZEPT 9.2). `PIMOptions.ReadOnly` schaltet auf einseitige Synchronisation. Die Schnittstelle `eas.PIMStore` ist bewusst klein, damit das PIM-Modul eigene Modelle anbinden kann; `easstore` bildet sie auf die vorhandenen Tabellen ab (Konventionen siehe Paketdokumentation: `Href` = ServerId, `ETag` = Hash des zuletzt synchronisierten iCalendar/vCard, `SyncToken` = JSON mit SyncKey und bekannten IDs). Wiederholungen werden als RRULE gespeichert, `cal_instances` enthält nur das erste Vorkommen – die Serienexpansion erfolgt im PIM-Modul. `sync.SyncEASPIM` ist ein Komfortaufruf für die App (periodisch neben dem Mail-Worker).

### Autodiscover

`autoconfig.Discoverer.UseAutodiscover = true` (opt-in; Rang nach Mozilla-Autoconfig, vor DNS-SRV). Reihenfolge: JSON v2 `https://autodiscover.<domain>/autodiscover/autodiscover.json/v1.0/<mail>?Protocol=ActiveSync`, POX `https://<domain>/autodiscover/autodiscover.xml`, POX `https://autodiscover.<domain>/…`, unauthentifizierter HTTP-Redirect, SRV `_autodiscover._tcp.<domain>`, zuletzt Exchange Online JSON (`outlook.office365.com`) – nur wenn der MX auf `*.mail.protection.outlook.com` zeigt. POX-Redirects (`Action/Redirect`) und HTTP-Redirects (nur auf HTTPS) werden verfolgt; Basic-Auth nur über HTTPS (`AutodiscoverPassword`). Ergebnis: Kandidat `kind: "eas"` mit `easUrl`; bei Exchange Online `oauthProvider: "microsoft"`.

## Einbindung in die App

`internal/app/eas.go` registriert den Provider mit `EASStoreState` und Remote-Wipe (`WipeEASAccountData`), verwendet `send.KindFactory` mit `EASFactory` (gemeinsamer Zustand), synchronisiert Kalender/Kontakte/Aufgaben aller EAS-Konten alle 15 Minuten (`App.SyncEASPIM`) und liefert mit `App.EASVacationServers` die Einträge für `vacation.Config.Servers`. Der Kontoassistent nutzt Autodiscover und legt Konten vom Typ `eas` an (`easUrl`, Benutzername auch `DOMÄNE\benutzer`, Passwort; `incoming.auth = "oauth2"` benötigt das OAuth-Modul).

## Bekannte Einschränkungen

- **Append:** EAS kann keine beliebigen Nachrichten hochladen. Ab 16.0 wird Append in den Entwürfe-Ordner als Entwurf (Sync Add mit Email2/AirSyncBase, inkl. Anhängen) umgesetzt; sonst `ErrNotSupported` (permanent). Kopieren in andere Ordner und das Ablegen gesendeter Mails übernimmt der Server.
- `\Answered`/`$Forwarded` lassen sich nicht setzen (nur der Server setzt `LastVerbExecuted` bei SmartReply/SmartForward).
- Kategorien werden ab 14.0 geschrieben; bei unbekanntem Kategorienstand eines Elements (z. B. nach Neustart ohne vorherigen Sync) wird der neue Satz ohne alte Kategorien gesendet.
- Richtlinien (PIN, Verschlüsselung) werden angezeigt, aber nicht erzwungen; die Bestätigung meldet standardmäßig „angewendet“ wie andere Desktop-Clients. Server mit `AllowNonProvisionableDevices=false` können das Gerät dennoch ablehnen.
- Zeitzonen von Terminen: die EAS-TIME_ZONE_INFORMATION wird als `X-EAS-TIMEZONE` erhalten; neue lokale Termine werden in UTC gesendet. Ausnahmen von Serien werden nur als gelöschte Vorkommen (EXDATE) abgebildet.
- Notizen (Notes), DocumentLibrary, RightsManagement und ValidateCert sind im Codec vorhanden, aber nicht als Funktionen umgesetzt.
- EAS 2.5: keine Kategorien, kein Search-BodyPreference, Kalender/Kontakte nur mit Basiseigenschaften.
- Freigegebene Postfächer (KONZEPT 7.3a) sind über EAS nicht verfügbar (EAS kennt nur das eigene Postfach) → Graph/EWS.

## Tests

Automatisch (`go test ./...`, offline): WBXML-Golden-Vektoren und Fuzzing (`go test -fuzz FuzzUnmarshal ./internal/providers/eas/wbxml`), End-to-End gegen `eastest` (Provisioning inkl. 449, Policy-Refresh, Remote-Wipe, Redirect 451, 503, Base64-Query, FolderSync/Create/Update/Delete, Sync mit SyncKey-Fortschritt und Paging, Flags/Kategorien, Move, Delete, FetchRaw, SendMail/SmartReply, Ping-Weckruf und Heartbeat-Aushandlung, OOF, Search/GAL, MeetingResponse, Entwürfe 16.1, PIM zweiseitig) sowie ein Sync-Manager-Test mit EAS-Konto (Store, Konversationen, Push, Flags, Verschieben, Remote-Wipe).

### Gegen echte Server

1. **Z-Push / Grommunio / Kopano** (Basic): Konto mit `kind: "eas"`, `incoming.host` = Server, `incoming.username`, Passwort im Secret-Store. Grommunio liefert eine Testumgebung als Docker-Image/Appliance; Z-Push lässt sich mit dem IMAP-Backend vor einem Dovecot betreiben. Prüfen: Ordnerliste, Ping-Push (Mail von außen senden), Flags in Webmail sichtbar, Kategorien (Grommunio), Kalender/Kontakte mit `SyncEASPIM`, OOF.
2. **Exchange On-Premises** (2016/2019/SE): Basic oder NTLM-Proxy mit Basic; Richtlinie mit PIN-Pflicht testen (Provisioning), Remote-Wipe aus dem ECP („Gerät löschen“ bzw. „Nur Kontodaten löschen“ ab 16.1).
3. **Exchange Online:** benötigt OAuth2 mit der Microsoft-App-Registrierung aus `docs/AUFGABEN-PROJEKTINHABER.md` (Abschnitt 3, Berechtigung `EAS.AccessAsUser.All` von „Office 365 Exchange Online“, Scope `https://outlook.office365.com/EAS.AccessAsUser.All offline_access`). Endpunkt `https://outlook.office365.com/Microsoft-Server-ActiveSync`, Benutzername = E-Mail-Adresse. Basic-Auth ist abgeschaltet. Mandanten können EAS oder Drittanbieter-Clients sperren.

Nur mit echten Servern verifizierbar: Verhalten bei Status 12/13 (Hierarchieänderung, leere Anfrage), Ping-Timeouts hinter Proxys/NAT, MIME-Kürzung bei sehr großen Kopfzeilen, Kategorien-Schreibrechte, Draft-Erstellung (16.x), Base64-Query-Akzeptanz, Policy-Bestätigung bei strikten Richtlinien, AccountOnlyRemoteWipe.
