# Entwicklungskonzept: Unified Communication Client

**Produktname:** *TuxMail* – E-Mail, Messenger, Kalender, Kontakte und Cloud-Dateien in einer Anwendung
**Zielplattformen:** Windows 10/11 (x64, ARM64), Linux (x64, ARM64)
**Stand:** 03.11.2026 – Version 1.6 des Konzepts (Entscheidungen aus Kapitel 23 eingearbeitet)

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
| M-21 | Mail | Druckansicht, PDF-Export, automatische PDF/A-Archivierung (z. B. Rechnungen) | Muss |
| M-22 | Mail | „Warten auf Antwort"-Erinnerungen | Muss |
| M-23 | Mail | Versand-Prüfungen (externe/private Empfänger, Anhang, Verschlüsselung, sensible Daten) | Muss |
| M-24 | Mail | Gemeinsame Postfächer mit Zuweisung und internen Kommentaren (Server-Modus) | Soll |
| W-01 | System | Bereichsübergreifendes Workflow-System (Auslöser, Bedingungen, Aktionen, grafischer Editor, YAML) | Muss |
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
| X-05 | Messenger | Telegram, Matrix | Muss |
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
- **Mehrsprachigkeit:** Deutsch und Englisch ab Version 1 über **Sprachdateien** (siehe 16.4); weitere Sprachen ohne Programmänderung ergänzbar.
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

### 6.5a Drei Ansichten: Klassisch · Hybrid · Chat
| Ansicht | Links | Rechts | Geeignet für |
|---|---|---|---|
| **Klassisch** | Ordnerbaum | Liste + Lesebereich | klassische Mail-Arbeit |
| **Hybrid** | Ordnerbaum (inkl. Projekte, Suchordner, Tags) | Konversation als **Blasen** | Ordner-Organisation + Chat-Lesbarkeit |
| **Chat** | Chatliste (ohne Ordner) | Unterhaltung | Messenger-Gefühl |

**Hybrid-Ansicht im Detail:** In einem Ordner zeigt die Liste alle Konversationen mit Nachrichten in diesem Ordner. Beim Öffnen erscheint die **ganze** Konversation als Blasen; die Blasen, die **in dem aktuellen Ordner liegen, sind hervorgehoben** (Akzentrahmen + kleiner Ordner-Chip), die übrigen leicht abgeblendet mit Chip ihres Ordners (z. B. „Gesendet", „Archiv/2026"). Schalter „Nur Nachrichten aus diesem Ordner"; automatisches Springen zur ersten hervorgehobenen Blase; einzelne Blase per Drag & Drop in einen anderen Ordner verschieben. Wechsel der Ansichten mit `Strg+Umschalt+V`, Standardansicht pro Ordner einstellbar.

### 6.6 Messenger-Ansicht für E-Mails

**Grundsatz:** Die Messenger-Ansicht sieht aus und funktioniert wie ein echter Messenger (WhatsApp/Signal/Telegram). **Ordner, Betreffzeilen, Mail-Köpfe (Von/An/CC) und Mail-Symbolleisten werden dort nicht angezeigt.** E-Mail ist nur ein weiterer Kanal.

