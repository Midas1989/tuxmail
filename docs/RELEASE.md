<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# TuxMail – Release erstellen

Diese Anleitung beschreibt, wie eine neue TuxMail-Version gebaut und veröffentlicht wird
(siehe auch [KONZEPT.md, Kap. 15](KONZEPT.md)). Die gesamte Build-Logik liegt in `build/`;
GitHub Actions (`.github/workflows/release.yml`) und Entwickler verwenden dieselben Skripte.

## 1. Versionsnummer

- **Einzige Quelle:** die Datei [`VERSION`](../VERSION) im Repository-Wurzelverzeichnis (`MAJOR.MINOR.PATCH`, z. B. `0.1.0`).
- `build/version.sh` liest sie und liefert die Version für alle Builds:
  - Release-Build (Tag `v0.1.0`): `0.1.0`
  - alle anderen Builds: `0.1.0-dev.<commit>` (Pakete: `0.1.0~dev.<commit>`, also älter als `0.1.0`)
  - `--numeric`: vierteilige Windows-Version `0.1.0.<CI-Laufnummer>`
- Verwendet für Go (`-ldflags -X main.version=…`, Ausgabe von `tuxmail version` und `/healthz`),
  Windows-Versionsinfo (go-winres) und Installer, `.deb`/`.rpm`, AppStream-Metadaten, Docker-Tags.
- Die Android-App behält ihre Version in `mobile/package.json`. Beim Release prüfen `release.yml`
  (`build/version.sh --check-tag`) und `android.yml`, dass Tag, `VERSION` und `mobile/package.json`
  übereinstimmen; die CI (`ci.yml`, Job *Version consistency*) prüft das bei jedem Push.
  `frontend/package.json` ist nur informativ (Warnung bei Abweichung).

## 2. Release erstellen

```bash
# 1. Version erhöhen (beide Dateien!)
echo 0.2.0 > VERSION
(cd mobile && npm version 0.2.0 --no-git-tag-version)
(cd frontend && npm version 0.2.0 --no-git-tag-version)   # optional, empfohlen
git commit -am "Release 0.2.0"

# 2. Auf main bringen (Pull Request), danach taggen
git tag -a v0.2.0 -m "TuxMail 0.2.0"
git push origin v0.2.0
```

Der Text für GitHub steht bei großen Versionen in `docs/releases/<v>.md`. Der Tag startet `release.yml` und `android.yml`. Nach etwa 20–30 Minuten liegt unter
**GitHub → Releases → v0.2.0** ein Release mit allen Dateien und automatisch erzeugten Release Notes
(aus den Pull Requests; bei Bedarf im Web nachbearbeiten).

Schlägt ein Job fehl: Ursache beheben, Tag löschen (`git push --delete origin v0.2.0 && git tag -d v0.2.0`),
ggf. das angelegte Release löschen und neu taggen. Ein Tag, der nicht zu `VERSION` passt, bricht sofort ab.

## 3. Was gebaut wird

