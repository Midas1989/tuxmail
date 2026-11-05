# Microsoft 365 / Exchange und Google in TuxMail

Stand: 03.11.2026 – betrifft `internal/oauth`, `internal/providers/{graph,ews,gmail,google}` (KONZEPT 7.1, 7.3, 7.3a, 7.4, 7.5).

Diese Seite erklärt, was der Projektinhaber bzw. ein Server-Administrator einrichten muss, welche Berechtigungen (Scopes) TuxMail anfordert, welche Grenzen es gibt und wie Client-IDs hinterlegt werden.

---

## 1. Überblick: Zugriffsarten

| Anbieter | Zugriffsart im Assistenten | Protokoll in TuxMail | Anmeldung |
|---|---|---|---|
| Microsoft 365 / Outlook.com | **Microsoft Graph (empfohlen)** | `internal/providers/graph` – Mail, Kalender, Kontakte, To Do, Abwesenheit, geteilte Postfächer | OAuth2 |
| | IMAP/SMTP mit OAuth2 | IMAP + SMTP mit XOAUTH2 (`outlook.office365.com`, `smtp.office365.com`) | OAuth2 |
| | Exchange ActiveSync | EAS-Provider (eigenes Modul) mit OAuth2-Token | OAuth2 |
| | Exchange Web Services | `internal/providers/ews` (nur noch bis zur Abschaltung in Exchange Online) | OAuth2 |
| Exchange On-Premises (2016/2019/SE) | EWS | `internal/providers/ews` | NTLM, Basic oder OAuth2 |
| Google / Gmail | **Gmail-API (empfohlen)** | `internal/providers/gmail` + Google Calendar / People (`internal/providers/google`) | OAuth2 |
| | IMAP/SMTP mit OAuth2 | `imap.gmail.com` / `smtp.gmail.com` mit XOAUTH2 | OAuth2 |
| | IMAP/SMTP mit App-Passwort | normales IMAP/SMTP | Passwort |

Ablauf im Konto-Assistenten: E-Mail-Adresse eingeben → die Autokonfiguration erkennt Microsoft oder Google → Zugriffsart wählen → **„Mit Microsoft anmelden“ / „Mit Google anmelden“** → Browser-Anmeldung → das Konto wird automatisch angelegt (Refresh-Token im Geheimnisspeicher, nie in der Datenbank).

Anmeldeverfahren:

- **Desktop:** Autorisierungscode-Fluss mit PKCE (RFC 7636); TuxMail öffnet den System-Browser und lauscht kurzzeitig auf `http://localhost:<zufälliger Port>` (Microsoft) bzw. `http://127.0.0.1:<Port>/callback` (Google), RFC 8252.
- **Server-Modus:** Web-Umleitung auf `https://<server>/api/oauth/{microsoft|google}/callback`.
- **Gerätecode** (RFC 8628) als Ausweichweg, nur Microsoft („Auf einem anderen Gerät anmelden“). Google erlaubt für Mail-Scopes keinen Gerätecode.

---

## 2. Was registriert werden muss

Die Schritt-für-Schritt-Anleitung für die App-Registrierungen steht in [AUFGABEN-PROJEKTINHABER.md](AUFGABEN-PROJEKTINHABER.md), Punkte 3 (Microsoft Entra ID) und 4 (Google Cloud). Kurzfassung und Ergänzungen:

### Microsoft Entra ID

- Kontotypen: „Konten in einem beliebigen Organisationsverzeichnis und persönliche Microsoft-Konten“ (Mandant `common`).
- Plattform **„Öffentlicher Client/nativ“** mit Umleitungs-URI `http://localhost` (Microsoft akzeptiert dann jeden Port), **„Öffentliche Clientflows zulassen“ = Ja** (für den Gerätecode).
- Server-Modus: zusätzlich Plattform **„Web“** mit `https://<domain>/api/oauth/microsoft/callback` und einem Client-Secret (Datei, siehe 4.2).
- Delegierte Berechtigungen (siehe Abschnitt 3). Für geteilte Postfächer zusätzlich die `.Shared`-Varianten eintragen.

