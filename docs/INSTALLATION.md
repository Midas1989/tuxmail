<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# TuxMail installieren

Diese Anleitung führt Schritt für Schritt durch die Installation aller Varianten: Windows, Linux, Android und den
eigenen Server. Wer es eilig hat, liest nur den ersten Abschnitt.

*English: a short summary is at the end of this page.*

**Inhalt**

0. [Schnellinstallation](#0-schnellinstallation)
1. [Vorbereitung: Welches Paket, Download prüfen](#1-vorbereitung)
2. [Windows](#2-windows)
3. [Linux (Desktop)](#3-linux-desktop)
4. [Android und Smartphone](#4-android-und-smartphone)
5. [Kommandozeile (Server-Archive)](#5-kommandozeile-server-archive)
6. [Eigener Server (Web, PWA, Android-Client)](#6-eigener-server)
7. [Selbst bauen](#7-selbst-bauen)
8. [Erste Schritte](#8-erste-schritte-nach-der-installation)
9. [Aktualisieren und Deinstallieren](#9-aktualisieren-und-deinstallieren)
10. [Probleme und Lösungen](#10-probleme-und-lösungen)

---

## 0. Schnellinstallation

Alle Pakete liegen unter **[Releases](https://github.com/Midas1989/tuxmail/releases)**. `<v>` steht für die
Versionsnummer, zurzeit `0.1.0`.

> **Vorab-Version:** Solange die Versionsnummer mit 0 beginnt, sind die Pakete Test-Versionen (auf GitHub als
> „Pre-release“ markiert). Bitte vorher eine Sicherung der eigenen Mails anlegen.

**Meine Empfehlung je nach Ziel:**

| Ich möchte … | Das ist der einfachste Weg |
|---|---|
| **TuxMail auf Windows** nutzen | `TuxMail-<v>-windows-amd64-setup.exe` herunterladen, doppelklicken, „Nur für mich“ wählen → [Abschnitt 2](#2-windows) |
| **TuxMail auf Debian, Ubuntu, Mint** | `sudo apt install ./tuxmail_<v>-1_amd64.deb` → [Abschnitt 3a](#3a-debian-ubuntu-linux-mint-deb) |
| **TuxMail auf Fedora, RHEL, Rocky, Alma** | `sudo dnf install ./tuxmail-<v>-1.x86_64.rpm` → [Abschnitt 3b](#3b-fedora-opensuse-rhel-rocky-alma-rpm) |
| **TuxMail auf einer anderen Linux-Distribution** | `chmod +x TuxMail-<v>-linux-amd64.AppImage && ./TuxMail-<v>-linux-amd64.AppImage` → [Abschnitt 3c](#3c-appimage-jede-distribution) |
| **TuxMail auf dem Handy** | Server einrichten (unten), dann `TuxMail-<v>-foss.apk` installieren → [Abschnitt 4](#4-android-und-smartphone) |
| **einen eigenen Server** für mich, meine Familie oder mein Team | Ansible-Playbook (5 Befehle, unten) → [Abschnitt 6](#6-eigener-server) |
| **alles selbst kompilieren** | `build/build-all.sh` → [Abschnitt 7](#7-selbst-bauen) |

**Eigener Server in fünf Befehlen** (Debian/Ubuntu/RHEL-Familie, Domain zeigt auf den Server, Ports 80 und 443 offen):

```bash
git clone https://github.com/Midas1989/tuxmail.git && cd tuxmail/deploy/ansible
cp inventory/hosts.example.ini inventory/hosts.ini && $EDITOR inventory/hosts.ini     # Server eintragen
ansible-galaxy collection install -r requirements.yml
ansible-playbook -i inventory/hosts.ini site.yml \
  -e tuxmail_domain=mail.example.de -e tuxmail_tls_letsencrypt_email=admin@example.de \
  -e tuxmail_allow_unsigned_release=true -e tuxmail_backup_allow_plaintext=true
```

Das Passwort des ersten Administrators (`admin`) steht danach in `credentials/<Servername>/admin_password` auf
deinem Rechner. Die beiden letzten Schalter sind Ausnahmen für den ersten Test (siehe
[6a](#6a-empfohlen-ansible-ohne-docker)): Das Release ist noch nicht signiert, und Sicherungen sind noch nicht
verschlüsselt.

**Unter Windows und Linux gilt:** Wer nichts installieren möchte, nimmt die **portable** Variante (ZIP bzw.
`tar.gz`), die man auch vom USB-Stick starten kann.

---

## 1. Vorbereitung

### Welches Paket brauche ich?

`<arch>`: `amd64` für normale PCs und Server mit Intel oder AMD, `arm64` für ARM-Geräte (z. B. Raspberry Pi 4/5,
Snapdragon-Notebooks, Ampere-Server). Bei `.rpm`-Dateien heißt es `x86_64` bzw. `aarch64`.

| Ich möchte … | Datei |
|---|---|
| TuxMail unter **Windows** installieren (empfohlen) | `TuxMail-<v>-windows-<arch>-setup.exe` |
| TuxMail unter Windows **ohne Installation** (USB-Stick) | `TuxMail-<v>-windows-<arch>-portable.zip` |
| **Debian / Ubuntu / Linux Mint** | `tuxmail_<v>-1_<arch>.deb` |
| **Fedora / openSUSE / RHEL / Rocky / Alma** | `tuxmail-<v>-1.<x86_64\|aarch64>.rpm` |
| **jede andere Linux-Distribution** (ohne Installation) | `TuxMail-<v>-linux-<arch>.AppImage` |
| Linux portabel (z. B. USB-Stick, GTK/WebKitGTK vom System) | `TuxMail-<v>-linux-<arch>-portable.tar.gz` |
| **Android**-App | `TuxMail-<v>-foss.apk` (bis zum ersten signierten Release: `…-foss-debug.apk`) |
| **eigenen TuxMail-Server** | `tuxmail_<v>_linux_<arch>.tar.gz` (Ansible) oder das Docker-Image `ghcr.io/midas1989/tuxmail` |
| Server/Kommandozeile unter Windows | `tuxmail_<v>_windows_<arch>.zip` |

### Systemvoraussetzungen

| System | Voraussetzung |
|---|---|
| Windows | Windows 10 22H2 oder Windows 11 (64 Bit). Microsoft Edge WebView2 ist normalerweise vorhanden; fehlt es, installiert das Setup es. |
| Linux-Desktop | 64-Bit-Linux mit grafischer Oberfläche, GTK 3 und WebKitGTK 4.1 (Debian 12+, Ubuntu 22.04+, Fedora 39+, openSUSE Tumbleweed/Leap 15.6, RHEL/Rocky/Alma 9+). Für das AppImage genügt glibc ab 2.39 (Ubuntu 24.04+, Debian 13+, Fedora 40+). Zum sicheren Speichern der Passwörter: ein Schlüsselbund (GNOME Keyring, KWallet oder KeePassXC). |
| Android | Android 7.0 oder neuer; ein TuxMail-Server (Abschnitt 6) |
| Server | Debian 12/13, Ubuntu 22.04/24.04 oder RHEL/Rocky/AlmaLinux 9, amd64 oder arm64, systemd, SSH-Zugang mit `sudo`; für Let's Encrypt eine Domain, die auf den Server zeigt, und offene Ports 80/443 |

### Download prüfen (empfohlen)

Jedes Release enthält `checksums.txt` mit den SHA-256-Prüfsummen aller Dateien.

**Linux / macOS:**
```bash
sha256sum -c checksums.txt --ignore-missing
```
Erwartet wird für jede heruntergeladene Datei die Ausgabe `<Datei>: OK`.

**Windows (PowerShell):**
```powershell
Get-FileHash .\TuxMail-0.1.0-windows-amd64-setup.exe -Algorithm SHA256
```
Den angezeigten Wert mit der Zeile in `checksums.txt` vergleichen.

Ist zusätzlich `checksums.txt.asc` vorhanden, ist das Release mit einem GPG-Schlüssel signiert:
```bash
gpg --import tuxmail-release-key.asc
gpg --verify checksums.txt.asc checksums.txt
```

---

## 2. Windows

### 2a) Setup (empfohlen): `TuxMail-<v>-windows-<arch>-setup.exe`

1. **Datei herunterladen.** Blockiert der Browser den Download („Gefährlicher Download blockiert“, „wird selten
   heruntergeladen“): Downloads öffnen, beim Eintrag **„…“ → „Beibehalten“**, dann **„Weitere Informationen“ →
   „Trotzdem behalten“**. Der Grund: Die Windows-Programme sind noch nicht digital signiert.
2. **Doppelklick auf die Datei.** Erscheint **„Der Computer wurde durch Windows geschützt“** (SmartScreen), wählst du
   **„Weitere Informationen“ → „Trotzdem ausführen“**.
3. **Installationsart wählen:**
   - **Nur für mich** (Standard, keine Administratorrechte nötig) → `%LOCALAPPDATA%\Programs\TuxMail`
   - **Für alle Benutzer** → `C:\Program Files\TuxMail` (Windows fragt nach Administratorrechten)
   - **Portabel entpacken** → beliebiger Ordner, z. B. USB-Stick, ohne Registry-Einträge
4. **Optionen:** Startmenü-Eintrag (voreingestellt an), Desktop-Verknüpfung, „Als E-Mail-Programm registrieren“
   (für `mailto:`-Links), Autostart. Alles, was nicht angekreuzt ist, bleibt aus.
5. **Installieren** und danach **TuxMail starten**.

Fehlt die WebView2-Laufzeit, lädt das Setup sie von Microsoft und prüft deren Signatur, bevor es sie ausführt.

**Für Administratoren (stille Installation):**
```bat
TuxMail-0.1.0-windows-amd64-setup.exe /S --scope=machine --desktop-shortcut --log=C:\Temp\tuxmail-setup.log
"C:\Program Files\TuxMail\TuxMail-Setup.exe" --uninstall --silent
```
Weitere Schalter: `--scope=user|machine|portable`, `--dir=C:\Pfad`, `--no-start-menu`, `--default-mail`,
`--autostart`, `--no-close-apps`. Exit-Code 0 = OK, 1 = Fehler, 2 = ungültige Argumente, 740 =
Administratorrechte verweigert.

### 2b) Portabel: `TuxMail-<v>-windows-<arch>-portable.zip`

1. Rechtsklick auf das ZIP → **Eigenschaften** → unten **„Zulassen“** anhaken → OK. Das verhindert eine
   SmartScreen-Meldung für jede einzelne Datei.
2. Rechtsklick → **Alle extrahieren…** in einen Ordner, z. B. `D:\TuxMail` oder auf den USB-Stick.
   **Nicht direkt aus dem ZIP starten.**
3. `TuxMail\TuxMail.exe` doppelklicken.
4. Beim ersten Start ein **Master-Passwort** festlegen. Im portablen Modus liegen die Zugangsdaten verschlüsselt in
   `data\secrets.vault`. Ohne das Passwort sind sie nicht wiederherstellbar, also gut aufbewahren.

Alle Daten liegen im Ordner `data` neben `TuxMail.exe`.

**Messenger-Plugins (WhatsApp, Telegram, Matrix, Signal):** TuxMail startet sie nur aus `plugins\` neben
`TuxMail.exe` und nur, wenn weder die Plugin-Datei noch ihr Ordner für andere Benutzer beschreibbar sind. Ein
normaler Ordner im Benutzerprofil oder unter `Programme` erfüllt das. Ein für alle beschreibbarer Ordner (z. B.
`C:\Tools` mit Standardrechten, ein Netzlaufwerk) oder ein FAT/exFAT-USB-Stick (keine Zugriffsrechte) nicht: Dort
bleiben die Plugins aus, und im Protokoll steht `ignoring plugin`. Wer das Risiko eines gemeinsam genutzten
Datenträgers bewusst trägt, setzt die Umgebungsvariable `TUXMAIL_ALLOW_UNSAFE_PLUGIN_DIR=1`. Unter Linux gilt
dasselbe: Besitzer root oder du, nicht für andere beschreibbar. Plugins sehen nur eine gefilterte Umgebung (PATH,
Heimatverzeichnis, Proxy, Sprache, `TUXMAIL_<PLUGIN>_*`), nicht die Geheimnisse des Servers.

---

## 3. Linux (Desktop)

Der Desktop-Client braucht GTK 3 und WebKitGTK 4.1. Die Pakete (`.deb`, `.rpm`) ziehen diese Abhängigkeiten
automatisch nach; das AppImage bringt sie selbst mit.

### 3a) Debian, Ubuntu, Linux Mint: `.deb`

```bash
sudo apt install ./tuxmail_0.1.0-1_amd64.deb
```
Der Punkt-Schrägstrich `./` ist wichtig, sonst sucht `apt` in den Paketquellen.

Start: im Anwendungsmenü **TuxMail** oder im Terminal `tuxmail`.

### 3b) Fedora, openSUSE, RHEL, Rocky, Alma: `.rpm`

```bash
# Fedora, RHEL, Rocky, Alma
sudo dnf install ./tuxmail-0.1.0-1.x86_64.rpm
# openSUSE
sudo zypper install --allow-unsigned-rpm ./tuxmail-0.1.0-1.x86_64.rpm
```
Unter RHEL/Rocky/Alma kommt WebKitGTK aus EPEL: vorher `sudo dnf install epel-release`.

### 3c) AppImage (jede Distribution)

```bash
chmod +x TuxMail-0.1.0-linux-amd64.AppImage
./TuxMail-0.1.0-linux-amd64.AppImage
```
Oder im Dateimanager: Rechtsklick → Eigenschaften → „Als Programm ausführen“ erlauben → Doppelklick.

Meldet das AppImage, dass FUSE fehlt:
- Ubuntu 22.04: `sudo apt install libfuse2`; Ubuntu 24.04 und neuer: `sudo apt install libfuse2t64`
- oder ohne FUSE starten: `./TuxMail-0.1.0-linux-amd64.AppImage --appimage-extract-and-run`

Für einen Eintrag im Anwendungsmenü eignen sich [Gear Lever](https://flathub.org/apps/it.mijorus.gearlever) oder
AppImageLauncher.

### 3d) Portabel: `TuxMail-<v>-linux-<arch>-portable.tar.gz`

Braucht GTK 3 und WebKitGTK 4.1 vom System (Debian/Ubuntu: `sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0`).
```bash
tar -xzf TuxMail-0.1.0-linux-amd64-portable.tar.gz
cd TuxMail
./tuxmail
```
Alle Daten liegen im Ordner `data` daneben. Die Zugangsdaten sind mit einem Master-Passwort verschlüsselt, wie
unter Windows.

---

## 4. Android und Smartphone

Die Android-App ist ein **Client für einen TuxMail-Server** (Abschnitt 6). Sie verbindet sich mit dessen Adresse und
meldet sich dort an. Das Synchronisieren der Konten erledigt der Server; die App zeigt an, sendet und empfängt
Push-Benachrichtigungen.

1. `TuxMail-<v>-foss.apk` (bzw. `…-foss-debug.apk`) **auf dem Handy** herunterladen.
2. Antippen. Android fragt einmalig: **„Installation aus dieser Quelle zulassen“**. Das für den Browser bzw. die
   Dateien-App erlauben, zurückgehen und **Installieren** wählen.
3. Google Play Protect kann warnen („Unbekannte App“): **Details → Trotzdem installieren**.
4. App öffnen, die Adresse des TuxMail-Servers eingeben (z. B. `https://mail.example.org`) und anmelden. Alternativ
   am PC unter *Einstellungen → Konten → TuxMail-Server* den Code zur Geräteanmeldung anzeigen lassen und am Handy
   bestätigen. Unverschlüsseltes `http://` ist nur für `localhost` erlaubt.
5. Benachrichtigungen erlauben. Die `foss`-Variante nutzt UnifiedPush; dafür eine Verteiler-App installieren, z. B.
   [ntfy](https://f-droid.org/packages/io.heckel.ntfy/). Ohne Verteiler prüft die App regelmäßig selbst.

Update: neue APK genauso installieren, die Daten bleiben erhalten. Eine `…-debug.apk` lässt sich später nicht direkt
durch eine signierte Release-APK ersetzen. Dann einmal deinstallieren und neu installieren.

**Ohne App (iPhone und jedes andere Smartphone):** Server-Adresse im Browser öffnen, dann *Teilen → Zum
Home-Bildschirm* (iPhone: Safari, ab iOS 16.4) bzw. *Menü → App installieren* (Android/Chrome). Benachrichtigungen
funktionieren auch so (PWA).

---

## 5. Kommandozeile (Server-Archive)

`tuxmail_<v>_linux_<arch>.tar.gz` bzw. `tuxmail_<v>_windows_<arch>.zip` enthalten das Konsolenprogramm `tuxmail`
(Server, Import/Export, Benutzerverwaltung) mit allen Messenger-Plugins, aber ohne Desktop-Fenster.

```bash
mkdir -p ~/tuxmail && tar -xzf tuxmail_0.1.0_linux_amd64.tar.gz -C ~/tuxmail
~/tuxmail/tuxmail version
~/tuxmail/tuxmail --help
```

Nützliche Befehle: `tuxmail serve` (Server starten), `tuxmail user …` (Benutzer verwalten), `tuxmail import` und
`tuxmail export` (Mails, Kalender, Kontakte), `tuxmail backup` und `tuxmail restore`, `tuxmail check-config`.

---

## 6. Eigener Server

Der Server liefert die Weboberfläche (auch als App auf dem Handy installierbar), verwaltet mehrere Benutzer und
gemeinsame Postfächer und synchronisiert die Mail-Konten im Hintergrund. Drei Wege führen dorthin:

| Weg | Wann sinnvoll |
|---|---|
| **6a Ansible** (empfohlen) | Eigener Linux-Server oder VM. Richtet alles ein: systemd-Dienst, nginx, TLS, Sicherungen, auf Wunsch Matrix-Bridges. Ohne Docker. |
| **6b Manuell** | Du willst jeden Schritt selbst kontrollieren, oder Ansible ist keine Option. |
| **6c Docker / Podman** | Du betreibst schon Container und einen eigenen Reverse Proxy. |

Alle drei Wege nutzen dasselbe Programm. Ob TLS vom Server selbst oder von einem Reverse Proxy (nginx, Caddy,
Apache, HAProxy, Traefik) beendet wird, legt die Konfigurationsdatei fest (Abschnitt 6d).

### 6a) Empfohlen: Ansible, ohne Docker

**Voraussetzungen** auf dem Steuerrechner (von dem aus du installierst): `ansible-core` ab 2.15 und Python 3, zum
Beispiel `pip install ansible`. Auf dem Server: eines der unterstützten Systeme (Abschnitt 1), SSH-Zugang mit
`sudo` und ausgehender HTTPS-Zugriff auf GitHub.

**Schritt für Schritt:**

1. Repository holen und ins Ansible-Verzeichnis wechseln:
   ```bash
   git clone https://github.com/Midas1989/tuxmail.git
   cd tuxmail/deploy/ansible
   ```
2. Server eintragen:
   ```bash
   cp inventory/hosts.example.ini inventory/hosts.ini
   $EDITOR inventory/hosts.ini        # Zeile: mail.example.de ansible_host=203.0.113.10 ansible_user=deploy
   ```
3. Einstellungen anpassen (Domain, Version, E-Mail für Let's Encrypt):
   ```bash
   $EDITOR inventory/group_vars/all.yml
   ```
4. Ansible-Sammlungen installieren:
   ```bash
   ansible-galaxy collection install -r requirements.yml
   ```
5. Playbook ausführen:
   ```bash
   ansible-playbook -i inventory/hosts.ini site.yml \
     -e tuxmail_domain=mail.example.de \
     -e tuxmail_tls_letsencrypt_email=admin@example.de
   ```
6. Das generierte Passwort des Administrators `admin` steht in
   `deploy/ansible/credentials/<Servername>/admin_password` (Datei nur für dich lesbar). Dann
   `https://mail.example.de` im Browser öffnen und anmelden.

**Was das Playbook einrichtet:** TuxMail unter `/opt/tuxmail`, Konfiguration in `/etc/tuxmail`, Daten in
`/var/lib/tuxmail`, einen gehärteten systemd-Dienst `tuxmail`, nginx mit Let's Encrypt (Erneuerung automatisch),
tägliche Sicherung und den ersten Administrator. Firewall, PDF-Rendering und der Matrix-Homeserver mit den
Messenger-Bridges sind optional und standardmäßig aus.

**Wichtig vor dem ersten Lauf, zwei Sicherheitsabfragen:**

- **Release-Signatur.** Das Programm läuft als Dienst mit Systemrechten, deshalb prüft das Playbook die
  GPG-Signatur von `checksums.txt` gegen den Fingerabdruck des Release-Schlüssels
  (`tuxmail_release_gpg_fingerprint`). Das Release `v0.1.0` ist noch nicht signiert. Für einen ersten Test erlaubt
  `-e tuxmail_allow_unsigned_release=true` die Installation nur anhand der Prüfsumme. Sobald es signierte Releases
  gibt, den Fingerabdruck eintragen und die Ausnahme entfernen. Wer ein **selbst gebautes** Archiv installiert
  (`-e tuxmail_binary_src=/pfad/tuxmail_<v>_linux_amd64.tar.gz`), braucht beides nicht, denn dann wird nichts
  heruntergeladen.
- **Sicherungen.** Das Backup enthält den Hauptschlüssel des Servers und wird daher verschlüsselt:
  `tuxmail_backup_age_recipients: ["age1…"]` (privaten Schlüssel getrennt aufbewahren). Ohne Empfänger verweigert
  das Playbook die Sicherung, außer du erlaubst Klartext ausdrücklich mit `tuxmail_backup_allow_plaintext: true`.

**Varianten**, die du mit denselben Befehlen und anderen Schaltern erreichst:

| Situation | Schalter |
|---|---|
| neuer Server, nginx und Let's Encrypt (Standard) | `-e tuxmail_nginx_mode=new -e tuxmail_tls_mode=letsencrypt` |
| nginx ist schon da, eigenes Zertifikat (Firmen-CA, Wildcard) | `-e tuxmail_nginx_mode=existing -e tuxmail_tls_mode=custom` |
| TuxMail unter einem Unterpfad neben Nextcloud | `-e tuxmail_nginx_mode=existing -e tuxmail_nginx_existing_style=snippet -e tuxmail_subpath=/tuxmail -e tuxmail_tls_mode=none` |
| TLS endet an einem vorgelagerten Proxy (HAProxy, Traefik, Load-Balancer) | `-e tuxmail_tls_mode=none -e tuxmail_nginx_upstream_terminates_tls=true` und die Proxy-Adresse in `tuxmail_trusted_proxies_extra` |
| ohne nginx, TuxMail beendet TLS selbst | `-e tuxmail_nginx_mode=none -e tuxmail_tls_mode=custom -e tuxmail_listen=0.0.0.0:443 -e tuxmail_bind_privileged_port=true` |

Alle Variablen, Updates, Rollback, Sicherungen, Benutzerverwaltung, Matrix-Bridges und Fehlerbehebung:
**[deploy/README.md](../deploy/README.md)**.

### 6b) Manuell, ohne Ansible

```bash
# 1. Benutzer und Verzeichnisse
sudo useradd --system --home-dir /var/lib/tuxmail --shell /usr/sbin/nologin tuxmail
sudo install -d -m 0755 /opt/tuxmail/versions/0.1.0
sudo install -d -o root -g tuxmail -m 0750 /etc/tuxmail
sudo install -d -o tuxmail -g tuxmail -m 0750 /var/lib/tuxmail /var/log/tuxmail

# 2. Programm entpacken (Archiv aus dem Release oder selbst gebaut)
sudo tar -xzf tuxmail_0.1.0_linux_amd64.tar.gz -C /opt/tuxmail/versions/0.1.0
sudo ln -sfn /opt/tuxmail/versions/0.1.0 /opt/tuxmail/current

# 3. Konfiguration und Dienst (Dateien liegen im Repository unter deploy/systemd/)
sudo install -o root -g tuxmail -m 0640 deploy/systemd/config.example.yaml /etc/tuxmail/config.yaml
sudoedit /etc/tuxmail/config.yaml            # public_url, listen, tls
sudo install -m 0644 deploy/systemd/tuxmail.service /etc/systemd/system/
/opt/tuxmail/current/tuxmail check-config --config /etc/tuxmail/config.yaml
sudo systemctl daemon-reload && sudo systemctl enable --now tuxmail

# 4. Ersten Administrator anlegen (Passwort wird über stdin übergeben)
echo 'SicheresPasswort' | sudo runuser -u tuxmail -- /opt/tuxmail/current/tuxmail user add \
  --config /etc/tuxmail/config.yaml --username admin --email admin@example.org --admin --password-stdin
```

Danach brauchst du TLS: entweder einen Reverse Proxy (Beispiel `deploy/nginx/tuxmail.conf`) oder die Einstellung
`tls.mode: file` (Abschnitt 6d). Den Dienst prüfst du mit `systemctl status tuxmail` und
`curl http://127.0.0.1:8080/healthz`.

### 6c) Docker / Podman

```bash
curl -LO https://raw.githubusercontent.com/Midas1989/tuxmail/main/deploy/docker/docker-compose.example.yml
mv docker-compose.example.yml docker-compose.yml
$EDITOR docker-compose.yml        # TUXMAIL_PUBLIC_URL und Image-Tag, z. B. ghcr.io/midas1989/tuxmail:0.1.0
docker compose up -d
docker compose exec tuxmail tuxmail user add --username admin --admin     # Passwort wird ausgegeben
```

Der Container lauscht nur auf `127.0.0.1:8080` und spricht HTTP. Davor gehört ein Reverse Proxy mit TLS. Das Volume
`/data` enthält alle Daten einschließlich `master.key` und muss **unbedingt gesichert** werden.

### 6d) Mit oder ohne Reverse Proxy, Zertifikat per Konfigurationsdatei

TuxMail kennt zwei Betriebsarten, eingestellt in `/etc/tuxmail/config.yaml`:

**Hinter einem Reverse Proxy** (nginx, Caddy, Apache, HAProxy, Traefik):
```yaml
listen: "127.0.0.1:8080"                 # oder "unix:/run/tuxmail/tuxmail.sock"
public_url: "https://mail.example.org"   # die Adresse, die die Benutzer sehen (https!)
tls:
  mode: "none"
trusted_proxies: ["127.0.0.1/32", "::1/128"]    # Adresse(n) des Proxys
```
Der Proxy muss `Host`, `X-Forwarded-For` und `X-Forwarded-Proto` weiterreichen, WebSocket-Verbindungen (`/api/events`)
erlauben und ein Upload-Limit passend zu `limits.max_upload_mb` setzen. Fertige Vorlagen gibt es nur für nginx
(`deploy/nginx/`).

**Ohne Proxy, Zertifikat aus Dateien:**
```yaml
listen: "0.0.0.0:443"
public_url: "https://mail.example.org"
tls:
  mode: "file"
  cert_file: "/etc/tuxmail/tls/fullchain.pem"
  key_file: "/etc/tuxmail/tls/privkey.pem"     # für die Gruppe tuxmail lesbar
```
Für Port 443 braucht der Dienst die Berechtigung `CAP_NET_BIND_SERVICE` (Ansible: `tuxmail_bind_privileged_port=true`,
manuell in der systemd-Unit). Das Zertifikat wird nur beim Start geladen: **nach jeder Erneuerung** (z. B. durch
certbot) `systemctl restart tuxmail` ausführen, am besten als Deploy-Hook. `tuxmail check-config` prüft, ob beide
Dateien lesbar sind.

### 6e) Server im eigenen Netz

Aus Sicherheitsgründen darf der Server standardmäßig keine Ziele im LAN oder auf `localhost` erreichen, zum Beispiel
einen internen Mailserver oder Ollama. Solche Ziele gibst du gezielt frei:
```yaml
security:
  allowed_networks: ["192.168.10.5:993"]
```
Mehr dazu in [deploy/README.md](../deploy/README.md).

---

## 7. Selbst bauen

Alle Varianten lassen sich ohne GitHub auf einem Linux-Rechner bauen.

```bash
git clone https://github.com/Midas1989/tuxmail.git && cd tuxmail
build/build-all.sh --check        # was lässt sich hier bauen, was fehlt?
build/build-all.sh                # alles Mögliche nach dist/
build/build-all.sh server         # nur die Server-Archive
```

Voraussetzungen je Ziel (Go, Node 22, für den Linux-Desktop GTK/WebKitGTK-Entwicklungspakete, für Android JDK 21 und
das Android SDK) sowie Einzelskripte und Signierung stehen in **[RELEASE.md](RELEASE.md)**, Abschnitt 4.
Eigene Server-Archive installierst du mit `tuxmail_binary_src` (Abschnitt 6a), eigene Pakete wie die
heruntergeladenen.

---

## 8. Erste Schritte nach der Installation

1. TuxMail starten, **Konto hinzufügen** und E-Mail-Adresse samt Passwort eingeben. Die Servereinstellungen (IMAP/SMTP,
   Exchange/ActiveSync, Microsoft 365, Gmail, JMAP) werden automatisch gesucht, sonst hilft „Manuell einrichten“.
   Das Passwort wird dabei nur an Server geschickt, deren Herkunft verlässlich ist, und nie unverschlüsselt über das Netz.
2. Microsoft 365 und Gmail: Die Anmeldung läuft im Browserfenster (OAuth).
3. Messenger (WhatsApp, Signal, Telegram, Matrix): *Einstellungen → Chats & Messenger → Verbinden* und den QR-Code
   bzw. Link mit dem Handy scannen. TuxMail wird ein „verknüpftes Gerät“, wie bei WhatsApp Web.
4. Umzug von einem anderen Programm: *Einstellungen → Daten → Import* (Thunderbird-Profil, mbox/EML, PST, ICS,
   vCard/CSV/LDIF).
5. **Auf dem Server als Administrator:**
   - **Zwei-Faktor-Anmeldung einrichten.** Für Administratoren ist sie standardmäßig Pflicht
     (`tuxmail_auth_totp_required_for_admins`, in der Konfiguration `auth.totp_required_for_admins`). Nach der ersten
     Anmeldung erscheint deshalb ein Dialog, den du nicht schließen kannst. Du scannst den QR-Code mit einer
     Authenticator-App (z. B. Aegis, FreeOTP, Google Authenticator), gibst den ersten 6-stelligen Code ein und
     speicherst die zehn **Wiederherstellungscodes** an einem sicheren Ort. Sie werden nur einmal angezeigt. Jeder
     Benutzer kann dasselbe später unter *Einstellungen → Sicherheit & Datenschutz* tun, dort auch das Passwort ändern
     und angemeldete Sitzungen beenden.
   - **Benutzer verwalten.** *Einstellungen → Administration* (nur für Administratoren im Web-Client) enthält vier
     Reiter: **Benutzer** (anlegen, bearbeiten, deaktivieren, Passwort und Zwei-Faktor zurücksetzen, Sitzungen
     beenden, löschen und **gesperrte Konten entsperren**), **Einladungen** (Registrierungslinks, setzt
     `auth.registration: invite` voraus), **Gruppen** und **Protokoll** (Audit-Log). Ein generiertes Passwort wird
     nur einmal angezeigt. Alternativ geht es weiter auf der Kommandozeile mit `tuxmail user add`
     ([deploy/README.md](../deploy/README.md), Abschnitt 8).
   - **Gemeinsame Postfächer** richtest du in den Team-Einstellungen ein.

---

## 9. Aktualisieren und Deinstallieren

| Variante | Aktualisieren | Deinstallieren |
|---|---|---|
| Windows-Setup | Neues Setup ausführen. Es erkennt die Installation, übernimmt Ordner und Optionen und schließt ein laufendes TuxMail nach Rückfrage. Konten und Mails bleiben. | *Einstellungen → Apps → Installierte Apps → TuxMail*. „Benutzerdaten behalten“ ist voreingestellt, sonst wird `%APPDATA%\TuxMail` gelöscht. |
| Windows portabel | Neues ZIP entpacken, den alten Ordner `data` und die leere Datei `portable` hinüberkopieren. | Ordner löschen. |
| `.deb` | Neue `.deb` genauso installieren. | `sudo apt remove tuxmail` |
| `.rpm` | Neue `.rpm` genauso installieren. | `sudo dnf remove tuxmail` bzw. `sudo zypper remove tuxmail` |
| AppImage, Linux portabel | Neue Datei bzw. neues Archiv verwenden; bei portabel `data` mitnehmen. | Datei bzw. Ordner löschen. |
| Android | Neue APK installieren, Daten bleiben erhalten. | Wie jede App. |
| Server (Ansible) | `ansible-playbook … -e tuxmail_version=<neu> --tags tuxmail`. Das Playbook sichert vorher die Daten und bietet Rollback. | Dienst stoppen, `/opt/tuxmail`, `/etc/tuxmail`, `/var/lib/tuxmail` entfernen. |
| Server (Docker) | Neues Image-Tag eintragen, `docker compose pull && docker compose up -d`. | `docker compose down` (Volume bleibt, bis du es löschst). |

Eine automatische Update-Funktion im Programm gibt es noch nicht.

---

## 10. Probleme und Lösungen

| Problem | Lösung |
|---|---|
| Windows meldet „Unbekannter Herausgeber“ oder SmartScreen blockiert | Siehe 2a. Signierte Pakete folgen. |
| Linux: Passwörter werden nicht gespeichert | Schlüsselbund installieren und entsperren (`gnome-keyring`, KWallet oder KeePassXC mit Secret-Service). |
| AppImage startet nicht, „FUSE“ | Siehe 3c. |
| Server: Playbook bricht mit Hinweis auf Signatur oder Fingerabdruck ab | Siehe 6a, „Release-Signatur“. |
| Server: Playbook bricht bei der Sicherung ab | Siehe 6a, „Sicherungen“. |
| Server erreicht den eigenen Mailserver nicht | Siehe 6e (`security.allowed_networks`). |
| Nach Zertifikatserneuerung zeigt der Browser noch das alte Zertifikat | Nur bei `tls.mode: file`: `sudo systemctl restart tuxmail`. |
| Anmeldung wird kurz gesperrt | Mehrere falsche Passwörter lösen eine Wartezeit von einer Minute bis zu einer Stunde aus. Abwarten, oder ein Administrator hebt die Sperre auf: *Einstellungen → Administration → Benutzer → Entsperren*. |
| Authenticator-App verloren | Mit einem der zehn Wiederherstellungscodes anmelden und Zwei-Faktor neu einrichten. Ohne Code setzt ein anderer Administrator ihn zurück: *Administration → Benutzer → Verwalten → Zwei-Faktor zurücksetzen*. |
| Messenger-Plugins fehlen unter Windows | Siehe 2b („Messenger-Plugins“). |

Diagnose: *Einstellungen → Info & Diagnose → Diagnosebericht*; Passwörter und Tokens werden geschwärzt.
Fehler melden: [Issues](https://github.com/Midas1989/tuxmail/issues). Sicherheitslücken bitte vertraulich:
[SECURITY.md](../SECURITY.md).

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

Server quick start: `git clone https://github.com/Midas1989/tuxmail.git && cd tuxmail/deploy/ansible`, copy and edit
`inventory/hosts.example.ini`, then
`ansible-playbook -i inventory/hosts.ini site.yml -e tuxmail_domain=mail.example.com -e tuxmail_tls_letsencrypt_email=you@example.com -e tuxmail_allow_unsigned_release=true -e tuxmail_backup_allow_plaintext=true`
(the two last switches are only for a first test: releases are not signed yet and backups are not encrypted).
TLS can be terminated by the server itself (`tls.mode: file`, restart after renewing the certificate) or by a reverse
proxy (`tls.mode: none`).

Build everything yourself on Linux: `build/build-all.sh --check`, then `build/build-all.sh` (see
[RELEASE.md](RELEASE.md)). Verify downloads: `sha256sum -c checksums.txt --ignore-missing`. ARM devices: use the
`arm64`/`aarch64` files.
