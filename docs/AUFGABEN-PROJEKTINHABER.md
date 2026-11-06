# Aufgaben für den Projektinhaber (Midas Wollinger)

Stand: 06.11.2026 (Abschnitt „Neu“ ergänzt, die nummerierten Aufgaben darunter stammen vom 03.11.) – Diese Liste enthält alles, was **nur du** erledigen kannst (Konten, Verträge, Entscheidungen). Die Programmierung läuft parallel weiter; nichts davon blockiert die aktuelle Entwicklung.

Legende: 🔴 bald nötig · 🟡 vor der ersten öffentlichen Version · 🟢 später / optional

---

## Neu: Aufgaben nach dem Sicherheits-Re-Audit (Stand 06.11.2026)

Der Programmcode ist behoben und getestet (Bericht: [security/REAUDIT-2026-11-05.md](security/REAUDIT-2026-11-05.md)).
Diese Punkte kann nur der Projektinhaber erledigen oder auf echter Hardware prüfen:

| # | Aufgabe | Priorität | Kosten |
|---|---|---|---|
| A | **GitHub Actions läuft nicht:** Alle Läufe der letzten Pushes schlagen nach wenigen Sekunden ohne Protokoll fehl (auch schon vor den Sicherheitsänderungen). Vermutlich Abrechnung oder Actions-Minuten: *Settings → Billing and plans* und den Reiter *Actions* prüfen. Danach die Läufe `CI`, `Security`, `Release`, `Android` erneut starten. | 🔴 | – |
| B | **Neues Release erstellen:** `v0.1.0` (04.11.) enthält die Korrekturen nicht. Nach grünen Läufen Version erhöhen und taggen ([RELEASE.md](RELEASE.md), Abschnitt 2). | 🔴 | – |
| C | **GPG-Release-Schlüssel anlegen:** `gpg --full-generate-key`, privaten Schlüssel (`gpg --armor --export-secret-keys KEYID`) als Secret `GPG_PRIVATE_KEY`, dazu `GPG_PASSPHRASE` in *Settings → Secrets and variables → Actions*. Fingerabdruck als `tuxmail_release_gpg_fingerprint` in `deploy/ansible/inventory/group_vars/all.yml` eintragen. Bis dahin installiert Ansible nur mit `tuxmail_allow_unsigned_release: true`. | 🔴 | kostenlos |
| D | Repository-Einstellung **„Require actions to be pinned to a full-length commit SHA“** aktivieren (*Settings → Actions → General*). Alle Actions sind bereits gepinnt. | 🔴 | – |
| E | **Android-Schlüssel erzeugen und sichern:** `keytool -genkeypair -v -keystore release.jks -alias tuxmail -keyalg RSA -keysize 4096 -validity 10000`, dann Secrets `KEYSTORE_BASE64` (`base64 -w0 release.jks`), `KEYSTORE_PASSWORD`, `KEY_ALIAS`, `KEY_PASSWORD`. Ohne den Schlüssel lassen sich installierte Apps nie wieder aktualisieren: Datei und Passwörter mehrfach sichern. | 🟡 | kostenlos |
| F | **Windows-Signatur** (siehe Punkt 6): bei SignPath Foundation bewerben, danach Secret `SIGNPATH_API_TOKEN` und die Variablen `SIGNPATH_ORGANIZATION_ID`, `SIGNPATH_PROJECT_SLUG`, `SIGNPATH_SIGNING_POLICY_SLUG` setzen ([RELEASE.md](RELEASE.md), Abschnitt 5). | 🟡 | 0 € (SignPath) |
| G | **Erste CI-Läufe beobachten:** `linux-desktop` (neue AppImage-Werkzeug-Pins) und der Windows-Job (WebView2-Schritt) wurden noch nie auf GitHub ausgeführt. | 🟡 | – |
| H | **Smoke-Tests auf echter Hardware:** Windows (Deinstallation für alle Benutzer, WebView2-Installation, Plugin-Vertrauensprüfung) und Android (erster Release-Build mit R8: Push, Benachrichtigungsaktionen, Teilen, Biometrie, Deep Links). | 🟡 | – |
| I | **Betrieb bestehender Server prüfen:** Sicherungen brauchen jetzt age-Empfänger (`tuxmail_backup_age_recipients`) oder die ausdrückliche Klartext-Ausnahme; `tuxmail_tls_mode: none` hinter einem TLS-beendenden Proxy braucht `tuxmail_nginx_upstream_terminates_tls: true`; die Version erscheint auf `/healthz` nur noch mit `security.expose_version: true`. | 🟡 | – |
| J | **Entscheidung:** Soll das Projekt eine **Update-Funktion** im Programm bekommen (Vorschlag: signierter Update-Hinweis zuerst)? | 🟡 | – |
| K | Projekt wurde nach `github.com/Midas1989/tuxmail` umbenannt: Der Go-Modulpfad (`go.mod`) heißt noch `github.com/Midas1989/Mailclient`. Das stört nichts; eine Umbenennung ist möglich, berührt aber alle Quelldateien. Ggf. Secrets/Variablen und Verknüpfungen (SignPath-Projekt, Docker-Paket auf ghcr.io) auf den neuen Namen prüfen. | 🟢 | – |