### Google Cloud

- Gmail API, Google Calendar API und People API aktivieren.
- OAuth-Client vom Typ **„Desktop-App“** (Client-ID **und** Client-Secret – Google betrachtet das Secret bei Desktop-Apps als nicht geheim).
- Server-Modus: zusätzlich Typ **„Webanwendung“** mit `https://<domain>/api/oauth/google/callback`.

---

## 3. Angeforderte Berechtigungen (Scopes)

TuxMail fordert nur die Scopes der gewählten Zugriffsart an.

**Immer (Microsoft):** `offline_access openid email profile`

| Zugriffsart | Microsoft-Scopes |
|---|---|
| Graph | `User.Read`, `Mail.ReadWrite`, `Mail.ReadWrite.Shared`, `Mail.Send`, `Mail.Send.Shared`, `MailboxSettings.ReadWrite`, `Calendars.ReadWrite`, `Calendars.ReadWrite.Shared`, `Contacts.ReadWrite`, `Contacts.ReadWrite.Shared`, `Tasks.ReadWrite` (alle `https://graph.microsoft.com/…`) |
| IMAP/SMTP | `https://outlook.office.com/IMAP.AccessAsUser.All`, `https://outlook.office.com/SMTP.Send` |
| EAS | `https://outlook.office.com/EAS.AccessAsUser.All` |
| EWS | `https://outlook.office.com/EWS.AccessAsUser.All` |

Microsoft stellt Zugriffstoken **pro Ressource** aus (Graph bzw. `outlook.office.com`). TuxMail speichert ein Refresh-Token und holt die Zugriffstoken je Ressource automatisch nach; Microsoft rotiert das Refresh-Token dabei, TuxMail speichert jeweils das neue.

**Google** (alle Zugriffsarten): `https://mail.google.com/`, `https://www.googleapis.com/auth/calendar`, `https://www.googleapis.com/auth/contacts`, `openid email profile` – mit `access_type=offline&prompt=consent`, damit Google ein Refresh-Token ausgibt.

---

## 4. Client-IDs hinterlegen

Reihenfolge, in der TuxMail eine Client-ID sucht:

1. **Eigene Client-ID des Benutzers** (Einstellungen → **Anmeldedienste**), sofern der Administrator das nicht verboten hat.
2. **Administrator-Vorgabe** (Server-Konfiguration, nur Server-Modus).
3. **Umgebungsvariablen**.
4. **Eingebaute TuxMail-Registrierung** (sobald der Projektinhaber die Client-IDs geliefert hat; Release-Builds setzen sie per `-ldflags "-X github.com/Midas1989/Mailclient/internal/oauth.builtinMSClientID=…"`, ebenso `builtinGoogleClientID` / `builtinGoogleClientSecret`).

> **Hinweis zum Google-Client-Secret (Sicherheitsprüfung NC-08, bewusst so):**
> Google verlangt für „Desktop-App“-Registrierungen ein Client-Secret im
> Token-Request, behandelt es aber ausdrücklich nicht als vertraulich (es steht
> in jedem Desktop-Client). Release-Builds des Desktop-Clients dürfen es daher
> per `-ldflags` einkompilieren (`TUXMAIL_BUILD_GOOGLE_CLIENT_SECRET` in
> `build/lib.sh`). Server- und Docker-Builds enthalten es **nicht**
> (`go_ldflags_server`, `build/package-server.sh`, `build/docker/Dockerfile`);
> Server-Betreiber hinterlegen ihre eigene Registrierung (Abschnitt 4.1/4.2).
> Microsoft nutzt einen öffentlichen Client ohne Secret. Das Secret gehört nie
> in das Repository oder in Logs; im CI nur als Secret/Variable des
> Release-Jobs setzen.

Ist nichts davon gesetzt, zeigt der Assistent: *„Die Anmeldung mit Microsoft/Google ist noch nicht eingerichtet …“* (Fehlercode `oauth_not_configured`) mit einem Knopf zu den Anmeldediensten.