| Datei | Inhalt | Skript / Job |
|---|---|---|
| `tuxmail_<v>_linux_amd64.tar.gz`, `…_linux_arm64.tar.gz` | Server/CLI (reines Go, `CGO_ENABLED=0`): `tuxmail` + `plugins/` + LICENSE, NOTICE, README.md – **Format für Ansible** (`deploy/ansible`) | `build/package-server.sh` / *server* |
| `tuxmail_<v>_windows_amd64.zip`, `…_windows_arm64.zip` | Server/CLI für Windows (Konsolenprogramm `tuxmail.exe` + `plugins\`) | `build/package-server.sh` / *server* |
| `TuxMail-<v>-linux-<arch>.AppImage` | Desktop, enthält GTK 3 + WebKitGTK 4.1 (amd64, arm64) und die Messenger-Plugins (`usr/lib/tuxmail/plugins`) | `build/linux/package-appimage.sh` / *linux-desktop* |
| `tuxmail_<v>-1_<arch>.deb`, `tuxmail-<v>-1.<x86_64\|aarch64>.rpm` | Desktop-Pakete (`/usr/bin/tuxmail`, `/usr/lib/tuxmail/plugins/`, Desktop-Datei, Icons, AppStream) | `build/linux/package-nfpm.sh` (nfpm) |
| `TuxMail-<v>-linux-<arch>-portable.tar.gz` | Desktop portabel (Binary + `plugins/` + Datei `portable` + `data/`), braucht GTK/WebKitGTK vom System | `build/linux/package-portable.sh` |
| `TuxMail-<v>-windows-<arch>-portable.zip` | `TuxMail/TuxMail.exe` + `plugins\` + leere Datei `portable` + `data/` + README-portable.txt (de/en) | `build/windows/package-portable.sh` / *windows-desktop* |
| `TuxMail-<v>-windows-<arch>-setup.exe` | **TuxMail-Setup** (eigenes Setup-Programm im TuxMail-Design, amd64, arm64) | `build/windows/package-setup.sh` + `cmd/tuxmail-setup` |
| `tuxmail_<v>_sbom.spdx.json` | Software-Stückliste (SPDX, Go-Module und npm-Pakete), optional | *publish* (anchore/sbom-action) |
| `checksums.txt` | SHA-256 aller Dateien oben (`sha256sum -c checksums.txt --ignore-missing`) | `build/checksums.sh` / *publish* |
| `checksums.txt.asc`, `tuxmail-release-key.asc` | GPG-Signatur + öffentlicher Schlüssel (nur mit Secret `GPG_PRIVATE_KEY`) | `build/sign-gpg.sh` |
| `TuxMail-<v>-foss.apk`, `…-play.apk/.aab`, `SHA256SUMS` | Android (eigene Prüfsummendatei) | `android.yml` |
| `ghcr.io/midas1989/tuxmail:<v>` und `:latest` | Docker-Image linux/amd64 + linux/arm64 | `build/docker/Dockerfile` / *docker* |

**Messenger-Plugins:** Alle Desktop- und Server-Pakete sowie das Docker-Image enthalten
`tuxmail-plugin-{whatsapp,telegram,matrix,signal}` (`build/plugins.sh`, reines Go, Matrix mit `-tags goolm`;
`build/lib.sh` → `stage_plugins`). Der Plugin-Host (`internal/messenger/host.DefaultDirs`) sucht in `plugins/`
neben dem Programm (Windows, portable und Server-Archive) und in `../lib/tuxmail/plugins` relativ dazu
(`/usr/lib/tuxmail/plugins` bei `.deb`/`.rpm`/AppImage, `/usr/local/lib/tuxmail/plugins` im Docker-Image).
`TUXMAIL_PLUGINS_DIR=<dir>` verwendet fertige (z. B. signierte) Plugins, `TUXMAIL_SKIP_PLUGINS=1` packt ohne.

**signal-cli (Signal-Plugin):** Jedes Paket enthält zusätzlich `plugins/signal-cli/` (`build/signal-cli.sh`,
aufgerufen von `stage_plugins`): Linux x86_64 den GraalVM-Native-Build (kein Java nötig), Linux ARM64 und
Windows x64 den JVM-Build mit Eclipse-Temurin-JRE 25 (`jre/`, mindestens die Java-Version `SIGNAL_CLI_JAVA` aus `pins.env`), Windows ARM64 denselben x64-Build (läuft unter
der x64-Emulation von Windows 11 on ARM, weil libsignal-client keine Windows-ARM64-Bibliothek mitliefert),
das Docker-Image (jetzt auf `debian:trixie-slim`, signal-cli braucht glibc) je nach Architektur eines davon.
Versionen, URLs und SHA-256 stehen an **einer** Stelle: `internal/messenger/signalcli/pins.env` (auch in die App
eingebettet). Downloads nur per HTTPS, Prüfsumme vor dem Entpacken geprüft, Cache `build/cache/signal-cli`.
`TUXMAIL_SIGNAL_CLI=bundle` (Tags in CI) bricht ab, wenn es nicht klappt; `auto` (Standard, Branch-Builds) packt
dann ohne, `skip` immer ohne – die App bietet dann „Signal-Komponente installieren“ an (gleiche Pins, Ziel
`<Datenordner>/plugins/signal-cli`). Aktualisieren: Workflow *Pin signal-cli* (manuell) oder
`build/signal-cli.sh pin [VERSION|latest] [TEMURIN_VERSION]` – vergleicht jede Prüfsumme mit dem GitHub-Asset-Digest
bzw. Adoptiums `.sha256.txt` und schreibt `pins.env` neu. signal-cli muss aktuell bleiben (Signal lehnt Clients
ab, die älter als etwa drei Monate sind): vor jedem Release neu pinnen. Lizenzen: THIRD_PARTY_NOTICES.md.

**Ablauf in `release.yml`:** *version* (Version bestimmen, Tag prüfen) → *frontend* (einmal `npm ci && npm run build`,
Artefakt `frontend-dist`, von allen Jobs nach `web/dist` geladen) → parallel *server* (4×), *linux-desktop*
(`ubuntu-24.04` und `ubuntu-24.04-arm`), *windows-desktop* (`windows-latest`, arm64 als Cross-Build), *docker* →
*publish* (Prüfsummen, SBOM, Signatur, Release).

**Ohne Tag** (Push auf `main` oder `claude/**`, manueller Start) läuft derselbe Ablauf ohne Release: alle Dateien
liegen als Workflow-Artefakte am Lauf (`server-*`, `linux-desktop-<arch>-{deb,rpm,appimage,portable}`, `windows-desktop-<arch>-{setup,portable}`, `checksums`, 14 Tage),
das Docker-Image wird nur bei Pushes auf `main` als `:edge` veröffentlicht (Branches `claude/**` bauen das Image
ohne Push; Tags veröffentlichen `:<version>` und `:latest`).

### Desktop- und Server-Binary

- Die **Desktop-Builds** (`-tags desktop,production[,webkit2_41]`) enthalten auch die komplette Kommandozeile
  (`tuxmail serve`, `tuxmail user …`). Ohne Argument öffnet sich das Fenster. Unter Linux kann das installierte
  Paket also auch als Server dienen, benötigt dann aber GTK/WebKitGTK.
- Für Server gibt es deshalb die **reinen Server-Builds** (`CGO_ENABLED=0`, ohne GUI-Bibliotheken, statisch).
  Ansible und das Docker-Image verwenden diese.
- Unter Windows ist `TuxMail.exe` ein Fensterprogramm (`-H windowsgui`, kein Konsolenfenster) – Ausgaben der
  Kommandozeile sind dort nicht sichtbar. Für die Kommandozeile das Server-ZIP (`tuxmail.exe`) verwenden.
- `mailto:`: Desktop-Datei und Setup registrieren TuxMail als Mailprogramm (Windows: optional, standardmäßig
  aus), übergeben die Adresse aber noch nicht, weil die Kommandozeile dafür noch keine Option hat – ein Klick auf
  einen `mailto:`-Link öffnet TuxMail. Sobald `tuxmail` eine `mailto:`-URL annimmt, in
  `build/linux/org.tuxmail.TuxMail.desktop` `Exec=tuxmail %u`, und in `internal/winsetup/registry.go` (`MailOps`)
  `"%1"` ergänzen.

### Linux-Details

- `.deb`: Abhängigkeiten `libc6 (>= 2.34)`, `libgtk-3-0`, `libwebkit2gtk-4.1-0` (Debian 12+, Ubuntu 22.04+, geprüft gegen das Archiv von 26.04);
  `.rpm`: `libgtk-3.so.0()(64bit)`, `libwebkit2gtk-4.1.so.0()(64bit)` (Fedora, openSUSE). In einem
  Ubuntu-24.04-Container per `apt install ./tuxmail_…deb` getestet.
- **AppImage:** linuxdeploy + linuxdeploy-plugin-gtk bündeln alle Bibliotheken; die WebKit-Hilfsprozesse
  (`WebKitWebProcess`, `WebKitNetworkProcess`) werden mitkopiert und die gebündelte `libwebkit2gtk` so gepatcht,
  dass sie diese relativ zu `$APPDIR/usr` startet (`build/linux/AppRun`). Gebaut auf Ubuntu 24.04 → benötigt
  glibc ≥ 2.39 auf dem Zielsystem (Ubuntu 24.04+, Debian 13+, Fedora 40+). Portabler Modus: Datei `portable`
  oder Ordner `data/` neben der `.AppImage`-Datei bzw. `--portable` → Daten in `data/` neben der AppImage.
  Die Werkzeuge sind versionierte Releases mit festem SHA-256 in `build/linux/package-appimage.sh` (linuxdeploy
  `1-alpha-20250213-2`, appimagetool `1.9.0`, linuxdeploy-plugin-gtk als Commit); ein Download ohne Pin wird
  abgelehnt. Anderes Werkzeug: `LINUXDEPLOY_VERSION`/`APPIMAGETOOL_VERSION`/`LINUXDEPLOY_PLUGIN_GTK_REF` **und** die
  passende `*_SHA256` setzen. Pins bewusst anheben: neues Release laden, Hash mit der Upstream-Quelle vergleichen,
  Version und Hash gemeinsam ändern. (Dieser Umstieg von „continuous“ auf feste Versionen ist beim Schreiben nicht
  gebaut worden: der erste Lauf von `linux-desktop` muss grün sein.)
- App-ID `org.tuxmail.TuxMail` (Desktop-Datei, Icon, AppStream `…metainfo.xml`), Dateien in `build/linux/`.

### Windows-Details

- Icon, Manifest (Per-Monitor-DPI v2) und Versionsinfo (CompanyName/ProductName „TuxMail“, Copyright) kommen aus
  `build/windows/winres.json` (+ `assets/branding/icons/tuxmail.ico`); go-winres erzeugt beim Build
  `cmd/tuxmail/rsrc_windows_<arch>.syso` (nicht eingecheckt, wird danach gelöscht).
- **Setup (`TuxMail-<v>-windows-<arch>-setup.exe`, `cmd/tuxmail-setup`):** eigenes Setup-Programm mit Wails/WebView2
  im TuxMail-Design (rahmenloses Fenster mit eigener Titelleiste, Mica unter Windows 11, hell/dunkel nach
  Windows-Einstellung, Deutsch/Englisch). Seiten: Willkommen (Sprache) → Installationsart → Zielordner →
  Optionen → Fortschritt → Fertig („TuxMail starten“). Screenshots: `frontend/docs/screenshots/setup-*.jpg`.
  - **Nur für mich:** `%LOCALAPPDATA%\Programs\TuxMail`, HKCU, keine Administratorrechte.
    **Für alle Benutzer:** `%ProgramFiles%\TuxMail`, HKLM; das Fenster bleibt ohne Rechte, die eigentliche
    Installation läuft in einer per UAC (`runas`) gestarteten Kopie des Setups, deren Fortschritt das Fenster anzeigt.
    **Portabel entpacken:** beliebiger Ordner (Vorschlag: neben dem Setup, z. B. USB-Stick) mit Datei `portable`
    und `data\`, ohne Registry-Einträge und Verknüpfungen.
  - Optionen: Startmenü (an), Desktop-Verknüpfung, als E-Mail-Programm registrieren (ProgID `TuxMail.mailto`,
    `Clients\Mail\TuxMail` mit Capabilities, `RegisteredApplications`; danach öffnet sich „Standard-Apps“),
    Autostart (`…\CurrentVersion\Run`) – alles aus, wenn nicht gewählt (KONZEPT 15.2).
  - „Apps & Features“: Eintrag `…\Uninstall\TuxMail` (DisplayIcon, DisplayVersion, Publisher „TuxMail“,
    EstimatedSize, `UninstallString` = `"<Ordner>\TuxMail-Setup.exe" --uninstall`). Das Setup kopiert sich dazu als
    `TuxMail-Setup.exe` in den Programmordner (Deinstallieren, Ändern, Reparieren). Die Deinstallation läuft aus
    einer Kopie in `%TEMP%`, zeigt dasselbe Design und bietet „Benutzerdaten behalten“ (Standard: an;
    sonst wird `%APPDATA%\TuxMail` gelöscht). Entfernt werden nur die in `tuxmail-install.json` aufgeführten Dateien.
  - **Update:** erkennt eine bestehende Installation (auch des klassischen NSIS-Installers), übernimmt Art, Ordner
    und Optionen, beendet ein laufendes TuxMail nach Rückfrage (WM_CLOSE, nach 15 s hart) und ersetzt die Dateien
    über `*.tuxmail-new` + Umbenennen; nicht mehr enthaltene Dateien (z. B. ein entferntes Plugin) werden gelöscht.
  - **WebView2:** fehlt die Runtime, installiert das Setup den Evergreen-Bootstrapper (mitgeliefert mit
    `WEBVIEW2_BOOTSTRAPPER=download|<Datei>`, sonst Download), sonst Hinweis mit Link auf die Download-Seite.
    Der Bootstrapper wird vor dem Start (oft mit Administratorrechten) mit `WinVerifyTrust` geprüft: gültige
    Authenticode-Signatur, Signierer „Microsoft Corporation“; eine mitgelieferte Datei muss zusätzlich ihrem im
    Setup abgelegten SHA-256 entsprechen, der Download folgt nur https-Weiterleitungen auf Microsoft-Hosts.
    Beim Bauen wird nur eine geprüfte Datei eingebettet (`verify_wv2` in `build/windows/package-setup.sh`:
    `WEBVIEW2_BOOTSTRAPPER_SHA256`, oder `osslsigncode` + `WEBVIEW2_CA_FILE`, oder ausdrücklich
    `WEBVIEW2_ALLOW_UNVERIFIED=1`); `release.yml` lädt die Datei auf dem Windows-Runner, prüft sie mit
    `Get-AuthenticodeSignature` und übergibt Datei und SHA-256.
  - **Still (Administratoren):** `TuxMail-…-setup.exe /S [--scope=user|machine|portable] [--dir=C:\Pfad | /D=C:\Pfad]
    [--desktop-shortcut] [--no-start-menu] [--default-mail] [--autostart] [--options=startmenu,desktop,mail,autostart]
    [--no-close-apps] [--log=Datei]`; `/ALLUSERS` und `/CURRENTUSER` wie beim früheren NSIS-Installer. Deinstallation:
    `"…\TuxMail-Setup.exe" --uninstall --silent [--remove-data]`. Exit-Codes: 0 OK, 1 Fehler, 2 ungültige
    Argumente, 740 Administratorrechte verweigert. Protokoll: `%TEMP%\TuxMail-Setup.log`.
  - Aufbau: die Programmdateien (TuxMail.exe, `plugins\`, LICENSE/NOTICE) werden beim Build als zstd-Zip
    (`cmd/tuxmail-setup/mkpayload`) per `go:embed` eingebettet – eine einzige, selbständige Datei. Logik und Tests
    in `internal/winsetup` (Pfade, Registry-Plan mit Fake-Registry, Payload, Update-Erkennung, Kommandozeile);
    Oberfläche statisch in `cmd/tuxmail-setup/ui/` (kein Build-Schritt), Browser-Mock in `ui-dev/mock.js`,
    Screenshots mit `NODE_PATH=$(npm root -g) node frontend/scripts/setup-screenshots.cjs frontend/docs/screenshots`.
    Manifest `asInvoker` (`build/windows/setup-winres.json`), damit Windows „*setup*.exe“ nicht pauschal mit UAC startet.
  - CI (`windows-desktop`, amd64) testet das Setup auf dem Windows-Runner: stille Installation, Update,
    portables Entpacken und Deinstallation aus der installierten Kopie.
- Der frühere klassische NSIS-Installer (`…-setup-classic.exe`) wird nicht mehr gebaut. TuxMail-Setup erkennt
  eine damit erstellte Installation weiterhin und übernimmt sie beim Update.
- Lokaler Test unter Linux (Windows-Builds sind reines Go und lassen sich überall cross-kompilieren):
  `build/windows/build.sh amd64 && build/windows/package-setup.sh amd64` (go-winres).

## 4. Lokal bauen

```bash
go generate ./web                          # Frontend nach web/dist
build/package-server.sh linux amd64        # -> dist/tuxmail_<v>_linux_amd64.tar.gz
build/linux/build.sh                       # Desktop-Binary (braucht libwebkit2gtk-4.1-dev libgtk-3-dev)
build/linux/package-nfpm.sh                # .deb/.rpm (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0)
build/linux/package-portable.sh
build/linux/package-appimage.sh            # lädt linuxdeploy/appimagetool herunter
build/windows/build.sh amd64               # TuxMail.exe + plugins; go install github.com/tc-hib/go-winres@v0.3.3
build/windows/package-portable.sh amd64
build/windows/package-setup.sh amd64       # TuxMail-Setup (…-setup.exe)
build/checksums.sh                         # dist/checksums.txt
docker build -f build/docker/Dockerfile -t tuxmail:local .
```

### Alles auf einmal: `build/build-all.sh`

Das Skript ruft genau die obigen Skripte auf, prüft vorher die Voraussetzungen und fasst am Ende zusammen, was
gebaut wurde. Es braucht weder GitHub noch Actions.

```bash
build/build-all.sh --check                  # was lässt sich auf diesem Rechner bauen, was fehlt?
build/build-all.sh                          # alles, was möglich ist; Fehlendes wird übersprungen
build/build-all.sh server windows-desktop   # nur diese Gruppen
build/build-all.sh --strict                 # fehlende Voraussetzung = Fehler (z. B. für Skripte)
build/build-all.sh --arch amd64 server      # nur eine Architektur (Server und Windows)
build/build-all.sh --offline server         # ohne Netz: Go-/npm-Caches müssen gefüllt sein, kein signal-cli
build/build-all.sh android --android-tasks "fossRelease playRelease"
```

| Gruppe | Ergebnis in `dist/` | Voraussetzungen auf dem Rechner |
|---|---|---|
| `frontend` | `web/dist` (Zwischenstand) | Go, Node 22.12 oder neuer |
| `server` | `tuxmail_<v>_linux_{amd64,arm64}.tar.gz`, `…_windows_{amd64,arm64}.zip` | Go, Node 22.12+ (Cross-Build, kein C-Compiler) |
| `linux-desktop` | `.deb`, `.rpm`, portables `tar.gz`, AppImage (nur für die **Host**-Architektur) | gcc, pkg-config, `libwebkit2gtk-4.1-dev`, `libgtk-3-dev`; nfpm und die AppImage-Werkzeuge werden geprüft geladen |
| `windows-desktop` | `…-windows-<arch>-portable.zip`, `…-setup.exe` (Cross-Build) | Go, go-winres (wird bei Bedarf installiert) |
| `android` | `foss`-APK (Debug; mit `TUXMAIL_KEYSTORE_FILE` Release) | JDK 21, Android SDK (Platform 36, Build-Tools 36), Node 22 |
| `docker` | Image `tuxmail:local` | docker oder podman |
| `checksums` | `checksums.txt` | – |

Hinweise:

- **Signal-Komponente:** Ohne `TUXMAIL_SIGNAL_CLI=skip` lädt der Build signal-cli (geprüfte Pins in
  `internal/messenger/signalcli/pins.env`). Mit `--offline` wird sie weggelassen; die App bietet sie dann zum
  Nachinstallieren an.
- **AppImage offline:** `LINUXDEPLOY`, `LINUXDEPLOY_PLUGIN_GTK` und `APPIMAGETOOL` müssen auf lokale Dateien zeigen.
- **Eigene Server-Archive installieren:** `ansible-playbook … -e tuxmail_binary_src=/pfad/tuxmail_<v>_linux_amd64.tar.gz`
  (siehe `deploy/README.md`). Diese Variante prüft keine Release-Signatur, weil nichts heruntergeladen wird.
- **Android:** Der Android-Build braucht das Frontend im Modus `build:capacitor`; `mobile/scripts/build.sh` erledigt das.
- **Windows-Binaries** sind nicht Authenticode-signiert; Windows zeigt „Unbekannter Herausgeber“.

Ergebnisse liegen in `dist/`, Zwischenstände in `build/out/` (beide von git ignoriert).
`TUXMAIL_VERSION=1.2.3` überschreibt die Version, `TUXMAIL_RELEASE=1` erzwingt die Release-Version aus `VERSION`.

## 5. Signierung – Secrets für später

Alle Signaturschritte sind vorbereitet und werden übersprungen, solange die Secrets fehlen
(*Settings → Secrets and variables → Actions*).

| Zweck | Secrets / Variablen | Wirkung |
|---|---|---|
| **Windows Authenticode** über [SignPath](https://signpath.org) (für Open-Source-Projekte kostenlos über die SignPath Foundation) | Secret `SIGNPATH_API_TOKEN`; Variablen `SIGNPATH_ORGANIZATION_ID`, `SIGNPATH_PROJECT_SLUG`, `SIGNPATH_SIGNING_POLICY_SLUG`; im SignPath-Projekt eine Artifact-Configuration `exe`, die alle PE-Dateien (`**/*.exe`) im ZIP signiert | `TuxMail.exe` und `plugins\*.exe` (vor dem Packen von ZIP und Setup, das die signierten Dateien einbettet) sowie `…-setup.exe` werden signiert → keine SmartScreen-Warnung „Unbekannter Herausgeber“. Ohne Signatur erklären die README-Dateien, wie man die Warnung übergeht. |
| **Android** (bereits in `android.yml`) | `KEYSTORE_BASE64`, `KEYSTORE_PASSWORD`, `KEY_ALIAS`, `KEY_PASSWORD`, optional `GOOGLE_SERVICES_JSON` | signierte APK/AAB im Release |
| **GPG** für Linux-Pakete (optional, aber **Voraussetzung für das Ansible-Deployment**) | `GPG_PRIVATE_KEY` (ASCII-armored, `gpg --armor --export-secret-keys KEYID`), `GPG_PASSPHRASE` | eingebettete Signaturen in `.deb` (`_gpgorigin`) und `.rpm`, dazu `checksums.txt.asc` und `tuxmail-release-key.asc` im Release. Die Ansible-Rolle `tuxmail` prüft `checksums.txt.asc` gegen den Fingerabdruck `tuxmail_release_gpg_fingerprint` und bricht ohne Signatur ab (außer `tuxmail_allow_unsigned_release: true`, siehe `deploy/README.md`). Den Fingerabdruck nach dem Anlegen des Schlüssels notieren: `gpg --show-keys --with-colons tuxmail-release-key.asc`. |
| Docker | – (`GITHUB_TOKEN` mit `packages: write`) | Push nach `ghcr.io/midas1989/tuxmail`; nach dem ersten Push das Paket unter *Packages* auf „public“ stellen |

Prüfen der Downloads:

```bash
sha256sum -c checksums.txt --ignore-missing
gpg --import tuxmail-release-key.asc && gpg --verify checksums.txt.asc checksums.txt
rpm --import tuxmail-release-key.asc && rpm -K tuxmail-*.rpm
```

## 6. Abhängigkeiten aktuell halten

`.github/dependabot.yml` öffnet wöchentlich Pull Requests für Go-Module, npm (`frontend/`, `mobile/`),
GitHub Actions, die Basis-Images des Dockerfiles (`/build/docker`, Tag und Digest) und Gradle (`/mobile/android`).
Alle Actions sind auf den vollen Commit-SHA gepinnt (der Versions-Tag steht als Kommentar dahinter; Dependabot
aktualisiert beides). Zusätzlich im Repository unter *Settings → Actions → General* „Require actions to be pinned
to a full-length commit SHA“ einschalten. Werkzeugversionen in `release.yml`
(`NFPM_VERSION`, `GO_WINRES_VERSION`) und `ci.yml` (actionlint) werden von Hand angehoben; nfpm ab v2.44
braucht Go ≥ 1.25.

## 7. Sicherheit vor dem Release

- **Workflow `security.yml`** (Push auf `main`/`claude/**`, Pull Requests, wöchentlich, manuell): govulncheck,
  gosec + staticcheck, CodeQL (Go, JavaScript/TypeScript), npm audit, osv-scanner, gitleaks, Trivy
  (Dateisystem + gebautes Docker-Image, SBOM), Lizenzbericht, Fuzzing (60 s je Fuzz-Ziel) und der
  dynamische Pentest-Harness (`security/pentest/`). Vor einem Release-Tag muss der letzte Lauf auf `main`
  grün sein (Lizenzbericht und Pentest sind informativ). Übersicht: [docs/security/README.md](security/README.md).
- **Lieferkette der CI-Werkzeuge:** osv-scanner, gitleaks und hadolint sind Release-Binaries mit festem SHA-256
  (`build/ci-fetch.sh`, Download mit Wiederholungen; die Pins stehen in `security.yml`/`ci.yml` und werden von Hand
  zusammen mit der Version angehoben). `go install`/`go mod download`/`npm ci` laufen über `build/ci-retry.sh`.
  Der Fuzz-Job reicht Matrixwerte nur über Umgebungsvariablen an die Shell und lehnt Pfade mit Sonderzeichen ab.
- **Release-Prüfsummen:** jeder Build-Job schreibt die SHA-256 seiner Dateien (`build/partsums.sh`), der
  Publish-Job vergleicht sie vor `checksums.txt` und bricht bei Abweichung oder unbekannten Dateien ab. Das SBOM
  ist bei Tags Pflicht (Branch-Builds warnen nur).
- **Google-OAuth-Client-Secret:** wird nur in die Desktop-Builds eingelinkt (`TUXMAIL_LINK_CLIENT_SECRET=1` in
  `build/linux/build.sh`/`build/windows/build.sh`), nie in Server-Archive oder das Docker-Image; Betreiber eines
  Servers registrieren eine eigene Google-App (`docs/MICROSOFT-GOOGLE.md`).
- **Go-Toolchain:** `setup-go` liest `go.mod`. Die Release-Binaries enthalten die Standardbibliothek dieser
  Version – Patch-Releases von Go (Sicherheitsfixes) also zeitnah übernehmen (`toolchain`-Zeile in `go.mod`).
- **Code Scanning:** Bei privatem Repository braucht der SARIF-Upload GitHub Advanced Security. Ohne GHAS
  bleiben die SARIF-Dateien Artefakte des Laufs; mit GHAS die Repository-Variable `CODE_SCANNING_ENABLED=true`
  setzen (Settings → Secrets and variables → Actions → Variables).
- **Meldungen von Schwachstellen:** über *Private vulnerability reporting* (Settings → Code security →
  Private vulnerability reporting aktivieren), Ablauf in [SECURITY.md](../SECURITY.md). Sicherheitsfixes werden
  mit einem GitHub Security Advisory (ggf. CVE) und einem Patch-Release veröffentlicht.
