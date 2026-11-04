<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# TuxMail – Server-Installation (Linux)

Dieses Verzeichnis enthält alles, um TuxMail im **Server-/Web-Modus** (`tuxmail serve`) auf einem
Linux-Server zu betreiben (siehe [KONZEPT.md, Kap. 4.5, 15.5, 15.6](../docs/KONZEPT.md)).

| Pfad | Inhalt |
|---|---|
| `ansible/` | Ansible-Projekt (empfohlen): Installation, Updates, nginx, TLS, Backups, Firewall, optionale Bridges |
| `systemd/` | Beispiel-Units und -Konfiguration für eine manuelle Installation (aus den Ansible-Templates erzeugt) |
| `nginx/` | Beispiel-vHost (`tuxmail.conf`) und location-Snippet (`tuxmail-snippet.conf`) |
| `docker/` | `docker-compose.example.yml` für das Container-Image `ghcr.io/midas1989/tuxmail` (Abschnitt 11) |

---

## 1. Voraussetzungen

**Zielsystem** (frisch oder bestehend):

- Debian 12/13, Ubuntu 22.04/24.04 oder RHEL/Rocky/AlmaLinux 9, Architektur amd64 oder arm64
- systemd, SSH-Zugang mit `sudo`-Rechten (oder root)
- Für Let's Encrypt: DNS-Eintrag der Domain zeigt auf den Server, Port 80 und 443 aus dem Internet erreichbar
- Ausgehender HTTPS-Zugriff auf die Release-URL (sonst `tuxmail_binary_src` verwenden)

**Steuerrechner** (von dem aus Ansible läuft):

- `ansible-core` ≥ 2.15 (getestet mit 2.21/2.22) und Python 3
- Collections: `ansible-galaxy collection install -r deploy/ansible/requirements.yml`
  (`ansible.posix`, `community.general`; im Paket `ansible` bereits enthalten)

```bash
cd deploy/ansible
cp inventory/hosts.example.ini inventory/hosts.ini     # Server eintragen
$EDITOR inventory/group_vars/all.yml                   # Domain, Version, Modi, E-Mail
ansible-galaxy collection install -r requirements.yml
```

## 2. Schnellstart

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
ansible-playbook site.yml --check --diff     # Probelauf (Befehle werden dabei übersprungen)
```

## 3. Was das Playbook einrichtet

| Element | Details |
|---|---|
| Benutzer/Gruppe | `tuxmail` (System, Shell `/usr/sbin/nologin`, Home `/var/lib/tuxmail`) |
| `/opt/tuxmail/versions/<version>/` | ein Verzeichnis je installierter Version (root-eigen, 0755) |
| `/opt/tuxmail/current` | Symlink auf die aktive Version; `/usr/local/bin/tuxmail` zeigt darauf |
| `/etc/tuxmail/` | `root:tuxmail 0750`; `config.yaml` (0640), `oidc_client_secret` (0640) |
| `/var/lib/tuxmail/` | `tuxmail:tuxmail 0750` – Daten (siehe Abschnitt 6) |
| `/var/log/tuxmail/` | `tuxmail:tuxmail 0750` – nur genutzt, wenn `tuxmail_log_file` gesetzt ist (sonst journald) |
| `/var/backups/tuxmail/` | `root 0700` – Sicherungen (Archive 0600) |
| `tuxmail.service` | gehärtet: `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `NoNewPrivileges`, leeres `CapabilityBoundingSet`, `RestrictAddressFamilies`, `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute`, `ReadWritePaths` nur Daten/Logs, `LimitNOFILE=65536`, `Restart=on-failure`, `ExecStartPre=… check-config` |
| `tuxmail-backup.timer` | optional (`tuxmail_backup_timer_enabled`), täglich |
| nginx | WebSocket `/api/events` (Upgrade, 1 h Timeout, ohne Puffer), alle übrigen Pfade (SPA, `/device`, API, Long-Polling) an TuxMail, `client_max_body_size` = `tuxmail_max_upload_mb`, HSTS, `nosniff`, `X-Frame-Options`, `Referrer-Policy`, CSP (`frame-ancestors`…), HTTP→HTTPS, ACME-Webroot |
| SELinux (RHEL) | Boolean `httpd_can_network_connect` (nginx → `127.0.0.1:8080`) |

