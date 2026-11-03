<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Server-Erweiterungen für die Android-App

Die App (`mobile/`, Thin Client nach KONZEPT 15.9 Stufe A) nutzt die vorhandene
Server-API aus `internal/server` (Geräteanmeldung, Bearer-Token, Web Push).
Folgendes fehlt serverseitig noch bzw. sollte ergänzt werden. Am Go-Code wurde
für die App **nichts** geändert.

## 1. Push-Registrierung: `POST /api/push/subscribe`

Die App sendet immer ein Feld `type`. Heute ignoriert der Server unbekannte
Felder, deshalb funktioniert UnifiedPush bereits ohne Änderung.

### UnifiedPush (foss-Variante) – funktioniert schon

```json
{ "type": "unifiedpush",
  "endpoint": "https://ntfy.example.org/upAbC123?up=1",
  "keys": { "p256dh": "<base64url>", "auth": "<base64url>" },
  "deviceName": "TuxMail Android (Pixel 8)" }
```

UnifiedPush-Endpunkte sind normale Web-Push-Endpunkte (RFC 8030); der
Connector erzeugt die RFC-8291-Schlüssel und entschlüsselt selbst. Die App
registriert sich mit dem VAPID-Schlüssel aus `GET /api/push/vapid-public-key`
(Feld `publicKey`), daher akzeptieren Verteiler wie ntfy die VAPID-signierten
Nachrichten des vorhandenen Senders. Empfehlungen:

- `type` speichern (Spalte `kind`: `webpush` | `unifiedpush` | `fcm`), damit die
  Geräteliste „Android (UnifiedPush)“ anzeigen kann.
- Chats/Termine mit `Urgency: high` senden (sonst verzögert Doze die Zustellung).

### FCM (play-Variante) – neu nötig

```json
{ "type": "fcm", "token": "<FCM registration token>",
  "deviceName": "TuxMail Android (Pixel 8)", "appId": "org.tuxmail.app" }
```

- `push.ValidateSubscription` lehnt das heute mit `400 invalid_subscription` ab
  (kein https-Endpunkt/keine Schlüssel); die App zeigt dann „Server unterstützt
  diesen Push-Dienst noch nicht“.
- Versand über die **FCM HTTP v1 API**:
  `POST https://fcm.googleapis.com/v1/projects/{project_id}/messages:send`
  mit OAuth2-Zugriffstoken aus einem Service-Account
  (Scope `https://www.googleapis.com/auth/firebase.messaging`).
  Konfiguration z. B. `push.fcm.project_id`, `push.fcm.credentials_file`.
- Nur **Data-Messages** senden (keine `notification`-Nachricht), damit die App
  Kanäle, Gruppierung und Schnellantwort selbst darstellt:

  ```json
  { "message": { "token": "<token>",
      "data": { "payload": "<dieselbe JSON wie beim Web Push>" },
      "android": { "priority": "HIGH", "ttl": "86400s", "collapse_key": "<tag>" } } }
  ```
- Antworten `404`/`UNREGISTERED` bzw. `INVALID_ARGUMENT` für das Token →
  Registrierung löschen (wie heute bei Web Push `410 Gone`).
- Datenschutzmodus (KONZEPT 15.7): bei „nur Neue Nachricht“ `body`/`sender`
  weglassen – FCM-Inhalte laufen über Google.

### Abmelden: `DELETE /api/push/subscribe`

UnifiedPush: `{ "endpoint": "<url>" }` (wie heute). FCM: die App sendet
`{ "endpoint": "fcm:<token>", "type": "fcm", "token": "<token>" }` – der Server
sollte FCM-Registrierungen über `token` (oder die Pseudo-URL) finden.

## 2. Push-Payload: optionale Zusatzfelder

`push.Payload` (`type, title, body, url, tag, accountId, conversationId`) wird
unverändert verstanden. Für bessere native Benachrichtigungen zusätzlich:

| Feld | Zweck |
|---|---|
| `sender` | Anzeigename des Absenders (Chat-Verlauf im MessagingStyle, Gruppenchats) |
| `itemId` | Nachricht, auf die die Schnellantwort antwortet (`replyToItemId`) |
| `type` | Kanal: `mail` → Mail, `chat` → Chats, `calendar`/`event`/`invitation` → Termine, sonst Erinnerungen |

`conversationId` muss numerisch sein (Gruppierung pro Konversation, Tippen öffnet
`tuxmail://open?conversationId=…`).

## 3. Aktionen aus der Benachrichtigung (ohne geöffnete App)

Authentifiziert mit `Authorization: Bearer <Geräte-Token>`:

- `POST /api/conversations/{id}/reply` – **neu**, Body
  `{ "text": "…", "replyToItemId": "…" }` → sendet im Kanal der Konversation
  (Mail: Antwort an den letzten Absender mit Standard-Identität; Messenger: in
  den Chat). Antwort `2xx`, sonst zeigt die App „Nicht gesendet“.
- `POST /api/conversations/{id}/read` – bereits in der Frontend-API vorgesehen
  (`HttpTransport.markConversationRead`), serverseitig noch umzusetzen.

## 4. WebSocket `/api/events` mit Geräte-Token

Browser-WebSockets können keinen `Authorization`-Header setzen. Die App öffnet

```
GET /api/events
Sec-WebSocket-Protocol: tuxmail.v1, tuxmail.bearer.<token>
```

Der Server muss das Token aus dem Subprotokoll lesen, wie ein Bearer-Token
prüfen und in der Antwort **`Sec-WebSocket-Protocol: tuxmail.v1`** setzen
(sonst bricht der Client die Verbindung ab). Das Token nie loggen. Eine
Origin-Prüfung muss bei Token-Authentifizierung `https://localhost` (WebView der
App) zulassen – ohne Cookies gibt es kein CSWSH-Risiko.

## 5. HTTP-Aufrufe / CORS

Die App aktiviert `CapacitorHttp`: `fetch` zu fremden Servern läuft über den
nativen HTTP-Stack, CORS-Header sind daher **nicht** nötig. Alle
Nicht-GET-Anfragen tragen `Authorization: Bearer`, die CSRF-Middleware nimmt
sie bereits aus. Falls CapacitorHttp später abgeschaltet wird, bräuchte der
Server CORS für `Origin: https://localhost` (nur ohne Cookies, `Authorization`
als erlaubter Header).

## 6. Geräteanmeldung: Bestätigungsseite `/device`

`verification_uri(_complete)` zeigt auf `<public_url>/device?user_code=…`. Die
Handler (`GET /api/device/request`, `POST /api/device/approve`) existieren, die
**Web-Oberfläche hat aber noch keine Seite `/device`** – bis dahin funktioniert
in der App nur die direkte Anmeldung (Benutzername/Passwort/TOTP). Die Seite
sollte Code, Gerätename und IP zeigen und nach der Bestätigung „Du kannst zur
App zurückkehren“ melden.

## 7. Komfort

- Web-Oberfläche: Link/QR-Code `tuxmail://connect?server=<public_url>` („In der
  Android-App öffnen“) – die App übernimmt die Adresse in „Server verbinden“.
- `POST /api/auth/logout` mit Bearer-Token widerruft die Gerätesitzung bereits;
  die Geräteliste sollte Push-Registrierungen der Sitzung mit entfernen.
