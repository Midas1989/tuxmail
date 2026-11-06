## Downloads – welche Datei? / Which file?

| Plattform / Platform | Datei / File |
|---|---|
| **Windows** (Setup, empfohlen / recommended) | `TuxMail-<v>-windows-amd64-setup.exe` · ARM: `…-windows-arm64-setup.exe` |
| Windows portabel / portable | `TuxMail-<v>-windows-amd64-portable.zip` |
| **Debian / Ubuntu / Mint** | `tuxmail_<v>-1_amd64.deb` → `sudo apt install ./tuxmail_<v>-1_amd64.deb` |
| **Fedora / RHEL / openSUSE** | `tuxmail-<v>-1.x86_64.rpm` → `sudo dnf install ./tuxmail-<v>-1.x86_64.rpm` |
| Linux (jede Distribution / any distro) | `TuxMail-<v>-linux-amd64.AppImage` → `chmod +x` und starten / and run |
| Linux portabel / portable | `TuxMail-<v>-linux-amd64-portable.tar.gz` |
| **Android** | `TuxMail-<v>-foss.apk` (Vorab-Builds / pre-release builds: `…-foss-debug.apk`) |
| **Server** (Ansible, systemd) | `tuxmail_<v>_linux_amd64.tar.gz` · Docker: `ghcr.io/midas1989/tuxmail:<v>` |
| Kommandozeile Windows / Windows CLI | `tuxmail_<v>_windows_amd64.zip` |

ARM: Dateien mit `arm64` (`.rpm`: `aarch64`) / use the `arm64` files.

📖 **Genaue Installationsanleitung / Installation guide:** [docs/INSTALLATION.md](https://github.com/Midas1989/tuxmail/blob/main/docs/INSTALLATION.md)
· Server: [deploy/README.md](https://github.com/Midas1989/tuxmail/blob/main/deploy/README.md)

Prüfsummen / Checksums: `sha256sum -c checksums.txt --ignore-missing` (Android: `SHA256SUMS`).

> 0.x = Vorab-Version, noch nicht digital signiert: Windows SmartScreen → „Weitere Informationen“ → „Trotzdem ausführen“.
> Pre-release, not yet code-signed: SmartScreen → "More info" → "Run anyway".