### 4.1 Umgebungsvariablen (Desktop und Server)

| Variable | Bedeutung |
|---|---|
| `TUXMAIL_MS_CLIENT_ID` | Anwendungs-ID (Client-ID) der Entra-App |
| `TUXMAIL_MS_CLIENT_SECRET` | nur für vertrauliche Web-Clients (Server-Modus) |
| `TUXMAIL_MS_TENANT` | `common` (Standard), `organizations`, `consumers` oder Mandanten-ID |
| `TUXMAIL_GOOGLE_CLIENT_ID` | Client-ID der Google-App |
| `TUXMAIL_GOOGLE_CLIENT_SECRET` | Client-Secret (Desktop-App: nicht geheim) |

### 4.2 Server-Konfiguration (`/etc/tuxmail/config.yaml`)

```yaml
oauth:
  microsoft:
    client_id: "00000000-0000-0000-0000-000000000000"
    client_secret_file: /etc/tuxmail/ms-client-secret   # Web-Plattform
    tenant: organizations                                # nur Firmenkonten
  google:
    client_id: "1234-abc.apps.googleusercontent.com"
    client_secret_file: /etc/tuxmail/google-client-secret
  allow_user_clients: true        # Benutzer dürfen eigene Client-IDs eintragen
  graph_notifications: false      # Graph-Änderungsbenachrichtigungen (siehe 6)
```

Die Umleitungs-URIs ergeben sich aus `public_url`: `<public_url>/api/oauth/microsoft/callback` und `<public_url>/api/oauth/google/callback`; die Einstellungsseite zeigt sie zum Kopieren an.

### 4.3 Eigene Client-ID (Einstellungen → Anmeldedienste)

Für Power-User und Google-Workspace-Firmen: eigene App registrieren (Google: Typ „Intern“ ist ohne Google-Prüfung möglich) und Client-ID (bei Google zusätzlich das Secret, bei Microsoft optional den Mandanten) eintragen. Das Secret liegt im Geheimnisspeicher (Schlüsselbund bzw. Tresor), nicht in der Datenbank. Wichtig: Ein Refresh-Token gehört zur Client-ID, mit der es ausgestellt wurde – nach einem Wechsel der Client-ID muss man sich bei bestehenden Konten neu anmelden.

---

## 5. Grenzen und bekannte Einschränkungen

**Google**

- Der volle Mail-Zugriff (`https://mail.google.com/`) ist ein *eingeschränkter Bereich*. Ohne Verifizierung bleibt die App im **Testmodus**: höchstens **100 eingetragene Testnutzer**, und Refresh-Tokens **laufen nach 7 Tagen ab** – TuxMail meldet dann „Anmeldung abgelaufen“ (`oauth_reauth_required`) und bietet die erneute Anmeldung an.
- Für die öffentliche Freigabe verlangt Google jährlich eine **CASA-Tier-2-Sicherheitsprüfung** (ca. 3.000–4.500 USD/Jahr, mehrere Wochen). Alternativen: App-Passwort + IMAP, eigene Client-ID, Workspace-App vom Typ „Intern“.
- Push über Pub/Sub wird nicht genutzt; Gmail wird über die History-API abgefragt (Intervall, Standard 60 s für den Posteingang).
- Benutzerlabels erscheinen als Ordner **und** als Schlagworte; „Verschieben“ entfernt das Quelllabel und setzt das Ziellabel, Papierkorb nutzt `trash/untrash`.

**Microsoft**

