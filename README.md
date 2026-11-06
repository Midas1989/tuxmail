<p align="center"><img src="assets/branding/tuxmail-logo.png" alt="TuxMail" width="320"></p>

# TuxMail

**E-Mail, Kalender, Kontakte und Messenger in einem Programm – für Windows, Linux und Android, wahlweise auch als
eigener Webserver.** Freie Software unter der AGPLv3.

> **Status:** Vorab-Version (0.x). Die Programme laufen und werden getestet, sind aber noch nicht für den
> unbeaufsichtigten Dauerbetrieb gedacht. Bitte vor dem Umstieg eine Sicherung der eigenen Mails anlegen.

## Was kann TuxMail?

- **E-Mail** mit IMAP, POP3, SMTP, Exchange ActiveSync, Microsoft 365 (Graph/EWS), Gmail und JMAP. Die
  Servereinstellungen werden beim Hinzufügen eines Kontos automatisch gesucht.
- **Drei Ansichten für dieselben Daten:** klassische Drei-Spalten-Ansicht, Hybrid und eine **Messenger-artige
  Konversationsansicht auch für E-Mails**. Jederzeit umschaltbar.
- **Kalender, Aufgaben und Kontakte** (CalDAV/CardDAV, Einladungen per E-Mail, Zusammenführung doppelter Kontakte).
- **Messenger** (WhatsApp, Signal, Telegram, Matrix) als verknüpftes Gerät, in derselben Oberfläche wie die Mails.
- **Verschlüsselung:** OpenPGP (inkl. Autocrypt) und S/MIME.
- **Ordnung und Automatik:** Tags, Projektordner, Suchordner, Regeln und Workflows, lernender Spam-Filter,
  Newsletter-Abmeldung, smarte Kategorien, „Warten auf Antwort“, Versand-Prüfungen.
- **Cloud-Anhänge** als Freigabelink über Nextcloud, OpenCloud und Vaultwarden Send.
- **PDF-Archiv und Druckansicht**, Erkennung von **E-Rechnungen** (ZUGFeRD/Factur-X, XRechnung).
- **Optionale KI-Assistenz**, lokal (z. B. Ollama) oder über einen Cloud-Anbieter. Standardmäßig aus.
- **Import und Export:** Thunderbird-Profil, mbox, EML, Maildir, PST, ICS, vCard, CSV, LDIF; **Backup und Restore**.
- **Server-Modus für Teams:** mehrere Benutzer, gemeinsame Postfächer, Anmeldung per OIDC, Zwei-Faktor-Anmeldung (TOTP) und **Passkeys** (WebAuthn),
  Web-Oberfläche als **PWA** mit Push-Benachrichtigungen, **Android-App** als Client.
- **Portabel oder installiert:** Windows-ZIP und Linux-Archiv laufen ohne Installation, auch vom USB-Stick.

Oberfläche auf Deutsch und Englisch.

## Schnell installieren

