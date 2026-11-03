# Konflux (Arbeitstitel)

Plattformübergreifender Kommunikations-Client für **Windows und Linux**:
E-Mail (IMAP/POP3/SMTP, Exchange ActiveSync, Microsoft Graph/EWS, Gmail, JMAP),
Kalender & Aufgaben, Kontakte, Messenger (WhatsApp, Signal, optional Instagram/Facebook/Google)
sowie Cloud-Anhänge über Nextcloud, OpenCloud und Vaultwarden Send.

Besonderheit: Neben der klassischen 3-Spalten-Ansicht gibt es eine **Messenger-artige
Konversationsansicht auch für E-Mails** – beide arbeiten auf denselben Daten und sind
jederzeit umschaltbar.

📄 Das vollständige Entwicklungskonzept: [docs/KONZEPT.md](docs/KONZEPT.md)

**Geplanter Stack:** Go (Kern & Protokolle) · Wails + Svelte 5/TypeScript (Oberfläche) · SQLite (Daten) ·
Messenger als separate Plugin-Prozesse (gRPC) · portabel lauffähig (Windows-ZIP, Linux-AppImage) oder installierbar.