Bewusst offen bleiben: Authenticode-Signatur der Windows-Programme (Punkt F), Signaturen für Messenger-Plugins und eine
Sandbox für Plugins (sie laufen als derselbe Benutzer wie TuxMail).

---

## Übersicht

| # | Aufgabe | Priorität | Kosten (ca.) | Zeitaufwand |
|---|---|---|---|---|
| 1 | Entscheidung KI-Hinweis im README | 🔴 | – | 1 Minute |
| 2 | GitHub-Repository vorbereiten | 🔴 | kostenlos | 15 Minuten |
| 3 | Microsoft-App-Registrierung (Entra ID) | 🟡 | kostenlos | 30–60 Minuten |
| 4 | Google-App-Registrierung (Google Cloud) | 🟡 | kostenlos – Prüfung ggf. 3.000–4.500 USD/Jahr | 30–60 Minuten (+ Wochen für Prüfung) |
| 5 | Telegram-`api_id` beantragen | 🟡 | kostenlos | 10 Minuten |
| 6 | Code-Signatur für Windows | 🟡 | 0 € (SignPath) bis ca. 69–359 € (Certum) | 1–2 Stunden + Wartezeit |
| 7 | Test-Infrastruktur (Testkonten/Server) | 🟢 | 0–10 €/Monat | 1 Stunde |
| 8 | Domain & Projektseite | 🟢 | ca. 10–20 €/Jahr | optional |

---

## 1. 🔴 Entscheidung: KI-Hinweis im README

Gesetzlich nicht vorgeschrieben (siehe Chat). Vorschlag für einen freiwilligen Hinweis:

> *TuxMail wird mit Unterstützung von KI-Werkzeugen entwickelt. Alle Änderungen werden von Menschen geprüft, getestet und verantwortet.*

**Zu tun:** „Ja" oder „Nein" sagen.

---

## 2. 🔴 GitHub-Repository vorbereiten

Repository: `github.com/Midas1989/tuxmail` (früher `Mailclient`; GitHub leitet alte Links weiter)

- [ ] **Zwei-Faktor-Anmeldung** für dein GitHub-Konto aktivieren (Pflicht für kostenlose Code-Signatur über SignPath, siehe Punkt 6).
- [ ] Entscheiden, ob das Repository **öffentlich** wird (nötig für SignPath und für Open Source generell). Empfehlung: öffentlich schalten, sobald die erste lauffähige Version steht.
- [ ] Optional: Repository-Namen in `TuxMail` ändern (*Settings → General → Repository name*). GitHub leitet alte Links automatisch weiter.
- [ ] Unter *Settings → Actions → General* GitHub Actions erlauben (für automatische Builds und Releases).
- [ ] Wenn die Entwicklung fertig ist: Pull Request von `claude/mail-messaging-calendar-app-u4loru` nach `main` prüfen und zusammenführen.

---

## 3. 🟡 Microsoft-App-Registrierung (für Microsoft 365, Outlook.com, Exchange Online)

Nötig, damit sich Nutzer mit Microsoft-Konten anmelden können (OAuth2 – Passwort-Anmeldung ist bei Microsoft abgeschaltet).

**Wo:** <https://entra.microsoft.com> → *Identität → Anwendungen → App-Registrierungen → Neue Registrierung*
(Ein kostenloses Microsoft-Konto genügt; ggf. wird automatisch ein kostenloser Entra-Mandant angelegt.)

**Einstellungen:**
1. Name: `TuxMail`
2. Unterstützte Kontotypen: **„Konten in einem beliebigen Organisationsverzeichnis und persönliche Microsoft-Konten"**
3. Umleitungs-URI: Plattform **„Öffentlicher Client/nativ (mobil & Desktop)"**, URI `http://localhost`
4. Nach dem Anlegen unter *Authentifizierung*: **„Öffentliche Clientflows zulassen" = Ja**
5. Für den Server-Modus zusätzlich Plattform **„Web"** mit `https://<deine-domain>/api/oauth/microsoft/callback` (kann später ergänzt werden)
6. Unter *API-Berechtigungen* → *Delegiert* hinzufügen:
   - Microsoft Graph: `offline_access`, `openid`, `email`, `profile`, `User.Read`, `Mail.ReadWrite`, `Mail.Send`, `MailboxSettings.ReadWrite`, `Calendars.ReadWrite`, `Contacts.ReadWrite`, `Tasks.ReadWrite`
   - Office 365 Exchange Online: `IMAP.AccessAsUser.All`, `SMTP.Send`, `EAS.AccessAsUser.All`, `EWS.AccessAsUser.All`