- **Aufbau:** Aktivitätsleiste · Chatliste · Unterhaltung · (optional Info-Bereich). Der Ordnerbaum existiert nur in der klassischen Ansicht.
- **Chatliste:** Suchfeld („Suchen oder neuen Chat beginnen"), Filter *Alle · Ungelesen · Mail · WhatsApp · Signal · Telegram · Gruppen · Favoriten*, angeheftete Chats oben, Avatar mit kleinem Kanal-Abzeichen, letzte Nachricht mit „Du:" und Häkchen, Ungelesen-Zähler, Stummschalten, Bereich „Archiviert". Konversationen fassen automatisch alle Ordner zusammen (Posteingang, Gesendet, Archiv, Unterordner; ohne Spam/Papierkorb). „Chat archivieren" archiviert die zugehörigen Mails.
- **Unterhaltung:** Kopf mit Avatar, Name und bekannten Kanälen; Blasen gruppiert, Zeit und Häkchen in der Blase, Datumstrenner („Heute", „Gestern"), Markierung „Ungelesene Nachrichten". Zitate und Signaturen sind ausgeblendet; das Original ist über das Blasen-Menü („Original-E-Mail anzeigen") erreichbar. Ein neuer Betreff erscheint nur als kleine Systemzeile. Newsletter erscheinen als kompakte Karte. Bilder als Medien-Vorschau, Dateien als Karten, Link-Vorschauen.
- **Interaktion:** Antworten mit Zitat-Vorschau (setzt bei Mail korrekt `In-Reply-To`), Reaktionen (bei Mail lokal bzw. als RFC-9078-Reaktion), Weiterleiten, Kopieren, als Aufgabe/Termin, Löschen.
- **Eingabe:** wachsende Eingabezeile, Enter sendet (Umschalt+Enter = neue Zeile, einstellbar), Emoji-Auswahl, „+"-Menü (Datei, Foto, aus Nextcloud/OpenCloud, *als vollständige E-Mail bearbeiten*), Kanalwahl als kleiner Chip. Bei Mail wird der Betreff automatisch gesetzt und nicht angezeigt.
- **Signaturen auch im Chat:** Mail-Antworten aus der Messenger-Ansicht erhalten **standardmäßig die Signatur** der Identität (Antwortsignatur bzw. Standardsignatur). Pro Identität wählbar: volle Signatur, **Kurzsignatur für Chat** (z. B. Name + Telefon) oder keine. Unter der Eingabezeile zeigt eine dezente Zeile die aktive Signatur; sie lässt sich für die einzelne Nachricht wechseln oder entfernen.
- **Eingehende Signaturen** werden in der Blase ausgeblendet, aber ausgewertet: Der Info-Bereich zeigt die letzte Signatur des Kontakts als **Visitenkarte** (Name, Position, Firma, Telefon, Adresse, Website) mit „Zu Kontakt hinzufügen/aktualisieren".
- **Mobil (PWA):** Chatliste → Unterhaltung im Vollbild mit Zurück-Pfeil, wie in Messenger-Apps.
- **Signatur-Darstellung:** Einstellung „Signaturen in der Chat-Ansicht": *eingeklappt* (Standard – die Blase zeigt nur die aktuelle Nachricht, ein kleiner Pfeil klappt die Signatur auf) oder *anzeigen*. Gesendet wird die Signatur trotzdem.
- **Eigene Chat-Ordner** (wie bei Telegram): selbst definierte Listen wie „Arbeit", „Familie", „Kunden" als Reiter über der Chatliste – nach Personen, Chats, Kanälen, Konten, Gruppen oder „nur ungelesen"; mit Ungelesen-Zähler. Unabhängig von Mail-Ordnern.
- **Markierte & angeheftete Nachrichten:** einzelne Nachrichten mit Stern markieren (Übersicht global und pro Chat); bis zu drei Nachrichten pro Chat oben anheften.
- **Chat schlummern & Fokusmodus:** Chat bis später ausblenden (1 Std., heute Abend, morgen früh, Montag, frei wählbar), danach erscheint er wieder oben. Fokusmodus zeigt nur wichtige Personen; alle anderen sammeln sich in „Wartet (n)"; optional zeitgesteuert.
- **Entwürfe & geplantes Senden:** angefangene Nachricht bleibt pro Chat erhalten („Entwurf: …" in der Liste); „Später senden" mit Vorgaben und Datum/Uhrzeit; geplante Nachrichten erscheinen gedimmt im Verlauf und sind änderbar.
- **Schnellantworten mit „/":** Textbausteine (z. B. `/danke`, `/termin`, `/iban`) mit Platzhaltern; `@Name` in Gruppen-Mails fügt die Person zu CC hinzu.
- **KI im Chat:** drei Antwortvorschläge als Chips, Nachrichten inline übersetzen, lange Verläufe zusammenfassen – über die KI-Assistenz (8.10) mit denselben Datenschutzregeln.
- **Termin vorschlagen:** freie Zeiten aus dem Kalender als auswählbare Vorschläge senden (bei Mail mit `.ics`); die Auswahl des Empfängers legt den Termin automatisch an.

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

### 8.1a Tags (Schlagworte)
- Farbige, optional hierarchische Tags (z. B. `Kunde/Müller`) für **Mails, Chat-Nachrichten, Konversationen, Termine, Kontakte und Dateien**.
- Vergabe über Blasen-/Kontextmenü, Mehrfachauswahl, Taste `t`, Workflows; Anzeige als Chips in Listen und Blasen; Tag-Bereich in der Seitenleiste mit Zählern; Tag-Filter in der Chat-Ansicht; Verwaltung (umbenennen, zusammenführen, Farbe, Symbol).
- **Synchronisation:** Abbildung auf IMAP-Keywords, Gmail-Labels und Outlook-Kategorien, damit Tags auch in anderen Programmen sichtbar sind; Chat-Tags bleiben lokal bzw. auf dem TuxMail-Server.
- In der Suche über `tag:` nutzbar, als Facette sichtbar.

### 8.1b Projektordner
Ein **Projekt** bündelt alles, was zu einem Vorhaben gehört – unabhängig davon, in welchem Konto, Ordner oder Kanal es liegt:
- **Zusatzinfos:** Name, Kürzel (z. B. `HB-2026`), Beschreibung, Status (geplant, aktiv, pausiert, erledigt, archiviert), Farbe, Kunde/Organisation, Start/Fälligkeit, Budget, **eigene Felder** (per Projektvorlage definierbar), Notizen, Mitglieder mit Rollen, verknüpfter **Cloud-Ordner** (Nextcloud/OpenCloud).
- **Inhalte:** Mails, Konversationen, Chats und einzelne Nachrichten, **Kalendereinträge und Aufgaben**, **Kontakte**, Dateien/Cloud-Links, Suchordner.
- **Projekt-Dashboard** mit Reitern: Übersicht · **Zeitachse** (alles chronologisch) · Kommunikation · Termine & Aufgaben · Kontakte · Dateien · **Workflows** · Regeln.
- **Automatische Zuordnung** per Regel in der Such-Abfragesprache (z. B. `from:*@mueller-gmbh.de OR subject:"HB-2026"`).
- **Workflow-Einbindung:** Auslöser (Element hinzugefügt, Status geändert, Fälligkeit naht), Aktionen (zu Projekt hinzufügen, Status setzen, Projekt aus Vorlage anlegen); Workflows lassen sich direkt an ein Projekt binden.
- Projektvorlagen (eigene Felder, Standard-Tags, Standard-Workflows); im Server-Modus mit dem Team teilbar.

### 8.1c Suchordner
- Jede Suche (einfach oder Experte) kann als **Suchordner** gespeichert werden: erscheint im Ordnerbaum (global, unter einem Konto oder in einem Projekt) mit Ungelesen-Zähler und aktualisiert sich laufend.
- Nutzbar in allen drei Ansichten, als **Chat-Ordner** und als Bereich für Workflow-Auslöser („wenn eine Mail neu in den Suchordner fällt").

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
- Lokale Volltextsuche (FTS5) über **alle Bereiche**: Mails (Betreff, Text, Adressen, Anhangsnamen), **Messenger-Nachrichten** (Text, Bildunterschriften, Dateinamen), **Termine und Aufgaben**, **Kontakte**, **Notizen in Konversationen** und **Cloud-Links**; Textextraktion aus PDF/DOCX/ODT-Anhängen optional.
- **Einstellbar, WAS durchsucht wird:** Bereiche (Mail, Chats, Kalender, Aufgaben, Kontakte, Dateien) einzeln an-/abwählbar; innerhalb der Mail die Felder (Betreff, Text, Absender/Empfänger, Anhangsnamen, Anhangsinhalte).
- **Einstellbar, WO gesucht wird:** alle Konten oder ausgewählte Konten/Kanäle (z. B. nur Firmenkonto + Signal), bestimmte Ordner inkl./exkl. Unterordner, Papierkorb/Spam ein- oder ausschließen, Zeitraum.
- **Lokal und/oder auf dem Server:** lokale Suche (schnell, offline) und Server-Suche für nicht lokal gespeicherte Mails (IMAP `SEARCH`, EAS `Search`, Graph `$search`, Gmail-API) – automatisch, immer oder nie.
- Die Auswahl erfolgt über **Filter-Chips** direkt im Suchfeld; die Standard-Einstellung wird unter *Einstellungen → Suche* festgelegt. Häufige Kombinationen lassen sich als **Suchprofil** speichern (z. B. „Buchhaltung": nur Firmenkonto, Ordner Rechnungen, inkl. Anhangsinhalte) und als virtueller Ordner anheften.
- **Indexierung einstellbar:** pro Konto, ob Inhalte lokal indexiert werden (z. B. aus Datenschutzgründen nicht für ein bestimmtes Konto), Anhangsinhalte ja/nein, verschlüsselte (PGP-)Inhalte ja/nein.
- Suchsyntax: `from:`, `to:`, `subject:`, `has:attachment`, `before:`, `after:`, `in:`, `is:unread`, `account:`, `channel:whatsapp`, `type:event`, `category:rechnung`.

#### 8.4.1 Expertenmodus (Abfragesprache ähnlich Kibana/Lucene)
Neben der einfachen Suche (Wörter + Filter-Chips) gibt es einen **Expertenmodus** mit einer vollständigen Abfragesprache:

| Element | Beispiele |
|---|---|
| **Boolesche Operatoren** | `AND`, `OR`, `NOT` (auch `&&`, `\|\|`, `!` sowie deutsch `UND`, `ODER`, `NICHT`), `+muss`, `-darf_nicht`; Vorrang NOT > AND > OR |
| **Klammern** | `(from:anna OR from:max) AND subject:angebot` |
| **Felder** | `from:`, `to:`, `cc:`, `participant:`, `subject:`, `body:`, `attachment:` (Inhalt), `filename:`, `account:`, `folder:`/`in:`, `channel:`, `type:`, `category:`, `label:`, `header.X-Mailer:`, `lang:`; Feldgruppen `from:(anna OR max)` |
| **Phrasen & Nähe** | `"rechnung april"`, `"vertrag kündigung"~5` (Wörter höchstens 5 Wörter auseinander) |
| **Platzhalter & Unschärfe** | `rechn*`, `m?ier`, `meier~` (Tippfehler-tolerant), `/re[- ]?\d{6}/` (regulärer Ausdruck) |
| **Bereiche & Vergleiche** | `date:[2025-01-01 TO 2025-06-30]`, `date:>now-7d`, `date:"letzte Woche"`, `size:>10MB`, `attachments:>2` |
| **Zustände** | `is:unread`, `is:flagged`, `is:encrypted`, `is:signed`, `has:attachment`, `has:calendar`, `_exists_:attachment` |
| **Gewichtung** | `vertrag^3 OR angebot` |

**Weitere Merkmale**
- **Facetten wie in Kibana:** Trefferzahlen nach Konto, Ordner, Absender/Domain, Kanal, Typ, Anhang und ein **Zeit-Histogramm** (Monate/Jahre) – Klick auf eine Facette fügt die passende Bedingung zur Abfrage hinzu.
- **Sortierung:** Relevanz (BM25 mit einstellbarer Feldgewichtung, z. B. Betreff > Absender > Text > Anhang), Datum, Absender, Größe.
- **Hervorhebung** der Treffer im Ausschnitt und in der geöffneten Mail.
- **Objekt-Eingabe mit Autovervollständigung:** Während der Eingabe schlägt das Feld passend zur Cursorposition vor:
  - am Anfang eines Begriffs **Felder** (`from:`, `subject:`, `date:` …) mit Symbol und Kurzbeschreibung sowie `(` und `NOT`;
  - nach einem Feld passende **Werte** (Konten, Ordner, bekannte Absender mit Avatar, Kategorien, Kanäle, Datumsvorgaben wie „heute", „letzte Woche", Größen);
  - nach einer vollständigen Bedingung die **Operatoren** `AND` / `OR` / `NOT` (bzw. `UND` / `ODER` / `NICHT`) und `)`, wenn eine Klammer offen ist.
  
  Fertige Bestandteile werden **als Objekte (Chips) dargestellt**: `Feld:Wert`-Chips farbig nach Feldtyp (entfernbar, per Klick bearbeitbar), Operator-Chips (Klick wechselt AND↔OR), **Klammern als zusammengehörige Objekte** (passendes Paar wird hervorgehoben, fehlende Klammer rot). Tastatur: ↑/↓, Enter/Tab übernehmen, Esc schließen, Rücktaste macht einen Chip wieder zu Text. Umschaltbar auf reine Textansicht. Verständliche Fehlermeldungen mit Position; fehlerhafte Abfragen fallen automatisch auf die einfache Suche zurück.
- **Gespeicherte Abfragen** als Suchprofile/virtuelle Ordner, **Suchverlauf**, Abfragen auch in **Workflows** als Bedingung nutzbar.
- **Geschwindigkeit:** Ziel < 100 ms für typische Abfragen bei 100.000+ Mails. Umsetzung: eigener Parser → Syntaxbaum → **eine** optimierte SQL-Abfrage (FTS5-Volltext mit BM25 + indizierte Filter), zusätzlicher **Trigramm-Index** für Teilwort- und Tippfehlersuche, seitenweises Laden per Cursor, inkrementelle Indexierung im Hintergrund, Index-Optimierung und -Statistik in den Einstellungen.
- Im **Server-Modus** läuft dieselbe Suche serverseitig; für sehr große Team-Installationen ist optional ein externer Index (OpenSearch/Elasticsearch-kompatibel oder Meilisearch) als austauschbares Such-Backend vorgesehen.

### 8.5 Workflow-System (Regeln & Automatisierung)
Statt einer reinen Mail-Regel-Engine erhält TuxMail ein **allgemeines Workflow-System**, das über alle Bereiche hinweg arbeitet: Mail, Messenger, Kalender, Kontakte, Dateien/Cloud und KI. Klassische Mail-Regeln sind darin nur ein Spezialfall.

**Aufbau eines Workflows: Auslöser → Bedingungen → Aktionen**

| Baustein | Beispiele |
|---|---|
| **Auslöser** | Mail empfangen / gesendet / verschoben / markiert · Messenger-Nachricht empfangen · **keine Antwort nach X Tagen** (8.12) · Termin beginnt in X Minuten / wurde angenommen · Kontakt neu · Datei in Cloud-Ordner neu · Zeitplan (täglich, wöchentlich, Cron) · manuell (Schaltfläche, Befehlspalette, Kontextmenü) · Webhook (nur Server-Modus) |
| **Bedingungen** | Absender/Empfänger/Domain, Betreff/Text (enthält, Regex), Header, Konto, Ordner, Kategorie (8.8), Anhang (Typ, Name, Größe), Rechnungserkennung inkl. **ZUGFeRD/XRechnung**, Kanal (Mail/WhatsApp/Signal/…), Kontaktgruppe, Uhrzeit/Wochentag, PGP-Status, KI-Klassifikation; verknüpfbar mit UND/ODER/NICHT |
| **Aktionen** | verschieben, kopieren, markieren, Label/Kategorie, löschen, archivieren · antworten mit Vorlage, weiterleiten, Abwesenheitsnotiz · **als PDF archivieren** (8.11) · Anhänge in Nextcloud/OpenCloud ablegen · Termin/Aufgabe erzeugen · Kontakt anlegen/aktualisieren · Benachrichtigung (Desktop, Push, an mich per Signal/Telegram/Matrix) · Konversation stummschalten/anheften · **im gemeinsamen Postfach zuweisen** (8.14) · KI zusammenfassen/klassifizieren · HTTP-Webhook aufrufen (z. B. n8n, Node-RED, Home Assistant) · Variable setzen · Unter-Workflow starten |

**Eigenschaften**
- **Grafischer Editor** (Bausteine untereinander, Wenn/Dann-Verzweigungen) und gleichwertige **Textansicht in YAML** zum Kopieren, Versionieren und Teilen. Workflows sind exportier-/importierbar und Teil des Backups „Nur Konfiguration".
- **Vorlagen-Galerie** für typische Abläufe: „Rechnungen als PDF in Nextcloud ablegen", „Newsletter automatisch archivieren", „Warten auf Antwort", „VIP-Mails per Signal melden", „Anhänge > 10 MB automatisch als Cloud-Link", „Bewerbungen ins Team-Postfach und Ticket zuweisen".
- **Testlauf (Trockenlauf):** Workflow gegen vorhandene Mails simulieren und anzeigen, was passieren würde – ohne etwas zu ändern. Rückwirkend auf bestehende Ordner anwendbar.
- **Ausführungsprotokoll** je Workflow (wann, worauf, welches Ergebnis, Fehler) mit Wiederholen-Schaltfläche.
- **Ausführungsort:** lokal im Desktop-Client oder im **Server-Modus** dauerhaft im Hintergrund (läuft auch, wenn kein Client offen ist). Wo möglich, werden einfache Mail-Bedingungen zusätzlich als **Sieve**-Skript auf den Server übertragen bzw. als Graph/EWS-Posteingangsregel angelegt, damit sie auch ohne TuxMail greifen.
- **Sicherheit:** Aktionen mit Außenwirkung (automatisch antworten, weiterleiten, Webhook) zeigen beim Aktivieren eine Bestätigung; Schleifenschutz (max. Auslösungen pro Nachricht/Zeit), Ratenbegrenzung, keine Weiterleitung entschlüsselter PGP-Inhalte ohne ausdrückliche Freigabe.
- **Technik:** Ereignisse kommen über den bestehenden Event-Bus (4.2); Workflows werden durch eine eigene Engine (`internal/workflow`) mit persistenter Warteschlange (Op-Queue) ausgeführt, damit nach Neustart nichts verloren geht. Bedingungen werden mit einer sicheren Ausdruckssprache (z. B. `expr-lang/expr`) ausgewertet – kein beliebiger Code.
- **Expertenmodus für Workflows** (wie bei der Suche): Neben dem grafischen Editor gibt es
  - eine kompakte **Regel-Syntax** mit Objekt-Eingabe (Chips) und kontextabhängiger Autovervollständigung, z. B.
    `WHEN mail.received IN account:"Firma" IF from:*@kunde.de AND has:attachment THEN tag "Kunde", move "Kunden/Eingang", notify signal "Neue Kundenmail: {{betreff}}"` – Bedingungen verwenden **dieselbe Abfragesprache wie die Suche**;
  - die **YAML-Ansicht** mit Syntaxhervorhebung, Zeilennummern, Fehlermarkierung mit Zeile/Spalte und Autovervollständigung (inkl. JSON-Schema für externe Editoren).
  Zwischen den drei Modi kann verlustfrei gewechselt werden, soweit darstellbar.
- **Abwesenheitsnotiz** ist ein fester Bestandteil des Workflow-Systems (eingebauter Workflow-Typ mit eigener, einfacher Oberfläche):
  - Zeitraum (von/bis, optional), gültig für ausgewählte Konten/Identitäten.
  - Getrennte Texte für **interne** und **externe** Absender; Betreff und Text mit Platzhaltern (z. B. `{{bis_datum}}`, `{{vertretung}}`).
  - Antwort **höchstens einmal pro Absender** innerhalb von N Tagen; keine Antworten an Newsletter/Verteiler (`List-Id`, `Precedence: bulk`), automatisch erzeugte Mails (`Auto-Submitted`), `noreply`-Adressen oder eigene Adressen; gesendete Antworten tragen `Auto-Submitted: auto-replied`.
  - Optional: Weiterleitung an eine **Vertretung**, automatische Antwort auch in Messenger-Kanälen, Kalender-Eintrag „Abwesend" und automatische Absage neuer Einladungen im Zeitraum.
  - **Server-Synchronisation:** Wo möglich, wird die Notiz auf den Server übertragen (Sieve `vacation` RFC 5230, Graph `automaticRepliesSetting`, EAS `Settings/Oof`) – dann greift sie auch bei ausgeschaltetem PC. Ansonsten führt TuxMail sie lokal bzw. im Server-Modus aus.

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

### 8.11 Druckansicht & PDF-Archivierung
- **Druckansicht** für Mail, ganze Konversation (klassisch oder als Chat-Verlauf), Termin, Tages-/Wochenkalender und Kontakt; Kopfzeile mit Absender/Empfänger/Datum, Anhangsliste, wählbar mit/ohne externe Bilder.
- **PDF-Export** einzeln oder für viele Mails auf einmal (z. B. ganzer Ordner, Suchergebnis).
- **Archiv-PDF nach PDF/A-3:** Die Original-Mail (`.eml`) und alle Anhänge werden **in die PDF eingebettet** – revisionssicher lesbar und trotzdem vollständig. Dateiname nach Muster, z. B. `{{datum}}_{{absender}}_{{betreff}}.pdf`.
- **Automatisch per Workflow** (8.5): z. B. „Jede Rechnung als PDF in Nextcloud `/Buchhaltung/{{jahr}}/{{monat}}/` ablegen". Rechnungen werden erkannt über Kategorie (8.8), Schlagwörter, PDF-Anhänge und strukturierte E-Rechnungen (**ZUGFeRD/Factur-X, XRechnung**) – deren Daten (Rechnungsnummer, Betrag, Fälligkeit) können für Dateinamen und eine Aufgabe „Rechnung bezahlen bis …" verwendet werden.
- Ziele: lokaler Ordner, Nextcloud, OpenCloud, WebDAV; Duplikatschutz über Message-ID.
- **Technik:** Desktop rendert über das System-WebView (WebView2 `PrintToPdf`, WebKitGTK-Druckausgabe) – identisch zur Anzeige. Im Server-Modus wird ein eigener Go-Renderer für den Mailkopf plus HTML-Rendering über einen optionalen Headless-Chromium-Dienst (z. B. Gotenberg im docker-compose) genutzt; PDF/A-Anreicherung und Einbettung mit `pdfcpu`.
- Hinweis: TuxMail unterstützt eine geordnete Ablage; ob diese den Anforderungen der GoBD genügt, hängt vom Gesamtverfahren (Verfahrensdokumentation, Unveränderbarkeit des Speicherorts) ab.

### 8.12 „Warten auf Antwort"
- Beim Senden (oder nachträglich) Schalter **„Erinnern, wenn keine Antwort bis …"** (morgen, 3 Tage, 1 Woche, Datum).
- TuxMail erkennt Antworten über das Konversations-Mapping (Kapitel 6) – auch, wenn die Antwort über einen **anderen Kanal** (z. B. WhatsApp derselben Person) kommt (einstellbar).
- Kommt keine Antwort: Mail erscheint wieder oben im Posteingang bzw. im Bereich **„Wartet auf Antwort"**, mit Schnellaktion „Nachfassen" (Vorlage + KI-Vorschlag).
- Automatische Vorschläge: Mails mit Fragen („?", „bitte um Rückmeldung", „bis wann") werden als Kandidaten markiert.
- Technisch ein Workflow-Auslöser („keine Antwort nach X") – auch in eigenen Workflows nutzbar.

### 8.13 Versand-Prüfungen
Vor dem Senden prüft TuxMail die Mail und zeigt Hinweise – einzeln abschaltbar, als Warnung oder als Sperre konfigurierbar:
- **Externe Empfänger:** Warnung, wenn eine Firmenmail Empfänger außerhalb der eigenen Domain(s) enthält; deutlich bei **vielen** externen Empfängern in An/CC („BCC verwenden?").
- **Private Adressen in Firmenmails** (oder umgekehrt): z. B. gmail/gmx-Adressen im CC einer dienstlichen Mail; falsche Identität für den Empfänger (Firmenkontakt, aber privates Absenderkonto).
- **Anhang vergessen** (Schlüsselwörter „anbei", „im Anhang", „attached"), **Anhang zu groß** → Cloud-Link-Vorschlag.
- **Verschlüsselung:** Empfänger hat PGP-Schlüssel, Mail ist aber unverschlüsselt; vertrauliche Schlagwörter („vertraulich", „Gehalt", IBAN) ohne Verschlüsselung.
- **Sensible Daten:** IBAN, Kreditkartennummern, Passwörter im Klartext erkannt.
- **Fehlender Betreff**, leere Mail, Platzhalter nicht ersetzt (`{{vorname}}`), „Allen antworten" an große Verteiler, Antwort an eine alte Konversation (> 6 Monate).
- **Uhrzeit-Hinweis:** Versand außerhalb der Arbeitszeit des Empfängers → „Später senden?"
- Eigene Prüfungen lassen sich als Workflow mit Auslöser „vor dem Senden" ergänzen; im Server-Modus können Administratoren Prüfungen für alle Benutzer vorgeben.

### 8.14 Gemeinsame Postfächer (Server-Modus)
Im Server-Modus können mehrere TuxMail-Benutzer ein Postfach gemeinsam bearbeiten (z. B. `info@`, `support@`, `bewerbung@`):
- Das Postfach wird **einmal** auf dem Server verbunden; Benutzer/Gruppen erhalten Rechte (lesen, beantworten, zuweisen, verwalten).
- **Zuweisung** von Konversationen an Personen („Mir zuweisen", „an Anna"), **Status** (offen, in Bearbeitung, wartet, erledigt), Fälligkeit, Prioritäten.
- **Interne Kommentare und @Erwähnungen** an der Konversation – für Kunden unsichtbar; Erwähnungen erzeugen Benachrichtigungen.
- **Kollisionserkennung:** „Anna schreibt gerade eine Antwort" – verhindert doppelte Antworten; Entwürfe sind für das Team sichtbar (optional).
- Antworten gehen mit der Postfach-Identität (z. B. „Firma Support") und optional persönlicher Signatur des Bearbeiters.
- Gemeinsame Textbausteine, Vorlagen und Workflows (z. B. automatische Zuweisung nach Stichwort oder reihum).
- Ansichten: „Mir zugewiesen", „Nicht zugewiesen", „Erledigt"; einfache Statistik (Anzahl, Reaktionszeit).
- Auch Messenger-Konten (z. B. ein Firmen-WhatsApp über Bridge) können als gemeinsames Postfach betrieben werden.
- Rechte- und Audit-Log: wer hat was gelesen, beantwortet, zugewiesen.

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
| **Telegram** (Version 1.0) | `gotd/td` (offizielle MTProto-API, Drittclients ausdrücklich erlaubt; eigene `api_id` des Projekts) | Telefonnummer + Code bzw. QR-Code, 2FA-Passwort | Chats, Gruppen, Kanäle, Medien, Reaktionen, Antworten, Bearbeiten | Gering |
| **Matrix** (Version 1.0) | `mautrix-go` (inkl. Ende-zu-Ende-Verschlüsselung über Olm/Megolm, Geräteverifizierung) | Login mit Passwort oder SSO/OIDC | Räume, Direktchats, Medien, Reaktionen, Threads | Gering |

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

### 15.5 Server-Installation (Linux) mit Ansible
Für den Server-Modus (Kapitel 4.5) wird ein **Ansible-Playbook** mitgeliefert (`deploy/ansible/`). Unterstützt werden Debian 12/13, Ubuntu 22.04/24.04 sowie RHEL/Rocky/Alma 9.

**Was das Playbook erledigt**
- Systembenutzer `tuxmail` (ohne Login), Verzeichnisse `/opt/tuxmail` (Programm), `/var/lib/tuxmail` (Daten), `/etc/tuxmail` (Konfiguration), `/var/log/tuxmail`.
- Installation des TuxMail-Binaries in einer festen Version (Download aus Releases mit Prüfsumme **oder** lokale Datei), Konfigurationsdatei `config.yaml` aus Template.
- **systemd-Dienst** `tuxmail.service` mit Härtung (`ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`, eingeschränkte Schreibpfade), automatischer Neustart, optional `tuxmail-backup.timer` für tägliche Backups.
- **nginx als Reverse-Proxy**, wahlweise:
  - **neu installieren und konfigurieren**, oder
  - **in eine bestehende nginx-Installation einhängen** (eigene `server`-Datei oder nur ein `location`-Snippet zum Einbinden in einen vorhandenen vHost, z. B. neben Nextcloud), ohne bestehende Konfiguration zu überschreiben; vorher `nginx -t`.
  - WebSocket-Weiterleitung, große Upload-Limits für Anhänge, Sicherheits-Header (HSTS, CSP), HTTP→HTTPS-Umleitung.
- **TLS-Zertifikat**, wahlweise:
  - **Let's Encrypt** über certbot (HTTP-01 via nginx-Webroot) inkl. automatischer Erneuerung, oder
  - **eigenes Zertifikat** über Pfadangaben (`tls_cert_path`, `tls_key_path`, optional Kette), z. B. Firmen-CA oder Wildcard-Zertifikat, oder
  - **kein TLS in TuxMail/nginx**, wenn ein vorgelagerter Proxy TLS terminiert.
- Optional: Matrix-Homeserver + mautrix-Bridges (Bridge-Modus) und Gotenberg (PDF) als Container bzw. Dienste, Firewall-Regeln (ufw/firewalld).
- **Erstinstallation:** legt den ersten Administrator an (Passwort aus Ansible Vault oder zufällig generiert und ausgegeben).
- Idempotent und für Updates nutzbar (Version ändern → Playbook erneut ausführen; Datenbank-Migration läuft automatisch, vorher Backup).

**Beispiel**
```bash
ansible-playbook -i inventory.ini deploy/ansible/site.yml \
  -e tuxmail_domain=mail.example.de \
  -e tuxmail_nginx_mode=new        # new | existing
  -e tuxmail_tls_mode=letsencrypt  # letsencrypt | custom | none
```

### 15.6 Benutzerverwaltung (Server-Modus)
- **Benutzer und Rollen:** `admin` (alles inkl. Benutzerverwaltung und Server-Einstellungen), `user` (eigene Konten, Daten, Workflows), optional `readonly`/Gast für gemeinsame Postfächer. **Gruppen** für die Rechtevergabe an gemeinsamen Postfächern (8.14).
- **Strikte Datentrennung:** jeder Benutzer hat eine eigene Datenbank und einen eigenen Blob-Speicher unter `/var/lib/tuxmail/users/<id>/`; gemeinsame Postfächer liegen in einem eigenen Bereich mit Rechteprüfung.
- **Anmeldung:** Benutzername/E-Mail + Passwort (Argon2id-Hash), **Zwei-Faktor** per TOTP (Authenticator-App) und **WebAuthn/Passkeys**, Wiederherstellungscodes; alternativ/zusätzlich **OIDC** (Nextcloud, OpenCloud, Keycloak, Authentik, Entra ID) mit automatischer Benutzeranlage und Gruppen-Zuordnung aus Claims; optional LDAP/Active Directory.
- **Sitzungen:** sichere Cookies (HttpOnly, Secure, SameSite), Übersicht aktiver Sitzungen/Geräte mit „Abmelden"; Desktop-App als Thin Client per Geräte-Token.
- **Verwaltung:** Web-Oberfläche für Admins (Benutzer anlegen, sperren, Passwort zurücksetzen, 2FA zurücksetzen, Speicherverbrauch, Quoten, Rollen/Gruppen) **und** Kommandozeile für Skripte/Ansible: `tuxmail user add|list|disable|enable|reset-password|reset-2fa|set-role|delete`.
- Selbstregistrierung standardmäßig aus; optional per Einladungslink.
- **Sicherheit:** Login-Ratenbegrenzung und temporäre Sperre, Passwort-Richtlinie, Audit-Log (Anmeldungen, Admin-Aktionen), Benachrichtigung bei neuer Anmeldung.
- **Geheimnisse der Benutzer** (Mail-Passwörter, Tokens) werden pro Benutzer verschlüsselt; der Schlüssel wird aus dem Benutzerpasswort abgeleitet bzw. bei OIDC über einen Server-Schlüssel (KEK) geschützt.

### 15.7 PWA für Android und iPhone mit Push-Benachrichtigungen
- Der Web-Client des Server-Modus ist eine **installierbare Progressive Web App**: „Zum Startbildschirm hinzufügen" auf Android (Chrome, Firefox, Edge) und iPhone/iPad (Safari, ab iOS/iPadOS 16.4).
- **Aktive Benachrichtigungen per Web Push** (VAPID, RFC 8030/8291/8292) – auch wenn die App geschlossen ist. Der Server versendet Push-Nachrichten bei neuer Mail, Chat-Nachricht, Termin-Erinnerung, „Warten auf Antwort", Zuweisung im gemeinsamen Postfach und Workflow-Benachrichtigungen.
  - Android: Push funktioniert im Browser und in der installierten PWA.
  - **iPhone: Apple erlaubt Web Push nur für PWAs, die zum Home-Bildschirm hinzugefügt wurden** – TuxMail zeigt dafür in Safari einen Installationshinweis mit Anleitung.
- Einstellungen je Gerät: Benachrichtigungen an/aus, **Ruhezeiten**, Auswahl je Konto/Kanal/Ordner (z. B. nur Posteingang Firma + Signal), **Datenschutzmodus** („Neue Nachricht von Anna" mit Vorschau oder nur „Neue Nachricht" ohne Inhalt – Inhalte gehen dann nicht über die Push-Dienste von Apple/Google), Testbenachrichtigung.
- Tippen auf die Benachrichtigung öffnet direkt die betreffende Mail bzw. Konversation; gruppierte Benachrichtigungen pro Konversation; Ungelesen-Zähler auf dem App-Symbol (Badging API, sofern unterstützt).
- Offline-Fähigkeit: App-Shell und zuletzt geöffnete Konversationen werden im Service Worker zwischengespeichert; Entwürfe werden lokal gehalten und beim nächsten Kontakt gesendet.
- Später optional: native Apps (z. B. über Capacitor) für noch zuverlässigere Zustellung und Teilen-Menü – die PWA deckt Version 1.0 ab.

### 15.8 Client-Anmeldung am Server & automatische Einrichtung
Ein Desktop-Client (Windows/Linux, portabel oder installiert) kann sich am eigenen TuxMail-Server anmelden und wird dadurch **automatisch eingerichtet**.

**Ablauf „Mit TuxMail-Server verbinden"**
1. Im Client Server-Adresse eingeben (oder Link/QR-Code aus der Web-Oberfläche verwenden).
2. Anmeldung **per Gerätebestätigung** (wie beim Fernseher, angelehnt an RFC 8628): Der Client zeigt einen kurzen Code, der Browser öffnet sich, man bestätigt dort – inkl. Zwei-Faktor oder OIDC/Single-Sign-On. Alternativ direkte Anmeldung mit Benutzername, Passwort und TOTP.
3. Der Server stellt ein **Geräte-Token** aus (widerrufbar, in der Geräteliste sichtbar).
4. Der Client lädt das **Einrichtungsprofil**: Konten (Mail, Kalender, Kontakte, Cloud, Messenger-Bridge), Identitäten/Aliase, Signaturen, Workflows, Suchprofile, Ansichtseinstellungen. Der Benutzer wählt per Häkchen aus, was übernommen wird.
5. **Zugangsdaten** werden nur auf ausdrücklichen Wunsch übertragen – **Ende-zu-Ende-verschlüsselt** an einen Schlüssel, den der Client bei der Anmeldung erzeugt hat.

**Zwei Betriebsarten nach der Einrichtung**
| Modus | Beschreibung | Geeignet für |
|---|---|---|
| **Thin Client** | Der Server synchronisiert alle Konten; der Client zeigt nur an und arbeitet über die Server-API (mit lokalem Cache für Offline-Lesen). | Firmen, viele Geräte, Messenger über Bridge, schwache Rechner |
| **Lokal (provisioniert)** | Der Client synchronisiert die Konten selbst; vom Server kommen nur Konfiguration und laufende **Einstellungs-Synchronisation** (Signaturen, Workflows, Regeln bleiben auf allen Geräten gleich). | Laptops mit viel Offline-Arbeit, maximale Unabhängigkeit |

**Für Administratoren**
- **Einrichtungsvorlagen** (z. B. Firmen-IMAP/SMTP/CalDAV/CardDAV, Nextcloud, Signaturvorlage mit `%NAME%`, `%EMAIL%`, `%TELEFON%`) je Benutzer oder Gruppe – neue Mitarbeiter melden sich an und haben alles fertig eingerichtet.
- Vorgabe, welche Modi erlaubt sind, ob Zugangsdaten übertragen werden dürfen und ob portabler Betrieb erlaubt ist.
- Geräteübersicht pro Benutzer mit Sperren/Fernabmeldung.

### 15.9 Android-App (APK) und iOS
Die Oberfläche (Svelte) und die Transport-Abstraktion ermöglichen native Mobil-Apps ohne Neuentwicklung der UI.

| Stufe | Umsetzung | Beschreibung |
|---|---|---|
| **A – Thin Client (empfohlen zuerst)** | **Capacitor** (Android, später iOS) | UI im APK, verbindet sich mit beliebigen TuxMail-Servern (Geräte-Anmeldung wie 15.8). Native Push: **FCM** (Play-Store-Variante) bzw. **UnifiedPush** (F-Droid/ohne Google-Dienste, z. B. ntfy). Teilen-Menü (Dateien/Text an TuxMail senden), Anhänge mit System-Apps öffnen, Kamera/Galerie, biometrische App-Sperre, Benachrichtigungen mit Schnellantwort, Offline-Cache. |
| **B – Eigenständig (später)** | Go-Kern per `gomobile bind` als Android-Bibliothek + gleiche UI | Synchronisiert Konten direkt auf dem Gerät (ohne Server). Hintergrund-Sync per Vordergrunddienst/WorkManager; Messenger nur über den Bridge-Modus. |
| PWA-Verpackung (TWA) | Bubblewrap | Nur für eine feste Server-Domain sinnvoll (z. B. Firmenserver), nicht als allgemeine App. |

**Verteilung:** F-Droid (freie Abhängigkeiten, UnifiedPush), Google Play (einmalige Entwicklergebühr, Identitätsprüfung), APK-Download über GitHub Releases (künftige Entwickler-Verifizierung durch Google beachten). **iOS** mit demselben Capacitor-Projekt (Mac + Apple-Entwicklerkonto nötig); bis dahin deckt die PWA (15.7) iPhone/iPad ab.

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

### 16.4 Sprachen & Sprachdateien
- **Englisch und Deutsch** von Anfang an vollständig; **Englisch ist die Standard- und Referenzsprache** (Rückfall bei fehlenden Übersetzungen). Ohne passende System-/Browsersprache startet TuxMail auf Englisch; deutsche Systeme erhalten automatisch Deutsch.
- Alle Texte liegen in **Sprachdateien** im Repository unter `locales/` (`de.json`, `en.json`) – **eine gemeinsame Quelle** für Oberfläche (Desktop, Web, PWA) **und** Server-Texte (Push-Benachrichtigungen, Standardtexte der Abwesenheitsnotiz, Versand-Prüfungen, PDF-Kopfzeilen, Fehlermeldungen, E-Mails des Servers wie Einladungen/Passwort-Reset). Der Go-Kern bettet die Dateien ein (`internal/i18n`), das Frontend bindet dieselben Dateien beim Build ein.
- Aufbau: verschachtelte Schlüssel nach Bereich (`mail.compose.send`), Platzhalter `{name}`, Mehrzahlformen nach Unicode-CLDR-Regeln, Metadaten (Name, Eigenname, Schreibrichtung). Datum, Uhrzeit, Zahlen, Währungen und relative Zeitangaben („vor 5 Minuten") werden über die Locale formatiert.
- **Sprachwahl:** Englisch / Deutsch / Systemsprache – pro Benutzer; im Server-Modus erhalten Push-Benachrichtigungen und Server-Mails die Sprache des jeweiligen Benutzers.
- **Neue Sprache hinzufügen:** `en.json` kopieren, übersetzen, fertig – Anleitung in `locales/README.md`. Zusätzlich können Sprachdateien zur Laufzeit aus dem Datenverzeichnis (`<data>/locales/xx.json`) geladen werden, um Sprachen zu ergänzen oder einzelne Begriffe zu überschreiben (z. B. firmeneigene Bezeichnungen), ohne neu zu bauen.
- **Qualitätssicherung:** automatischer Test, dass alle Sprachdateien dieselben Schlüssel und Platzhalter haben; Prüfung auf fest verdrahtete Texte im Code; fehlende Übersetzungen fallen auf Englisch zurück und werden im Entwicklungsmodus gemeldet. Optional Anbindung an eine Übersetzungsplattform (z. B. Weblate) für Community-Übersetzungen.

### 16.5 Region: Zeitzone, Datums- und Zeitformate
Unabhängig von der Sprache einstellbar unter *Einstellungen → Sprache & Region* (pro Benutzer, im Server-Modus serverseitig gespeichert):
- **Zeitzone** (IANA-Liste mit Suche; Standard: System/Browser; Option „immer Systemzeitzone verwenden"); **zweite Zeitzone** im Kalender einblendbar; Termine aus anderen Zeitzonen werden umgerechnet und mit Hinweis angezeigt.
- **Datumsformat:** Standard der Sprache, `TT.MM.JJJJ`, `JJJJ-MM-TT` (ISO 8601), `MM/TT/JJJJ`, `TT/MM/JJJJ`, ausgeschrieben („3. November 2026") oder eigenes Muster.
- **Uhrzeitformat:** 24 Stunden / 12 Stunden (AM/PM) / Standard der Sprache.
- **Erster Wochentag:** Montag / Sonntag / Samstag / Standard der Sprache; Kalenderwochen-Anzeige (ISO).
- **Zahlenformat:** Dezimal- und Tausendertrennzeichen (z. B. `1.234,56` oder `1,234.56`).
- **Relative Zeitangaben** („vor 5 Minuten") ein/aus.
- Live-Vorschau in den Einstellungen; alle Datums-/Zeitanzeigen (Listen, Lesebereich, Chat-Blasen, Kalender, Suche, PDF-Export, Server-Benachrichtigungen) laufen über **ein zentrales Formatierungsmodul**. Intern werden Zeitpunkte immer in UTC gespeichert.

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
│   │   ├── workflow/          # Workflow-Engine (Auslöser, Bedingungen, Aktionen)
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
| **7 – Messenger** | Plugin-Host, **WhatsApp, Signal, Telegram, Matrix** (Pflicht für 1.0), gemischte Personen-Zeitleiste | Unified Inbox | 5,5 PM |
| **8 – Bridge-Modus** | `plugin-matrix-bridge`, Bridge-Zuordnung, Kopplungsassistent, docker-compose mit Homeserver + Bridges | Messenger 24/7 | 2 PM |
| **9 – Server & Web-Client** | `tuxmail serve`, API-Generierung (Connect-RPC), Transport-Abstraktion im Frontend, Benutzerverwaltung (Rollen, Gruppen, 2FA, OIDC, CLI), PWA/Web-Push, Docker-Image, **Ansible-Playbook** (nginx neu/bestehend, systemd, Let's Encrypt/eigenes Zertifikat) | Zugriff über Browser | 5 PM |
| **9b – Workflows & Team** | Workflow-Engine + grafischer Editor + YAML, Vorlagen, Trockenlauf, Protokoll; „Warten auf Antwort"; Versand-Prüfungen; Druck/PDF/A-Archivierung inkl. E-Rechnungserkennung; gemeinsame Postfächer (Zuweisung, Kommentare, Kollisionserkennung) | Automatisierung & Teamarbeit | 5 PM |
| **10 – Feinschliff & Release 1.0** | Videokonferenz-Links, Exchange-Delegation, KI-Assistenz (lokal + Cloud-Anbieter), S/MIME, Regeln/Sieve, Barrierefreiheit, Übersetzungen, Code-Signierung, OAuth-Verifizierungen, Sicherheitsprüfung des Web-Modus, Dokumentation | Release | 5 PM |
| | | **Summe 1.0** | **≈ 49 PM** |
| *nach 1.0* | Instagram/Facebook (experimentell), Google Messages, Google Chat | Optionale Kanäle | *≈ 3–4 PM* |
| *nach 1.0* | **Android-App** als Thin Client (Capacitor, FCM/UnifiedPush), danach iOS; später eigenständige Android-App mit Go-Kern | Mobil-Apps | *≈ 1,5 PM (Thin Client) + 3–4 PM (eigenständig)* |

- Mit 3 Entwicklern: **ca. 16–18 Monate** bis Version 1.0 (mit 4 Entwicklern ca. 12–14 Monate); ein nutzbarer Mail-Client mit PGP und Konversationsansicht (Phasen 0–2) nach **ca. 5 Monaten**.
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
| 8 | Messenger für 1.0 | **WhatsApp, Signal, Telegram, Matrix**; Instagram, Facebook, Google Messages/Chat nach 1.0 | Kapitel 11, Roadmap |
| 9 | OpenPGP | **Bereits im Mail-MVP** (inkl. Autocrypt, WKD, geschützte Header) | Kapitel 8.6, Phase 1 |
| 10 | Zusatzfunktionen 1.0 | Spam-Filter & Newsletter-Abmeldung, smarte Kategorien, Rechtschreibprüfung & Serienmails, Videokonferenz-Links, Exchange-Delegation, KI-Assistenz (optional; lokal oder OpenAI/Gemini/DeepSeek) | Kapitel 7.3a, 8.7–8.10, 9.1 |
| 12 | Weitere Funktionen 1.0 | **Workflow-System** (ersetzt reine Regeln), Druck & PDF/A-Archivierung, „Warten auf Antwort", Versand-Prüfungen, gemeinsame Postfächer (Server-Modus), **Telegram und Matrix** als zusätzliche Messenger | Kapitel 8.5, 8.11–8.14, 11.2 |
| 11 | Bewusst **nicht** in 1.0 | Wegwerf-Aliase (SimpleLogin/addy.io), Terminbuchungsseite | ggf. nach 1.0 |

---

*Ende des Konzepts. Nächster Schritt: Phase 0 – Projektgerüst (Go-Modul, Wails, SQLite-Schema, Build-Pipeline) aufsetzen.*
