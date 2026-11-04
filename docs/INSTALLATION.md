<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# TuxMail installieren / Installing TuxMail

Alle Pakete liegen unter **[Releases](https://github.com/Midas1989/Mailclient/releases)**.
`<v>` steht in dieser Anleitung für die Versionsnummer (z. B. `0.1.0`), `<arch>` für die Architektur
(`amd64` = normale PCs/Server mit Intel/AMD, `arm64` = ARM, z. B. Snapdragon-Notebooks, Raspberry Pi 4/5,
Ampere-Server).

> **Vorab-Versionen (0.x):** Solange die Versionsnummer mit 0 beginnt, sind die Pakete Test-Versionen
> (auf GitHub als „Pre-release“ markiert). Bitte vorher eine Sicherung der eigenen Mails haben.

## Welches Paket brauche ich?

| Ich möchte … | Datei |
|---|---|
| TuxMail unter **Windows** installieren (empfohlen) | `TuxMail-<v>-windows-amd64-setup.exe` |
| TuxMail unter Windows **ohne Installation** (USB-Stick) | `TuxMail-<v>-windows-amd64-portable.zip` |
| **Debian / Ubuntu / Linux Mint** | `tuxmail_<v>-1_amd64.deb` |
| **Fedora / openSUSE / RHEL / Rocky / Alma** | `tuxmail-<v>-1.x86_64.rpm` |
| **jede andere Linux-Distribution** (ohne Installation) | `TuxMail-<v>-linux-amd64.AppImage` |
| Linux portabel (z. B. USB-Stick, GTK/WebKitGTK vom System) | `TuxMail-<v>-linux-amd64-portable.tar.gz` |
| **Android**-App | `TuxMail-<v>-foss.apk` (bis zum ersten signierten Release: `…-foss-debug.apk`) |
| **eigenen TuxMail-Server** (Web-Oberfläche, PWA, Team-Postfächer) | `tuxmail_<v>_linux_amd64.tar.gz` (Ansible) oder Docker-Image |
| Server/Kommandozeile unter Windows | `tuxmail_<v>_windows_amd64.zip` |

ARM-Geräte: dieselben Dateien mit `arm64` (`.rpm`: `aarch64`).

---

## 0. Download prüfen (empfohlen)

Jedes Release enthält `checksums.txt` mit den SHA-256-Prüfsummen aller Dateien.

**Linux / macOS:**
```bash
sha256sum -c checksums.txt --ignore-missing
```
Erwartet: `<Datei>: OK`.

**Windows (PowerShell):**
```powershell
Get-FileHash .\TuxMail-0.1.0-windows-amd64-setup.exe -Algorithm SHA256
```
Den angezeigten Wert mit der Zeile in `checksums.txt` vergleichen.

Ist zusätzlich `checksums.txt.asc` vorhanden, kann die Datei mit dem Release-Schlüssel geprüft werden:
```bash
gpg --import tuxmail-release-key.asc
gpg --verify checksums.txt.asc checksums.txt
```

---

## 1. Windows

**Voraussetzungen:** Windows 10 22H2 oder Windows 11 (64 Bit). Microsoft Edge WebView2 ist dort normalerweise
vorinstalliert – fehlt es, installiert das Setup es automatisch.

### 1a) Setup (empfohlen) – `TuxMail-<v>-windows-<arch>-setup.exe`

1. Datei herunterladen.
   - Blockiert der Browser den Download („Gefährlicher Download blockiert“ / „wird selten heruntergeladen“):
     Downloads öffnen → beim Eintrag **„…“ → „Beibehalten“** → **„Weitere Informationen“ → „Trotzdem behalten“**.
     Grund: Die Vorab-Versionen sind noch nicht digital signiert.
2. Doppelklick auf die Datei.
   - Erscheint **„Der Computer wurde durch Windows geschützt“** (SmartScreen): **„Weitere Informationen“ →
     „Trotzdem ausführen“**.
3. Im TuxMail-Setup die Installationsart wählen:
   - **Nur für mich** (Standard, keine Administratorrechte nötig) → `%LOCALAPPDATA%\Programs\TuxMail`
   - **Für alle Benutzer** → `C:\Program Files\TuxMail` (Windows fragt nach Administratorrechten)
   - **Portabel entpacken** → beliebiger Ordner, z. B. USB-Stick, ohne Registry-Einträge
4. Optionen: Startmenü-Eintrag (an), Desktop-Verknüpfung, „Als E-Mail-Programm registrieren“
   (für `mailto:`-Links), Autostart.
5. **Installieren** → **TuxMail starten**.

**Update:** Das neue Setup einfach ausführen. Es erkennt die vorhandene Installation, übernimmt Ordner und
Optionen und schließt ein laufendes TuxMail nach Rückfrage. Konten und Mails bleiben erhalten.

**Deinstallieren:** *Einstellungen → Apps → Installierte Apps → TuxMail → Deinstallieren*.
„Benutzerdaten behalten“ ist voreingestellt; sonst wird auch `%APPDATA%\TuxMail` gelöscht.

**Für Administratoren (stille Installation):**
```bat
TuxMail-0.1.0-windows-amd64-setup.exe /S --scope=machine --desktop-shortcut --log=C:\Temp\tuxmail-setup.log
"C:\Program Files\TuxMail\TuxMail-Setup.exe" --uninstall --silent
```
Weitere Schalter: `--scope=user|machine|portable`, `--dir=C:\Pfad`, `--no-start-menu`, `--default-mail`,
`--autostart`. Exit-Code 0 = OK, 1 = Fehler, 2 = ungültige Argumente, 740 = Administratorrechte verweigert.

### 1b) Portabel – `TuxMail-<v>-windows-<arch>-portable.zip`

1. Rechtsklick auf das ZIP → **Eigenschaften** → unten **„Zulassen“** anhaken → OK
   (verhindert SmartScreen-Meldungen für jede einzelne Datei).
2. Rechtsklick → **Alle extrahieren…** in einen Ordner, z. B. `D:\TuxMail` oder auf den USB-Stick.
   **Nicht direkt aus dem ZIP starten.**
3. `TuxMail\TuxMail.exe` doppelklicken.
4. Beim ersten Start ein **Master-Passwort** festlegen: Im portablen Modus liegen die Zugangsdaten verschlüsselt in
   `data\secrets.vault`. Ohne dieses Passwort sind sie nicht wiederherstellbar – gut aufbewahren.

Alle Daten liegen im Ordner `data` neben `TuxMail.exe`. **Update:** neues ZIP entpacken und den alten Ordner
`data` (sowie die leere Datei `portable`) hinüberkopieren.

---

## 2. Linux (Desktop)

**Voraussetzungen:** 64-Bit-Linux mit grafischer Oberfläche; für die Pakete GTK 3 und WebKitGTK 4.1
(Debian 12+, Ubuntu 22.04+, Fedora 39+, openSUSE Tumbleweed/Leap 15.6, RHEL/Rocky/Alma 9+).
Zum sicheren Speichern der Passwörter: ein Schlüsselbund (GNOME Keyring, KWallet oder KeePassXC).

### 2a) Debian, Ubuntu, Linux Mint – `.deb`

```bash
sudo apt install ./tuxmail_0.1.0-1_amd64.deb
```
(`apt` installiert fehlende Abhängigkeiten automatisch; der Punkt-Schrägstrich `./` ist wichtig.)

Start: im Anwendungsmenü **TuxMail** oder im Terminal `tuxmail`.
Update: neue `.deb` genauso installieren. Entfernen: `sudo apt remove tuxmail`.

### 2b) Fedora, openSUSE, RHEL, Rocky, Alma – `.rpm`

```bash
# Fedora, RHEL, Rocky, Alma
sudo dnf install ./tuxmail-0.1.0-1.x86_64.rpm
# openSUSE
sudo zypper install --allow-unsigned-rpm ./tuxmail-0.1.0-1.x86_64.rpm
```
RHEL/Rocky/Alma: WebKitGTK kommt aus EPEL (`sudo dnf install epel-release` vorher).
Entfernen: `sudo dnf remove tuxmail` bzw. `sudo zypper remove tuxmail`.

### 2c) AppImage (jede Distribution, ohne Installation)

```bash
chmod +x TuxMail-0.1.0-linux-amd64.AppImage
./TuxMail-0.1.0-linux-amd64.AppImage
```
Oder im Dateimanager: Rechtsklick → Eigenschaften → „Als Programm ausführen“ erlauben → Doppelklick.

Meldet das AppImage „FUSE“ fehlt:
- Ubuntu 22.04: `sudo apt install libfuse2` · Ubuntu 24.04+: `sudo apt install libfuse2t64`
- oder ohne FUSE starten: `./TuxMail-0.1.0-linux-amd64.AppImage --appimage-extract-and-run`

Für einen Eintrag im Anwendungsmenü z. B. [Gear Lever](https://flathub.org/apps/it.mijorus.gearlever) oder
AppImageLauncher verwenden.

### 2d) Portabel – `TuxMail-<v>-linux-<arch>-portable.tar.gz`

Braucht GTK 3 und WebKitGTK 4.1 vom System
(Debian/Ubuntu: `sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0`).
```bash
tar -xzf TuxMail-0.1.0-linux-amd64-portable.tar.gz
cd TuxMail
./tuxmail
```
Alle Daten liegen im Ordner `data` daneben; Zugangsdaten verschlüsselt mit Master-Passwort (wie unter Windows).

---

## 3. Android

**Voraussetzungen:** Android 7.0 oder neuer. Die App ist ein Client für einen **TuxMail-Server** (Abschnitt 5) –
sie verbindet sich mit dessen Adresse und meldet sich dort an.

1. `TuxMail-<v>-foss.apk` (bzw. `…-foss-debug.apk`) **auf dem Handy** herunterladen.
2. Antippen. Android fragt einmalig: **„Installation aus dieser Quelle zulassen“** → für den Browser bzw. die
   Dateien-App erlauben → zurück → **Installieren**.
3. Google Play Protect kann warnen („Unbekannte App“) → **Details → Trotzdem installieren**.
4. App öffnen → Adresse des TuxMail-Servers eingeben (z. B. `https://mail.example.org`) → anmelden
   (oder am PC unter *Einstellungen → Konten → TuxMail-Server* den Code zur Geräteanmeldung anzeigen lassen).
5. Benachrichtigungen erlauben. Die `foss`-Variante nutzt UnifiedPush; dafür eine Verteiler-App installieren,
   z. B. [ntfy](https://f-droid.org/packages/io.heckel.ntfy/). Ohne Verteiler prüft die App regelmäßig selbst.

Update: neue APK genauso installieren (Daten bleiben erhalten). Hinweis: Eine `…-debug.apk` lässt sich später
nicht direkt durch eine signierte Release-APK ersetzen – dann einmal deinstallieren und neu installieren.

**Ohne App (iPhone, jedes Smartphone):** Server-Adresse im Browser öffnen → *Teilen → Zum Home-Bildschirm*
(iPhone: Safari, iOS 16.4+) bzw. *Menü → App installieren* (Android/Chrome). Benachrichtigungen funktionieren
auch so.

---

## 4. Windows/Linux-Kommandozeile (Server-Archive)

`tuxmail_<v>_linux_<arch>.tar.gz` bzw. `tuxmail_<v>_windows_<arch>.zip` enthalten das Konsolenprogramm
`tuxmail` (Server, Import/Export, Benutzerverwaltung) mit allen Messenger-Plugins – ohne Desktop-Fenster.
```bash
tar -xzf tuxmail_0.1.0_linux_amd64.tar.gz -C ~/tuxmail
~/tuxmail/tuxmail version
~/tuxmail/tuxmail --help
```

---

## 5. Eigener TuxMail-Server (Web, PWA, Android-Client)

### 5a) Empfohlen: Ansible (Linux-Server, ohne Docker)

Unterstützt Debian 12/13, Ubuntu 22.04/24.04, RHEL/Rocky/Alma 9 (amd64, arm64) – richtet TuxMail als
systemd-Dienst ein, auf Wunsch mit nginx und Let's Encrypt oder eigenem Zertifikat.
```bash
git clone https://github.com/Midas1989/Mailclient.git
cd Mailclient/deploy/ansible
cp inventory/hosts.example.ini inventory/hosts.ini    # Server eintragen
$EDITOR inventory/group_vars/all.yml                  # Domain, Version (0.1.0), E-Mail für Let's Encrypt
ansible-galaxy collection install -r requirements.yml
ansible-playbook -i inventory/hosts.ini site.yml
```
Varianten (vorhandenes nginx, eigenes Zertifikat, Unterpfad neben Nextcloud, ohne nginx), Updates, Backups
und Benutzerverwaltung: **[deploy/README.md](../deploy/README.md)**.

### 5b) Manuell (ohne Ansible)

```bash
sudo useradd --system --home-dir /var/lib/tuxmail --shell /usr/sbin/nologin tuxmail
sudo install -d -m 0755 /opt/tuxmail/versions/0.1.0
sudo tar -xzf tuxmail_0.1.0_linux_amd64.tar.gz -C /opt/tuxmail/versions/0.1.0
sudo ln -sfn /opt/tuxmail/versions/0.1.0 /opt/tuxmail/current
sudo install -d -o root -g tuxmail -m 0750 /etc/tuxmail
sudo install -d -o tuxmail -g tuxmail -m 0750 /var/lib/tuxmail /var/log/tuxmail
sudo install -o root -g tuxmail -m 0640 deploy/systemd/config.example.yaml /etc/tuxmail/config.yaml
sudoedit /etc/tuxmail/config.yaml                     # public_url, listen, TLS
sudo install -m 0644 deploy/systemd/tuxmail.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now tuxmail
# Ersten Administrator anlegen (Passwort wird abgefragt bzw. per stdin übergeben):
echo 'SicheresPasswort' | sudo runuser -u tuxmail -- /opt/tuxmail/current/tuxmail user add \
  --config /etc/tuxmail/config.yaml --username admin --email admin@example.org --admin --password-stdin
```
nginx-Beispiel: `deploy/nginx/tuxmail.conf`. Danach `https://<deine-domain>` im Browser öffnen.

### 5c) Docker / Podman

```bash
curl -LO https://raw.githubusercontent.com/Midas1989/Mailclient/main/deploy/docker/docker-compose.example.yml
mv docker-compose.example.yml docker-compose.yml
$EDITOR docker-compose.yml        # TUXMAIL_PUBLIC_URL und Image-Tag ghcr.io/midas1989/tuxmail:0.1.0
docker compose up -d
docker compose exec tuxmail tuxmail user add --username admin --admin   # Passwort wird ausgegeben
```
Das Volume `/data` enthält alle Daten (inkl. `master.key`) – **unbedingt sichern**.

**Wichtig für Server im eigenen Netz:** Aus Sicherheitsgründen darf der Server standardmäßig keine Ziele im
LAN oder auf `localhost` erreichen (z. B. einen internen Mailserver oder Ollama). Solche Ziele in der
Konfiguration freigeben: `security.allowed_networks: ["192.168.10.5:993"]` (siehe deploy/README.md).

---

## 6. Erste Schritte nach der Installation

1. TuxMail starten → **Konto hinzufügen** → E-Mail-Adresse und Passwort eingeben. Server-Einstellungen
   (IMAP/SMTP, Exchange/ActiveSync, Microsoft 365, Gmail, JMAP) werden automatisch gesucht; sonst
   „Manuell einrichten“.
2. Microsoft 365 / Gmail: Anmeldung im Browserfenster (OAuth).
3. Messenger (WhatsApp, Signal, Telegram, Matrix): *Einstellungen → Chats & Messenger → Verbinden* –
   QR-Code bzw. Link mit dem Handy scannen (TuxMail wird ein „verknüpftes Gerät“, wie WhatsApp Web).
4. Umzug von einem anderen Programm: *Einstellungen → Daten → Import* (Thunderbird-Profil, mbox/EML, PST,
   ICS, vCard/CSV/LDIF).

## Probleme?

- Windows-Meldung „Unbekannter Herausgeber“ / SmartScreen: siehe 1a. Signierte Pakete folgen.
- Linux: Passwörter werden nicht gespeichert → Schlüsselbund installieren/entsperren
  (`gnome-keyring`, KWallet oder KeePassXC mit Secret-Service).
- Diagnose: *Einstellungen → Info & Diagnose → Diagnosebericht* (Passwörter und Tokens werden geschwärzt).
- Fehler melden: [Issues](https://github.com/Midas1989/Mailclient/issues) ·
  Sicherheitslücken bitte vertraulich: [SECURITY.md](../SECURITY.md).

---

## English (short version)

| Platform | File | Install |
|---|---|---|
| Windows (recommended) | `TuxMail-<v>-windows-amd64-setup.exe` | Run it. Browser blocks it → Downloads → "…" → *Keep* → *Keep anyway*. SmartScreen → *More info* → *Run anyway*. Choose *Just me*, *All users* or *Portable*. Uninstall via *Settings → Apps*. Silent: `/S --scope=user\|machine\|portable` |
| Windows portable | `…-windows-amd64-portable.zip` | Properties → *Unblock*, extract, run `TuxMail\TuxMail.exe`, set a master password |
| Debian/Ubuntu | `tuxmail_<v>-1_amd64.deb` | `sudo apt install ./tuxmail_<v>-1_amd64.deb` |
| Fedora/RHEL/openSUSE | `tuxmail-<v>-1.x86_64.rpm` | `sudo dnf install ./…rpm` / `sudo zypper install --allow-unsigned-rpm ./…rpm` |
| Any Linux | `TuxMail-<v>-linux-amd64.AppImage` | `chmod +x` and run; without FUSE: `--appimage-extract-and-run` |
| Linux portable | `…-linux-amd64-portable.tar.gz` | extract, run `./tuxmail` (needs GTK 3 + WebKitGTK 4.1) |
| Android | `TuxMail-<v>-foss.apk` | allow "install unknown apps", install, enter your TuxMail server URL |
| Server | `tuxmail_<v>_linux_amd64.tar.gz` / `ghcr.io/midas1989/tuxmail:<v>` | Ansible: `deploy/ansible`; Docker: `deploy/docker`; see [deploy/README.md](../deploy/README.md) |

Verify downloads: `sha256sum -c checksums.txt --ignore-missing`. ARM devices: use the `arm64`/`aarch64` files.