7. **Kein** geheimer Clientschlüssel nötig (Desktop-App).

**Mir mitteilen:** die **Anwendungs-ID (Client-ID)** – sie ist nicht geheim und kommt in den Quellcode.

**Empfohlen für die Veröffentlichung – Herausgeberüberprüfung:** In vielen Firmen dürfen Mitarbeiter nur Apps von *verifizierten Herausgebern* zustimmen. Dafür kostenlos am **Microsoft AI Cloud Partner Program** teilnehmen (Partner-ID) und unter *Branding & Eigenschaften* → *Herausgeberdomäne* + *Partner-ID* eintragen. Voraussetzung: eigene Domain (siehe Punkt 8).

---

## 4. 🟡 Google-App-Registrierung (für Gmail, Google Kalender, Google Kontakte)

**Wo:** <https://console.cloud.google.com>

**Schritte:**
1. Neues Projekt `TuxMail` anlegen.
2. *APIs & Dienste → Bibliothek*: **Gmail API**, **Google Calendar API**, **People API** aktivieren.
3. *OAuth-Zustimmungsbildschirm* (Google Auth Platform): Typ **„Extern"**, App-Name `TuxMail`, Logo (`assets/branding/icons/icon-128.png`), Support-E-Mail, Links zu Datenschutzerklärung und Startseite (siehe Punkt 8).
4. Bereiche (Scopes): `https://mail.google.com/` (IMAP/SMTP), `.../auth/calendar`, `.../auth/contacts`, `openid`, `email`, `profile`.
5. *Anmeldedaten → OAuth-Client-ID*: Typ **„Desktop-App"** (für den Desktop-Client); für den Server-Modus zusätzlich **„Webanwendung"** mit Weiterleitungs-URI `https://<deine-domain>/api/oauth/google/callback`.

