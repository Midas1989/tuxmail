<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Sicherheitsrichtlinie / Security Policy

*Deutsch unten, English below.*

## Deutsch

### Schwachstelle melden

Bitte melde Sicherheitslücken **nicht** über öffentliche Issues, Pull Requests oder Diskussionen.

Nutze stattdessen die private Meldung von GitHub:
**Repository → Security → Advisories → „Report a vulnerability“**
(<https://github.com/Midas1989/tuxmail/security/advisories/new>).

Hilfreich sind:

- betroffene Version (`tuxmail version`, „Info“ in den Einstellungen; `/healthz` nennt sie nur mit `security.expose_version: true`) und Betriebsart (Server, Desktop, Android, Plugin),
- Beschreibung, Auswirkung und Schritte zum Nachvollziehen (Proof of Concept),
- relevante Konfiguration (ohne Passwörter/Schlüssel), Logs, ggf. ein Vorschlag zur Behebung.

### Ablauf

| Schritt | Ziel |
|---|---|
| Eingangsbestätigung | innerhalb von 5 Werktagen |
| Erste Bewertung (Schweregrad, betroffene Versionen) | innerhalb von 14 Tagen |
| Fix und Release | je nach Schwere, Ziel ≤ 90 Tage |

Wir veröffentlichen ein GitHub Security Advisory (bei Bedarf mit CVE) zusammen mit dem Patch-Release und
nennen dich auf Wunsch als Entdecker:in. Bitte gib uns bis zur Veröffentlichung des Fixes oder bis 90 Tage
nach der Meldung Zeit (Coordinated Disclosure); bei aktiver Ausnutzung veröffentlichen wir früher.

### Unterstützte Versionen

TuxMail ist in der 0.x-Phase (Pre-Release). Sicherheitsfixes gibt es nur für die **jeweils neueste
veröffentlichte Version** (und den Branch `main`). Ältere 0.x-Versionen bitte aktualisieren.

| Version | Unterstützt |
|---|---|
| neueste 0.x | ja |
| ältere 0.x | nein |

### Release-Signaturschlüssel

**Fingerabdruck hier eintragen** (Platzhalter, wird vom Maintainer nach dem Anlegen des Release-Schlüssels
gesetzt, siehe `docs/RELEASE.md`):

```
Release-Signaturschlüssel: Fingerabdruck hier eintragen
```

Vergleiche den Fingerabdruck von `tuxmail-release-key.asc` aus dem Release mit diesem Wert **und** mit dem Wert
auf der Projektseite, bevor du Signaturen vertraust.

### Geltungsbereich

Server (`tuxmail serve`, Web-App/PWA, API), Desktop-App, Setup-Programm und Pakete, Android-App,
Messenger-Plugins, Docker-Image und die Deploy-Vorlagen in `deploy/`. Nicht im Geltungsbereich:
Schwachstellen in Mailservern oder Diensten Dritter, Social Engineering, Angriffe, die bereits volle
Kontrolle über das Gerät oder den Server voraussetzen, und der Entwicklungs-Mailserver
`tuxmail devserver` (nur für Tests).

Bitte teste nur gegen eigene Installationen und greife nicht auf Daten anderer zu.

## English

### Reporting a vulnerability

Please do **not** report security issues through public issues, pull requests or discussions.

Use GitHub private vulnerability reporting instead:
**Repository → Security → Advisories → "Report a vulnerability"**
(<https://github.com/Midas1989/tuxmail/security/advisories/new>).

Please include the affected version (`tuxmail version` or Settings, About; `/healthz` only reports it with `security.expose_version: true`), the component (server, desktop,
Android, plugin), a description with impact and reproduction steps (proof of concept), relevant
configuration (without secrets) and logs, and a suggested fix if you have one.

### Process

We acknowledge reports within 5 business days, give a first assessment (severity, affected versions)
within 14 days and aim to ship a fix within 90 days depending on severity. Fixes are published as a
patch release together with a GitHub Security Advisory (with a CVE where appropriate); reporters are
credited if they wish. We ask for coordinated disclosure: please keep details private until the fix is
released or 90 days have passed; we disclose earlier if the issue is actively exploited.

### Supported versions

TuxMail is in its 0.x pre-release phase. Security fixes are provided for the **latest released version**
(and `main`) only.

### Scope

In scope: server (`tuxmail serve`, web app/PWA, API), desktop app, setup program and packages, Android
app, messenger plugins, Docker image and the deployment templates in `deploy/`. Out of scope: issues in
third-party mail servers or services, social engineering, attacks that require full prior control of the
device or server, and the development mail server `tuxmail devserver` (testing only).

Only test against installations you own and do not access other people's data.
