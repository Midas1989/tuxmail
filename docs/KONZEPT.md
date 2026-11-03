# Entwicklungskonzept: Unified Communication Client

**Produktname:** *TuxMail* – E-Mail, Messenger, Kalender, Kontakte und Cloud-Dateien in einer Anwendung
**Zielplattformen:** Windows 10/11 (x64, ARM64), Linux (x64, ARM64)
**Stand:** 03.11.2026 – Version 1.2 des Konzepts (Entscheidungen aus Kapitel 23 eingearbeitet)

---

## Inhaltsverzeichnis

1. [Zusammenfassung & Empfehlung](#1-zusammenfassung--empfehlung)
2. [Anforderungen](#2-anforderungen)
3. [Technologie-Entscheidung: Go oder Python?](#3-technologie-entscheidung-go-oder-python)
4. [Gesamtarchitektur](#4-gesamtarchitektur)
5. [Datenhaltung & Datenmodell](#5-datenhaltung--datenmodell)
6. [Kernstück: Konversations-Mapping (Mail ↔ Messenger-Ansicht)](#6-kernstück-konversations-mapping-mail--messenger-ansicht)
7. [E-Mail-Protokolle & Kontotypen](#7-e-mail-protokolle--kontotypen)
8. [E-Mail-Funktionen](#8-e-mail-funktionen)
9. [Kalender & Aufgaben](#9-kalender--aufgaben)
10. [Kontakte & Personen-Zusammenführung](#10-kontakte--personen-zusammenführung)
11. [Messenger-Integration (WhatsApp, Signal, Instagram, Facebook, Google)](#11-messenger-integration)
12. [Cloud-Anhänge: Nextcloud, OpenCloud, Vaultwarden Send](#12-cloud-anhänge-nextcloud-opencloud-vaultwarden-send)
13. [Import & Export](#13-import--export)
14. [Backup & Restore](#14-backup--restore)
15. [Portabilität, Installation & Updates](#15-portabilität-installation--updates)
16. [Benutzeroberfläche & UX](#16-benutzeroberfläche--ux)
17. [Sicherheit & Datenschutz](#17-sicherheit--datenschutz)
18. [Rechtliche Aspekte & Lizenzen](#18-rechtliche-aspekte--lizenzen)
19. [Projektstruktur (Repository)](#19-projektstruktur-repository)
20. [Teststrategie & Qualitätssicherung](#20-teststrategie--qualitätssicherung)
21. [Roadmap & Aufwandsschätzung](#21-roadmap--aufwandsschätzung)
22. [Risiken & Gegenmaßnahmen](#22-risiken--gegenmaßnahmen)
23. [Offene Entscheidungen](#23-offene-entscheidungen)

---

## 1. Zusammenfassung & Empfehlung

Ziel ist ein **lokal laufender Desktop-Client**, der klassische E-Mail-Arbeit (Ordner, Signaturen, Aliase, Regeln) mit einer **Messenger-artigen Konversationsansicht** verbindet – und zwar für E-Mails *und* echte Messenger (WhatsApp, Signal, optional Instagram/Facebook, Google). Kalender, Aufgaben und Kontakte sind vollständig integriert. Anhänge können wahlweise direkt oder als passwortgeschützter Freigabelink über Nextcloud, OpenCloud oder Vaultwarden Send verschickt werden.

**Meine Empfehlungen in Kürze:**

| Thema | Empfehlung | Begründung (Kurz) |
|---|---|---|
| Backend-Sprache | **Go** | Einzelnes Binary, Cross-Compiling, ausgezeichnete Bibliotheken für IMAP/SMTP/CalDAV/CardDAV und – entscheidend – die einzigen ausgereiften WhatsApp-/Signal-/Meta-Bibliotheken (`whatsmeow`, `signalmeow`, `messagix`) sind in Go geschrieben. |
| GUI | **Wails** (Go + System-WebView) mit **Svelte 5 + TypeScript** | Moderne Oberfläche mit Web-Technologie, aber ohne Electron-Ballast (~15–25 MB statt 150 MB+). Nutzt WebView2 (Windows) bzw. WebKitGTK (Linux). |
| Datenbank | **SQLite** (WAL-Modus, FTS5-Volltextsuche), optional verschlüsselt | Ein-Datei-Datenbank, perfekt für Portabilität und Backup. |
| Erweiterbarkeit | **Connector-Plugins als eigene Prozesse** (gRPC über stdio) | Absturzsicherheit, saubere Lizenztrennung (AGPL-Bibliotheken), einzeln (de)aktivierbar. |
| Auslieferung | **Portabel als Standard**, zusätzlich Installer | Windows: eine `.exe` + Datenordner. Linux: AppImage. Zusätzlich MSI/winget, Flatpak, .deb/.rpm. |
| Microsoft | **Microsoft Graph** (M365), **ActiveSync** (EAS 16.1), **EWS** nur für On-Premises-Exchange | Microsoft schaltet EWS in Exchange Online ab (angekündigt ab 01.10.2026) – Graph ist dort der Zukunftsweg. |

**Meine Einschätzung zur Machbarkeit:** Das Projekt ist ambitioniert, aber realistisch umsetzbar, wenn es **in Phasen** gebaut wird. Der E-Mail-Kern inklusive Konversationsansicht, Kalender und Kontakte ist technisch solide planbar. Die Messenger-Anbindungen (insbesondere Meta/Instagram/Facebook) basieren größtenteils auf **inoffiziellen Protokollimplementierungen** und tragen ein dauerhaftes Wartungs- und Kontosperr-Risiko – sie sollten daher als optionale Plugins umgesetzt werden (siehe Kapitel 11 und 18).

---

## 2. Anforderungen

### 2.1 Funktionale Anforderungen

| ID | Bereich | Anforderung | Priorität |
|---|---|---|---|
| M-01 | Mail | IMAP/IMAPS, POP3/POP3S, SMTP/SMTPS/STARTTLS | Muss |
| M-02 | Mail | Exchange ActiveSync (EAS) | Muss |
| M-03 | Mail | Microsoft-Protokolle: Microsoft Graph, EWS (On-Prem) | Muss |
| M-04 | Mail | Google (Gmail API bzw. IMAP + OAuth2) | Muss |
| M-05 | Mail | JMAP (Fastmail, Stalwart u. a.) | Kann |
| M-06 | Mail | Ordner anlegen, umbenennen, verschieben, löschen, abonnieren | Muss |
| M-07 | Mail | Mehrere Signaturen (HTML/Text) pro Identität, automatisch wählbar | Muss |
| M-08 | Mail | Alias-/Absenderadressen (Identitäten) inkl. eigenem SMTP-Server je Identität | Muss |
| M-09 | Mail | Manuelle Server-Konfiguration + Autokonfiguration | Muss |
| M-10 | Mail | Klassische Ansicht (3-Spalten) **und** Messenger-Ansicht, nahtlos umschaltbar | Muss |
| M-11 | Mail | Persistente Beziehungstabelle (Antwort, Weiterleitung, …) – auch nach Verschieben | Muss |
| M-12 | Mail | Import/Export (EML, MBOX, Maildir, PST-Import) | Muss |
| M-13 | Mail | Filter/Regeln (lokal + Sieve) | Soll |
| M-14 | Mail | OpenPGP (inkl. Autocrypt, WKD) | Muss |
| M-15 | Mail | S/MIME | Soll |
| M-16 | Mail | Lokaler Spam-Filter, Ein-Klick-Newsletter-Abmeldung | Muss |
| M-17 | Mail | Smarte Kategorien (lokal) | Soll |
| M-18 | Mail | Rechtschreibprüfung, Serienmails | Muss |
| M-19 | Mail | Exchange-Delegation, geteilte Postfächer, Send-As/On-Behalf | Soll |
| M-20 | Mail | KI-Assistenz optional: lokal (Ollama) oder Cloud (OpenAI, Gemini, DeepSeek u. a.) | Kann |
| K-04 | Kalender | Videokonferenz-Links (Nextcloud Talk, Jitsi, Teams) | Soll |
| K-01 | Kalender | Volle Integration: Termine, Serien, Einladungen, Erinnerungen, Aufgaben | Muss |
| K-02 | Kalender | CalDAV, Google Calendar, Microsoft Graph, EAS, EWS, lokale Kalender | Muss |
| K-03 | Kalender | Import/Export iCalendar (.ics) | Muss |
| C-01 | Kontakte | CardDAV, Google People, Graph, EAS, lokal | Muss |
| C-02 | Kontakte | Import/Export vCard 3.0/4.0, CSV (Outlook/Google-Format) | Muss |
| C-03 | Kontakte | Zusammenführen von Personen über alle Kanäle (Mail, Telefon, WhatsApp, Signal …) | Soll |
| X-01 | Messenger | WhatsApp (mehrere Konten) | Soll |
| X-02 | Messenger | Signal (mehrere Konten) | Soll |
| X-03 | Messenger | Instagram, Facebook Messenger | Kann (experimentell) |
| X-04 | Messenger | Google Messages (RCS/SMS), Google Chat (Workspace) | Kann |
| F-01 | Dateien | Nextcloud & OpenCloud: Dateien als Anhang holen und Anhänge dort speichern | Muss |
| F-02 | Dateien | Anhang wahlweise direkt oder als Freigabelink mit Passwort/Ablaufdatum | Muss |
| F-03 | Dateien | Vaultwarden (Bitwarden) Send als Versandweg | Soll |
| B-01 | System | Backup & Restore: komplett oder nur Konfiguration | Muss |
| B-02 | System | Mehrere Konten in **allen** Bereichen | Muss |
| B-03 | System | Portabler Betrieb ohne Installation | Soll |

### 2.2 Nicht-funktionale Anforderungen

- **Performance:** Start < 2 s, Ordnerwechsel < 100 ms (lokaler Cache), flüssige Listen mit 500.000+ Nachrichten (virtualisierte Listen).
- **Offline-Fähigkeit:** Lesen, Suchen, Entwürfe, Verschieben offline möglich; Aktionen werden in einer Warteschlange nachsynchronisiert.
- **Sicherheit:** Zugangsdaten nie im Klartext; HTML-Mails in isolierter Sandbox; externe Inhalte standardmäßig blockiert.
- **Barrierefreiheit:** Tastaturbedienung vollständig, Screenreader-Unterstützung (ARIA), Kontraste nach WCAG 2.1 AA.
- **Mehrsprachigkeit:** Deutsch und Englisch ab Version 1, i18n-Framework von Beginn an.
- **Wartbarkeit:** Klare Trennung Kern / Protokolle / Oberfläche; automatisierte Tests gegen echte Testserver.

---

## 3. Technologie-Entscheidung: Go oder Python?

### 3.1 Vergleich

| Kriterium | Go | Python |
|---|---|---|
| Portable Auslieferung | ✅ Ein statisches Binary pro Plattform | ⚠️ PyInstaller/Nuitka möglich, aber groß, langsamer Start, Virenscanner-Fehlalarme |
| Nebenläufigkeit (viele Konten, IDLE, Push) | ✅ Goroutinen – ideal für hunderte parallele Verbindungen | ⚠️ asyncio funktioniert, aber GIL und komplexere Fehlersuche |
| IMAP/SMTP/MIME | ✅ `emersion/go-imap v2`, `go-smtp`, `go-message`, `go-sasl` | ✅ `imaplib`, `aioimaplib`, `email` |
| CalDAV/CardDAV | ✅ `emersion/go-webdav`, `go-ical`, `go-vcard` | ✅ `caldav`, `vobject` |
| **WhatsApp** | ✅ **`whatsmeow`** (Referenzimplementierung, aktiv gepflegt) | ❌ nur veraltete/instabile Bibliotheken |
| **Signal** | ✅ **`signalmeow`** (mautrix-signal, Go + libsignal) | ⚠️ nur über externes `signal-cli` (Java) |
| **Instagram/Facebook** | ✅ **`messagix`** (mautrix-meta) | ❌ nichts Brauchbares |
| Google Messages | ✅ `libgm` (mautrix-gmessages) | ❌ |
| Desktop-GUI | ✅ Wails (WebView), Fyne, Gio | ⚠️ Qt (PySide6) – gut, aber schwer portabel und LGPL-Pflichten |
| Speicherverbrauch | ✅ gering | ⚠️ höher |

### 3.2 Entscheidung

**Go für das Backend** ist hier klar die sinnvollste Wahl. Das wichtigste Argument: Die Messenger-Anbindungen existieren in ausgereifter Form *nur* in Go (aus dem mautrix-/Matrix-Bridge-Ökosystem). Mit Python müssten diese über Fremdprozesse eingebunden werden – damit hätte man die Nachteile beider Welten.

**Für die Oberfläche** wird Web-Technologie (Svelte 5 + TypeScript + Tailwind CSS) in einem nativen WebView verwendet, eingebettet über **Wails**. Das liefert eine moderne, animierte, gut gestaltbare UI bei kleinem Binary.

| Schicht | Technologie |
|---|---|
| Backend / Kern | Go 1.25+ |
| Desktop-Rahmen | Wails v3 (bei Projektstart prüfen, sonst v2) |
| Frontend | Svelte 5, TypeScript, Tailwind CSS, bits-ui/Melt-UI-Komponenten, TanStack Virtual für Listen |
| Datenbank | SQLite (`modernc.org/sqlite`, rein Go, kein CGO) bzw. SQLCipher bei Verschlüsselung |
| Volltextsuche | SQLite FTS5 (trigram + unicode61 Tokenizer) |
| Plugin-IPC | gRPC über stdio (`hashicorp/go-plugin`) |
| Build | `task` (Taskfile), GoReleaser, GitHub Actions |

> **Alternative (bewusst verworfen):** Tauri (Rust) – gute Technik, aber die Messenger-Bibliotheken müssten dann trotzdem als Go-Sidecars laufen. Electron – zu groß und speicherhungrig für eine Anwendung, die dauerhaft im Hintergrund läuft.

---

## 4. Gesamtarchitektur

### 4.1 Schichtenmodell

```
┌──────────────────────────────────────────────────────────────────────┐
│                   Frontend (Svelte 5 / TypeScript)                   │
│  Mail-Klassisch │ Konversation │ Kalender │ Kontakte │ Dateien │ ⚙   │
└───────────────────────────────▲──────────────────────────────────────┘
                                │ Wails-Bindings (typisiert) + Event-Bus
┌───────────────────────────────┴──────────────────────────────────────┐
│                         Application Layer (Go)                       │
│  MailService · ConversationService · CalendarService · ContactService│
│  ComposeService · AttachmentService · SearchService · BackupService  │
├──────────────────────────────────────────────────────────────────────┤
│                           Domain Core (Go)                           │
│  Threading-Engine · Personen-Resolver · Regel-Engine · Op-Queue      │
│  Sync-Scheduler · Benachrichtigungen · Secret-Store                  │
├───────────────┬───────────────┬──────────────────┬───────────────────┤
│ Mail-Provider │ PIM-Provider  │ Storage-Provider │ Messenger-Plugins │
│ IMAP · POP3   │ CalDAV        │ Nextcloud        │ (eigene Prozesse) │
│ SMTP · JMAP   │ CardDAV       │ OpenCloud        │ WhatsApp          │
│ EAS · Graph   │ Google APIs   │ Vaultwarden Send │ Signal            │
│ EWS · Gmail   │ Graph/EAS/EWS │ Lokal/WebDAV     │ Meta · Google Msg │
├───────────────┴───────────────┴──────────────────┴───────────────────┤
│        Persistenz: SQLite (Metadaten, FTS) + Blob-Store (Dateien)    │
└──────────────────────────────────────────────────────────────────────┘
```

### 4.2 Wichtige Architekturprinzipien

1. **Local-First:** Die Oberfläche liest *ausschließlich* aus der lokalen Datenbank. Synchronisation läuft im Hintergrund. Dadurch ist die UI immer schnell und offline nutzbar.
2. **Operations-Queue (Op-Log):** Jede Benutzeraktion (verschieben, markieren, löschen, senden) wird zuerst lokal angewendet und als Operation in eine persistente Warteschlange geschrieben. Ein Worker spielt sie gegen den Server ab – mit Wiederholung, Backoff und Konfliktauflösung.
3. **Provider-Interfaces:** Jeder Kontotyp implementiert dieselben Go-Interfaces. Der Kern kennt keine Protokolldetails.
4. **Headless-fähiger Kern + Web-Client (Version 1.0):** Der Kern läuft auch ohne GUI (`tuxmail serve`). Das Svelte-Frontend ist dasselbe für Desktop und Browser; es spricht über eine **Transport-Abstraktion** mit dem Kern – im Desktop über Wails-Bindings, im Web über HTTPS + WebSocket (siehe 4.5). Ein Mobil-Client kann später dieselbe API nutzen.
5. **Event-getrieben:** Änderungen erzeugen Events (`message.added`, `conversation.updated`, …), die per Event-Bus an die UI gepusht werden – kein Polling in der Oberfläche.

### 4.3 Provider-Interfaces (Auszug)

```go
// MailProvider wird von IMAP, EAS, Graph, EWS, JMAP, Gmail implementiert.
type MailProvider interface {
    Capabilities() MailCaps                       // z. B. MOVE, IDLE, OBJECTID, THREAD
    ListFolders(ctx context.Context) ([]Folder, error)
    CreateFolder(ctx context.Context, parent FolderRef, name string) (Folder, error)
    RenameFolder(ctx context.Context, f FolderRef, newName string) error
    DeleteFolder(ctx context.Context, f FolderRef) error
    Sync(ctx context.Context, f FolderRef, state SyncState) (SyncDelta, SyncState, error)
    FetchBody(ctx context.Context, ref RemoteRef) (io.ReadCloser, error)
    Move(ctx context.Context, refs []RemoteRef, to FolderRef) ([]RemoteRef, error)
    SetFlags(ctx context.Context, refs []RemoteRef, add, remove []Flag) error
    Delete(ctx context.Context, refs []RemoteRef, permanent bool) error
    Append(ctx context.Context, f FolderRef, raw io.Reader, flags []Flag) (RemoteRef, error)
    Watch(ctx context.Context, f FolderRef) (<-chan ChangeHint, error) // IDLE / Push / Ping
}

type Transport interface { // SMTP, Graph sendMail, EAS SendMail, EWS CreateItem
    Send(ctx context.Context, id Identity, msg *OutgoingMessage) (SentResult, error)
}

type CalendarProvider interface { /* ListCalendars, SyncEvents, PutEvent, DeleteEvent, RespondInvite, FreeBusy */ }
type ContactProvider  interface { /* ListAddressBooks, SyncContacts, PutContact, DeleteContact */ }
type StorageProvider  interface { /* Browse, Download, Upload, CreateShare(opts ShareOptions) */ }
type MessengerProvider interface { /* Pair (QR), ListChats, SyncHistory, Send, React, MarkRead, Events */ }
```

### 4.4 Prozessmodell

```
tuxmail.exe (Hauptprozess: GUI + Kern)
 ├─ plugin-whatsapp   (je Plugin ein Prozess, verwaltet n Konten)
 ├─ plugin-signal
 ├─ plugin-meta       (experimentell, standardmäßig deaktiviert)
 └─ plugin-gmessages
```

Alle Plugins liegen als Binaries neben der Hauptanwendung (`plugins/`). Der Kern startet sie bei Bedarf, überwacht sie (Health-Check, automatischer Neustart) und kommuniziert per gRPC. Ein Absturz eines Messenger-Plugins reißt damit nie den Mail-Client mit.

### 4.5 Betriebsarten: Desktop und Server/Web (Version 1.0)

| Betriebsart | Läuft wo | Zugriff | Typischer Einsatz |
|---|---|---|---|
| **Desktop (lokal)** | Windows/Linux-PC, portabel oder installiert | Wails-Fenster | Einzelplatz, alles lokal |
| **Server (Web)** | Linux-Server/NAS als Docker-Container (`tuxmail serve`) | Browser (HTTPS), optional Desktop-App als „Thin Client" | Zugriff von überall, Messenger 24/7 online |
| **Desktop + Bridge** | Desktop-Client lokal, Messenger über Matrix-Bridges auf dem Server | Wails-Fenster | Messenger auch bei ausgeschaltetem PC, Mails lokal |

**API-Design:** Alle Application Services werden als typisierte API beschrieben (Protobuf/Connect-RPC oder OpenAPI) und daraus Go-Server- und TypeScript-Client-Code generiert. Dieselben Aufrufe laufen
- im Desktop direkt in-process über Wails-Bindings,
- im Server-Modus über **HTTPS (Connect-RPC/JSON) + WebSocket** für Live-Events.

**Server-Modus – Zusatzanforderungen:**
- Benutzerverwaltung (mehrere Benutzer pro Server, strikt getrennte Datenbanken je Benutzer), Anmeldung per Passwort + TOTP/WebAuthn oder **OIDC** (z. B. Nextcloud, OpenCloud, Authentik, Keycloak).
- TLS über Reverse-Proxy (Caddy/Traefik) oder eingebautes ACME (Let's Encrypt).
- Sitzungsverwaltung, CSRF-Schutz, Rate-Limits, Audit-Log.
- Web-Push-Benachrichtigungen (PWA) – der Web-Client ist als installierbare **Progressive Web App** nutzbar, auch auf dem Smartphone.
- Lieferform: Docker-Image + `docker-compose.yml` (TuxMail, optional Matrix-Homeserver und mautrix-Bridges).

---

## 5. Datenhaltung & Datenmodell

### 5.1 Speicherorte

```
<Datenverzeichnis>/
├── tuxmail.db              # SQLite: Metadaten, Beziehungen, Index
├── tuxmail.db-wal
├── blobs/                  # Inhaltsadressierter Speicher (SHA-256)
│   ├── ab/cd/abcd…eml      # Rohmails (komprimiert, zstd)
│   └── 12/34/1234…bin      # Anhänge, Medien aus Messengern
├── plugins/<name>/         # Sitzungsdaten der Messenger (je Konto)
├── secrets.vault           # Nur im portablen Modus (siehe 17.2)
├── logs/
└── settings.json           # Nur Bootstrap-Einstellungen; Rest in DB
```

- **Installiert:** Windows `%APPDATA%\TuxMail`, Linux `$XDG_DATA_HOME/tuxmail` (meist `~/.local/share/tuxmail`).
- **Portabel:** `./data/` neben der ausführbaren Datei (siehe Kapitel 15).

Rohmails werden **inhaltsadressiert** gespeichert: Dieselbe Mail in zwei Ordnern (z. B. Gmail-Labels) belegt nur einmal Platz.

### 5.2 Kern-Schema (vereinfacht)

```sql
-- ==== Konten & Identitäten ===================================================
CREATE TABLE accounts (
  id            INTEGER PRIMARY KEY,
  kind          TEXT NOT NULL,         -- imap|pop3|eas|graph|ews|jmap|gmail|caldav|carddav|
                                       -- nextcloud|opencloud|vaultwarden|whatsapp|signal|meta|...
  display_name  TEXT NOT NULL,
  color         TEXT,
  config_json   TEXT NOT NULL,         -- Server, Ports, TLS, Auth-Methode (ohne Geheimnisse)
  secret_ref    TEXT,                  -- Verweis in Keyring/Vault
  enabled       INTEGER NOT NULL DEFAULT 1,
  sort_order    INTEGER
);

CREATE TABLE identities (               -- Absender/Aliase
  id              INTEGER PRIMARY KEY,
  account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name            TEXT NOT NULL,
  email           TEXT NOT NULL,
  reply_to        TEXT,
  bcc_self        INTEGER DEFAULT 0,
  smtp_account_id INTEGER REFERENCES accounts(id), -- optional eigener Ausgangsserver
  default_signature_id INTEGER REFERENCES signatures(id),
  reply_signature_id   INTEGER REFERENCES signatures(id),
  sent_folder_id  INTEGER REFERENCES folders(id),
  drafts_folder_id INTEGER REFERENCES folders(id),
  is_default      INTEGER DEFAULT 0
);

CREATE TABLE signatures (
  id        INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  html      TEXT,
  plain     TEXT,
  placement TEXT DEFAULT 'below_reply'  -- above_quote|below_reply
);

-- ==== Ordner =================================================================
CREATE TABLE folders (
  id           INTEGER PRIMARY KEY,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  parent_id    INTEGER REFERENCES folders(id),
  remote_id    TEXT NOT NULL,          -- IMAP-Pfad, EAS ServerId, Graph-ID ...
  name         TEXT NOT NULL,
  role         TEXT,                   -- inbox|sent|drafts|trash|junk|archive|NULL
  uidvalidity  INTEGER,
  sync_state   TEXT,                   -- MODSEQ/HIGHESTMODSEQ, EAS SyncKey, Graph deltaLink
  UNIQUE(account_id, remote_id)
);

-- ==== Nachrichten: Identität getrennt vom Speicherort =========================
CREATE TABLE messages (                 -- EINE Zeile pro logischer Nachricht
  id             INTEGER PRIMARY KEY,
  account_id     INTEGER NOT NULL REFERENCES accounts(id),
  message_id_hdr TEXT,                  -- RFC 5322 Message-ID (normalisiert)
  fingerprint    TEXT NOT NULL,         -- Fallback-Hash, falls Message-ID fehlt
  server_obj_id  TEXT,                  -- EMAILID (RFC 8474), X-GM-MSGID, Graph immutableId
  subject        TEXT,
  from_addr      TEXT,
  sent_at        INTEGER,
  received_at    INTEGER,
  size           INTEGER,
  blob_hash      TEXT,                  -- Verweis in blobs/
  snippet        TEXT,
  has_attachments INTEGER DEFAULT 0,
  flags          INTEGER DEFAULT 0,     -- seen, flagged, answered, forwarded, draft ...
  conversation_id INTEGER REFERENCES conversations(id),
  state          TEXT DEFAULT 'live',   -- live|orphaned|deleted
  orphaned_since INTEGER
);
CREATE INDEX ix_msg_mid  ON messages(account_id, message_id_hdr);
CREATE INDEX ix_msg_fp   ON messages(account_id, fingerprint);
CREATE INDEX ix_msg_conv ON messages(conversation_id, sent_at);

CREATE TABLE message_locations (        -- WO liegt die Nachricht (n Orte möglich)
  message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  folder_id    INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
  remote_ref   TEXT NOT NULL,           -- IMAP UID, EAS ServerId, Graph-ID
  PRIMARY KEY (folder_id, remote_ref)
);

CREATE TABLE message_addresses (
  message_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
  role       TEXT,                      -- from|to|cc|bcc|reply_to
  name       TEXT,
  email      TEXT,
  person_id  INTEGER REFERENCES persons(id)
);

CREATE TABLE attachments (
  id          INTEGER PRIMARY KEY,
  message_id  INTEGER REFERENCES messages(id) ON DELETE CASCADE,
  filename    TEXT, mime_type TEXT, size INTEGER,
  content_id  TEXT,                     -- für Inline-Bilder
  blob_hash   TEXT,                     -- NULL = noch nicht geladen
  cloud_link_id INTEGER REFERENCES cloud_links(id)
);

-- ==== Beziehungen & Konversationen (Kapitel 6) ================================
CREATE TABLE message_relations (
  id          INTEGER PRIMARY KEY,
  source_key  TEXT NOT NULL,            -- normalisierte Message-ID der NEUEN Mail
  target_key  TEXT NOT NULL,            -- normalisierte Message-ID der BEZUGS-Mail
  kind        TEXT NOT NULL,            -- reply|forward|reference|draft_of|redirect|manual|invite_update
  origin      TEXT NOT NULL,            -- header|thread_index|server_thread|compose|heuristic|user
  confidence  REAL NOT NULL DEFAULT 1.0,
  created_at  INTEGER NOT NULL,
  UNIQUE(source_key, target_key, kind)
);

CREATE TABLE conversations (
  id            INTEGER PRIMARY KEY,
  kind          TEXT NOT NULL,          -- email_thread|chat|person_view
  title         TEXT,
  last_activity INTEGER,
  unread_count  INTEGER DEFAULT 0,
  pinned        INTEGER DEFAULT 0,
  muted         INTEGER DEFAULT 0,
  archived      INTEGER DEFAULT 0
);

CREATE TABLE conversation_overrides (   -- manuelle Benutzerentscheidungen
  message_key     TEXT PRIMARY KEY,
  conversation_id INTEGER REFERENCES conversations(id),
  action          TEXT                  -- split|merge|pin_to
);

-- ==== Personen (kanalübergreifend) ===========================================
CREATE TABLE persons (
  id INTEGER PRIMARY KEY, display_name TEXT, avatar_blob TEXT, is_me INTEGER DEFAULT 0
);
CREATE TABLE person_handles (
  person_id INTEGER REFERENCES persons(id) ON DELETE CASCADE,
  kind      TEXT,                       -- email|phone|whatsapp|signal|instagram|facebook
  value     TEXT,                       -- normalisiert (E.164, lowercase ...)
  verified  INTEGER DEFAULT 0,
  PRIMARY KEY (kind, value)
);

-- ==== Messenger ==============================================================
CREATE TABLE chats (
  id INTEGER PRIMARY KEY,
  account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  remote_id TEXT, kind TEXT,            -- direct|group|channel
  conversation_id INTEGER REFERENCES conversations(id),
  UNIQUE(account_id, remote_id)
);
CREATE TABLE chat_messages (
  id INTEGER PRIMARY KEY,
  chat_id INTEGER REFERENCES chats(id) ON DELETE CASCADE,
  remote_id TEXT, sender_person_id INTEGER, sent_at INTEGER,
  body TEXT, reply_to_remote_id TEXT, media_blob TEXT, media_mime TEXT,
  status TEXT,                          -- sent|delivered|read|failed
  reactions_json TEXT, edited_at INTEGER, deleted INTEGER DEFAULT 0,
  UNIQUE(chat_id, remote_id)
);

-- ==== Kalender, Aufgaben, Kontakte (Kurzform) ================================
CREATE TABLE calendars   (id INTEGER PRIMARY KEY, account_id INTEGER, remote_id TEXT, name TEXT, color TEXT, sync_token TEXT, read_only INTEGER);
CREATE TABLE cal_objects (id INTEGER PRIMARY KEY, calendar_id INTEGER, uid TEXT, kind TEXT /*event|todo|journal*/,
                          etag TEXT, ical TEXT /*Original-iCalendar*/, dtstart INTEGER, dtend INTEGER, rrule TEXT, summary TEXT);
CREATE TABLE cal_instances (object_id INTEGER, start INTEGER, end INTEGER); -- vorberechnete Serien im Sichtfenster
CREATE TABLE addressbooks (id INTEGER PRIMARY KEY, account_id INTEGER, remote_id TEXT, name TEXT, sync_token TEXT);
CREATE TABLE contacts    (id INTEGER PRIMARY KEY, addressbook_id INTEGER, uid TEXT, etag TEXT, vcard TEXT,
                          person_id INTEGER REFERENCES persons(id), display_name TEXT);

-- ==== Cloud-Freigaben ========================================================
CREATE TABLE cloud_links (
  id INTEGER PRIMARY KEY, account_id INTEGER, provider TEXT, remote_path TEXT,
  share_id TEXT, url TEXT, password_ref TEXT, expires_at INTEGER, created_at INTEGER
);

-- ==== Operations-Queue =======================================================
CREATE TABLE ops (
  id INTEGER PRIMARY KEY, account_id INTEGER, kind TEXT, payload TEXT,
  state TEXT DEFAULT 'pending', attempts INTEGER DEFAULT 0, next_try_at INTEGER, last_error TEXT
);

-- ==== Volltextsuche ==========================================================
CREATE VIRTUAL TABLE fts_items USING fts5(subject, body, addresses, attachments,
  content='', tokenize='unicode61 remove_diacritics 2');
```

**Wichtigster Designpunkt:** `messages` (logische Identität) ist von `message_locations` (physischer Speicherort) getrennt. Verschieben ändert nur den Ort – die Nachricht, ihre Beziehungen und ihre Konversationszugehörigkeit bleiben unverändert.

---

## 6. Kernstück: Konversations-Mapping (Mail ↔ Messenger-Ansicht)

### 6.1 Ziel

Jede Mail gehört zu genau einer **Konversation**. Die Zugehörigkeit ergibt sich aus Beziehungen (Antwort, Weiterleitung, Referenz, Einladungs-Update, manuelle Verknüpfung). Diese Beziehungen werden **dauerhaft** in `message_relations` gespeichert – unabhängig davon, in welchem Ordner die Mail liegt oder ob sie verschoben wird. Beide Ansichten (klassisch und Messenger) arbeiten auf denselben Daten; jede Aktion in einer Ansicht ist sofort in der anderen sichtbar.

### 6.2 Stabile Nachrichtenidentität

Damit Beziehungen ein Verschieben überleben, braucht jede Mail einen stabilen Schlüssel – nicht die IMAP-UID (die sich beim Verschieben ändert):

| Priorität | Schlüssel | Quelle |
|---|---|---|
| 1 | `Message-ID`-Header (normalisiert: `<…>` entfernt, Kleinschreibung der Domain) | RFC 5322 |
| 2 | Server-Objekt-ID, die Verschieben überlebt | IMAP `OBJECTID`/`EMAILID` (RFC 8474), Gmail `X-GM-MSGID`, Graph `immutableId`, JMAP Email-ID |
| 3 | Fingerprint = SHA-256 über (Date, From, To, Subject, Größe des Textteils, Hash der ersten 4 KB des Bodys) | Fallback für Mails ohne Message-ID |

### 6.3 Erkennung von Beziehungen

| Quelle | Beziehungstyp | Konfidenz |
|---|---|---|
| `In-Reply-To` | reply | 1.0 |
| `References` | reference (letzter Eintrag ≙ reply) | 1.0 |
| Eigene Aktion „Antworten" / „Weiterleiten" im Client | reply / forward (wird **beim Senden** gespeichert) | 1.0 |
| Gmail `X-GM-THRID`, Graph `conversationId`, EAS `ConversationId`, Outlook `Thread-Index` | reference (Server-Thread) | 0.95 |
| Weitergeleitete Mail als `message/rfc822`-Anhang → deren Message-ID | forward | 1.0 |
| Inline-Weiterleitung („---------- Forwarded message ----------"): Absender, Datum, Betreff im Zitatblock werden extrahiert und gegen die DB gematcht | forward | 0.8 |
| iCalendar `UID` (Einladung, Update, Absage, Antwort) | invite_update | 1.0 |
| Betreff-Heuristik (gleicher normalisierter Betreff ohne Re:/AW:/Fwd:/WG:, gleiche Teilnehmer, ≤ 14 Tage) | heuristic | 0.5 (abschaltbar) |
| Benutzer verknüpft/trennt manuell | manual | 1.0 (hat Vorrang) |

**Weiterleitungen beim Senden:** Wenn der Benutzer weiterleitet, setzt der Client optional (konfigurierbar) auch `References: <Original-ID>`. So erkennen auch andere Clients die Zusammengehörigkeit – und bei einer Antwort auf die Weiterleitung landet alles im selben Strang.

### 6.4 Threading-Algorithmus

1. Neue Mail eingehend → stabilen Schlüssel ermitteln → Beziehungen aus Headern/Server-IDs extrahieren → in `message_relations` schreiben (auch wenn die Bezugs-Mail noch **nicht** lokal vorhanden ist; die Beziehung wird gespeichert und greift, sobald sie eintrifft).
2. Konversation bestimmen: Union-Find über alle Beziehungen mit Konfidenz ≥ Schwellwert (Standard 0.8). Basis ist der bewährte **JWZ-Threading-Algorithmus**, erweitert um die zusätzlichen Beziehungstypen.
3. Treffen zwei bestehende Konversationen zusammen (z. B. eine Mail referenziert beide) → **Merge**, ältere Konversations-ID bleibt.
4. `conversation_overrides` werden zuletzt angewendet – eine manuelle Trennung überschreibt jede automatische Erkennung.
5. Für die **Personen-Ansicht** (Messenger-Stil „alles mit Person X") wird zusätzlich über `message_addresses.person_id` aggregiert – unabhängig von Threads.

### 6.5 Verschieben, Löschen, Wiederfinden

```
Mail wird verschoben (im Client):
  → message_locations wird aktualisiert (über COPYUID/MOVE-Antwort, RFC 4315/6851)
  → messages, relations, conversation: unverändert ✔

Mail wird extern verschoben (anderer Client, Webmail, Serverregel):
  1. Sync bemerkt: UID in Ordner A verschwunden
     → location entfernt; hat die Nachricht keine Orte mehr → state = 'orphaned', orphaned_since = now
  2. Sync in Ordner B findet neue Mail
     → Lookup über Message-ID / OBJECTID / Fingerprint
     → Treffer: neue location anlegen, state = 'live' ✔ (Beziehungen bleiben erhalten)
  3. Garbage Collection: Nachrichten, die > 7 Tage (konfigurierbar) 'orphaned' sind
     und in keinem Ordner wieder auftauchen → state = 'deleted'.
     Die Einträge in message_relations bleiben trotzdem erhalten (sie hängen am Schlüssel,
     nicht an der Zeile) – taucht die Mail z. B. per Import wieder auf, ist sie sofort korrekt eingeordnet.
```

### 6.6 Messenger-Ansicht für E-Mails

- **Blasen-Darstellung:** Eigene Mails rechts, fremde links, gruppiert nach Absender und Zeit.
- **Zitat-Entfernung:** Zitierter Verlauf (`>`-Zeilen, „Am … schrieb …", Outlook-Header-Blöcke, `<blockquote>`) und Signaturen werden erkannt und eingeklappt – die Blase zeigt nur den **neuen** Text. Ein Klick zeigt die Originalmail vollständig.
- **Anhänge** als Karten in der Blase (Vorschau für Bilder/PDF), Cloud-Links als Karte mit Symbol.
- **Antwortfeld unten** wie im Messenger: Schreiben erzeugt eine vollwertige, korrekte Mail (richtige Identität, `In-Reply-To`/`References`, `Re:`-Betreff, Zitat optional nach Einstellung, Signatur). Mit einem Klick auf „Erweitern" öffnet sich der volle Editor (CC/BCC, Formatierung, Betreff).
- **Gemischte Konversationen:** In der Personen-Ansicht können Mails, WhatsApp- und Signal-Nachrichten derselben Person in einem Zeitstrahl erscheinen (mit Kanal-Symbol je Blase). Beim Antworten wählt man den Kanal (Standard: zuletzt genutzter).
- **Nahtloser Wechsel:** Die aktuelle Auswahl (markierte Mail ↔ zugehörige Konversation/Blase) bleibt beim Umschalten erhalten (`Strg+Shift+V`). Die Ansicht ist global, pro Konto oder pro Ordner einstellbar.

---

## 7. E-Mail-Protokolle & Kontotypen

### 7.1 Übersicht

| Protokoll | Bibliothek / Umsetzung | Mail | Kalender | Kontakte | Push |
|---|---|---|---|---|---|
| IMAP4rev1/rev2 (+ TLS/STARTTLS) | `emersion/go-imap/v2` | ✅ | – | – | IDLE / NOTIFY |
| POP3 (+ TLS) | eigene schlanke Implementierung | ✅ (nur Posteingang) | – | – | Intervall |
| SMTP / Submission (465/587) | `emersion/go-smtp` | Versand | – | – | – |
| JMAP (RFC 8620/8621) | `git.sr.ht/~rockorager/go-jmap` | ✅ | ✅ (JMAP Calendars, sofern Server) | ✅ | EventSource |
| CalDAV / CardDAV | `emersion/go-webdav`, `go-ical`, `go-vcard` | – | ✅ | ✅ | Sync-Token-Polling |
| **Exchange ActiveSync** (EAS 14.1 / 16.1) | **Eigenentwicklung** (WBXML-Codec + Kommandos) | ✅ | ✅ | ✅ | Ping (Long-Poll) |
| **Microsoft Graph** | `msgraph-sdk-go` bzw. schlanker eigener REST-Client | ✅ | ✅ | ✅ | Delta-Queries + Polling |
| **EWS** (Exchange On-Prem 2016/2019/SE) | Eigenentwicklung (SOAP) | ✅ | ✅ | ✅ | Streaming Notifications |
| **Gmail API / Google Calendar / People API** | `google.golang.org/api` | ✅ | ✅ | ✅ | History-API + Polling |
| Sieve (ManageSieve, RFC 5804) | eigene Implementierung | Serverregeln | – | – | – |

### 7.2 Exchange ActiveSync (EAS) – Details

Für EAS gibt es keine reife Go-Bibliothek; die Implementierung erfolgt eigenständig nach den offenen Microsoft-Spezifikationen **[MS-ASCMD], [MS-ASWBXML], [MS-ASHTTP], [MS-ASEMAIL], [MS-ASCAL], [MS-ASCNTC], [MS-ASTASK], [MS-ASPROV]**.

- Kommandos: `Provision`, `FolderSync`, `FolderCreate/Update/Delete`, `Sync`, `GetItemEstimate`, `ItemOperations`, `MoveItems`, `SendMail`, `SmartReply`, `SmartForward`, `MeetingResponse`, `Search`, `Ping`, `Settings`.
- Authentifizierung: Basic (On-Prem, Z-Push, Kopano, Grommunio, Zimbra …) und **OAuth2 (Pflicht für Exchange Online)**.
- Provisioning-Richtlinien (Remote-Wipe, PIN-Anforderungen) werden angezeigt; Remote-Wipe löscht nur die Kontodaten dieses Kontos, nie den ganzen Client.
- Aufwand realistisch: 2–3 Personenmonate für eine robuste Implementierung.

### 7.3 Microsoft 365 / Exchange Online

- **Bevorzugt: Microsoft Graph.** Microsoft hat die Abschaltung von EWS für Exchange Online angekündigt (ab 01.10.2026) – für M365-Konten wird daher Graph verwendet, EAS als Alternative.
- **IMAP/SMTP mit OAuth2 (XOAUTH2)** bleibt als Fallback möglich. Basic-Auth ist bei Exchange Online abgeschaltet.
- App-Registrierung in **Microsoft Entra ID** als *öffentlicher Client* (Multi-Tenant, ohne Client-Secret, PKCE, Loopback-Redirect `http://localhost:<port>`). In manchen Organisationen muss ein Admin der App zustimmen – der Client zeigt dafür einen verständlichen Hinweis.
- **EWS** wird für **On-Premises-Exchange** angeboten (z. B. Exchange Server SE), wo es weiterhin unterstützt wird.

### 7.3a Exchange-Delegation & geteilte Postfächer
- **Geteilte Postfächer** (Shared Mailboxes) und Postfächer, auf die der Benutzer Vollzugriff hat, werden als eigene Einträge im Ordnerbaum eingebunden (Graph `/users/{id}/…`, EWS `Mailbox`-Element, Autodiscover `AlternativeMailbox`; bei IMAP über `NAMESPACE`/Shared-Folders, z. B. Dovecot ACL).
- **Senden als** (Send-As) und **Senden im Auftrag von** (Send-on-Behalf): als eigene Identitäten auswählbar; der Client setzt korrekt `From` bzw. `From` + `Sender`; die gesendete Mail wird optional im Gesendet-Ordner des geteilten Postfachs abgelegt.
- **Kalender-Delegation:** Kalender anderer Personen öffnen (Lese- oder Bearbeitungsrechte), Termine im Auftrag anlegen und Einladungen für den Vorgesetzten beantworten; Freigabe des eigenen Kalenders mit Rechtestufen (Frei/Gebucht, Details, Bearbeiten) über Graph/EWS bzw. CalDAV-ACL (Nextcloud-Kalenderfreigaben).
- Geteilte Kontaktordner werden ebenso eingebunden.

### 7.4 Google

- **OAuth2 mit PKCE** über den System-Browser (Loopback-Redirect).
- Mail wahlweise über **IMAP + XOAUTH2** (einfach, Labels werden als Ordner abgebildet, `X-GM-EXT-1` für Labels und Thread-IDs) oder **Gmail API** (bessere Label- und Thread-Semantik).
- Kalender über Google Calendar API (alternativ CalDAV), Kontakte über People API.
- **Wichtig:** Für die öffentliche Verteilung verlangt Google bei Gmail-Vollzugriff eine App-Verifizierung inkl. Sicherheitsprüfung (CASA). Bis dahin: Option „eigene OAuth-Client-ID hinterlegen" für Power-User, oder App-Passwort + IMAP.

### 7.5 Konto-Einrichtung (Autokonfiguration)

Der Assistent fragt nur nach E-Mail-Adresse und Passwort und versucht nacheinander:

1. Bekannte Anbieter (Gmail, Outlook.com/M365, iCloud, GMX, Web.de, T-Online, Posteo, mailbox.org …) → passende OAuth- oder Server-Einstellungen.
2. **Mozilla Autoconfig** (`autoconfig.<domain>`, `/.well-known/autoconfig/`, ISPDB).
3. **Microsoft Autodiscover** (POX + JSON v2) → EAS/EWS/Graph-Erkennung.
4. **DNS-SRV** (RFC 6186 `_imaps._tcp`, `_submission._tcp`; RFC 6764 `_caldavs._tcp`, `_carddavs._tcp`).
5. Heuristik (`imap.<domain>`, `mail.<domain>`) mit Verbindungstest.
6. Fallback: **manuelle Eingabe** aller Parameter (Ein-/Ausgangsserver, Port, Verschlüsselung SSL/TLS, STARTTLS oder keine, Authentifizierung: Passwort, OAuth2, Client-Zertifikat, Benutzername getrennt einstellbar).

Bei einem erkannten Mail-Konto werden automatisch **passende Kalender und Adressbücher** angeboten (z. B. Nextcloud, mailbox.org, Posteo via CalDAV/CardDAV).

---

## 8. E-Mail-Funktionen

### 8.1 Ordner & Organisation
- Ordner anlegen, umbenennen, verschachteln, per Drag & Drop verschieben, löschen, abonnieren/abbestellen.
- Spezialordner-Erkennung (RFC 6154 `SPECIAL-USE`) mit manueller Zuordnung.
- **Gemeinsamer Posteingang** über alle Konten; virtuelle Ordner („Gespeicherte Suchen"), Favoriten-Ordner.
- Markierungen/Schlagworte (IMAP-Keywords, Gmail-Labels, Outlook-Kategorien vereinheitlicht), Farben.
- Archivieren mit einem Klick (pro Konto konfigurierbares Archivschema, z. B. `Archiv/2026`).

### 8.2 Verfassen
- WYSIWYG-Editor (TipTap/ProseMirror) mit Umschaltung auf Nur-Text; Markdown-Eingabe optional.
- **Identitäten/Aliase:** Absender-Auswahl im Kopf; automatische Wahl der Identität anhand der Adresse, an die die Originalmail ging. Freie Absenderadresse für Catch-All-Domains (z. B. `shop-xy@meinedomain.de`).
- **Signaturen:** beliebig viele, HTML und Text, mit Bildern; je Identität Standard- und Antwortsignatur; Position vor/nach dem Zitat.
- Vorlagen/Textbausteine, Platzhalter (`{{vorname}}`).
- **Später senden**, Senden rückgängig (Verzögerung 5–30 s), Entwürfe automatisch speichern (lokal + Server).
- Lesebestätigung (MDN) anfordern/beantworten (nachfragen, nie automatisch).
- Anhänge: Drag & Drop, aus Dateisystem **oder direkt aus Nextcloud/OpenCloud** auswählen; Größenwarnung mit Vorschlag „als Cloud-Link senden" (Kapitel 12).
- Prüfung „Anhang vergessen?" anhand von Schlüsselwörtern.

### 8.3 Lesen
- HTML-Darstellung in **isoliertem iframe** (CSP, kein JavaScript, keine Formulare), Bereinigung mit Allowlist-Sanitizer (`bluemonday`).
- Externe Bilder standardmäßig blockiert, pro Absender/Domain freigebbar; Tracking-Pixel-Erkennung.
- Anzeige von Authentifizierungsergebnissen (SPF/DKIM/DMARC aus `Authentication-Results`), Phishing-Hinweis bei abweichendem Link-Ziel.
- Kalender-Einladungen direkt in der Mail: **Zusagen / Vorbehalt / Absagen / Neuen Zeitpunkt vorschlagen** (iTIP/iMIP, RFC 5546/6047).

### 8.4 Suche
- Lokale Volltextsuche (FTS5) über Betreff, Text, Adressen, Anhangsnamen; Textextraktion aus PDF/DOCX/ODT optional.
- Suchsyntax: `from:`, `to:`, `subject:`, `has:attachment`, `before:`, `after:`, `in:`, `is:unread`, `account:`.
- Server-Suche als Ergänzung für nicht lokal gespeicherte Mails (IMAP `SEARCH`, EAS `Search`, Graph `$search`).
- Eine Suche findet auch Messenger-Nachrichten, Termine und Kontakte (umschaltbare Filter).

### 8.5 Regeln & Automatisierung
- Lokale Regel-Engine (Bedingungen: Absender, Empfänger, Betreff, Header, Größe, Konto; Aktionen: verschieben, markieren, Label, weiterleiten, Benachrichtigung, Konversation stummschalten).
- Serverseitige Regeln per **Sieve** (Editor mit grafischer Oberfläche und Rohansicht) bzw. Graph/EWS-Posteingangsregeln.
- Abwesenheitsnotiz (Sieve `vacation`, Graph `automaticRepliesSetting`, EAS `Settings/Oof`).

### 8.6 Verschlüsselung
**OpenPGP ist Bestandteil des Mail-MVP (Phase 1)** – nicht erst des Feinschliffs.

- Bibliothek: `ProtonMail/go-crypto` (unterstützt v4- und v6-Schlüssel nach RFC 9580), Paket `internal/crypto/pgp`.
- **Schlüsselverwaltung:** Erzeugen (Ed25519/Curve25519 als Standard, RSA-4096 optional), Import/Export (ASCII-armored, binär), Passphrase ändern, Widerruf, Ablaufdatum, mehrere Benutzer-IDs; Schlüssel je Identität zuordnen.
- **PGP/MIME** (RFC 3156) zum Signieren und Verschlüsseln, Lesen von **Inline-PGP**; **geschützte Header** (verschlüsselter Betreff).
- **Autocrypt Level 1:** Schlüssel automatisch mit jeder Mail mitsenden und empfangen; Empfehlung „verschlüsseln / möglich / abraten" beim Verfassen.
- **Schlüsselsuche:** WKD (Web Key Directory), keys.openpgp.org; angehängte Schlüssel mit einem Klick importieren.
- **Vertrauensmodell:** unbekannt → TOFU (zuerst gesehen) → verifiziert (Fingerabdruck bestätigt, z. B. per QR-Code), Warnung bei Schlüsselwechsel.
- **UI:** Schloss-/Siegel-Symbol in Liste, Lesebereich und Chat-Blase; im Verfasser Schalter „Signieren" / „Verschlüsseln" mit Automatik pro Empfänger; Entwürfe verschlüsselter Mails werden ebenfalls verschlüsselt abgelegt.
- **Suche in verschlüsselten Mails:** entschlüsselter Text wird nur im (optional verschlüsselten) lokalen Index gespeichert – abschaltbar.
- **Später:** Hardware-Schlüssel (YubiKey/OpenPGP-Card) über `gpg-agent`, Autocrypt-Setup-Message zum Übertragen des Schlüssels.
- **S/MIME** mit Zertifikatsverwaltung (Phase 10); Nutzung des Windows-Zertifikatsspeichers optional.

### 8.7 Spam-Filter & Newsletter-Abmeldung
- **Lokaler, lernender Spam-Filter** (Naiver Bayes über Tokens aus Betreff, Text, Headern und Links) zusätzlich zur Server-Bewertung (`X-Spam-Status`, `X-Rspamd-*`). Trainiert durch „Spam" / „Kein Spam"; Modell je Benutzer, kontoübergreifend.
- Beim Verschieben in den Junk-Ordner lernt auf Wunsch auch der Server (IMAP `$Junk`/`$NotJunk`-Keywords, rspamd-Lernordner).
- **Ein-Klick-Abmeldung** von Newslettern: `List-Unsubscribe` mit `List-Unsubscribe-Post` (RFC 8058, HTTPS-POST ohne Browser) bzw. `mailto:`-Abmeldung; Leiste „Newsletter von X – abmelden / alle archivieren".
- Übersicht „Abonnements": alle erkannten Newsletter mit Häufigkeit, Massenabmeldung und Regel „künftig automatisch archivieren".

### 8.8 Smarte Kategorien
- Der Posteingang wird **lokal** (ohne Cloud) in Kategorien eingeteilt: **Wichtig/Persönlich, Newsletter, Rechnungen & Bestellungen, Benachrichtigungen, Social, Termine**.
- Grundlage: Regeln/Heuristiken (Header wie `List-Id`, `Precedence: bulk`, Absenderdomänen, Schlagwörter, Anhänge „Rechnung*.pdf", iCalendar-Inhalte) plus der lernende Klassifikator aus 8.7, der durch Verschieben zwischen Kategorien nachtrainiert wird.
- Darstellung als Reiter über der Liste bzw. als Filter in der Messenger-Ansicht; Kategorien sind auch in Regeln und Suche nutzbar (`category:rechnung`).
- Kopplung mit Cloud-Ablage: „Rechnungen automatisch in Nextcloud `/Rechnungen/<Jahr>` speichern".
- Optional verfeinert durch die KI-Assistenz (8.10).

### 8.9 Rechtschreibprüfung & Serienmails
- **Rechtschreibprüfung** im Editor mit **Hunspell**-Wörterbüchern (Deutsch, Englisch, weitere ladbar); eigenes Benutzerwörterbuch; Sprache je Mail automatisch erkannt oder manuell gewählt; Prüfung im Go-Kern, damit sie in Desktop **und** Web-Client identisch funktioniert.
- **Serienmails:** Empfänger aus Kontaktgruppe, CSV- oder Tabellendatei; Platzhalter (`{{vorname}}`, `{{firma}}`, eigene Spalten); Vorschau je Empfänger; Versand als **einzelne** Mails (kein offener Verteiler) mit Drosselung (z. B. 30 Mails/Minute) zum Schutz vor Server-Limits; Protokoll mit Fehlern und Wiederholung.
- Kombinierbar mit Signaturen, Identitäten, PGP (je Empfänger verschlüsselt, wenn Schlüssel vorhanden) und Cloud-Links.

### 8.10 KI-Assistenz (lokal oder Cloud, optional)
- **Standardmäßig ausgeschaltet**; Aktivierung bewusst in den Einstellungen. Mehrere KI-Anbieter können parallel eingerichtet werden; pro Funktion wählbar, welcher genutzt wird.
- **Anbieter (Provider-Schnittstelle `internal/ai`):**

  | Anbieter | Art | Anbindung |
  |---|---|---|
  | **Ollama**, LM Studio, llama.cpp-Server, LocalAI | lokal / eigener Server im LAN | OpenAI-kompatible API |
  | **OpenAI (ChatGPT-Modelle)** | Cloud | OpenAI API (eigener API-Schlüssel) |
  | **Google Gemini** | Cloud | Gemini API (API-Schlüssel) |
  | **DeepSeek** | Cloud | DeepSeek API (OpenAI-kompatibel) |
  | Weitere: Anthropic Claude, Mistral (EU-Hosting), beliebige OpenAI-kompatible Endpunkte | Cloud | jeweilige API |

  API-Schlüssel liegen im Secret-Store; Modellname, Temperatur und Basis-URL frei konfigurierbar.
- **Funktionen:** **Zusammenfassung** langer Mails und Konversationen (auch Messenger-Gruppen), **Antwortvorschläge** in drei Varianten (kurz/freundlich/förmlich), Umformulieren und Übersetzen im Editor, Extraktion von **Terminen und Aufgaben** aus Mails („Termin erkennen → in Kalender übernehmen"), Unterstützung der smarten Kategorien, natürlichsprachliche Suche („Rechnungen von Telekom aus dem Sommer").
- **Datenschutz bei Cloud-Anbietern:**
  - Deutlicher Hinweis beim Einrichten, dass Inhalte an den Anbieter (ggf. außerhalb der EU, z. B. USA/China) übertragen werden.
  - Freigabe **pro Konto** (z. B. privates Konto ja, Firmenkonto nein) und Sperrliste für Ordner/Absender.
  - Option „Personenbezogene Daten maskieren": E-Mail-Adressen, Telefonnummern, IBANs und Namen werden vor dem Senden durch Platzhalter ersetzt und in der Antwort zurückgesetzt.
  - **Verschlüsselte (PGP) Mails werden nie an Cloud-Anbieter gesendet**, an lokale Modelle nur nach ausdrücklicher Freigabe.
  - Kein automatischer Hintergrund-Versand an Cloud-KI – nur auf Aktion des Benutzers (Ausnahme: lokale Modelle, wenn freigegeben).
- **Transparenz:** KI-Texte sind immer Vorschläge und werden nie automatisch gesendet; jede Anfrage an einen Cloud-Anbieter wird im Protokoll „KI-Aktivität" aufgeführt (Zeitpunkt, Anbieter, Zweck, Größe).

---

## 9. Kalender & Aufgaben

### 9.1 Funktionsumfang
- Ansichten: **Tag, Arbeitswoche, Woche, Monat, Jahr, Agenda/Liste**, Zeitleiste mehrerer Kalender nebeneinander.
- Mehrere Kalender pro Konto, beliebig viele Konten, Farben, Ein-/Ausblenden, Kalendergruppen.
- Termine: ganztägig, mehrtägig, **Serien** (RRULE inkl. Ausnahmen EXDATE/RECURRENCE-ID; Bibliothek `teambition/rrule-go`), Zeitzonen (IANA-Datenbank eingebettet), Ort mit Kartenlink, Online-Meeting-Link, Anhänge, Kategorien.
- **Einladungen** versenden und verwalten (Teilnehmer, Rollen, Status), **Frei/Gebucht-Abfrage** (CalDAV Scheduling, Graph `getSchedule`, EWS), Raum-/Ressourcenbuchung (Exchange).
- **Erinnerungen** (VALARM) mit Desktop-Benachrichtigung und Schlummern.
- **Aufgaben (VTODO)**: Fälligkeit, Priorität, Fortschritt, Unteraufgaben, Wiederholung; Synchronisation über CalDAV, Graph (Microsoft To Do), Google Tasks, EAS-Tasks.
- **Mail → Termin / Aufgabe**: Aus jeder Mail oder Messenger-Nachricht mit einem Klick einen Termin oder eine Aufgabe erzeugen (Verknüpfung bleibt erhalten).
- Drag & Drop von Terminen, Größe per Maus ändern, Schnellerfassung mit natürlicher Sprache („Zahnarzt morgen 15 Uhr").
- Abonnierte Kalender (ICS-URL, z. B. Feiertage, Schulferien) mit Aktualisierungsintervall.
- Geburtstage aus Kontakten als virtueller Kalender.
- **Videokonferenz-Links:** Beim Anlegen eines Termins mit einem Klick einen **Nextcloud-Talk-Raum** (OCS Talk API, Raum mit Teilnehmern bzw. öffentlich mit Passwort) oder einen **Jitsi-Raum** (eigene oder öffentliche Instanz, zufälliger Raumname) erzeugen. Der Link wird in `LOCATION`, `CONFERENCE` (RFC 7986) und die Beschreibung der Einladung eingetragen; Teams-Links bei Microsoft-365-Konten über Graph (`isOnlineMeeting`). Vor dem Termin: Benachrichtigung mit „Beitreten"-Schaltfläche.

### 9.2 Technik
- Speicherung des **Original-iCalendar** pro Objekt (`cal_objects.ical`) → keine Datenverluste bei unbekannten Eigenschaften (X-Properties bleiben erhalten).
- Serieninstanzen werden für das sichtbare Zeitfenster (± 1 Jahr) vorberechnet (`cal_instances`) – schnelle Darstellung.
- Synchronisation: CalDAV `sync-collection` (RFC 6578) mit ETags; Konflikt → Server gewinnt, lokale Version wird als Kopie angeboten.

---

## 10. Kontakte & Personen-Zusammenführung

### 10.1 Kontaktverwaltung
- Mehrere Adressbücher je Konto, Gruppen/Listen, Favoriten, Fotos.
- Vollständige vCard-4.0-Felder (mehrere Adressen, Telefonnummern, E-Mail, Geburtstag, Jahrestag, Organisation, Notizen, benutzerdefinierte Felder, Social-Profile, Messenger-Handles).
- Sync: CardDAV, Google People, Graph, EAS, EWS, lokal.
- Adress-Autovervollständigung aus Kontakten + zuletzt verwendeten Adressen; GAL-Suche (Exchange) und LDAP (optional).
- Duplikaterkennung und Zusammenführen mit Vorschau.

### 10.2 Personen-Resolver
Ein Kontakt in Nextcloud, ein WhatsApp-Kontakt und drei E-Mail-Adressen sind oft dieselbe **Person**. Der Resolver verknüpft sie über `person_handles`:

- Telefonnummern werden auf E.164 normalisiert (`nyaruka/phonenumbers`) → Abgleich mit WhatsApp-/Signal-Nummern.
- E-Mail-Adressen → Abgleich mit Kontakten.
- Vorschläge unsicherer Zuordnungen („Ist *Anna M.* (WhatsApp) dieselbe Person wie *Anna Müller* (Kontakt)?") – Benutzer bestätigt.
- Ergebnis: Personen-Ansicht mit allen Kanälen, gemeinsamer Zeitleiste, nächsten Terminen und geteilten Dateien.

---

## 11. Messenger-Integration

### 11.1 Grundsätzliches

Keiner der großen Messenger bietet eine offizielle API für **private Konten** in Drittanbieter-Clients. Die Integration erfolgt deshalb – wie bei Beeper, Ferdium-Alternativen oder Matrix-Bridges – als **verknüpftes Gerät** über inoffizielle, aber verbreitete Open-Source-Implementierungen. Jedes Messenger-Plugin:

- läuft als eigener Prozess (Kapitel 4.4),
- unterstützt **mehrere Konten** (je Konto eine eigene Sitzung/Datenbank),
- wird beim ersten Start bewusst aktiviert, mit klarem Hinweis auf Risiken (Kapitel 18).

### 11.2 Übersicht

| Dienst | Umsetzung | Kopplung | Funktionsumfang | Risiko |
|---|---|---|---|---|
| **WhatsApp** | `go.mau.fi/whatsmeow` (MPL-2.0) | QR-Code oder Kopplungscode wie WhatsApp Web | Einzel-/Gruppenchats, Medien, Sprachnachrichten (Wiedergabe), Reaktionen, Antworten, Lesebestätigungen, Status lesen | Mittel – Verstoß gegen ToS, Kontosperre möglich (selten bei normaler Nutzung) |
| **Signal** | `signalmeow` aus mautrix-signal (AGPL-3.0, nutzt libsignal) | QR-Code als verknüpftes Gerät | Chats, Gruppen, Medien, Reaktionen, verschwindende Nachrichten | Gering – „verknüpfte Geräte" sind ein vorgesehenes Konzept. Alternative: `signal-cli` (Java) als Sidecar |
| **Instagram** | `messagix` aus mautrix-meta (AGPL-3.0) | Login per eingebettetem Browser (Cookies) | Direktnachrichten, Medien, Reaktionen | **Hoch** – Meta erkennt inoffizielle Clients, Konto-Checkpoints möglich |
| **Facebook Messenger** | `messagix` (mautrix-meta) | wie Instagram | Chats, Gruppen, Medien | **Hoch** |
| **Google Messages** (RCS/SMS des Android-Handys) | `libgm` aus mautrix-gmessages (AGPL-3.0) | QR-Code / Google-Konto-Kopplung | SMS/RCS über das eigene Smartphone | Mittel |
| **Google Chat** (Workspace) | Offizielle Google Chat API | OAuth2 | Spaces, Direktnachrichten | Gering (nur Workspace) |
| Optional: Telegram | `gotd/td` (offizielle MTProto-API erlaubt Drittclients) | Telefonnummer + Code | voll | Gering |
| Optional: Matrix | `mautrix-go` | Login | voll | Gering |

> **Offizielle Alternative für Unternehmen:** WhatsApp Business Cloud API und die Meta Messenger-/Instagram-Plattform für **Unternehmensseiten** sind offiziell und sperrsicher – aber nur für Business-Konten nutzbar. Beide werden als zusätzlicher, offizieller Connector-Typ vorgesehen.

### 11.3 Architektur-Variante „Bridge-Modus"

**Entscheidung: Der Bridge-Modus ist Bestandteil von Version 1.0.**

Der Client kann – zusätzlich zum direkten Modus – mit einem **eigenen Matrix-Homeserver + mautrix-Bridges** arbeiten: Die Messenger laufen dann auf einem Server (z. B. neben Nextcloud), der Client spricht nur Matrix. Vorteil: Nachrichten kommen auch an, wenn der PC aus ist; mehrere Geräte gleichzeitig; Ende-zu-Ende-Verschlüsselung zwischen Client und Homeserver möglich. Nachteil: Serverbetrieb nötig.

Umsetzung:
- Neues Plugin **`plugin-matrix-bridge`** implementiert dieselbe `MessengerPlugin`-Schnittstelle (11.4) auf Basis von `mautrix-go`. Für den Rest des Clients ist es egal, ob WhatsApp direkt oder über die Bridge kommt.
- Die Bridge-Räume werden anhand der Bridge-Metadaten (`m.bridge`-State-Events) wieder den Kanälen WhatsApp/Signal zugeordnet → gleiches Symbol, gleiche Personen-Zuordnung.
- Kopplung (QR-Code) wird über die Bot-Kommandos der Bridge im Client grafisch abgebildet – der Benutzer sieht denselben Assistenten wie im direkten Modus.
- Mitgelieferte `docker-compose.yml`: Homeserver (**Synapse** oder das schlankere **Continuwuity/Conduit-Familie**), `mautrix-whatsapp`, `mautrix-signal`, optional `mautrix-meta`, `mautrix-gmessages`, PostgreSQL.
- Ein Konto kann **entweder** direkt **oder** über die Bridge laufen (nicht beides gleichzeitig, sonst doppelte verknüpfte Geräte). Ein Umzug „direkt → Bridge" wird per Assistent unterstützt (Neukopplung nötig).
- Im **Server-/Web-Betrieb** (4.5) ist der Bridge-Modus die empfohlene Messenger-Anbindung.

### 11.4 Plugin-Schnittstelle (gRPC, Auszug)

```protobuf
service MessengerPlugin {
  rpc ListAccounts(Empty) returns (AccountList);
  rpc StartPairing(PairRequest) returns (stream PairEvent);   // QR-Code-Daten, Erfolg, Fehler
  rpc RemoveAccount(AccountRef) returns (Empty);
  rpc ListChats(AccountRef) returns (ChatList);
  rpc FetchHistory(HistoryRequest) returns (stream ChatMessage);
  rpc Send(SendRequest) returns (SendResult);                  // Text, Medien, Antwort auf
  rpc React(ReactRequest) returns (Empty);
  rpc MarkRead(MarkReadRequest) returns (Empty);
  rpc DownloadMedia(MediaRef) returns (stream Chunk);
  rpc Events(AccountRef) returns (stream MessengerEvent);      // neue Nachricht, Status, Tippen
}
```

### 11.5 Verhalten im Client
- Messenger-Chats erscheinen in der Konversationsliste mit Kanal-Symbol; Filter „Nur Mail / Nur Chats / Alles".
- Medien werden im Blob-Store gespeichert und sind durchsuchbar (Dateiname, Bildunterschrift).
- Aus einer Chat-Nachricht: „Als Mail weiterleiten", „Termin erstellen", „Anhang in Nextcloud speichern".
- Benachrichtigungen je Konto/Chat konfigurierbar, „Bitte nicht stören"-Zeiten.

---

## 12. Cloud-Anhänge: Nextcloud, OpenCloud, Vaultwarden Send

### 12.1 Funktionen
1. **Dateien als Anhang holen:** Datei-Browser im Anhang-Dialog (Nextcloud/OpenCloud/WebDAV/lokal) – Datei wird heruntergeladen und klassisch angehängt *oder* als Link eingefügt.
2. **Anhänge speichern:** Rechtsklick auf Anhang (Mail oder Messenger) → „In Nextcloud/OpenCloud speichern…" mit Zielordnerwahl; Regel-Aktion „Anhänge automatisch ablegen" (z. B. Rechnungen → `/Dokumente/Rechnungen/2026`).
3. **Versandart pro Anhang wählbar:** *Direkt anhängen* · *Nextcloud-Link* · *OpenCloud-Link* · *Vaultwarden Send*.
4. Automatischer Vorschlag ab konfigurierbarer Größe (z. B. > 10 MB) oder wenn der Server die Größe ablehnen würde (SMTP `SIZE`-Erweiterung).

### 12.2 Ablauf „Als Freigabelink senden"

```
Benutzer wählt Anhang → Versandart "Nextcloud-Link"
  1. Upload per WebDAV (Chunked Upload v2 bei großen Dateien) nach
     /TuxMail/Mail-Anhänge/2026-11-03/<Betreff>/datei.pdf
  2. Freigabe erstellen mit:
       - Passwort (automatisch generiert, 16 Zeichen, oder manuell)
       - Ablaufdatum (Standard 14 Tage, einstellbar)
       - Nur-Lesen / Download erlaubt
  3. In die Mail wird ein formatierter Block eingefügt (Vorlage anpassbar):
       📎 datei.pdf (24,3 MB)
       Link: https://cloud.example.de/s/AbCdEf
       Passwort: K7#pQ2…   (gültig bis 17.11.2026)
  4. Speicherung in cloud_links (Verwaltung/Widerruf später möglich)
```

**Sicherheitsoption:** Passwort *nicht* in dieselbe Mail schreiben, sondern
a) in einer zweiten, separaten Mail senden, oder
b) per Messenger (Signal/WhatsApp) an denselben Empfänger schicken – praktisch, da der Client beide Kanäle kennt.
**Entscheidung: Es gibt keine feste Voreinstellung – der Client fragt bei jedem Versand mit Cloud-Link nach.** Beim Senden erscheint ein kompakter Dialog:

```
Passwort für die Freigabe „Angebot.pdf" übermitteln:
  ( ) In dieser Mail
  ( ) Separat per zweiter Mail
  ( ) Per Signal / WhatsApp an Anna Müller   (nur wenn Kanal bekannt)
  ( ) Nicht senden – ich übermittle es selbst (Passwort wird in Zwischenablage kopiert)
  [ ] Für diesen Empfänger merken
```

Optional kann die Wahl pro Empfänger/Domain gespeichert werden; der Dialog lässt sich dann für diese Empfänger überspringen.

### 12.3 Nextcloud
- Anmeldung über **Login Flow v2** (Browser öffnet sich, App-Passwort wird erzeugt – kein Hauptpasswort im Client).
- Dateien: WebDAV (`/remote.php/dav/files/<user>/`), Chunked Upload v2.
- Freigaben: **OCS Share API** `POST /ocs/v2.php/apps/files_sharing/api/v1/shares` mit `shareType=3` (öffentlicher Link), `password`, `expireDate`, `permissions`.
- Passwortrichtlinien des Servers werden abgefragt (Password-Policy-Capability), damit generierte Passwörter akzeptiert werden.
- Bonus: Kalender/Kontakte desselben Nextcloud-Kontos werden automatisch per CalDAV/CardDAV angeboten.

### 12.4 OpenCloud
- Anmeldung per **OpenID Connect** (PKCE, Loopback-Redirect).
- Dateien: WebDAV über Spaces (`/dav/spaces/<space-id>/…`), Upload per TUS-Protokoll (resumable) für große Dateien.
- Freigaben: **LibreGraph API** `POST /graph/v1beta1/drives/{drive-id}/items/{item-id}/createLink` mit `type`, `password`, `expirationDateTime`.
- Da OpenCloud aus ownCloud Infinite Scale hervorgegangen ist, funktioniert derselbe Connector mit kleinen Anpassungen auch für **oCIS**.

### 12.5 Vaultwarden (Bitwarden) Send
Bitwarden-Send ist Ende-zu-Ende-verschlüsselt: Der Schlüssel steht nur im URL-Fragment (`#…`) und erreicht den Server nie.

- **Anmeldung** an Vaultwarden: Prelogin (KDF-Parameter abfragen, PBKDF2 oder Argon2id) → Master-Key ableiten → Token holen → symmetrischen Benutzerschlüssel entschlüsseln. Zwei-Faktor-Anmeldung (TOTP, WebAuthn über Browser) wird unterstützt. Anmeldedaten im Secret-Store.
- **Send erstellen** (Datei- oder Text-Send): zufälliger Send-Schlüssel → Ableitung des Verschlüsselungsschlüssels (HKDF) → Datei und Metadaten verschlüsseln (AES-256-CBC + HMAC-SHA256, Bitwarden-EncString) → Send-Schlüssel mit Benutzerschlüssel verschlüsseln → Upload.
- Optionen: Passwort, maximale Zugriffszahl, Ablauf- und Löschdatum, E-Mail-Adresse des Absenders verbergen.
- Link `https://vault.example.de/#/send/<accessId>/<key>` wird in die Mail eingefügt, optional mit Passwort (bzw. Passwort getrennt, siehe 12.2).
- **Fallback:** Falls die native Implementierung bei einer Server-Version scheitert, kann die offizielle `bw`-CLI als Werkzeug genutzt werden (`bw send create`).
- Ergänzend möglich: Passwörter für Kontozugänge aus Vaultwarden beziehen (später, optional).

---

## 13. Import & Export

### 13.1 E-Mail

| Format | Import | Export | Bemerkung |
|---|---|---|---|
| **EML** (einzeln/Ordner) | ✅ | ✅ | Drag & Drop aus/zu Dateimanager |
| **MBOX** (mboxrd/mboxo) | ✅ | ✅ | Thunderbird, Apple Mail, Google Takeout |
| **Maildir** | ✅ | ✅ | Linux-Clients, Dovecot |
| **PST/OST** (Outlook) | ✅ | ❌ | Bibliothek `mooijtech/go-pst`, alternativ `libpff`/`readpst` |
| **Thunderbird-Profil** | ✅ | – | Konten, Identitäten, Signaturen, lokale Ordner |
| **Outlook (Windows)-Konten** | ✅ (Kontoeinstellungen) | – | Aus Registry/Profil, soweit lesbar |
| **PDF** (einzelne Mail/Konversation) | – | ✅ | Druck/Archivierung, Konversation als Chat-PDF |
| IMAP → IMAP-Migration | ✅ | ✅ | Konto-zu-Konto-Kopie mit Fortschritt (wie imapsync) |

Beim Import wird die **Ordnerstruktur** wahlweise übernommen oder in einen Zielordner gelegt; die Konversations-Engine verknüpft importierte Mails automatisch mit vorhandenen (über Message-IDs).

### 13.2 Kalender
- **Import:** `.ics` (RFC 5545) einzeln oder mehrere, ICS-URL-Abonnement, Outlook-CSV, Kalender-Export aus Google Takeout.
- **Export:** `.ics` pro Kalender oder alle, Zeitraum wählbar; CSV für Tabellenkalkulation.
- Dublettenprüfung über `UID`; Optionen „überspringen / überschreiben / als Kopie".

### 13.3 Kontakte
- **Import:** vCard 2.1 / 3.0 / 4.0 (`.vcf`, auch Sammeldateien), CSV mit **Feldzuordnungs-Assistent** (Vorlagen für Outlook, Google, Thunderbird), LDIF.
- **Export:** vCard 3.0/4.0, CSV (Outlook- und Google-Format), LDIF.
- Dublettenerkennung mit Zusammenführungsvorschlag.

### 13.4 Messenger
- Export einzelner Chats als HTML/PDF/JSON inkl. Medien.
- Import von WhatsApp-Chat-Exporten (`.txt` + Medien aus der Handy-Exportfunktion) zur Archivierung.

---

## 14. Backup & Restore

### 14.1 Umfang (wählbar)

| Modus | Inhalt |
|---|---|
| **Nur Konfiguration** | Konten (ohne/mit Zugangsdaten), Identitäten, Signaturen, Regeln, Vorlagen, Ansichten, Einstellungen, Plugin-Konfiguration |
| **Konfiguration + lokale Daten** | zusätzlich lokale Ordner, lokale Kalender/Kontakte, Konversations-Zuordnungen inkl. manueller Overrides |
| **Komplett** | gesamte Datenbank + Blob-Store (alle Mails, Anhänge, Messenger-Verläufe und -Sitzungen) |
| **Benutzerdefiniert** | Auswahl je Konto und Bereich (Häkchen-Baum) |

### 14.2 Format & Sicherheit
- Ein Archiv `tuxmail-backup-2026-11-03.txmb` = **tar + zstd**, verschlüsselt mit **age** (Passphrase, scrypt) – Pflicht, sobald Zugangsdaten oder Messenger-Sitzungen enthalten sind.
- `manifest.json` mit Version, Schema-Version, Zeitstempel, Umfang, SHA-256-Prüfsummen.
- **Konsistente Sicherung im laufenden Betrieb** über die SQLite-Online-Backup-API bzw. `VACUUM INTO`.
- Inkrementelle Backups (Blob-Store ist inhaltsadressiert → nur neue Blobs werden gesichert).
- Ziel: lokaler Ordner, USB-Stick, **Nextcloud/OpenCloud/WebDAV**; zeitgesteuert (täglich/wöchentlich) mit Aufbewahrungsregel (z. B. 7 täglich, 4 wöchentlich).

### 14.3 Wiederherstellung
- Assistent zeigt Inhalt des Backups und lässt wählen, **was** wiederhergestellt wird (komplett, nur Konfiguration, einzelne Konten).
- Modi: **Ersetzen** (sauberer Neuaufbau) oder **Zusammenführen** (z. B. Konten aus Backup zu bestehender Installation hinzufügen).
- Schema-Migrationen werden automatisch angewendet (Backups älterer Versionen bleiben lesbar).
- Hinweis bei Messenger-Sitzungen: WhatsApp/Signal erlauben eine Sitzung nicht beliebig parallel – nach Wiederherstellung auf einem neuen Rechner wird ggf. ein erneutes Koppeln angeboten.
- **Umzug auf einen neuen PC** = Backup „Komplett" + Restore „Ersetzen".

---

## 15. Portabilität, Installation & Updates

### 15.1 Einschätzung
**Portabler Betrieb ist mit dem gewählten Stack sehr gut möglich** und wird als gleichwertige Variante umgesetzt.

| Plattform | Portabel | Installiert |
|---|---|---|
| **Windows 10/11** | `TuxMail-portable.zip`: `tuxmail.exe` + `plugins\` + leere Datei `portable` → Daten in `.\data\`. Mindestversion Windows 10 22H2 / Windows 11 – dort ist WebView2 vorinstalliert, das Portable-Paket braucht keine zusätzliche Runtime. | MSI-Installer bzw. **winget**: Startmenü, `mailto:`/`webcal:`-Handler, Autostart, Standard-Mailprogramm |
| **Linux** | **AppImage** (enthält WebKitGTK und alle Abhängigkeiten); Portable-Modus über `TuxMail.AppImage.home`-Ordner oder `--portable`-Schalter | **Flatpak** (Flathub), `.deb`, `.rpm`, AUR; Desktop-Integration, Benachrichtigungen, Tray (StatusNotifierItem) |

### 15.2 Portabler Modus – Details
- Erkennung: Datei `portable` oder Ordner `data/` neben der Binärdatei, oder Parameter `--data-dir`.
- Keine Registry-Einträge, keine Dateien außerhalb des Programmordners.
- Geheimnisse liegen dann **nicht** im System-Schlüsselbund, sondern in `secrets.vault` (verschlüsselt mit Master-Passwort, siehe 17.2). Beim Start wird das Master-Passwort abgefragt (optional „auf diesem Rechner merken" über den Keyring).
- Pfade werden relativ gespeichert → der USB-Stick kann zwischen Rechnern (auch Windows ↔ Linux mit beiden Binaries im Ordner) wandern.
- Einschränkung: Systemintegration (Standard-Mailprogramm, `mailto:`) nur auf Wunsch und mit Hinweis.

### 15.3 Updates
- Integrierter Updater mit **signierten Releases** (minisign/ed25519); funktioniert auch portabel (ersetzt Binary im Ordner).
- Kanäle: *Stable*, *Beta*. Updates nie ohne Zustimmung; Changelog-Anzeige.
- Paketmanager-Installationen (Flatpak, winget, apt) aktualisieren über den Paketmanager.

### 15.4 Binary-Größen (Schätzung)
- Hauptanwendung: 25–35 MB; Plugins: je 10–25 MB (Signal etwas mehr wegen libsignal).
- Code-Signierung: Windows Authenticode (vermeidet SmartScreen-Warnungen), Linux GPG-signierte Pakete.

---

## 16. Benutzeroberfläche & UX

### 16.1 Grundlayout

```
┌────┬──────────────────┬──────────────────────────┬─────────────────────────┐
│ ✉  │ 🔍 Suche (Strg+K)                            │  ☰ Klassisch | 💬 Chat  │
│ 💬 ├──────────────────┼──────────────────────────┴─────────────────────────┤
│ 📅 │ ▾ Alle Konten    │  Liste (Klassisch)       │  Lesebereich             │
│ 👤 │   Posteingang 12 │  ● Anna Müller   10:42   │  Von: Anna Müller        │
│ ☁  │   Favoriten      │    Angebot Q4 …          │  Betreff: Angebot Q4     │
│    │ ▾ privat@…       │    Max Weber     09:13   │  ───────────────────     │
│    │   Posteingang    │    Re: Termin …          │  Hallo, anbei …          │
│    │   Gesendet       │                          │  📎 Angebot.pdf          │
│    │   ▸ Projekte     │                          │                          │
│    │ ▾ firma@… (EAS)  │                          │                          │
│    │ ▾ WhatsApp (2)   │                          │                          │
│    │ ▾ Signal         │                          │                          │
│ ⚙  │                  │                          │                          │
└────┴──────────────────┴──────────────────────────┴─────────────────────────┘
 Aktivitätsleiste   Konten/Ordner          umschaltbar: Liste ↔ Konversationen
```

**Messenger-Ansicht** (gleicher Platz, umgeschaltet):

```
┌──────────────────┬──────────────────────────────────────────────────────┐
│ 🔍 Konversationen│  Anna Müller          ✉ 💬(WA)        [☰ Klassisch]  │
│ ● Anna Müller ✉💬│ ──────────────────────────────────────────────────── │
│   Angebot Q4 …   │   ┌──────────────────────────┐                       │
│ ○ Projekt X   ✉  │   │ ✉ Hallo, anbei das …     │ 09:12                 │
│   Max: passt …   │   │ 📎 Angebot.pdf           │                       │
│ ○ Familie    💬  │   └──────────────────────────┘                       │
│   Mama: 👍       │                      ┌──────────────────────────┐    │
│                  │               10:05  │ ✉ Danke, ich prüfe das.  │    │
│                  │                      └──────────────────────────┘    │
│                  │   ┌──────────────────────────┐                       │
│                  │   │ 💬 Hast du die Mail? 🙂  │ 10:42 (WhatsApp)      │
│                  │   └──────────────────────────┘                       │
│                  │ ──────────────────────────────────────────────────── │
│                  │ [Kanal: ✉ anna@… ▾] Nachricht schreiben…   📎 ☁  ➤  │
└──────────────────┴──────────────────────────────────────────────────────┘
```

### 16.2 UX-Grundsätze
- **Ein Klick, eine Taste:** Umschalten Klassisch ↔ Chat (`Strg+Shift+V`), Befehlspalette (`Strg+K`) für alle Aktionen, Navigation, Konten.
- Tastaturkürzel-Schemata wählbar (Standard, Gmail-Stil, Outlook-Stil).
- Hell/Dunkel/System-Design, Akzentfarbe, **Dichte** (kompakt/normal/luftig), Schriftgröße.
- Wischgesten/Hover-Aktionen (Archivieren, Löschen, Später, Markieren).
- **Schlummern/Später erinnern** für Mails und Chats.
- Undo-Leiste für jede destruktive Aktion.
- Systemtray mit Ungelesen-Zähler, native Benachrichtigungen mit Schnellantwort.
- Onboarding-Assistent: Konten hinzufügen (Mail, Kalender, Messenger, Cloud) in einem Fluss; Import aus Thunderbird/Outlook anbieten.
- Leere Zustände mit Hilfetexten, verständliche Fehlermeldungen („Server lehnt Anmeldung ab – App-Passwort nötig? So geht's …").
- **Mehrfenster:** Mail/Chat/Kalender in eigenem Fenster lösbar.

### 16.3 Design-System
- Eigenes, schlankes Design-System (Design-Tokens für Farben, Abstände, Radien, Typografie), Komponenten auf Basis zugänglicher Headless-Bausteine.
- Icons: Lucide; Schrift: Inter (eingebettet).
- Animationen dezent (≤ 200 ms), reduzierbar (`prefers-reduced-motion`).

---

## 17. Sicherheit & Datenschutz

### 17.1 Grundsätze
- **Keine Telemetrie** ohne ausdrückliche Zustimmung; keine Cloud-Komponente des Herstellers notwendig.
- TLS-Pflicht standardmäßig (TLS 1.2+), Zertifikatsfehler deutlich anzeigen, Ausnahmen pro Server mit Fingerprint-Pinning.
- HTML-Mails: Sanitizer + iframe-Sandbox + strenge CSP; Remote-Inhalte blockiert; Links öffnen im externen Browser mit Anzeige der Zieladresse.
- Anhänge: Warnung bei ausführbaren Dateien, Öffnen über temporäres Verzeichnis, Bereinigung beim Beenden.

### 17.2 Geheimnisverwaltung
- **Installiert:** Betriebssystem-Schlüsselbund (`zalando/go-keyring`: Windows Credential Manager, Linux Secret Service/KWallet).
- **Portabel:** `secrets.vault` – verschlüsselt mit XChaCha20-Poly1305, Schlüssel per **Argon2id** aus Master-Passwort.
- OAuth-Refresh-Tokens werden genauso behandelt wie Passwörter.
- Optional: **Datenbank-Verschlüsselung** (SQLCipher) mit demselben Master-Passwort – sinnvoll im portablen Modus (USB-Stick-Verlust).

### 17.3 Datenschutz (DSGVO)
- Alle Daten bleiben lokal bzw. bei den Diensten des Benutzers.
- Löschfunktionen pro Konto „inkl. aller lokalen Daten".
- Protokolldateien ohne Inhalte und ohne Zugangsdaten (Redaktion von Tokens/Passwörtern).

---

## 18. Rechtliche Aspekte & Lizenzen

### 18.1 Inoffizielle Messenger-Anbindungen
- WhatsApp-, Instagram- und Facebook-Nutzungsbedingungen untersagen inoffizielle Clients. Konsequenz kann eine **Sperrung des Kontos** sein. Signal toleriert verknüpfte Geräte technisch, offizielle Drittclients sind aber ebenfalls nicht vorgesehen.
- Maßnahmen: Plugins standardmäßig **deaktiviert**, beim Aktivieren klarer Hinweis + Bestätigung; konservatives Verhalten (keine Massennachrichten, realistische Client-Kennung, Ratenbegrenzung); Meta-Plugins als „experimentell" markiert.
- Die Protokolle ändern sich regelmäßig → Plugins müssen unabhängig vom Hauptprogramm schnell aktualisierbar sein (eigener Update-Kanal je Plugin).

### 18.2 Lizenzen
| Komponente | Lizenz | Folge |
|---|---|---|
| whatsmeow | MPL-2.0 | Datei-basiertes Copyleft, unkritisch |
| mautrix-signal / signalmeow, mautrix-meta, mautrix-gmessages | **AGPL-3.0** | Plugins, die diese nutzen, müssen AGPL-kompatibel veröffentlicht werden |
| libsignal | **AGPL-3.0** | wie oben |
| emersion/go-imap, go-smtp, go-webdav | MIT | unkritisch |
| Wails | MIT | unkritisch |

**Entscheidung: Das gesamte Projekt wird unter AGPL-3.0 veröffentlicht.** Damit sind alle verwendeten Bibliotheken (MIT, MPL-2.0, AGPL-3.0) kompatibel. Da es einen Server-/Web-Modus gibt, ist die AGPL hier auch inhaltlich passend: Wer TuxMail als Dienst für andere betreibt, muss seinen geänderten Quellcode bereitstellen. Jede Quelldatei erhält einen SPDX-Header (`SPDX-License-Identifier: AGPL-3.0-or-later`); Beiträge Dritter werden über ein DCO (`Signed-off-by`) angenommen.

### 18.3 Weitere Punkte
- Google-OAuth-Verifizierung (CASA) und Microsoft-Publisher-Verifizierung für die öffentliche Verteilung einplanen (Zeit: mehrere Wochen, ggf. Kosten für das Sicherheitsaudit).
- Markenrechte: Logos von WhatsApp/Signal/Meta nur gemäß deren Markenrichtlinien verwenden.

---

## 19. Projektstruktur (Repository)

```
Mailclient/
├── cmd/
│   ├── tuxmail/               # Hauptprogramm (GUI + Kern)
│   ├── tuxmail-cli/           # Kommandozeile: Backup, Import/Export, Headless
│   └── plugins/
│       ├── whatsapp/
│       ├── signal/
│       ├── meta/
│       └── gmessages/
├── internal/
│   ├── app/                   # Application Services (Wails-Bindings)
│   ├── core/
│   │   ├── threading/         # Konversations-Engine (Kapitel 6)
│   │   ├── persons/           # Personen-Resolver
│   │   ├── rules/             # Regel-Engine
│   │   ├── opqueue/           # Operations-Queue
│   │   ├── sync/              # Sync-Scheduler
│   │   └── search/
│   ├── store/                 # SQLite, Migrationen, Blob-Store
│   ├── secrets/               # Keyring + Vault
│   ├── providers/
│   │   ├── imap/  pop3/  smtp/  jmap/
│   │   ├── eas/               # inkl. wbxml/
│   │   ├── graph/  ews/  google/
│   │   ├── caldav/  carddav/
│   │   └── storage/ (nextcloud/ opencloud/ vaultwarden/ webdav/)
│   ├── mime/                  # Parsing, Zitat-/Signaturerkennung, Sanitizer
│   ├── importexport/          # eml, mbox, maildir, pst, ics, vcf, csv, ldif
│   ├── backup/
│   ├── autoconfig/
│   └── plugin/                # gRPC-Host, Protobuf-Definitionen
├── frontend/                  # Svelte 5 + TypeScript
│   ├── src/lib/components/
│   ├── src/lib/stores/
│   ├── src/routes/ (mail, chat, calendar, contacts, files, settings)
│   └── src/i18n/ (de, en)
├── proto/                     # .proto-Dateien
├── test/
│   ├── docker-compose.yml     # Testserver (siehe Kapitel 20)
│   └── fixtures/              # Beispielmails, ics, vcf
├── build/                     # Icons, Installer-Skripte, AppImage, Flatpak, MSI
├── docs/
│   └── KONZEPT.md             # dieses Dokument
├── Taskfile.yml
└── go.mod
```

---

## 20. Teststrategie & Qualitätssicherung

| Ebene | Inhalt | Werkzeuge |
|---|---|---|
| Unit-Tests | MIME-Parsing, Threading-Algorithmus, Zitat-Erkennung, RRULE, vCard/iCal-Konvertierung, WBXML-Codec | `go test`, Tabellentests, **Fuzzing** (`go test -fuzz`) für Parser |
| Golden-Tests | Korpus echter (anonymisierter) Mails aus Outlook, Gmail, Apple Mail, Thunderbird → erwartete Konversationsstruktur | eigene Fixtures |
| Integrationstests | Gegen echte Server in Docker: **Dovecot + Postfix** (IMAP/SMTP/Sieve), **Stalwart** (JMAP/IMAP), **Radicale/Baïkal** (CalDAV/CardDAV), **Nextcloud**, **OpenCloud**, **Vaultwarden**, **Z-Push/Grommunio** (ActiveSync) | `docker compose`, `testcontainers-go` |
| Microsoft/Google | Testmandanten (M365 Developer, Google Workspace Test-Domain) – nächtliche Läufe | CI mit geschützten Secrets |
| Frontend | Komponenten-Tests, E2E-Tests der Oberfläche | Vitest, Playwright |
| Performance | Synthetische Postfächer mit 1 Mio. Mails; Startzeit, Speicher, Listen-Scrollen | Benchmarks, pprof |
| Sicherheit | `govulncheck`, Abhängigkeits-Scans, HTML-Sanitizer-Tests mit XSS-Korpus | CI |

CI: GitHub Actions – Build für Windows/Linux (x64, ARM64), Tests, Lint (`golangci-lint`, `eslint`, `svelte-check`), Release mit GoReleaser.

---

## 21. Roadmap & Aufwandsschätzung

Die Schätzung gilt für ein kleines Team (2–3 Entwickler); PM = Personenmonat.

| Phase | Inhalt | Ergebnis | Aufwand |
|---|---|---|---|
| **0 – Fundament** | Repo, Build, Wails-Grundgerüst, SQLite + Migrationen, Blob-Store, Secret-Store, Op-Queue, Event-Bus, Design-System | Lauffähige leere App, portabel & installiert | 2 PM |
| **1 – Mail-MVP** | IMAP/SMTP/POP3, Autokonfiguration, Ordnerverwaltung, Identitäten/Aliase, Signaturen, Editor + Rechtschreibprüfung, Lesen (Sanitizer), Suche, Offline, **OpenPGP inkl. Autocrypt/WKD**, Spam-Filter & Newsletter-Abmeldung | **Alltagstauglicher Mail-Client** | 7 PM |
| **1b – Komfort** | Smarte Kategorien, Serienmails | Organisierter Posteingang | 1,5 PM |
| **2 – Konversationen** | Beziehungstabelle, Threading-Engine, Verschiebe-Erkennung, Zitat-Entfernung, Messenger-Ansicht, nahtloser Wechsel | **Alleinstellungsmerkmal fertig** | 3 PM |
| **3 – Kalender & Kontakte** | CalDAV/CardDAV, Kalender-UI, Aufgaben, Einladungen (iMIP), Kontakte-UI, Personen-Resolver | Vollständiges PIM | 4 PM |
| **4 – Import/Export & Backup** | EML/MBOX/Maildir/PST, ICS, vCard/CSV/LDIF, Backup/Restore (alle Modi), Thunderbird-Import | Datenhoheit | 2 PM |
| **5 – Microsoft & Google** | OAuth2, Graph, Gmail API, Google Calendar/People, **ActiveSync**, EWS (On-Prem) | Unternehmenstauglich | 5 PM |
| **6 – Cloud-Anhänge** | Nextcloud, OpenCloud, Vaultwarden Send, Link-Verwaltung, Anhang-Ablage-Regeln | Cloud-Workflow | 2 PM |
| **7 – Messenger** | Plugin-Host, **WhatsApp, Signal** (Pflicht für 1.0), gemischte Personen-Zeitleiste | Unified Inbox | 4 PM |
| **8 – Bridge-Modus** | `plugin-matrix-bridge`, Bridge-Zuordnung, Kopplungsassistent, docker-compose mit Homeserver + Bridges | Messenger 24/7 | 2 PM |
| **9 – Server & Web-Client** | `tuxmail serve`, API-Generierung (Connect-RPC), Transport-Abstraktion im Frontend, Benutzerverwaltung, OIDC, PWA/Web-Push, Docker-Image | Zugriff über Browser | 4 PM |
| **10 – Feinschliff & Release 1.0** | Videokonferenz-Links, Exchange-Delegation, KI-Assistenz (lokal + Cloud-Anbieter), S/MIME, Regeln/Sieve, Barrierefreiheit, Übersetzungen, Code-Signierung, OAuth-Verifizierungen, Sicherheitsprüfung des Web-Modus, Dokumentation | Release | 5 PM |
| | | **Summe 1.0** | **≈ 41,5 PM** |
| *nach 1.0* | Instagram/Facebook (experimentell), Google Messages, Google Chat, optional Telegram, Mobil-App | Optionale Kanäle | *≈ 3–4 PM* |

- Mit 3 Entwicklern: **ca. 14–16 Monate** bis Version 1.0; ein nutzbarer Mail-Client mit PGP und Konversationsansicht (Phasen 0–2) nach **ca. 5 Monaten**.
- Phasen 5–8 lassen sich nach Phase 2 teilweise **parallel** bearbeiten.

### Meilensteine
| Meilenstein | Inhalt |
|---|---|
| **M1 – Alpha** | Phasen 0–2: IMAP/SMTP, Ordner, Signaturen, Aliase, Konversationsansicht |
| **M2 – Beta 1** | + Kalender, Kontakte, Import/Export, Backup |
| **M3 – Beta 2** | + Microsoft/Google, ActiveSync, Cloud-Anhänge |
| **M4 – Beta 3** | + WhatsApp, Signal (direkt und über Bridge) |
| **M5 – Beta 4** | + Server-Modus und Web-Client |
| **M6 – 1.0** | + Feinschliff, Signierung, Verifizierungen |

---

## 22. Risiken & Gegenmaßnahmen

| Risiko | Wahrscheinlichkeit | Auswirkung | Gegenmaßnahme |
|---|---|---|---|
| Messenger-Protokolländerungen brechen Plugins | Hoch | Mittel | Upstream-Bibliotheken eng verfolgen, Plugins separat aktualisierbar, Prozess-Isolation |
| Kontosperren bei WhatsApp/Meta | Mittel | Hoch (für Nutzer) | Opt-in mit Warnung, konservatives Verhalten, offizielle Business-APIs als Alternative |
| EAS-Implementierung aufwendiger als geplant | Mittel | Mittel | Früh gegen Z-Push/Grommunio und Exchange Online testen, Graph als Ausweichweg |
| Google/Microsoft-Verifizierung verzögert Release | Mittel | Mittel | Früh beantragen; „eigene Client-ID" als Übergangslösung |
| Threading-Fehlzuordnungen (falsch zusammengeführte Konversationen) | Mittel | Mittel | Konfidenzwerte, Heuristik abschaltbar, manuelles Trennen/Zusammenführen, Golden-Tests |
| WebKitGTK-Unterschiede unter Linux | Mittel | Gering | AppImage/Flatpak mit definierter WebKit-Version, E2E-Tests unter Linux |
| Große Postfächer (> 1 Mio. Mails) langsam | Gering | Mittel | Virtualisierte Listen, Indizes, inkrementeller Sync, Bodies nur bei Bedarf laden |
| Lizenzkonflikte (AGPL) | Gering | Hoch | Lizenzentscheidung vor Phase 7 treffen (Kapitel 18.2) |

---

## 23. Getroffene Entscheidungen

| # | Thema | Entscheidung | Auswirkung im Konzept |
|---|---|---|---|
| 1 | Produktname | **TuxMail** | Binary `tuxmail`, Datenordner `TuxMail`/`tuxmail`, Backup-Endung `.txmb` |
| 2 | Lizenz | **AGPL-3.0 für das gesamte Projekt** | Kapitel 18.2 |
| 3 | Frontend | **Svelte 5** + TypeScript + Tailwind CSS | Kapitel 3.2 |
| 4 | Windows-Mindestversion | **Windows 10 22H2 und Windows 11** (WebView2 vorinstalliert) | Kapitel 15.1 – keine Runtime im Portable-Paket nötig |
| 5 | Bridge-Modus | **Bestandteil von Version 1.0** | Kapitel 11.3, Phase 8 der Roadmap |
| 6 | Web-/Mobil-Client | **Web-Client in Version 1.0**, Mobil-App später über dieselbe API | Kapitel 4.2, 4.5, Phase 9 der Roadmap |
| 7 | Cloud-Link-Passwort | **Bei jedem Versand fragen** (Wahl pro Empfänger merkbar) | Kapitel 12.2 |
| 8 | Messenger für 1.0 | **WhatsApp und Signal**; Instagram, Facebook, Google Messages/Chat nach 1.0 | Kapitel 11, Roadmap |
| 9 | OpenPGP | **Bereits im Mail-MVP** (inkl. Autocrypt, WKD, geschützte Header) | Kapitel 8.6, Phase 1 |
| 10 | Zusatzfunktionen 1.0 | Spam-Filter & Newsletter-Abmeldung, smarte Kategorien, Rechtschreibprüfung & Serienmails, Videokonferenz-Links, Exchange-Delegation, KI-Assistenz (optional; lokal oder OpenAI/Gemini/DeepSeek) | Kapitel 7.3a, 8.7–8.10, 9.1 |
| 11 | Bewusst **nicht** in 1.0 | Wegwerf-Aliase (SimpleLogin/addy.io), Terminbuchungsseite | ggf. nach 1.0 |

---

*Ende des Konzepts. Nächster Schritt: Phase 0 – Projektgerüst (Go-Modul, Wails, SQLite-Schema, Build-Pipeline) aufsetzen.*