**Konfigurationsprüfung:** `config.yaml` wird nur geschrieben, wenn `tuxmail check-config` der
*neuen* Version sie akzeptiert (`template … validate`). Anschließend wird sie zusätzlich als
Benutzer `tuxmail` geprüft (Leserechte). `check-config` lehnt unbekannte Schlüssel ab und prüft,
dass TLS-Dateien, OIDC-Secret-Datei und Logdatei vorhanden bzw. lesbar sind – das Playbook legt
diese Dateien deshalb vorher an.

**Health-Check:** Nach jedem (Neu-)Start fragt Ansible `GET /healthz` direkt am Listener ab
(bis zu 30 × 2 s). Schlägt das fehl, bricht der Lauf ab (siehe Fehlerbehebung).

**Erster Administrator:** Beim ersten Lauf wird `tuxmail user add … --admin --password-stdin
--if-not-exists` ausgeführt. Das Passwort kommt aus `tuxmail_admin_password` (Vault) oder wird
generiert, unter `deploy/ansible/credentials/<host>/admin_password` (0600, per `.gitignore`
ausgeschlossen) gespeichert und **einmal** ausgegeben. Eine Markierung
`/etc/tuxmail/.admin-initialized` verhindert Wiederholungen. Nach der ersten Anmeldung Passwort
ändern, TOTP einrichten (für Admins standardmäßig Pflicht) und die Datei löschen.

**Unix-Socket statt TCP:** `tuxmail_listen=unix:/run/tuxmail/tuxmail.sock`. systemd legt
`/run/tuxmail` an (setgid, Gruppe `tuxmail-web`), nginx wird Mitglied von `tuxmail-web`.
Unter RHEL mit aktivem SELinux nicht empfohlen (nginx darf ohne eigenes Policy-Modul nicht
verbinden) – dort TCP verwenden.

## 4. Variablen (Auswahl)

Vollständige Liste mit Kommentaren: `roles/*/defaults/main.yml` und `inventory/group_vars/all.yml`.

| Variable | Standard | Bedeutung |
|---|---|---|
| `tuxmail_version` | `1.0.0` | zu installierende Version (ohne `v`) |
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
| `tuxmail_release_base_url` | GitHub-Release `v<version>/` | Quelle für Archiv und `checksums.txt` |
| `tuxmail_binary_src` | `""` | lokales Binary/`.tar.gz` auf dem Steuerrechner statt Download |
| `tuxmail_keep_versions` | `3` | installierte Versionen (für Rollback) |
| `tuxmail_max_upload_mb` | `50` | Upload-Limit (TuxMail und nginx) |
| `tuxmail_log_level` / `_format` / `_file` | `info` / `json` / `""` | Logging (`""` = journald) |
| `tuxmail_trusted_proxies_extra` | `[]` | zusätzliche vertrauenswürdige Proxys |
| `tuxmail_auth_registration` | `disabled` | `disabled` \| `invite` |
| `tuxmail_auth_totp_required_for_admins` | `true` | TOTP-Pflicht für Admins |
| `tuxmail_auth_password_min_length` | `12` | Passwort-Mindestlänge |
| `tuxmail_auth_session_lifetime` | `168h` | Sitzungsdauer |
| `tuxmail_login_rate_per_minute` | `10` | Login-Ratenbegrenzung |
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
| `tuxmail_backup_timer_enabled` | `false` | tägliche Sicherung |
| `tuxmail_backup_keep_scheduled` | `14` | aufbewahrte tägliche Sicherungen |
| `tuxmail_backup_compression` | `zstd` | `zstd` (tar.zst) \| `gzip` (tar.gz) |
| `tuxmail_backup_age_recipients` | `[]` | öffentliche age-Schlüssel → Sicherungen verschlüsseln |
| `tuxmail_service_environment` | `{}` | zusätzliche Umgebungsvariablen (z. B. `TUXMAIL_LOG_LEVEL`) |
| `tuxmail_nginx_strip_subpath` | `false` | Unterpfad vor dem Weiterreichen entfernen |
| `tuxmail_nginx_csp` | `frame-ancestors 'self'; …` | zusätzliche CSP von nginx (`""` = keine) |
| `tuxmail_nginx_ipv6` | automatisch | auch auf `[::]` lauschen |
| `tuxmail_nginx_disable_default_site` | `true` | Debian-Standardseite entfernen (nur `new`) |
| `tuxmail_firewall_enabled` | `false` | ufw/firewalld: SSH, 80, 443 freigeben |
| `tuxmail_bridges_enabled` | `false` | Matrix + mautrix-Bridges + Gotenberg (Container) |

