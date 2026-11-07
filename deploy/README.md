<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# TuxMail – Server-Installation (Linux)

Dieses Verzeichnis enthält alles, um TuxMail im **Server-/Web-Modus** (`tuxmail serve`) auf einem
Linux-Server zu betreiben (siehe [KONZEPT.md, Kap. 4.5, 15.5, 15.6](../docs/KONZEPT.md)).

| Pfad | Inhalt |
|---|---|
| `ansible/` | Ansible-Projekt (empfohlen): Installation **ohne Docker/Podman**, Updates, nginx, TLS, Backups, Firewall, optional Matrix-Homeserver + Bridges und PDF-Rendering |
| `systemd/` | Beispiel-Units und -Konfiguration für eine manuelle Installation (aus den Ansible-Templates erzeugt) |
| `nginx/` | Beispiel-vHost (`tuxmail.conf`) und location-Snippet (`tuxmail-snippet.conf`) |
| `docker/` | `docker-compose.example.yml` für das Container-Image `ghcr.io/midas1989/tuxmail` – nur als Alternative (Abschnitt 12) |

> **Grundsatz:** Die gesamte Linux-Installation funktioniert **ohne Docker und ohne Podman**.
> TuxMail selbst und alle optionalen Komponenten laufen als native Programme unter systemd
> (Abschnitt 2). Container sind nur noch eine ausdrückliche, abschaltbare Ausnahme
> (`tuxmail_bridges_runtime=container`, `tuxmail_pdf_renderer=gotenberg_container`).

---

## 1. Voraussetzungen

**Zielsystem** (frisch oder bestehend):