Alle Pakete stehen unter **[Releases](https://github.com/Midas1989/tuxmail/releases)**. Ausführliche Anleitung
mit allen Varianten: **[docs/INSTALLATION.md](docs/INSTALLATION.md)**.

| Ich nutze … | Empfehlung |
|---|---|
| **Windows 10/11** | `TuxMail-<v>-windows-amd64-setup.exe` herunterladen und starten |
| **Debian, Ubuntu, Linux Mint** | `sudo apt install ./tuxmail_<v>-1_amd64.deb` |
| **Fedora, RHEL, Rocky, Alma** | `sudo dnf install ./tuxmail-<v>-1.x86_64.rpm` |
| **jede andere Linux-Distribution** | `TuxMail-<v>-linux-amd64.AppImage`: `chmod +x` und starten |
| **Android** | `TuxMail-<v>-foss.apk` installieren und die Adresse des eigenen TuxMail-Servers eingeben |
| **iPhone / jedes Smartphone** | Server-Adresse im Browser öffnen und zum Home-Bildschirm hinzufügen (PWA) |
| **eigener Server (Linux)** | Ansible-Playbook in [deploy/](deploy/README.md), alternativ Docker |

> **Stand der Releases:** `v0.1.0` enthält bisher nur die Android-Debug-APK. Alles andere baust du bis zum nächsten
> Release selbst, siehe „Selbst bauen“ unten.

`<v>` ist die Versionsnummer, z. B. `0.1.0`. Auf ARM-Geräten (`arm64`, Raspberry Pi 4/5, Snapdragon-Notebooks)
dieselben Dateien mit `arm64` bzw. `aarch64` nehmen.

Die Windows-Programme sind noch nicht digital signiert. Windows zeigt deshalb einmalig „Unbekannter Herausgeber“; die
Anleitung erklärt, wie man das umgeht. Downloads lassen sich über `checksums.txt` prüfen
(`sha256sum -c checksums.txt --ignore-missing`).

## Selbst bauen

Alle Varianten lassen sich ohne GitHub auf einem Linux-Rechner bauen:

```bash
build/build-all.sh --check     # zeigt, was auf diesem Rechner gebaut werden kann und was fehlt
build/build-all.sh             # baut alles Mögliche nach dist/
```

Einzelheiten, Voraussetzungen und der Weg zu einem Release: [docs/RELEASE.md](docs/RELEASE.md). Entwicklung und
Tests: [docs/ENTWICKLUNG.md](docs/ENTWICKLUNG.md), Beitragen: [CONTRIBUTING.md](CONTRIBUTING.md).

## Technik in Kürze

Go (Kern und Protokolle) · Wails und Svelte 5/TypeScript (Oberfläche) · SQLite (Daten) · Messenger als getrennte
Plugin-Prozesse · Android-App mit Capacitor. Desktop und Server sind dasselbe Programm: `tuxmail` öffnet das Fenster,
`tuxmail serve` startet den Webserver.

## Dokumentation

| Thema | Dokument |
|---|---|
| Installation aller Pakete (Windows, Linux, Android, Server) | [docs/INSTALLATION.md](docs/INSTALLATION.md) |
| Server einrichten: Ansible, nginx, TLS, Docker, Updates, Backups | [deploy/README.md](deploy/README.md) |
| Android-App bauen und verteilen | [mobile/README.md](mobile/README.md) |
| Release erstellen, alles lokal bauen, Signaturen | [docs/RELEASE.md](docs/RELEASE.md) |
| Microsoft 365 und Google einrichten | [docs/MICROSOFT-GOOGLE.md](docs/MICROSOFT-GOOGLE.md) |
| Exchange ActiveSync | [docs/EAS.md](docs/EAS.md) |
| Entwicklung, Tests, Entwicklungsserver | [docs/ENTWICKLUNG.md](docs/ENTWICKLUNG.md) |
| Konzept und Architektur | [docs/KONZEPT.md](docs/KONZEPT.md) |
| Aufgaben für den Projektinhaber | [docs/AUFGABEN-PROJEKTINHABER.md](docs/AUFGABEN-PROJEKTINHABER.md) |

## Sicherheit

Sicherheitslücken bitte **nicht** öffentlich melden, sondern vertraulich über
[GitHub Security Advisories](https://github.com/Midas1989/tuxmail/security/advisories/new). Ablauf und
unterstützte Versionen: [SECURITY.md](SECURITY.md). Die Ergebnisse der Sicherheitsprüfungen stehen in
[docs/security/](docs/security/README.md).

## Lizenz / License

TuxMail ist freie Software unter der **GNU Affero General Public License v3.0 (AGPLv3)** – siehe [LICENSE](LICENSE).
Du darfst TuxMail nutzen, verändern und weitergeben. Wer eine veränderte Version weitergibt **oder als Netzwerkdienst
(z. B. Server-Modus) für andere betreibt**, muss den Quellcode dieser Version unter derselben Lizenz zugänglich machen.
Lizenzen der verwendeten Bibliotheken: [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

TuxMail is free software licensed under the **GNU Affero General Public License v3.0 or later**
(`SPDX-License-Identifier: AGPL-3.0-or-later`).

**English in short:** TuxMail is a cross-platform mail, calendar, contacts and messenger client for Windows, Linux and
Android, with an optional multi-user web server. Download packages from
[Releases](https://github.com/Midas1989/tuxmail/releases); see [docs/INSTALLATION.md](docs/INSTALLATION.md)
(English summary at the end) and build everything yourself with `build/build-all.sh`.

Copyright (C) 2026 Midas Wollinger and TuxMail contributors