**Geheimnisse mit Ansible Vault:**

```bash
ansible-vault encrypt_string --name tuxmail_admin_password 'S3hr-langes-Passwort'
ansible-vault encrypt_string --name tuxmail_oidc_client_secret 'abc123…'
# Ausgabe in inventory/group_vars/all.yml oder host_vars/<host>.yml einfügen, dann:
ansible-playbook site.yml --ask-vault-pass
```

## 5. Update und Rollback

**Update:** Version ändern und Playbook erneut ausführen:

```bash
ansible-playbook site.yml -e tuxmail_version=1.1.0 --tags tuxmail
```

Ablauf: neues Archiv herunterladen und per `checksums.txt` (SHA-256) prüfen → nach
`/opt/tuxmail/versions/1.1.0/` entpacken → `config.yaml` mit dem **neuen** Binary prüfen →
Dienst stoppen → `/var/lib/tuxmail` sichern
(`/var/backups/tuxmail/tuxmail-pre-upgrade-<datum>-<alt>-to-<neu>.tar.zst`) → Symlink `current`
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
sudo tar --zstd -xpf /var/backups/tuxmail/tuxmail-pre-upgrade-<…>.tar.zst -C /var/lib   # .tar.gz: -xzpf
# verschlüsselt: age -d -i schluessel.txt <datei>.tar.zst.age | sudo tar --zstd -xpf - -C /var/lib
sudo ln -sfn /opt/tuxmail/versions/1.0.0 /opt/tuxmail/current
sudo systemctl start tuxmail && curl -fsS http://127.0.0.1:8080/healthz
```

Manuelle Sicherung jederzeit: `sudo /usr/local/sbin/tuxmail-backup --stop manual 10`.

## 6. Daten und Sicherungen – wichtig

`/var/lib/tuxmail` enthält u. a.:

| Datei | Bedeutung |
|---|---|
| `server.db` | Server-Datenbank (Benutzer, Sitzungen, Einstellungen) |
| `master.key` | **Hauptschlüssel** (0600). Ohne ihn sind gespeicherte Benutzer-Geheimnisse (Mail-Passwörter, Tokens) und TOTP-Daten unlesbar. TuxMail startet nicht, wenn die Datei für Gruppe/Andere lesbar ist. |
| `vapid.json` | Web-Push-Schlüssel (0600). Bei Verlust funktionieren bestehende Push-Abonnements nicht mehr – bei Neuinstallation übernehmen. |
| `users/<id>/` | je Benutzer `tuxmail.db` und `secrets.vault` |

Sicherungen enthalten daher `master.key` und sind entsprechend zu schützen: Das Skript erzeugt
sie mit `umask 077` (Archive 0600, Verzeichnis 0700). Werden Sicherungen außerhalb des Servers
abgelegt, **verschlüsseln** (`tuxmail_backup_age_recipients: ["age1…"]`, privaten age-Schlüssel
getrennt aufbewahren). Beim Zurückspielen Rechte erhalten (`tar -p` als root).

## 7. Benutzerverwaltung

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
`tuxmail user --help`. Alles davon ist auch in der
Weboberfläche für Administratoren möglich. Bei OIDC werden Benutzer bei der ersten Anmeldung
angelegt; Admin-Rechte über `tuxmail_oidc_admin_group`.

Umgebungsvariablen `TUXMAIL_LISTEN`, `TUXMAIL_DATA_DIR`, `TUXMAIL_PUBLIC_URL`,
`TUXMAIL_LOG_LEVEL` überschreiben einzelne Werte aus `config.yaml` (z. B. über
`tuxmail_service_environment`).

## 8. Optionale Rollen

**Firewall** (`tuxmail_firewall_enabled: true`): ufw (Debian/Ubuntu) bzw. firewalld (RHEL). Gibt
immer zuerst SSH (`tuxmail_firewall_ssh_port`) frei, dann 80/443 und `tuxmail_firewall_extra_ports`.
ufw wird mit „eingehend verweigern“ aktiviert (`tuxmail_firewall_ufw_enable`).

**Bridges & Gotenberg** (`tuxmail_bridges_enabled: true`, KONZEPT 11.3/8.11) – optional,
standardmäßig aus. Startet per podman-compose (oder `docker compose`) unter `/opt/tuxmail-bridges`:

- Matrix-Homeserver: **Synapse** (Standard; Bridges werden automatisch per
  `app_service_config_files` registriert) oder **Continuwuity** (Registrierung einmalig im
  Admin-Raum mit `!admin appservices register`, Anleitung wird ausgegeben). Nur an
  `127.0.0.1:8008` gebunden, Registrierung deaktiviert.
- `mautrix-whatsapp` und `mautrix-signal`: Konfiguration wird erzeugt und angepasst (Homeserver,
  SQLite, Rechte), Registrierung erzeugt.
- **Gotenberg** (PDF) auf `127.0.0.1:3000`.

Danach Matrix-Konto für TuxMail anlegen (`register_new_matrix_user`, Befehl wird ausgegeben) und
in TuxMail hinzufügen. Images in den Defaults sind `:latest` – für den Produktivbetrieb feste
Versionen setzen. Rechtliche Hinweise zu inoffiziellen Messenger-Anbindungen: KONZEPT 18.1.

## 9. Fehlerbehebung

| Problem | Prüfen / Lösung |
|---|---|
| Health-Check schlägt fehl | `journalctl -u tuxmail -n 100`; `sudo runuser -u tuxmail -- tuxmail check-config --config /etc/tuxmail/config.yaml`; `curl -v http://127.0.0.1:8080/healthz`. Bei Problemen nach einem Update: Rollback (Abschnitt 5). |
| `check-config` lehnt Konfiguration ab | Fehlermeldung im Ansible-Output (validate); unbekannte Schlüssel, fehlende TLS-/Secret-/Logdatei, Rechte. |
| Dienst startet nicht: „master.key permissions“ | `sudo chmod 600 /var/lib/tuxmail/master.key && sudo chown tuxmail:tuxmail /var/lib/tuxmail/master.key` |
| `nginx -t` schlägt fehl (existing) | Der vorherige Zustand wurde wiederhergestellt. Häufig: doppelter `server_name`, Port-/`default_server`-Konflikte. Fehlermeldung mit `sudo nginx -t` ansehen. |
| „existiert bereits und wurde nicht von diesem Playbook angelegt“ | Eine fremde Datei hat denselben Namen: `tuxmail_nginx_vhost_name` bzw. `tuxmail_nginx_snippet_path` ändern. |
| certbot scheitert | DNS zeigt auf den Server? Port 80 offen (Firewall, Cloud-Security-Group)? `curl http://<domain>/.well-known/acme-challenge/test` muss 404 von nginx liefern. Logs: `/var/log/letsencrypt/letsencrypt.log`. Rate-Limits: mit `tuxmail_tls_letsencrypt_staging=true` testen. |
| 502 Bad Gateway | TuxMail läuft nicht (`systemctl status tuxmail`) oder RHEL/SELinux: `getsebool httpd_can_network_connect`, `ausearch -m avc -ts recent`. Unix-Socket: nginx Mitglied von `tuxmail-web`? |
| WebSocket/Live-Updates fehlen | Vorgelagerter Proxy muss `Upgrade`/`Connection` durchreichen; Timeouts ≥ `tuxmail_nginx_websocket_timeout`. |
| 413 Request Entity Too Large | `tuxmail_max_upload_mb` erhöhen (wirkt auf TuxMail und nginx); vorgelagerte Proxys ebenfalls anpassen. |
| Anmeldung klappt nicht hinter Proxy / Cookies fehlen | `tuxmail_public_url` muss die tatsächliche HTTPS-URL sein (Cookies mit `__Host-`-Präfix und `Secure`); Proxy-Adresse in `tuxmail_trusted_proxies_extra`. |
| Doppelte HSTS-Header | nginx blendet den HSTS-Header von TuxMail aus (`proxy_hide_header`), wenn es selbst HSTS sendet; ohne TLS in nginx bleibt der von TuxMail erhalten. |
| `nginx -t`: „Address family not supported“ | Host ohne IPv6: `tuxmail_nginx_ipv6=false` (Standard erkennt das automatisch). |
| Download/Prüfsumme schlägt fehl | Release-URL erreichbar? `tuxmail_release_base_url` anpassen oder `tuxmail_binary_src` verwenden. |

Ansible ausführlicher: `ansible-playbook site.yml -vv`. Probelauf: `--check --diff`.

## 10. Manuelle Installation (ohne Ansible)

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
```

## 11. Docker / Podman

Für jede Version gibt es ein Multi-Arch-Image (linux/amd64, linux/arm64) im GitHub Container Registry:

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