- Debian 12/13, Ubuntu 22.04/24.04/**26.04** oder RHEL/Rocky/AlmaLinux 9, Architektur amd64 oder arm64
- systemd, SSH-Zugang mit `sudo`-Rechten (oder root)
- Für Let's Encrypt: DNS-Eintrag der Domain zeigt auf den Server, Port 80 und 443 aus dem Internet erreichbar
- Ausgehender HTTPS-Zugriff auf die Release-URL (sonst `tuxmail_binary_src` verwenden)
- **Release-Signatur:** Das Binary läuft als root-eigener Dienst, daher wird das Release vor der Installation
  über die GPG-Signatur von `checksums.txt` geprüft – siehe „Release-Signatur“ unten (Fingerabdruck
  `tuxmail_release_gpg_fingerprint` ist **Pflicht**, sonst bricht das Playbook vor jeder Änderung ab).
- **Sicherungen:** nach dem Rollout in der Oberfläche den Backup-Schlüssel erzeugen (Einstellungen → Administration →
  Sicherung). Bis dahin läuft keine Sicherung, das Playbook warnt und läuft weiter, siehe Abschnitt 7.

**Steuerrechner** (von dem aus Ansible läuft):

- `ansible-core` ≥ 2.15 (getestet mit 2.21/2.22) und Python 3. Für **Ubuntu 26.04** als Ziel (bringt Python 3.14 mit)
  wird `ansible-core` ≥ 2.20 empfohlen
- Collections: `ansible-galaxy collection install -r deploy/ansible/requirements.yml`
  (`ansible.posix`, `community.general`, `community.postgresql`; alle im pip-Paket `ansible` bereits
  enthalten – praktisch, wenn Ansible Galaxy nicht erreichbar ist: `pip install ansible`)

```bash
cd deploy/ansible
cp inventory/hosts.example.ini inventory/hosts.ini     # Server eintragen
$EDITOR inventory/group_vars/all.yml                   # Domain, Version, Modi, E-Mail
ansible-galaxy collection install -r requirements.yml
```

### Hinweise zu Ubuntu 26.04 LTS („resolute“)

Ubuntu 26.04 wird unterstützt. Gegen das Paketarchiv von 26.04 wurde geprüft, dass alle Pakete vorhanden sind, die
das Playbook und das `.deb` brauchen: `nginx` 1.28, `certbot` und `python3-certbot-nginx` 4.0, `ufw`, `age`, `zstd`,
`gpg`, `python3-debian`, `postgresql` 18, `python3-psycopg2`, `podman`, `liburing2` sowie für den Desktop
`libwebkit2gtk-4.1-0` 2.52 und `libgtk-3-0t64` (liefert `libgtk-3-0`). Gleichzeitig gilt:

- **Chromium (PDF-Export):** Ubuntu liefert es weiter nur als Snap. Wie unter 22.04/24.04 muss
  `tuxmail_pdf_chromium_path` angegeben werden (siehe „PDF-Rendering“); Gotenberg als Container ist die Alternative.
- **Matrix-Homeserver:** Das matrix.org-Repository bietet für neue Ubuntu-Versionen oft erst verspätet Pakete an. Das
  Playbook prüft die Suite `resolute` vorab und bricht mit einem Hinweis ab, wenn sie fehlt. Empfohlen ist dann
  Continuwuity (`tuxmail_matrix_server=continuwuity`, Standard).
- **Python 3.14 auf dem Ziel:** Nutze auf dem Steuerrechner `ansible-core` ≥ 2.20.
- Das Server-Archiv ist ein statisches Programm ohne Systembibliotheken und läuft unverändert.

Das Playbook selbst wurde auf einer echten 26.04-Maschine noch nicht ausgeführt; die Systemprüfung, die Syntax und
die Paketnamen sind getestet.

## 2. Installation ohne Docker (Standard)

Alles wird aus Distributionspaketen oder aus den **offiziellen Release-Dateien** (feste Version,
SHA-256-Prüfsumme) installiert und läuft als eigener Systembenutzer in einer gehärteten
systemd-Unit. Jede optionale Komponente ist standardmäßig **aus** und idempotent.

### Komponenten

| Komponente | Schalter (Standard) | Herkunft | Dienst / Benutzer | Pfade |
|---|---|---|---|---|
| TuxMail | immer | GitHub-Release + `checksums.txt` | `tuxmail.service` / `tuxmail` | `/opt/tuxmail/{versions,current}`, `/etc/tuxmail`, `/var/lib/tuxmail` |
| nginx, certbot | `tuxmail_nginx_mode` (`new`), `tuxmail_tls_mode` | Distribution (EPEL) | `nginx` | `/etc/nginx` |
| PDF: Chromium | `tuxmail_pdf_renderer: chromium` (`none`) | Distribution (`chromium`, RHEL: EPEL `chromium-headless`) | Kindprozess von `tuxmail` | `/etc/tuxmail/pdf.env` |
| Matrix: Continuwuity | `tuxmail_bridges_enabled: true` (`false`), `tuxmail_matrix_server: continuwuity` | statisches Binary (Forgejo-Release) | `continuwuity.service` / `continuwuity` | `/opt/continuwuity`, `/etc/continuwuity`, `/var/lib/continuwuity` |
| Matrix: Synapse | `tuxmail_matrix_server: synapse` | apt-Repo `packages.matrix.org` (nur Debian/Ubuntu) | `matrix-synapse.service` / `matrix-synapse` | `/etc/matrix-synapse`, `/var/lib/matrix-synapse` |
| mautrix-whatsapp | `tuxmail_bridges_whatsapp_enabled` (`true`) | GitHub-Release + SHA-256 | `mautrix-whatsapp.service` / `mautrix-whatsapp` | `/opt/mautrix-whatsapp`, `/etc/mautrix-whatsapp`, `/var/lib/mautrix-whatsapp` |
| mautrix-signal | `tuxmail_bridges_signal_enabled` (`true`) | GitHub-Release + SHA-256 | `mautrix-signal.service` / `mautrix-signal` | analog |
| mautrix-gmessages | `tuxmail_bridges_gmessages_enabled` (`false`) | GitHub-Release + SHA-256 | `mautrix-gmessages.service` / `mautrix-gmessages` | analog |
| PostgreSQL | `tuxmail_bridges_db: postgres` (`sqlite`) | Distribution | `postgresql.service` | Distributionsstandard |
| *Gotenberg* | `tuxmail_pdf_renderer: gotenberg_container` | **Container** (Ausnahme) | `tuxmail-gotenberg.service` | – |

Meta-Bridges (Instagram/Facebook) sind entfallen (KONZEPT 11.2).

### Beispiel: alles nativ

```bash
ansible-playbook site.yml \
  -e tuxmail_domain=mail.example.de -e tuxmail_tls_letsencrypt_email=admin@example.de \
  -e tuxmail_pdf_renderer=chromium \
  -e tuxmail_bridges_enabled=true \
  -e '{"tuxmail_continuwuity_sha256": {"amd64": "<sha256 des Continuwuity-Binaries>"}}'
```

Ergebnis: TuxMail + nginx + Let's Encrypt, Chromium für PDFs, Continuwuity auf `127.0.0.1:8008`
(öffentlich nur `https://mail.example.de/_matrix/…`, Föderation aus), `mautrix-whatsapp` und
`mautrix-signal` mit SQLite, ein Matrix-Admin-Konto `@admin:mail.example.de` (Passwort unter
`credentials/<host>/matrix_admin_password`) und vollautomatisch registrierte Bridges.
Mit `-e tuxmail_matrix_server=synapse -e tuxmail_bridges_db=postgres` stattdessen Synapse mit PostgreSQL.

### Matrix-Homeserver

**Continuwuity** (Standard): einzelnes statisches Binary, sehr geringer Speicherbedarf.

- Download: `tuxmail_continuwuity_url` =
  `https://forgejo.ellis.link/continuwuation/continuwuity/releases/download/v<version>/conduwuit-linux-static-<arch>`.
  Die offiziellen Releases liegen auf Forgejo; der GitHub-Spiegel enthält keine Binaries. Asset-Name
  (`tuxmail_continuwuity_asset`) und Basis-URL (`tuxmail_continuwuity_release_base_url`) haben sich
  zwischen Versionen geändert – **vor dem Einsatz auf der Release-Seite prüfen**.
- Die Prüfsumme ist **Pflicht** und nicht vorbelegt (`tuxmail_continuwuity_sha256: {amd64: …, arm64: …}`
  oder `tuxmail_continuwuity_checksum_url`). Ermitteln: Datei herunterladen, `sha256sum` ausführen,
  mit der Release-Seite vergleichen. Alternativ `tuxmail_continuwuity_binary_src` (lokale Datei).
- Konfiguration `/etc/continuwuity/continuwuity.toml`: `server_name`, Listener nur `127.0.0.1`,
  Registrierung aus, Föderation laut `tuxmail_matrix_federation`, `[global.well_known]`.
  Zusätzliche Schlüssel: `tuxmail_continuwuity_extra_config`.
- **Erstes Konto:** Beim ersten Lauf enthält die Konfiguration kurzzeitig ein zufälliges, einmaliges
  `registration_token`; Ansible registriert damit über die Client-API `@<tuxmail_matrix_admin_user>`
  (das erste Konto wird Server-Admin und Mitglied von `#admins:<server_name>`) und schaltet die
  Registrierung sofort wieder ab. Das Zugriffstoken liegt nur auf dem Server
  (`/etc/continuwuity/admin.token`, root 0600).
- **Bridges registrieren:** Continuwuity liest Registrierungen nicht aus Dateien. Ansible sendet
  daher mit dem Admin-Token im Admin-Raum `!admin appservices register` + Registrierung (Markierung
  `/etc/continuwuity/appservices/<bridge>.registered`; bei Änderung vorher `unregister`).
  Ohne Token (`tuxmail_continuwuity_automate=false`, Konto manuell angelegt) gibt das Playbook die
  genauen Schritte aus:

  ```text
  # im Raum #admins:<server_name> als Admin senden, je Bridge:
  !admin appservices register
  ```
  ```yaml
  <Inhalt von /etc/continuwuity/appservices/whatsapp.yaml>
  ```
  ```text
  !admin appservices list
  sudo systemctl restart mautrix-whatsapp mautrix-signal
  ```

**Synapse** (`tuxmail_matrix_server: synapse`): Paket `matrix-synapse-py3` aus dem offiziellen
Repository `https://packages.matrix.org/debian/` (Schlüssel wird gegen den Fingerabdruck
`AAF9AE843A7584B5A3E4CD2BCF45A512DE2DA058` geprüft, Version optional per `tuxmail_synapse_version`).
Eigene Einstellungen nur in `/etc/matrix-synapse/conf.d/tuxmail.yaml` (Listener `127.0.0.1`,
`enable_registration: false`, `app_service_config_files`, ggf. PostgreSQL); die Bridges werden
vollautomatisch registriert, das Admin-Konto per `register_new_matrix_user` angelegt. Zusätzliche
Härtung per Drop-in `matrix-synapse.service.d/tuxmail-hardening.conf`.
**RHEL/Rocky/Alma: nicht unterstützt** (keine offiziellen Pakete, Systempython zu alt) – dort
Continuwuity verwenden.

**nginx:** `https://<tuxmail_domain>/_matrix/` (bei Synapse auch `/_synapse/client/`) wird an den
Homeserver weitergereicht (`tuxmail_matrix_nginx_proxy`); `/_matrix/federation` und `/_matrix/key`
liefern 404, solange `tuxmail_matrix_federation=false`. `/.well-known/matrix/client` (und bei
Föderation `/server`) optional mit `tuxmail_matrix_well_known=true` – nur sinnvoll, wenn
`tuxmail_matrix_server_name` gleich `tuxmail_domain` ist. Im Snippet-Modus stehen die Matrix-Pfade
mit im Snippet. **`tuxmail_matrix_server_name` und die Föderation später nicht mehr ändern.**

### mautrix-Bridges

Je Bridge: `--generate-example-config` → Ansible setzt Homeserver (`http://127.0.0.1:8008`,
`domain`), Appservice-Adresse `127.0.0.1:<port>` (whatsapp 29318, signal 29328, gmessages 29336),
Datenbank, Logging nach journald und die **Rechte** (`tuxmail_bridges_permissions`: Server-Domain
`user`, `tuxmail_bridges_matrix_admin` `admin`) → `--generate-registration` → Start mit
`--no-update` (die Bridge schreibt ihre Konfiguration nie selbst um). Eigene Anpassungen:
`tuxmail_mautrix_config_overrides: {whatsapp: {…}}`. Die Units starten nach dem Homeserver
(`After=`/`Wants=`, bei PostgreSQL zusätzlich `Requires=postgresql.service`).

**Datenbank:** SQLite unter `/var/lib/mautrix-<name>/` (Standard) oder PostgreSQL
(`tuxmail_bridges_db: postgres`): je Bridge Rolle `mautrix-<name>` und Datenbank `mautrix_<name>`,
Anmeldung nur über den Unix-Socket mit `peer` – keine Passwörter. Ein späterer Wechsel
SQLite → PostgreSQL migriert keine Daten (Bridges neu koppeln).

### Bridges in TuxMail koppeln

1. In TuxMail ein **Matrix-Konto im Bridge-Modus** hinzufügen: Homeserver
   `tuxmail_matrix_public_base_url` (z. B. `https://mail.example.de`), Benutzer
   `@admin:<server_name>`, Passwort aus `credentials/<host>/matrix_admin_password` bzw. Vault.
2. Im Messenger-Bereich WhatsApp bzw. Signal **„Über Bridge koppeln“** wählen. TuxMail spricht mit
   dem Bridge-Bot (`@whatsappbot:…`, `@signalbot:…`, `@gmessagesbot:…`) und zeigt den QR-Code an
   (KONZEPT 11.3; bietet die eingesetzte TuxMail-Version den Assistenten noch nicht, Schritt 3
   verwenden). Auf dem Handy: WhatsApp → Verknüpfte Geräte → Gerät hinzufügen bzw.
   Signal → Verknüpfte Geräte.
3. Ohne Assistent (oder zur Fehlersuche) geht es mit jedem Matrix-Client: Direktchat mit
   `@whatsappbot:<server_name>` öffnen, `login` senden, QR-Code scannen; `help` listet alle Befehle.

Ein Messenger-Konto läuft **entweder** direkt **oder** über die Bridge, nie beides gleichzeitig.
Weitere Benutzer dürfen koppeln, wenn ihr Matrix-Konto auf diesem Server liegt (Recht `user`).

### PDF-Rendering

`tuxmail_pdf_renderer`:

- `none` (Standard) – kein HTML-Rendering durch einen Browser.
- `chromium` – Chromium aus den Distributionspaketen (Debian `chromium`, RHEL EPEL
  `chromium-headless`) plus Schriftarten; der gefundene Pfad landet als
  `TUXMAIL_CHROMIUM_PATH=…` in `/etc/tuxmail/pdf.env`, die `tuxmail.service` per
  `EnvironmentFile=-` einliest. Für Chromium als Kindprozess werden in `tuxmail.service`
  `MemoryDenyWriteExecute` (JIT), die Sperre von Namensräumen (außer cgroup) und der
  `@privileged`-Filter gelockert (`tuxmail_service_chromium_compat`). **Ubuntu** liefert Chromium
  nur als Snap, der unter der gehärteten Unit nicht läuft: Chrome/Chromium als `.deb` installieren
  und `tuxmail_pdf_chromium_path` setzen (oder `tuxmail_pdf_chromium_install=false`).
- `gotenberg_container` – Ausnahme mit Container: Gotenberg (`tuxmail_gotenberg_image`, feste
  Version) über `tuxmail-gotenberg.service` mit `podman` bzw. `docker run` auf
  `127.0.0.1:3000`; `pdf.env` erhält `TUXMAIL_GOTENBERG_URL`.

Wechsel auf `none`/`chromium` entfernt eine frühere Gotenberg-Unit wieder.

### Update und Rollback der Komponenten

Native Binaries liegen versioniert unter `/opt/<dienst>/versions/<version>/` mit Symlink
`/opt/<dienst>/current` (wie TuxMail). Update = Version **und** SHA-256 ändern, Playbook erneut
ausführen:

```bash
ansible-playbook site.yml --tags bridges \
  -e tuxmail_mautrix_whatsapp_version=0.2611.0 \
  -e '{"tuxmail_mautrix_whatsapp_sha256": {"amd64": "<sha256 aus sha256sums.txt>"}}'
```

Bei einem Versionswechsel wird der Dienst gestoppt, `/var/lib/<dienst>` nach
`/var/backups/tuxmail-bridges/<dienst>-<alt>-to-<neu>-<zeit>.tar.gz` gesichert
(`tuxmail_bridges_backup_before_upgrade`), der Symlink umgestellt und der Dienst neu gestartet.
`tuxmail_bridges_keep_versions` (3) Versionen bleiben für Rollbacks liegen:

```bash
# Rollback: alte Version (und deren Prüfsumme) wieder setzen und Playbook ausführen – oder manuell:
sudo systemctl stop mautrix-whatsapp
sudo ln -sfn /opt/mautrix-whatsapp/versions/0.2610.0 /opt/mautrix-whatsapp/current
# nur falls die neue Version die Datenbank migriert hat:
sudo tar -xzpf /var/backups/tuxmail-bridges/mautrix-whatsapp-0.2610.0-to-0.2611.0-<zeit>.tar.gz -C /var/lib
sudo systemctl start mautrix-whatsapp
```

Synapse wird über apt aktualisiert (`tuxmail_synapse_version` oder `apt upgrade`), Chromium und
PostgreSQL mit den normalen Systemupdates.

### Release-Signatur (Pflicht, Re-Audit ND-05)

Die Rolle `tuxmail` lädt `checksums.txt` **und** die abgetrennte Signatur `checksums.txt.asc` und prüft sie mit
`gpg` gegen den Fingerabdruck, den du festlegst. Erst der SHA-256 aus der so bestätigten Datei gilt für das
Archiv (`get_url … checksum`). Eine Prüfsumme aus demselben Release allein würde nur Übertragungsfehler erkennen.
Der Schlüssel selbst darf aus dem Release kommen (`tuxmail-release-key.asc`): das Vertrauen hängt allein am
Fingerabdruck, den du über einen zweiten Kanal bestätigst.

```yaml
# inventory/group_vars/all.yml
tuxmail_release_gpg_fingerprint: "ABCD EF01 2345 6789 ABCD  EF01 2345 6789 ABCD EF01"   # 40 Hex-Zeichen
# optional, unabhängig vom Release: öffentlicher Schlüssel als Datei auf dem Steuerrechner
# tuxmail_release_gpg_key_src: files/tuxmail-release-key.asc
```

Fingerabdruck ermitteln (nach dem Anlegen des Release-Schlüssels, `GPG_PRIVATE_KEY` in der Pipeline, docs/RELEASE.md):
`gpg --show-keys --with-colons tuxmail-release-key.asc | awk -F: '$1=="fpr"{print $10; exit}'`.
Die Prüfung läuft auf dem Zielsystem in einem leeren, temporären Schlüsselbund (Paket `gpg`/`gnupg2` wird
installiert) und bricht ab, wenn die Signatur fehlt, ungültig ist, von einem anderen Schlüssel stammt oder das
Archiv nicht in `checksums.txt` steht.

- **Release ohne Signatur** (die Pipeline signiert nur mit dem Secret `GPG_PRIVATE_KEY`): erst signieren lassen
  oder auf eigene Verantwortung `tuxmail_allow_unsigned_release: true` setzen (nur die Prüfsumme aus dem Release
  gilt dann; das Playbook gibt eine Warnung aus).
- `tuxmail_binary_src` (lokales Archiv/Binary vom Steuerrechner) braucht keine Signatur.
- Die Bridges-Rolle verlangt unverändert operatorseitig gepinnte SHA-256-Werte für ihre Binaries.

## 3. Schnellstart

Alle Befehle aus `deploy/ansible/` heraus. Einstellungen können in `inventory/group_vars/all.yml`
oder per `-e` gesetzt werden.

### a) Neuer Server: nginx neu + Let's Encrypt (Standard)

```bash
ansible-playbook site.yml \
  -e tuxmail_domain=mail.example.de \
  -e tuxmail_tls_letsencrypt_email=admin@example.de \
  -e tuxmail_nginx_mode=new -e tuxmail_tls_mode=letsencrypt
```

Ablauf: nginx wird installiert und liefert zuerst einen reinen HTTP-vHost mit
`/.well-known/acme-challenge/` aus, certbot holt das Zertifikat (Webroot-Verfahren), danach wird der
vHost mit TLS, HTTP→HTTPS-Umleitung und HSTS neu geschrieben. Die Erneuerung übernimmt der
systemd-Timer von certbot (`certbot.timer` bzw. `certbot-renew.timer`); ein Deploy-Hook prüft
nach jeder Erneuerung `nginx -t` und lädt nginx neu.

Zum Testen zuerst `-e tuxmail_tls_letsencrypt_staging=true` verwenden. Beim späteren Wechsel auf
Produktion einmalig `-e tuxmail_tls_letsencrypt_force_renewal=true` setzen.

### b) Vorhandenes nginx + eigenes Zertifikat (Firmen-CA, Wildcard)

```bash
ansible-playbook site.yml \
  -e tuxmail_domain=mail.firma.de \
  -e tuxmail_nginx_mode=existing -e tuxmail_nginx_existing_style=vhost \
  -e tuxmail_tls_mode=custom \
  -e tuxmail_tls_cert_path=/etc/ssl/firma/wildcard.crt \
  -e tuxmail_tls_key_path=/etc/ssl/firma/wildcard.key \
  -e tuxmail_tls_chain_path=/etc/ssl/firma/chain.pem
```

Im Modus `existing` wird nginx weder installiert noch wird eine vorhandene Datei verändert.
Es entsteht nur eine eigene Datei (`sites-available/tuxmail.conf` + Link in `sites-enabled/` bzw.
`conf.d/tuxmail.conf`). Existiert unter diesem Namen bereits eine fremde Datei, bricht das Playbook ab.
Vorher und nachher läuft `nginx -t`; schlägt die Prüfung fehl, wird der vorherige Zustand
wiederhergestellt und nginx **nicht** neu geladen.

Zertifikat und Schlüssel werden geprüft (Datei vorhanden, nicht abgelaufen, Schlüssel passt zum
Zertifikat via `openssl`). Ist `tuxmail_tls_chain_path` gesetzt, wird daraus eine Fullchain-Datei
erzeugt. Alternativ lassen sich die Dateien vom Steuerrechner kopieren
(`tuxmail_tls_copy_from_controller=true`, `tuxmail_tls_cert_src`, `tuxmail_tls_key_src` – der
Schlüssel darf mit `ansible-vault encrypt` verschlüsselt sein).

### c) Unterpfad neben Nextcloud (Snippet) bzw. TLS am vorgelagerten Proxy

```bash
ansible-playbook site.yml \
  -e tuxmail_domain=cloud.example.de \
  -e tuxmail_nginx_mode=existing -e tuxmail_nginx_existing_style=snippet \
  -e tuxmail_subpath=/tuxmail -e tuxmail_tls_mode=none
```

Es wird nur `/etc/nginx/snippets/tuxmail.conf` angelegt. Anschließend im **vorhandenen**
HTTPS-server-Block (z. B. dem von Nextcloud) eintragen:

```nginx
server {
    # ... bestehende Nextcloud-Konfiguration ...
    include /etc/nginx/snippets/tuxmail.conf;
}
```

und `nginx -t && systemctl reload nginx` ausführen. TuxMail ist dann unter
`https://cloud.example.de/tuxmail/` erreichbar. Hinweis: Das Snippet setzt in seinen
location-Blöcken eigene Header (inkl. HSTS) – nginx vererbt `add_header` aus dem server-Block
nicht in location-Blöcke, die selbst `add_header` verwenden.

**TLS am vorgelagerten Proxy** (z. B. HAProxy, Traefik, Load-Balancer): `tuxmail_nginx_mode=new`,
`tuxmail_tls_mode=none` und die Adresse des Proxys in `tuxmail_trusted_proxies_extra` eintragen.
nginx spricht dann nur HTTP (Port 80) und reicht `X-Forwarded-Proto: https` weiter.
`tuxmail_public_url` bleibt `https://…`.

### d) Ohne nginx (TuxMail terminiert TLS selbst)

```bash
ansible-playbook site.yml -e tuxmail_nginx_mode=none -e tuxmail_tls_mode=custom \
  -e tuxmail_listen=0.0.0.0:443 -e tuxmail_bind_privileged_port=true \
  -e tuxmail_tls_copy_from_controller=true \
  -e tuxmail_tls_cert_src=files/fullchain.pem -e tuxmail_tls_key_src=files/privkey.pem
```

`config.yaml` erhält dann `tls.mode: file`. Der Schlüssel muss für die Gruppe `tuxmail` lesbar sein.

### Nur einzelne Teile ausführen

```bash
ansible-playbook site.yml --tags tuxmail     # nur Binary/Konfiguration/Dienst
ansible-playbook site.yml --tags nginx,tls   # nur Reverse-Proxy und Zertifikat
ansible-playbook site.yml --tags pdf         # nur PDF-Rendering (pdf.env, Chromium/Gotenberg)
ansible-playbook site.yml --tags nginx,bridges  # Homeserver/Bridges (+ /_matrix im vHost)
ansible-playbook site.yml --check --diff     # Probelauf (Befehle werden dabei übersprungen)
```

## 4. Was das Playbook einrichtet

| Element | Details |
|---|---|
| Benutzer/Gruppe | `tuxmail` (System, Shell `/usr/sbin/nologin`, Home `/var/lib/tuxmail`) |
| `/opt/tuxmail/versions/<version>/` | ein Verzeichnis je installierter Version (root-eigen, 0755) |
| `/opt/tuxmail/current` | Symlink auf die aktive Version; `/usr/local/bin/tuxmail` zeigt darauf |
| `/etc/tuxmail/` | `root:tuxmail 0750`; `config.yaml` (0640), `oidc_client_secret` (0640) |
| `/var/lib/tuxmail/` | `tuxmail:tuxmail 0750` – Daten (siehe Abschnitt 7) |
| `/var/lib/tuxmail/backup/` | `tuxmail:tuxmail 0750` – öffentlicher Backup-Schlüssel, Ergebnis des letzten Laufs, während des Laufs die bereinigte Kopie (`.staging.*`, 0700); das Backup-Skript liest als root |
| `/var/log/tuxmail/` | `tuxmail:tuxmail 0750` – nur genutzt, wenn `tuxmail_log_file` gesetzt ist (sonst journald) |
| `/var/backups/tuxmail/` | `root 0700` – Sicherungen (Archive 0600) |
| `tuxmail.service` | gehärtet: `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `NoNewPrivileges`, leeres `CapabilityBoundingSet`, `RestrictAddressFamilies`, `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute`, `ReadWritePaths` nur Daten/Logs, `LimitNOFILE=65536`, `Restart=on-failure`, `ExecStartPre=… check-config` |
| `/etc/tuxmail/pdf.env` | optional (`tuxmail_pdf_renderer`), per `EnvironmentFile=-` eingelesen |
| `/etc/tuxmail/fcm-service-account.json` | optional (`tuxmail_fcm_service_account_src`), `root:tuxmail 0640` |
| `tuxmail-backup.timer` | täglich, nur Einstellungen, ohne Dienststopp, abschaltbar (`tuxmail_backup_timer_enabled`) |
| nginx | WebSocket `/api/events` (Upgrade, 1 h Timeout, ohne Puffer), alle übrigen Pfade (SPA, `/device`, API, Long-Polling) an TuxMail, `client_max_body_size` = `tuxmail_max_upload_mb`, HSTS, `nosniff`, `X-Frame-Options`, `Referrer-Policy`, CSP (`frame-ancestors`…), HTTP→HTTPS, ACME-Webroot |
| SELinux (RHEL) | Boolean `httpd_can_network_connect` (nginx → `127.0.0.1:8080`) |

**Konfigurationsprüfung:** `config.yaml` wird nur geschrieben, wenn `tuxmail check-config` der
*neuen* Version sie akzeptiert (`template … validate`). Anschließend wird sie zusätzlich als
Benutzer `tuxmail` geprüft (Leserechte). `check-config` lehnt unbekannte Schlüssel ab und prüft,
dass TLS-Dateien, OIDC-Secret-Datei und Logdatei vorhanden bzw. lesbar sind – das Playbook legt
diese Dateien deshalb vorher an.

**Health-Check:** Nach jedem (Neu-)Start fragt Ansible `GET /healthz` direkt am Listener ab
(bis zu 30 × 2 s). Schlägt das fehl, bricht der Lauf ab (siehe Fehlerbehebung).
Geprüft wird nur der Status 200; `/healthz` nennt die Version anonymen Clients nur mit
`tuxmail_security_expose_version: true` (sonst `{"status":"ok","product":"tuxmail"}`).

**Erster Administrator:** Beim ersten Lauf wird `tuxmail user add … --admin --password-stdin
--if-not-exists` ausgeführt. Das Passwort kommt aus `tuxmail_admin_password` (Vault) oder wird
generiert, unter `deploy/ansible/credentials/<host>/admin_password` (0600, per `.gitignore`
ausgeschlossen) gespeichert und **einmal** ausgegeben. Eine Markierung
`/etc/tuxmail/.admin-initialized` verhindert Wiederholungen. Nach der ersten Anmeldung Passwort
ändern und die Datei löschen. Für Admins ist TOTP standardmäßig Pflicht: Die Weboberfläche zeigt nach der ersten
Anmeldung einen Einrichtungsdialog (QR-Code, erster Code, Wiederherstellungscodes); später unter *Einstellungen →
Sicherheit & Datenschutz*.

**Unix-Socket statt TCP:** `tuxmail_listen=unix:/run/tuxmail/tuxmail.sock`. systemd legt
`/run/tuxmail` an (setgid, Gruppe `tuxmail-web`), nginx wird Mitglied von `tuxmail-web`.
Unter RHEL mit aktivem SELinux nicht empfohlen (nginx darf ohne eigenes Policy-Modul nicht
verbinden) – dort TCP verwenden.

## 5. Variablen (Auswahl)

Vollständige Liste mit Kommentaren: `roles/*/defaults/main.yml` und `inventory/group_vars/all.yml`.

| Variable | Standard | Bedeutung |
|---|---|---|
| `tuxmail_version` | `0.1.0` (aktuelles Release) | zu installierende Version (ohne `v`) |
| `tuxmail_domain` | `mail.example.org` | öffentlicher Hostname |
| `tuxmail_domain_aliases` | `[]` | weitere Hostnamen (server_name, Zertifikat) |
| `tuxmail_subpath` | `""` | Unterpfad, z. B. `/tuxmail` |
| `tuxmail_public_url` | `https://<domain><subpath>` | Basis-URL für Links, OIDC, WebAuthn |
| `tuxmail_listen` | `127.0.0.1:8080` | oder `unix:/run/tuxmail/tuxmail.sock` |
| `tuxmail_nginx_mode` | `new` | `new` \| `existing` \| `none` |
| `tuxmail_nginx_existing_style` | `vhost` | `vhost` \| `snippet` (nur bei `existing`) |
| `tuxmail_tls_mode` | `letsencrypt` | `letsencrypt` \| `custom` \| `none` |
| `tuxmail_tls_letsencrypt_email` | `""` | Pflicht bei Let's Encrypt |
| `tuxmail_tls_letsencrypt_staging` | `false` | Staging-CA zum Testen |
| `tuxmail_tls_cert_path` / `_key_path` | LE: `/etc/letsencrypt/live/<domain>/…`, sonst `/etc/ssl/tuxmail/<domain>.crt/.key` | Zertifikat/Schlüssel |
| `tuxmail_tls_chain_path` | `""` | Zwischenzertifikate (custom) |
| `tuxmail_tls_copy_from_controller` | `false` | Zertifikatsdateien vom Steuerrechner kopieren |
| `tuxmail_release_base_url` | GitHub-Release `v<version>/` | Quelle für Archiv, `checksums.txt`, `checksums.txt.asc` |
| `tuxmail_release_gpg_fingerprint` | `""` (**Pflicht**) | 40-stelliger Fingerabdruck des Release-Schlüssels; Leerzeichen erlaubt |
| `tuxmail_release_gpg_key_src` / `_key_url` | `""` / `<release>/tuxmail-release-key.asc` | öffentlicher Schlüssel (Datei auf dem Steuerrechner bevorzugt) |
| `tuxmail_allow_unsigned_release` | `false` | **Opt-out** der Signaturprüfung (nur Prüfsumme aus dem Release); Warnung im Lauf |
| `tuxmail_binary_src` | `""` | lokales Binary/`.tar.gz` auf dem Steuerrechner statt Download |
| `tuxmail_keep_versions` | `3` | installierte Versionen (für Rollback) |
| `tuxmail_max_upload_mb` | `50` | Upload-Limit (TuxMail und nginx) |
| `tuxmail_log_level` / `_format` / `_file` | `info` / `json` / `""` | Logging (`""` = journald) |
| `tuxmail_trusted_proxies_extra` | `[]` | zusätzliche vertrauenswürdige Proxys |
| `tuxmail_auth_registration` | `disabled` | `disabled` \| `invite` |
| `tuxmail_auth_totp_required_for_admins` | `true` | TOTP-Pflicht für Admins. Ausgenommen sind nur Sitzungen, die einen eigenen zweiten Faktor mitbringen: per OIDC-Anmeldung entstandene (der IdP ist dort der zweite Faktor) und Passkey-Sitzungen (Nutzerverifizierung per PIN/Biometrie); wer sich als OIDC-verknüpfter oder Passkey-Admin mit lokalem Passwort anmeldet, muss TOTP einrichten |
| `tuxmail_auth_passkeys_enabled` / `_rp_name` | `auto` / `TuxMail` | Passkeys (WebAuthn): `auto` = an bei https-`tuxmail_public_url` mit Hostnamen (oder `http://localhost`), sonst aus; `true`/`false` erzwingt. Ein Passkey-Login zählt als zwei Faktoren (erfüllt die TOTP-Pflicht der Admins), ein Passwort-Login nicht. **Ein Wechsel des Hostnamens von `tuxmail_public_url` macht alle registrierten Passkeys unbrauchbar** |
| `tuxmail_security_expose_version` | `false` | `true`: `/healthz` und `/api/auth/config` nennen anonymen Clients die exakte Version (sonst nur `"product":"tuxmail"`) |
| `tuxmail_security_hsts_include_subdomains` | `true` | `false`, wenn eine Subdomain kein HTTPS nutzt: ohne `includeSubDomains` im HSTS-Header der Anwendung (nie `preload`) |
| `tuxmail_security_headers` / `tuxmail_security_hsts` | `true` / `true` | Sicherheits-Header bzw. HSTS der Anwendung; nur abschalten, wenn ein eigener Proxy sie setzt |
| `tuxmail_auth_password_min_length` | `12` | Passwort-Mindestlänge |
| `tuxmail_auth_session_lifetime` | `12h` | Höchstdauer einer Web-Sitzung ab Anmeldung (längere Werte werden auf 12 h begrenzt) |
| `tuxmail_auth_session_idle_timeout` | `2h` | Web-Sitzung endet nach dieser Zeit ohne Anfrage (längere Werte werden auf 2 h begrenzt) |
| `tuxmail_login_rate_per_minute` | `10` | Login-Ratenbegrenzung |
| `tuxmail_security_allow_private_networks` | `false` | SSRF-Schutz: `true` erlaubt Benutzern Verbindungen in private Netze/zu Loopback (z. B. Mailserver im LAN) – gilt für alle Benutzer (`security.allow_private_networks`) |
| `tuxmail_security_allowed_networks_extra` | `[]` | immer erlaubte Ziele, IP/CIDR optional mit Port, z. B. `["192.168.10.5:993"]`; der lokale Matrix-Homeserver der Bridges wird automatisch ergänzt (`security.allowed_networks`) |
| `tuxmail_background_sync` | `true` | Hintergrund-Sync (Mail, Workflows, Abwesenheit, Push) für nicht angemeldete Benutzer mit aktivem Konto (`limits.background_sync`) |
| `tuxmail_oidc_enabled` | `false` | OIDC-Anmeldung |
| `tuxmail_oidc_issuer` / `_client_id` | `""` | OIDC-Provider |
| `tuxmail_oidc_client_secret` | `""` | **Vault!** → `/etc/tuxmail/oidc_client_secret` (0640) |
| `tuxmail_oidc_admin_group` / `_user_group` / `_groups_claim` | `tuxmail-admins` / `""` / `groups` | Gruppen-Zuordnung |
| `tuxmail_admin_create` | `true` | ersten Admin anlegen |
| `tuxmail_admin_username` / `_email` | `admin` / `admin@<domain>` | erster Admin |
| `tuxmail_admin_password` | `""` | **Vault!** – leer = generieren |
| `tuxmail_backup_before_upgrade` | `true` | Sicherung bei Versionswechsel |
| `tuxmail_backup_keep_upgrade` | `5` | aufbewahrte Upgrade-Sicherungen |
| `tuxmail_backup_timer_enabled` | `true` | tägliche Sicherung der Einstellungen |
| `tuxmail_backup_keep_scheduled` | `14` | aufbewahrte tägliche Sicherungen |
| `tuxmail_backup_compression` | `zstd` | `zstd` (tar.zst) \| `gzip` (tar.gz) |
| `tuxmail_backup_age_recipients` | `[]` | nur Bestand: öffentliche age-Schlüssel, einmalig in die Ablage übernommen; neu nicht nötig |
| `tuxmail_backup_allow_plaintext` | `false` | Sicherungen ohne Schlüssel zulassen (sonst endet das Skript mit Exit 3) |
| `tuxmail_service_environment` | `{}` | zusätzliche Umgebungsvariablen (z. B. `TUXMAIL_LOG_LEVEL`) |
| `tuxmail_nginx_strip_subpath` | `false` | Unterpfad vor dem Weiterreichen entfernen |
| `tuxmail_nginx_csp` | `""` | `""` = die CSP von TuxMail gilt allein; sonst ersetzt nginx sie durch genau diese (nie zwei CSP-Header) |
| `tuxmail_nginx_snippet_frame_options` | `""` | nur Snippet-Modus: `X-Frame-Options` (z. B. `SAMEORIGIN`) statt `DENY` von TuxMail; sonst bleibt `DENY` |
| `tuxmail_nginx_hsts_max_age` / `_include_subdomains` / `_preload` | `63072000` / `true` / `false` | HSTS von nginx; `includeSubDomains` erst, wenn alle Subdomains HTTPS können |
| `tuxmail_nginx_upstream_terminates_tls` | `false` | bei `tuxmail_tls_mode=none`: ein vorgelagerter Proxy beendet TLS → `X-Forwarded-Proto https`; sonst `$scheme` |
| `tuxmail_nginx_ocsp_stapling` / `_resolver` | custom: `true`, sonst `false` / DNS-Server des Hosts | OCSP-Stapling im TLS-vHost (Let's Encrypt hat keine OCSP-Responder mehr) |
| `tuxmail_nginx_ipv6` | automatisch | auch auf `[::]` lauschen |
| `tuxmail_nginx_disable_default_site` | `true` | Debian-Standardseite entfernen (nur `new`) |
| `tuxmail_firewall_enabled` | `false` | ufw/firewalld: SSH, 80, 443 freigeben |
| `tuxmail_fcm_service_account_src` | `""` | Firebase-Dienstkonto (JSON, gern Vault) → Android-Push über FCM |
| `tuxmail_pdf_renderer` | `none` | `none` \| `chromium` \| `gotenberg_container` |
| `tuxmail_pdf_chromium_path` | `""` (automatisch) | Chromium-Binary; Pflicht unter Ubuntu |
| `tuxmail_pdf_container_engine` / `tuxmail_gotenberg_image` | `podman` / `gotenberg:8.39.0` | nur `gotenberg_container` |
| `tuxmail_bridges_enabled` | `false` | Matrix-Homeserver + mautrix-Bridges (Bridge-Modus) |
| `tuxmail_bridges_runtime` | `native` | `native` (ohne Container) \| `container` |
| `tuxmail_bridges_container_engine` | `podman` | nur `container`: `podman` \| `docker` |
| `tuxmail_matrix_server` | `continuwuity` | `continuwuity` \| `synapse` (nur Debian/Ubuntu) |
| `tuxmail_matrix_server_name` | `<tuxmail_domain>` | Matrix-Servername (nachträglich nicht änderbar) |
| `tuxmail_matrix_port` | `8008` | Homeserver auf `127.0.0.1` |
| `tuxmail_matrix_federation` | `false` | Föderation (nginx blockiert sonst `/_matrix/federation`) |
| `tuxmail_matrix_nginx_proxy` / `_well_known` | `true` / `false` | `/_matrix` über nginx / `.well-known/matrix/*` |
| `tuxmail_matrix_public_base_url` | `https://<tuxmail_domain>` | öffentliche Homeserver-URL |
| `tuxmail_matrix_admin_create` / `_admin_user` / `_admin_password` | `true` / `admin` / `""` | Matrix-Admin-Konto (**Vault!**, leer = generieren) |
| `tuxmail_bridges_whatsapp_enabled` / `_signal_enabled` / `_gmessages_enabled` | `true` / `true` / `false` | Bridges |
| `tuxmail_bridges_db` | `sqlite` | `sqlite` \| `postgres` (Distributionspakete, peer-Anmeldung) |
| `tuxmail_bridges_permissions_extra` | `{}` | weitere mautrix-Rechte (`{"@anna:…": "user"}`) |
| `tuxmail_bridges_keep_versions` / `_backup_before_upgrade` | `3` / `true` | Update/Rollback der Binaries |
| `tuxmail_continuwuity_version` / `_sha256` / `_url` | `26.7.2` / **leer (Pflicht)** / Forgejo-Muster | Continuwuity-Binary |
| `tuxmail_mautrix_<bridge>_version` / `_sha256` | `0.2610.0` / vorbelegt (amd64, arm64) | mautrix-Binaries |
| `tuxmail_mautrix_config_overrides` | `{}` | eigene Werte je Bridge (rekursiv) |
| `tuxmail_synapse_version` / `_extra_config` | `""` / `{}` | Synapse-Paketversion / zusätzliche Einstellungen |
| `tuxmail_synapse_apt_suite` | `""` (Codename des Systems) | Suite im matrix.org-Repository. Das Playbook prüft vorab, ob sie existiert; bei neuen Ubuntu-/Debian-Versionen fehlt sie oft (dann Continuwuity verwenden) |
| `tuxmail_synapse_container_version` / `tuxmail_bridges_synapse_container_gid` | `1.164.2` / `991` | nur Container-Modus: feste Synapse-Version statt `:latest`; GID des Containers für die Appservice-Registrierungen (0640) |

**Geheimnisse mit Ansible Vault:**

```bash
ansible-vault encrypt_string --name tuxmail_admin_password 'S3hr-langes-Passwort'
ansible-vault encrypt_string --name tuxmail_oidc_client_secret 'abc123…'
# Ausgabe in inventory/group_vars/all.yml oder host_vars/<host>.yml einfügen, dann:
ansible-playbook site.yml --ask-vault-pass
```

## 6. Update und Rollback

**Update:** Version ändern und Playbook erneut ausführen:

```bash
ansible-playbook site.yml -e tuxmail_version=1.1.0 --tags tuxmail
```

Ablauf: neues Archiv herunterladen und per `checksums.txt` (SHA-256) prüfen → nach
`/opt/tuxmail/versions/1.1.0/` entpacken → `config.yaml` mit dem **neuen** Binary prüfen →
Dienst stoppen → das ganze `/var/lib/tuxmail` sichern, auch mit Mails
(`/var/backups/tuxmail/tuxmail-pre-upgrade-<datum>-<alt>-to-<neu>.tar.zst.age`; ohne Backup-Schlüssel nur eine
Warnung und keine Sicherung) → Symlink `current`
umstellen → Dienst starten (Datenbank-Migration läuft automatisch) → Health-Check.
Ältere Versionen über `tuxmail_keep_versions` hinaus werden gelöscht.

**Rollback des Programms** (wenn das alte Versionsverzeichnis noch vorhanden ist):

```bash
ansible-playbook site.yml -e tuxmail_version=1.0.0 --tags tuxmail
# oder manuell:
sudo ln -sfn /opt/tuxmail/versions/1.0.0 /opt/tuxmail/current && sudo systemctl restart tuxmail
```

**Rollback der Daten:** Datenbank-Migrationen sind nicht zwingend rückwärtskompatibel. Wenn die
ältere Version die migrierten Daten nicht lesen kann, die Sicherung zurückspielen:

```bash
sudo systemctl stop tuxmail
sudo mv /var/lib/tuxmail /var/lib/tuxmail.defekt
# schluessel.txt enthält den privaten Schlüssel aus dem Tresor (AGE-SECRET-KEY-…)
age -d -i schluessel.txt /var/backups/tuxmail/tuxmail-pre-upgrade-<…>.tar.zst.age | sudo tar --zstd -xpf - -C /var/lib
# ausdrücklich unverschlüsselte Sicherung: sudo tar --zstd -xpf <datei>.tar.zst -C /var/lib   (.tar.gz: -xzpf)
sudo ln -sfn /opt/tuxmail/versions/1.0.0 /opt/tuxmail/current
sudo systemctl start tuxmail && curl -fsS http://127.0.0.1:8080/healthz
```

Manuelle Sicherung jederzeit: `sudo /usr/local/sbin/tuxmail-backup manual 10` (nur Einstellungen, Dienst läuft weiter)
oder `sudo /usr/local/sbin/tuxmail-backup --full --stop manual 10` (alles, Dienst steht).

## 7. Daten und Sicherungen – wichtig

`/var/lib/tuxmail` enthält u. a.:

| Datei | Bedeutung |
|---|---|
| `server.db` | Server-Datenbank (Benutzer, Sitzungen, Einstellungen) |
| `secretbox.json` | **Schlüsselring** (0600), Format `bx1`, siehe „Schlüssel rotieren“. Ohne ihn sind gespeicherte Benutzer-Geheimnisse (Mail-Passwörter, Tokens) und TOTP-Geheimnisse verloren. |
| `master.key` | **Älterer Hauptschlüssel** (0600), für Werte im alten Format und bis `tuxmail keys retire-legacy`. Ohne ihn sind gespeicherte Benutzer-Geheimnisse (Mail-Passwörter, Tokens) und TOTP-Daten unlesbar. TuxMail startet nicht, wenn die Datei für Gruppe/Andere lesbar ist. |
| `vapid.json` | Web-Push-Schlüssel (0600). Bei Verlust funktionieren bestehende Push-Abonnements nicht mehr – bei Neuinstallation übernehmen. |
| `users/<id>/` | je Benutzer `tuxmail.db` und `secrets.vault` |

Die Sicherungen enthalten `secretbox.json` und `master.key` und werden mit [age](https://age-encryption.org) verschlüsselt. Das Skript
erzeugt sie mit `umask 077` (Archive 0600, Verzeichnis 0700). Beim Zurückspielen Rechte erhalten (`tar -p` als root).

**Was die tägliche Sicherung enthält.** TuxMail ist ein Mailclient, die Mails liegen beim Mailserver. Darum sichert
der Job (`tuxmail backup-settings`) nur, was sich nicht neu holen lässt:

| Enthalten | Nicht enthalten |
|---|---|
| `server.db` (Benutzer, Gruppen, Sitzungen, Audit-Log, Team-Postfächer und -Rechte), `secretbox.json`, `master.key`, `vapid.json`, öffentlicher Backup-Schlüssel | Mails samt Metadaten und Anhänge, gesamter Blob-Speicher außer den Signaturbildern |
| je Benutzer und Team-Postfach `secrets.vault` (Passwörter, Tokens, private Schlüssel) | Chat-Verläufe und Messenger-Sitzungen (`plugins/`): Messenger-Konten müssen neu gekoppelt werden |
| aus `tuxmail.db`: Konten, Identitäten, Ordnerkonfiguration, Regeln und Workflows, Tags, Projekte ohne Zuordnungen, Suchordner, Signaturen, Textbausteine, S/MIME-Zertifikate, Abwesenheit, Einstellungen | Kalendereinträge, Kontakte, Such-Index, Caches, Warteschlangen, Suchverlauf, geplante Nachrichten, angelernter Spam- und Kategorienfilter, Team-Kommentare und -Entwürfe |

Welche Tabelle wohin gehört, steht an einer Stelle: `internal/backup/settings.go`. Eine neue Tabelle lässt einen Test
scheitern und stoppt den Job, bis sie eingestuft ist. Nicht enthalten sind außerdem `/etc/tuxmail/config.yaml` (kommt
aus dem Playbook), `imageproxy.key` (entsteht neu) und eigene Dateien unter `locales/` und `dictionaries/`.

**Die Sicherung vor einem Versionswechsel bleibt eine Vollsicherung** (`--full`, mit Dienststopp, auch mit Mails), denn
sie ist der Rückweg, wenn eine Migration scheitert. Sie liegt wie die täglichen Archive in `/var/backups/tuxmail/`.

**Zurückspielen:**

```bash
sudo systemctl stop tuxmail
sudo mv /var/lib/tuxmail /var/lib/tuxmail.alt
# schluessel.txt enthält den privaten Schlüssel aus dem Tresor (AGE-SECRET-KEY-…)
age -d -i schluessel.txt /var/backups/tuxmail/tuxmail-scheduled-<…>.tar.zst.age | sudo tar --zstd -xpf - -C /var/lib
sudo systemctl start tuxmail && curl -fsS http://127.0.0.1:8080/healthz
```

Beim Start laufen die Migrationen. Benutzer melden sich an wie zuvor, die Konten verbinden sich und holen die Mails neu
vom Mailserver, Kalender und Kontakte von CalDAV/CardDAV. Das dauert je nach Postfach.

**Nach dem Zurückspielen sind weg:** POP3-Mails, die auf dem Server gelöscht wurden, lokale Ordner, importierte Mails,
Messenger-Verläufe, lokale Kalender und Kontakte, Markierungen und Zuordnungen, die nur in TuxMail stehen (lokale Tags
an Mails, Projektzuordnungen). Wer das braucht, nutzt die **Benutzer-Sicherung** (`.txmb`, `tuxmail backup --user NAME`
oder in den Einstellungen). Sie ist davon nicht betroffen.

**Backup-Schlüssel – nach dem Rollout im Frontend erzeugen:** Einstellungen → Administration → Sicherung →
„Schlüssel erzeugen“ (nur Administratoren, mit Passwort bzw. zweitem Faktor bestätigen).

- Der private Schlüssel wird genau einmal angezeigt. Lege ihn im Tresor ab: Ohne ihn lässt sich keine Sicherung
  zurückspielen. Am Server bleibt nur der öffentliche Schlüssel (`/var/lib/tuxmail/backup/recipients.txt`).
- Er gilt erst, wenn du „Im Tresor gesichert“ bestätigt hast. Bis dahin wartet der öffentliche Teil in
  `recipients.txt.pending`, und der Job nutzt weiter den alten Schlüssel; beim ersten Schlüssel sichert er noch nicht.
- Statt dessen kannst du dort einen eigenen öffentlichen Schlüssel (`age1…`) eintragen, etwa aus `age-keygen`.
- Ersetzen geht jederzeit und landet im Audit-Log. Ältere Sicherungen brauchen weiter den alten privaten Schlüssel.
- **Bestand:** Steht `tuxmail_backup_age_recipients` im Inventory, übernimmt das Playbook die Werte einmalig in die
  Datei. Danach gilt allein die Oberfläche, die Variable kann entfallen.
- Die Seite zeigt auch das Ergebnis des letzten Laufs. Die tägliche Sicherung ist standardmäßig an; ohne Schlüssel
  scheitert der Job mit Fehlerstatus, Klartext entsteht nie. Abschalten: `tuxmail_backup_timer_enabled: false`.
- Installation ohne Ansible: `deploy/systemd/tuxmail-backup.sh` nutzt dieselbe Datei und braucht das Paket `age`.

**Kein Stillstand durch die tägliche Sicherung:** Der Timer ist standardmäßig an und läuft täglich zwischen 03:15 und
03:30 Uhr Serverzeit (`tuxmail_backup_timer_oncalendar`, plus bis zu 15 Minuten Zufall); war der Server aus, läuft er
beim nächsten Start nach. Der Dienst läuft dabei weiter und darf schreiben, denn der Job arbeitet mit konsistenten
SQLite-Kopien (`VACUUM INTO`). Abschalten: `tuxmail_backup_timer_enabled: false`. Die Grenzen:

- **Platz und Dauer:** Die Kopie jeder `tuxmail.db` entsteht zuerst vollständig in `/var/lib/tuxmail/backup/.staging.*`,
  dort braucht der Lauf kurz so viel Platz wie die größte Datenbank. Gemessen ohne Last: 19 Sekunden für eine 540-MB-
  Datenbank mit 50.000 Mails, das Archiv war 22 KB groß. Unverschlüsselt liegen die bereinigten Dateien und kurz auch die
  ungefilterte Kopie im Verzeichnis (0700), sie werden auch im Fehlerfall entfernt, nach einem harten Abbruch beim nächsten Lauf.
- **Zeitpunkt:** Die Datenbanken werden nacheinander kopiert. Jede für sich ist konsistent, zwischen `server.db` und
  einer `tuxmail.db` können Sekunden liegen.

Nur die Sicherung vor einem Versionswechsel und `--stop` halten den Dienst an, für die ganze Dauer: etwa 5 bis 15
Sekunden je GB unter `/var/lib/tuxmail`, gemessen ohne Last, ohne Obergrenze.

**Zustand für das Monitoring:** Das öffentliche `GET /healthz` sagt nichts über Sicherungen, sonst könnte jeder lesen,
dass ein Server keine hat. `GET /api/admin/backup` (nur Administratoren) meldet `health`: `ok` (letzter Lauf erfolgreich,
verschlüsselt, höchstens 48 Stunden alt), `warn` (noch kein Lauf, zu alt oder unverschlüsselt) oder `fail` (kein gültiger
Schlüssel, letzter Lauf gescheitert). Die Oberfläche zeigt die Einzelheiten.

**Ohne Schlüssel keine Klartext-Sicherung (Re-Audit ND-14):** `tuxmail-backup` endet mit Exit 3 und ohne Datei, bevor
der Dienst angehalten wird. Das Playbook bricht deshalb nicht ab: Es warnt, auch wenn dadurch die Sicherung vor einem
Versionswechsel entfällt, und der Rollout läuft weiter. Wer bewusst unverschlüsselt sichert (Archive sind nur für root
lesbar), setzt `tuxmail_backup_allow_plaintext: true` bzw. startet das Skript manuell mit
`TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1`. Eine beschädigte Schlüsseldatei stoppt den Lauf in jedem Fall.

## Schlüssel rotieren

Geheimnisse (Daten-Schlüssel der Benutzer, TOTP, Team-Tresore) liegen im Format `bx1` unter dem Schlüsselring
`secretbox.json`. Werte im alten Format bleiben lesbar, solange `master.key` da ist. Alle Befehle als Dienstbenutzer, Dienst darf laufen:

```bash
tuxmail keys status         # Werte je Schlüssel; „waiting for keys rewrap“ soll 0 werden
tuxmail keys rewrap         # stellt alles auf den Primärschlüssel um; wiederholbar, Abbruch jederzeit möglich
tuxmail keys rotate         # neuer Primärschlüssel; danach secretbox.json sichern, dann keys rewrap
tuxmail keys retire --kid K # entfernt einen alten Schlüssel, verweigert das, solange ein Wert ihn nutzt
tuxmail keys retire-legacy  # benennt master.key um, verweigert das, solange ein Wert es nutzt
```

`tuxmail keys rollback --yes` (Dienst gestoppt, vorher sichern) bringt Daten und Schema zurück auf die Vorversion, solange
`master.key` noch vorhanden ist. Danach sofort die Vorversion starten.

## 8. Benutzerverwaltung

Die Kommandozeile läuft als Dienstbenutzer (damit neue Dateien `tuxmail` gehören):

```bash
TM="sudo runuser -u tuxmail -- tuxmail"
CFG="--config /etc/tuxmail/config.yaml"

echo 'Passwort' | $TM user add $CFG --username anna --email anna@example.de --password-stdin
echo 'Passwort' | $TM user add $CFG --username chef --email chef@example.de --admin --password-stdin --if-not-exists
$TM user list $CFG                       # mit --json maschinenlesbar
$TM user disable $CFG anna
$TM user enable $CFG anna
echo 'NeuesPasswort' | $TM user reset-password $CFG anna --password-stdin   # ohne --password-stdin wird ein Passwort erzeugt und ausgegeben
$TM user reset-2fa $CFG anna
$TM user set-role $CFG anna admin           # admin | user | readonly
$TM user delete $CFG anna --yes            # --keep-data behält die Benutzerdaten
```

Exit-Codes: `0` ok, `1` Fehler, `2` falscher Aufruf. Die genauen Optionen zeigt
`tuxmail user --help`.

**In der Weboberfläche** (*Einstellungen → Administration*, nur für Administratoren, nicht in der Desktop- oder
Android-App) gibt es dasselbe und mehr: Benutzer anlegen, bearbeiten, deaktivieren, Passwort und Zwei-Faktor
zurücksetzen, Sitzungen beenden, löschen und **gesperrte Konten entsperren**; außerdem Einladungen (benötigen
`auth.registration: invite`), Gruppen und das Audit-Log. Ein Konto sperrt sich nach 5 falschen Anmeldungen je
Netzwerk (1 Minute, bis zu 1 Stunde); bei Angriffen aus vielen Netzwerken können sich nur noch bekannte Netze
anmelden. „Entsperren“ hebt beides auf. Gemeinsame (Team-)Postfächer verwalten Administratoren ebenfalls in der
Weboberfläche. Bei OIDC werden Benutzer bei der ersten Anmeldung
angelegt; Admin-Rechte über `tuxmail_oidc_admin_group`.

Umgebungsvariablen `TUXMAIL_LISTEN`, `TUXMAIL_DATA_DIR`, `TUXMAIL_PUBLIC_URL`,
`TUXMAIL_LOG_LEVEL` überschreiben einzelne Werte aus `config.yaml` (z. B. über
`tuxmail_service_environment`).

## 9. Optionale Rollen

**Firewall** (`tuxmail_firewall_enabled: true`): ufw (Debian/Ubuntu) bzw. firewalld (RHEL). Gibt
immer zuerst SSH (`tuxmail_firewall_ssh_port`) frei, dann 80/443 und `tuxmail_firewall_extra_ports`.
ufw wird mit „eingehend verweigern“ aktiviert (`tuxmail_firewall_ufw_enable`).

**PDF-Rendering** (`tuxmail_pdf_renderer`, Rolle `tuxmail_pdf`, KONZEPT 8.11) und
**Matrix-Homeserver + Bridges** (`tuxmail_bridges_enabled`, Rolle `tuxmail_bridges`,
KONZEPT 11.3): nativ, siehe Abschnitt 2. Rechtliche Hinweise zu inoffiziellen
Messenger-Anbindungen: KONZEPT 18.1.

**Android-Push über FCM** (Play-Store-Variante der App, KONZEPT 15.9): Den JSON-Schlüssel eines
Firebase-Dienstkontos (Rolle „Firebase Cloud Messaging API Admin“, Firebase-Projekt der App) auf
dem Steuerrechner ablegen – gern verschlüsselt mit `ansible-vault encrypt files/fcm.json` – und
`tuxmail_fcm_service_account_src: files/fcm.json` setzen. Ansible kopiert ihn nach
`/etc/tuxmail/fcm-service-account.json` (`root:tuxmail 0640`, Pfad: `tuxmail_fcm_service_account_file`)
und trägt nur dann `push.fcm.service_account_file` in `config.yaml` ein. Ohne die Variable bleibt
FCM aus; Web Push (VAPID) funktioniert immer. Die Umgebungsvariable `TUXMAIL_FCM_SERVICE_ACCOUNT`
(z. B. über `tuxmail_service_environment`) hat Vorrang vor `config.yaml`.

**Container-Modus (Alternative, nicht empfohlen):** `tuxmail_bridges_runtime: container` startet
Homeserver und Bridges wie früher per podman-compose bzw. `docker compose`
(`tuxmail_bridges_container_engine`) unter `/opt/tuxmail-bridges`, Ports nur auf `127.0.0.1`.
Images: `tuxmail_bridges_*_image` (feste Versionen setzen). Bei Synapse werden die Bridges per
`app_service_config_files` registriert, bei Continuwuity gibt das Playbook die Schritte für den
Admin-Raum aus; das Matrix-Konto wird hier nicht automatisch angelegt (Befehl wird ausgegeben).
Die alten Werte `tuxmail_bridges_runtime: podman|docker` werden noch als Container-Modus erkannt
(mit Warnung). Gotenberg ist nicht mehr Teil des Compose-Projekts, sondern
`tuxmail_pdf_renderer: gotenberg_container`.

### Sicherheits-Header und Weitergabe (Re-Audit ND-12)

TuxMail sendet selbst einen vollständigen Satz von Sicherheits-Headern (`nosniff`, `X-Frame-Options: DENY`,
`Referrer-Policy`, COOP/CORP, `Permissions-Policy`, strikte CSP mit `frame-ancestors 'none'`). Die nginx-Vorlagen
reichen sie **unverändert** durch und ergänzen nur HSTS; früher wurde `X-Frame-Options` auf `SAMEORIGIN`
heruntergesetzt und eine zweite CSP hinzugefügt. Wer im Snippet-Modus `SAMEORIGIN` braucht, setzt
`tuxmail_nginx_snippet_frame_options`. `X-Forwarded-Proto` ist das Schema der nginx-Verbindung (`$scheme`);
steht nginx hinter einem TLS-beendenden Proxy (`tuxmail_tls_mode: none`), muss das mit
`tuxmail_nginx_upstream_terminates_tls: true` bestätigt werden (Änderung gegenüber früher: dort wurde `https`
immer gesendet). Wird TLS vom verwalteten vHost beendet, kann OCSP-Stapling aktiviert werden
(`tuxmail_nginx_ocsp_stapling`, Standard nur bei eigenem Zertifikat).

**Secrets im Play-Protokoll:** Generierte Passwörter (TuxMail-Admin, Matrix-Admin) werden nie ausgegeben; sie
liegen nur in `credentials/<host>/` auf dem Steuerrechner (Datei 0600, Verzeichnis 0700) und das Playbook nennt
den Pfad. Das Synapse-Admin-Konto wird mit `register_new_matrix_user --password-file` angelegt (kurzlebige
root-Datei in `/run`), nicht mit dem Passwort in der Kommandozeile.

## 10. Fehlerbehebung

| Problem | Prüfen / Lösung |
|---|---|
| Health-Check schlägt fehl | `journalctl -u tuxmail -n 100`; `sudo runuser -u tuxmail -- tuxmail check-config --config /etc/tuxmail/config.yaml`; `curl -v http://127.0.0.1:8080/healthz`. Bei Problemen nach einem Update: Rollback (Abschnitt 6). |
| `check-config` lehnt Konfiguration ab | Fehlermeldung im Ansible-Output (validate); unbekannte Schlüssel, fehlende TLS-/Secret-/Logdatei, Rechte. |
| Dienst startet nicht: „secretbox.json is missing“ oder „master.key is missing“ | Datei aus der Sicherung zurückholen; der Dienst legt absichtlich keinen neuen Schlüssel an, weil die Daten sonst unlesbar blieben |
| Dienst startet nicht: „master.key permissions“ | `sudo chmod 600 /var/lib/tuxmail/master.key && sudo chown tuxmail:tuxmail /var/lib/tuxmail/master.key` |
| `nginx -t` schlägt fehl (existing) | Der vorherige Zustand wurde wiederhergestellt. Häufig: doppelter `server_name`, Port-/`default_server`-Konflikte. Fehlermeldung mit `sudo nginx -t` ansehen. |
| „existiert bereits und wurde nicht von diesem Playbook angelegt“ | Eine fremde Datei hat denselben Namen: `tuxmail_nginx_vhost_name` bzw. `tuxmail_nginx_snippet_path` ändern. |
| certbot scheitert | DNS zeigt auf den Server? Port 80 offen (Firewall, Cloud-Security-Group)? `curl http://<domain>/.well-known/acme-challenge/test` muss 404 von nginx liefern. Logs: `/var/log/letsencrypt/letsencrypt.log`. Rate-Limits: mit `tuxmail_tls_letsencrypt_staging=true` testen. |
| 502 Bad Gateway | TuxMail läuft nicht (`systemctl status tuxmail`) oder RHEL/SELinux: `getsebool httpd_can_network_connect`, `ausearch -m avc -ts recent`. Unix-Socket: nginx Mitglied von `tuxmail-web`? |
| WebSocket/Live-Updates fehlen | Vorgelagerter Proxy muss `Upgrade`/`Connection` durchreichen; Timeouts ≥ `tuxmail_nginx_websocket_timeout`. |
| Konto im LAN/auf dem Server nicht erreichbar: „destination address not allowed in server mode“ | SSRF-Schutz: Ziel in `tuxmail_security_allowed_networks_extra` eintragen (z. B. `"192.168.10.5:993"`) oder `tuxmail_security_allow_private_networks=true`. |
| 413 Request Entity Too Large | `tuxmail_max_upload_mb` erhöhen (wirkt auf TuxMail und nginx); vorgelagerte Proxys ebenfalls anpassen. |
| Anmeldung klappt nicht hinter Proxy / Cookies fehlen | `tuxmail_public_url` muss die tatsächliche HTTPS-URL sein (Cookies mit `__Host-`-Präfix und `Secure`); Proxy-Adresse in `tuxmail_trusted_proxies_extra`. |
| Doppelte HSTS-Header | nginx blendet den HSTS-Header von TuxMail aus (`proxy_hide_header`), wenn es selbst HSTS sendet; ohne TLS in nginx bleibt der von TuxMail erhalten. |
| `nginx -t`: „Address family not supported“ | Host ohne IPv6: `tuxmail_nginx_ipv6=false` (Standard erkennt das automatisch). |
| Download/Prüfsumme schlägt fehl | Release-URL erreichbar? `tuxmail_release_base_url` anpassen oder `tuxmail_binary_src` verwenden. |
| „keine SHA-256-Prüfsumme hinterlegt“ (Continuwuity) | `tuxmail_continuwuity_sha256` für die Architektur setzen (Abschnitt 2). 404 beim Download: Asset-Name auf der Forgejo-Release-Seite prüfen und `tuxmail_continuwuity_asset` anpassen. |
| `… --version` schlägt fehl (Bridge/Homeserver) | Falsche Architektur oder fehlende Laufzeitbibliothek: `ldd /opt/<dienst>/current/<dienst>`; Continuwuity braucht ggf. `liburing`. |
| Homeserver antwortet nicht | `journalctl -u continuwuity -n 100` bzw. `-u matrix-synapse`; `curl -s http://127.0.0.1:8008/_matrix/client/versions`. Port belegt? `ss -ltnp 'sport = :8008'`. |
| Bridge startet immer wieder neu | `journalctl -u mautrix-whatsapp -n 100`. „homeserver unreachable“: Homeserver läuft? „unknown token“/M_UNKNOWN_TOKEN: Registrierung fehlt beim Homeserver (Continuwuity: `!admin appservices list` im Admin-Raum, ggf. Markierung `/etc/continuwuity/appservices/<bridge>.registered` löschen und Playbook erneut ausführen; Synapse: `app_service_config_files` in `conf.d/tuxmail.yaml`). |
| Automatische Registrierung bei Continuwuity fehlgeschlagen | Admin-Raum `#admins:<server_name>` vorhanden und das Konto Mitglied? Token in `/etc/continuwuity/admin.token` gültig? Sonst Schritte aus der Playbook-Ausgabe manuell ausführen. |
| `login` beim Bridge-Bot: „You don't have permission“ | Konto nicht in `tuxmail_bridges_permissions` (Server-Domain = `user`); andere Server über `tuxmail_bridges_permissions_extra`. |
| Matrix-Client erreicht den Server nicht | `curl https://<domain>/_matrix/client/versions` muss JSON liefern (nginx-Weiterleitung, `tuxmail_matrix_nginx_proxy`). Nach dem Einschalten der Bridges `--tags nginx,bridges` ausführen, damit der vHost die `/_matrix`-Pfade erhält. |
| PostgreSQL: „Peer authentication failed“ | Dienst läuft als `mautrix-<name>` bzw. `matrix-synapse`? `pg_hba.conf` muss `local all all peer` enthalten (Distributionsstandard). |
| Chromium startet nicht (PDF) | `TUXMAIL_CHROMIUM_PATH` in `/etc/tuxmail/pdf.env` prüfen; `tuxmail_service_chromium_compat` muss `true` sein (lockert MemoryDenyWriteExecute/Namensräume); Ubuntu: Snap-Chromium funktioniert nicht, `.deb` verwenden. `journalctl -u tuxmail`. |
| Gotenberg (Container) nicht erreichbar | `systemctl status tuxmail-gotenberg`, `curl http://127.0.0.1:3000/health`; Image-Tag vorhanden? |

Ansible ausführlicher: `ansible-playbook site.yml -vv`. Probelauf: `--check --diff`.

## 11. Manuelle Installation (ohne Ansible)

Die Dateien in `systemd/` und `nginx/` sind aus den Ansible-Templates erzeugte Beispiele
(TCP-Listener, Let's Encrypt, Domain `mail.example.org`):

```bash
sudo useradd --system --home-dir /var/lib/tuxmail --shell /usr/sbin/nologin tuxmail
sudo install -d -o root -g root -m 0755 /opt/tuxmail/versions/1.0.0
sudo tar -xzf tuxmail_1.0.0_linux_amd64.tar.gz -C /opt/tuxmail/versions/1.0.0
sudo ln -sfn /opt/tuxmail/versions/1.0.0 /opt/tuxmail/current
sudo install -d -o root -g tuxmail -m 0750 /etc/tuxmail
sudo install -d -o tuxmail -g tuxmail -m 0750 /var/lib/tuxmail /var/log/tuxmail
sudo install -o root -g tuxmail -m 0640 systemd/config.example.yaml /etc/tuxmail/config.yaml
sudo install -m 0644 systemd/tuxmail.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now tuxmail
# nginx: nginx/tuxmail.conf nach sites-available/ bzw. conf.d/, Domain/Zertifikat anpassen, nginx -t

# Tägliche Sicherung (Skript und Timer; braucht die Pakete age, zstd und tar)
sudo apt install age zstd                    # RHEL-Familie: EPEL aktivieren, dann dnf install age zstd
sudo install -d -o tuxmail -g tuxmail -m 0750 /var/lib/tuxmail/backup
sudo install -d -o root -g root -m 0700 /var/backups/tuxmail
sudo install -o root -g root -m 0750 systemd/tuxmail-backup.sh /usr/local/sbin/tuxmail-backup
sudo install -m 0644 systemd/tuxmail-backup.service systemd/tuxmail-backup.timer /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now tuxmail-backup.timer
```

Danach den Backup-Schlüssel in der Oberfläche erzeugen (Einstellungen → Administration → Sicherung). Inhalt, Zurückspielen und
Abschalten stehen in Abschnitt 7.

## 12. Docker / Podman (Alternative)

Container sind für TuxMail **nicht nötig** – der empfohlene Weg ist die native Installation
(Abschnitt 2). Wer trotzdem Container betreiben möchte: Für jede Version gibt es ein Multi-Arch-Image (linux/amd64, linux/arm64) im GitHub Container Registry:

| Tag | Inhalt |
|---|---|
| `ghcr.io/midas1989/tuxmail:<version>` (z. B. `:0.1.0`) | feste Version (empfohlen) |
| `ghcr.io/midas1989/tuxmail:latest` | neueste veröffentlichte Version |
| `ghcr.io/midas1989/tuxmail:edge` | Entwicklungsstand (jeder Push auf `main`/`claude/**`), nicht für den Produktivbetrieb |

Eigenschaften des Images (`build/docker/Dockerfile`):

- Alpine-Basis, statisches Go-Binary `/usr/local/bin/tuxmail` mit eingebettetem Web-Frontend
- läuft als **Nicht-root**-Benutzer `tuxmail` (UID/GID 10001), funktioniert mit `read_only: true` und `cap_drop: [ALL]`
- Daten im Volume **`/data`** (Datenbanken, `master.key`, VAPID-Schlüssel, Benutzerdaten) – **dieses Volume sichern**
- Port **8080** (HTTP), Healthcheck auf `/healthz`
- Start: `tuxmail serve --config /etc/tuxmail/config.yaml`; die mitgelieferte Konfiguration
  (`build/docker/config.yaml`) wird über Umgebungsvariablen angepasst:

| Variable | Standard | Bedeutung |
|---|---|---|
| `TUXMAIL_PUBLIC_URL` | `http://localhost:8080` | externe Adresse, z. B. `https://mail.example.org` (Cookies, Links, Passkeys) |
| `TUXMAIL_LISTEN` | `0.0.0.0:8080` | Adresse im Container |
| `TUXMAIL_DATA_DIR` | `/data` | Datenverzeichnis |
| `TUXMAIL_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Weitere Einstellungen (OIDC, Limits, `trusted_proxies`) über eine eigene Datei:
`-v ./config.yaml:/etc/tuxmail/config.yaml:ro` (alle Schlüssel: `systemd/config.example.yaml`).
Die Standardkonfiguration vertraut `X-Forwarded-*`-Headern aus den privaten Netzen (Docker-Netzwerke);
ist der Container aus solchen Netzen auch direkt erreichbar, `trusted_proxies` einschränken.

**Schnellstart mit Compose**

```bash
cp deploy/docker/docker-compose.example.yml docker-compose.yml
$EDITOR docker-compose.yml                       # TUXMAIL_PUBLIC_URL, Image-Tag
docker compose up -d
docker compose exec tuxmail tuxmail user add --username admin --admin   # Passwort wird ausgegeben
docker compose ps                                # Status "healthy"
```

Ohne Compose:

```bash
docker run -d --name tuxmail --restart unless-stopped \
  -p 127.0.0.1:8080:8080 -v tuxmail-data:/data \
  -e TUXMAIL_PUBLIC_URL=https://mail.example.org \
  ghcr.io/midas1989/tuxmail:0.1.0
```

TLS übernimmt ein Reverse-Proxy vor dem Container (z. B. `nginx/tuxmail.conf` mit
`proxy_pass http://127.0.0.1:8080`, Caddy oder Traefik). **Update:** Tag ändern, `docker compose pull &&
docker compose up -d` – Datenbank-Migrationen laufen beim Start; vorher das Volume sichern
(`docker run --rm -v tuxmail-data:/data -v "$PWD":/backup alpine tar czf /backup/tuxmail-data.tgz -C /data .`).

Image selbst bauen (aus dem Repository-Wurzelverzeichnis):

```bash
docker build -f build/docker/Dockerfile --build-arg VERSION=$(build/version.sh) -t tuxmail:local .
docker buildx build -f build/docker/Dockerfile --platform linux/amd64,linux/arm64 -t tuxmail:local .
```