- In manchen Organisationen muss ein **Administrator der App zustimmen** (`oauth_consent_required`); empfohlen ist die Herausgeberüberprüfung (AUFGABEN Punkt 3).
- **EWS wird in Exchange Online abgeschaltet** – für Microsoft 365 Graph verwenden, EWS nur für On-Premises.
- Graph-Delta-Abfragen liefern geänderte und neue Nachrichten gleich; nach einem Neustart meldet TuxMail bis dahin unbekannte Nachrichten als „neu“, die Synchronisation erkennt vorhandene über die Message-ID.
- Per MIME angelegte Nachrichten (Entwürfe, kontenübergreifendes Verschieben) bleiben in Exchange als Entwurf markiert (Graph-Einschränkung).
- Gesendete Mails legt Exchange selbst im Gesendet-Ordner ab (bei „Senden als“ im Gesendet-Ordner des geteilten Postfachs).
- Drosselung (HTTP 429) wird mit `Retry-After` beantwortet; mehrere Operationen werden per `$batch` (20 je Anfrage) gebündelt.

**Geteilte Postfächer (KONZEPT 7.3a)**

- Einstellungen → Anmeldedienste → „Geteiltes Postfach hinzufügen“: Adresse eingeben, Sendeart wählen. Das Postfach erscheint als eigenes Konto, nutzt aber die Anmeldung des Besitzerkontos.
- **Senden als**: `From` = geteiltes Postfach, Versand über `/users/{postfach}/sendMail`.
- **Senden im Auftrag von**: `From` = geteiltes Postfach, `Sender` = eigene Adresse, Versand über `/me/sendMail`.
- Kalenderfreigaben (`calendarPermissions`) und Frei/Gebucht (`getSchedule`) stehen im Graph-Client zur Verfügung; die Oberfläche dafür folgt mit dem Kalendermodul.

**EWS On-Premises**

- Anmeldung per NTLM (Bibliothek `github.com/Azure/go-ntlmssp`, MIT), Basic oder OAuth2 (Hybrid/moderne Authentifizierung).
- Änderungen werden über Pull-Abonnements erkannt (Intervall); Streaming-Benachrichtigungen sind nicht umgesetzt.
- ExchangeImpersonation wird nicht verwendet; Stellvertretung läuft über das `Mailbox`-Element.

---

## 6. Graph-Änderungsbenachrichtigungen (optional, Server-Modus)

Mit `oauth.graph_notifications: true` und einer öffentlich erreichbaren `https://`-`public_url` abonniert TuxMail Änderungen am Posteingang (`/subscriptions`); Microsoft ruft `https://<server>/api/graph/notifications` auf. Ohne diese Option (und immer auf dem Desktop) fragt TuxMail die Ordnerzähler im Intervall ab.

---

## 7. Kalender, Kontakte, Aufgaben, Abwesenheit

- Graph: Kalender (`calendarView`, Serien über `instances`), Teilnehmer und Antworten (`accept`, `tentativelyAccept`, `decline`), Teams-Links (`isOnlineMeeting`), Kontakte, Microsoft To Do; Abwesenheitsnotiz über `mailboxSettings/automaticRepliesSetting`.
- Google: Calendar API v3 (Serien mit `singleEvents=true`, Antworten, Meet-Links), People API (Kontakte), Abwesenheit über `settings/vacation`.
- EWS: Standardkalender (CalendarView), Antworten, Kontakte, Abwesenheit über `Get/SetUserOofSettings`.
- „Kalender und Kontakte synchronisieren“ in den Einstellungen übernimmt die Daten in die lokalen Kalender-/Kontakttabellen (`internal/providers/pimapi`); das PIM-Modul kann diesen Adapter später durch seine eigene Synchronisation ersetzen.

---

## 8. Ohne echte Konten testen

Alle Provider haben Tests gegen nachgebaute Server (`httptest`): `internal/oauth` (PKCE, Loopback mit Test-Browser, Web-Umleitung, Gerätecode, Token-Erneuerung), `internal/providers/graph/graphtest`, `internal/providers/gmail/gmailtest` (inkl. Calendar/People), EWS mit SOAP-Golden-Dateien (`internal/providers/ews/testdata`) sowie ein Synchronisationstest mit einem Graph- und einem Gmail-Konto (`internal/sync/cloud_register_test.go`). Im Demo-Modus (Mock) ist Microsoft als „eingerichtet“ simuliert, Google nicht – so lassen sich beide Wege der Oberfläche ausprobieren.