**Mir mitteilen:** die **Client-ID** (bei Desktop-Apps behandelt Google auch das „Client-Secret" als nicht geheim).

**⚠️ Wichtig – Google-Prüfung:** Der volle Mail-Zugriff ist ein *eingeschränkter Bereich*. Ohne Prüfung bleibt die App im **Testmodus**: höchstens **100 Testnutzer** (manuell einzutragen) und die Anmeldung läuft **alle 7 Tage ab**. Für die öffentliche Freigabe verlangt Google eine jährliche Sicherheitsprüfung (**CASA Tier 2**) durch ein zugelassenes Prüflabor – laut Anbieterpreisen ca. **3.000–4.500 USD pro Jahr**, Dauer mehrere Wochen.

**Kostenlose Alternativen, die TuxMail zusätzlich anbietet:**
- **App-Passwort** in Gmail (bei aktivierter 2-Faktor-Anmeldung) → normales IMAP/SMTP, keine Registrierung nötig.
- **Eigene Client-ID**: Nutzer bzw. Server-Admins tragen eine eigene Google-Client-ID ein.
- **Google Workspace im Server-Modus**: Der Firmen-Admin legt eine App vom Typ **„Intern"** an – dafür ist **keine** Google-Prüfung nötig.

**Empfehlung:** Registrierung jetzt anlegen (Testmodus reicht für Entwicklung und dich selbst); über die kostenpflichtige Prüfung erst entscheiden, wenn TuxMail öffentlich viele Gmail-Nutzer haben soll.

---

## 5. 🟡 Telegram-`api_id` beantragen

**Wo:** <https://my.telegram.org> → mit deiner Telefonnummer anmelden → **„API development tools"**
- App title: `TuxMail`, Short name: `tuxmail`, Platform: `Desktop`, Beschreibung: „Open-source unified mail and messaging client"

**Mir mitteilen:** `api_id` und `api_hash` (diese gehören in die Build-Konfiguration, nicht öffentlich ins Repository – ich lege dafür ein GitHub-Secret an bzw. erkläre dir, wo du es einträgst).

---

## 6. 🟡 Code-Signatur für Windows

Ohne Signatur zeigt Windows beim Start der `.exe` die SmartScreen-Warnung „Unbekannter Herausgeber". Seit 2023 werden Zertifikate nur noch auf Hardware-Token oder in Cloud-HSMs ausgegeben; seit März 2026 sind Code-Signatur-Zertifikate kürzer gültig (max. ca. 15 Monate).

| Option | Kosten | Bemerkung |
|---|---|---|
| **SignPath Foundation** (Empfehlung) | **kostenlos** für Open-Source-Projekte | Repository muss öffentlich sein, OSI-Lizenz (AGPLv3 ✔), keine proprietären Teile, 2-Faktor-Anmeldung für alle Mitglieder. Zertifikat lautet auf die SignPath Foundation, Signatur läuft automatisch aus GitHub Actions. Antrag: <https://signpath.org> |
| **Certum Open Source Code Signing** | ab **ca. 69 €** (nur Zertifikat) bzw. **ca. 359 €** (Set) laut Certum-Shop | Zertifikat auf deinen Namen; Identitätsprüfung; Hardware-/Cloud-Schlüssel nötig. Vor dem Kauf prüfen, welches Paket passt und wie lange es gilt. |
| Azure Artifact Signing (ehem. Trusted Signing) | ca. 10 USD/Monat | Für **Einzelpersonen derzeit nur USA/Kanada** – für dich nur nutzbar, wenn du eine Firma hast. |
| Klassisches OV-Zertifikat (DigiCert, Sectigo …) | meist mehrere hundert € pro Jahr | Keine Vorteile gegenüber den obigen Optionen für dieses Projekt. |

**Empfehlung:** Bei SignPath bewerben, sobald das Repository öffentlich ist und eine erste Version existiert. Bis dahin unsigniert ausliefern (Nutzer klicken einmal „Trotzdem ausführen"). Linux-Pakete (AppImage, Flatpak, deb/rpm) brauchen kein kostenpflichtiges Zertifikat – sie werden mit einem eigenen GPG-Schlüssel signiert, den ich in der Build-Pipeline einrichte.

---

## 7. 🟢 Test-Infrastruktur

Für realistische Tests (automatische Tests laufen ohnehin gegen lokale Testserver in Docker):
- [ ] Ein **Test-Mailkonto** bei einem normalen Anbieter (z. B. mailbox.org, Posteo oder ein eigenes IMAP), das nicht dein Hauptpostfach ist.
- [ ] Optional ein **Microsoft-365-Entwicklerkonto** und ein **Google-Testkonto** für die Anmeldungen aus Punkt 3/4.
- [ ] Optional deine **Nextcloud** (falls vorhanden) mit einem Testbenutzer für Cloud-Anhänge, Kalender und Kontakte.
- [ ] Optional ein kleiner **Linux-Server/VM** (Debian/Ubuntu), um das Ansible-Playbook und den Server-Modus echt zu testen.

Zugangsdaten bitte **nicht in den Chat oder ins Repository** schreiben – wir legen sie als GitHub-Secrets bzw. lokal an.

---

## 8. 🟢 Domain & Projektseite

Für Google/Microsoft-Freigaben, SignPath und Vertrauen bei Nutzern hilfreich:
- [ ] Domain (z. B. `tuxmail.org`/`tuxmail.app` – Verfügbarkeit prüfen), ca. 10–20 €/Jahr.
- [ ] Einfache Projektseite (kann kostenlos über GitHub Pages laufen – ich kann sie erstellen) mit **Impressum** und **Datenschutzerklärung** (für Google/Microsoft Pflicht; in Deutschland für eine öffentliche Seite ebenfalls).
- [ ] Optional: E-Mail-Adresse für das Projekt (z. B. `security@` für Sicherheitsmeldungen).

---

## Hinweis zu TbSync

[TbSync](https://github.com/jobisoft/TbSync) ist ein Thunderbird-Add-on von John Bieling zum Synchronisieren von Kalendern, Kontakten und Aufgaben; das zugehörige [EAS-4-TbSync](https://github.com/jobisoft/EAS-4-TbSync) implementiert **Exchange ActiveSync** (EAS 2.5, 14.0, 14.1 und 16.1). Beide stehen unter der **MPL-2.0**, die mit der AGPLv3 kombinierbar ist.

Für TuxMail heißt das:
- **Kein Ersatz, aber eine wertvolle Referenz:** TbSync ist in JavaScript für Thunderbird geschrieben und kann nicht direkt eingebaut werden. Die ActiveSync-Umsetzung von TuxMail (Go) nutzt es aber als **Referenz für Protokoll-Details und Server-Eigenheiten** (Exchange, Z-Push, Kopano, Grommunio) neben den offiziellen Microsoft-Spezifikationen. Übernommene Ideen oder Codeteile werden mit Quellenangabe gekennzeichnet.
- **Import:** Der geplante Thunderbird-Import kann auch die in TbSync eingerichteten Konten (Server, Benutzer, Kalender/Adressbücher) übernehmen.
- **Für dich zu tun:** nichts.
