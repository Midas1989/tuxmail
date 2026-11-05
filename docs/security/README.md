<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Sicherheit / Security – Übersicht

Meldung von Schwachstellen: [SECURITY.md](../../SECURITY.md) (GitHub Private Vulnerability Reporting).

## Audit-Berichte / Audit reports

| Audit | Bereich / Scope | Bericht / Report |
|---|---|---|
| A | Server, Authentifizierung, Sitzungen, API-Autorisierung / server, auth, sessions, API authorization | [AUDIT-A-server.md](AUDIT-A-server.md) |
| B | Inhalte: HTML-Mails, XSS, Sanitizer, Remote-Inhalte / content, XSS, sanitizer | [AUDIT-B-content.md](AUDIT-B-content.md) |
| C | Speicher, Kryptografie, Injection (SQL, IMAP, Sieve, …) / storage, crypto, injection | [AUDIT-C-storage-crypto.md](AUDIT-C-storage-crypto.md) |
| D | Desktop, Installer, Android, Messenger-Plugins / desktop, installer, Android, plugins | [AUDIT-D-endpoint.md](AUDIT-D-endpoint.md) |
| E | CI-Sicherheitsscans und dynamischer Black-Box-Pentest / CI scanning and dynamic pentest | [PENTEST-E-dynamic.md](PENTEST-E-dynamic.md) |
| Re-Audit | Vollständige Wiederholung aller Bereiche A–E, 5.11.2026 / full re-audit of all scopes | [REAUDIT-2026-11-05.md](REAUDIT-2026-11-05.md) |


## Kontinuierliche Prüfungen / Continuous checks

Workflow [`.github/workflows/security.yml`](../../.github/workflows/security.yml) – bei Push auf `main` und
`claude/**`, bei Pull Requests, wöchentlich (Montag 03:17 UTC) und manuell:

| Job | Werkzeug / Tool | Bricht ab bei / Fails on |
|---|---|---|
| govulncheck | `golang.org/x/vuln` (source mode, auch `-tags desktop,production` für Windows) | erreichbaren Schwachstellen / reachable vulns |
| gosec + staticcheck | gosec (SARIF), staticcheck | staticcheck `SA*` (ohne SA1019/SA4006); gosec nur Bericht |
| CodeQL | Go, JavaScript/TypeScript (`security-extended`) | Bericht (Code Scanning) |
| npm audit | `frontend/`, `mobile/` | Runtime-Abhängigkeiten: jede Meldung; alle: ab *moderate* |
| osv-scanner | `go.mod`, `package-lock.json`; geprüfte Ausnahmen mit Begründung und Ablaufdatum in [`osv-scanner.toml`](../../osv-scanner.toml) | bekannten Schwachstellen |
| gitleaks | ganze Git-Historie, Allowlist [`.gitleaks.toml`](../../.gitleaks.toml) | Geheimnissen |
| Trivy | Dateisystem (vuln, misconfig, secret) + gebautes Image `build/docker/Dockerfile`, SBOM (SPDX) | HIGH/CRITICAL mit Fix |
| licenses | go-licenses (AGPL-Kompatibilität; `forbidden`/`unknown` schlagen an, TuxMail selbst und `segmentio/asm` (MIT-0) ausgenommen) | nur Bericht / report only |
| fuzz | alle `func Fuzz*` (automatisch ermittelt), je 60 s | Absturz (Eingabe als Artefakt) |
| pentest | [`security/pentest/harness.py`](../../security/pentest/harness.py) gegen frisch gestarteten Server | informativ / informational |

**Code Scanning bei privatem Repository:** SARIF-Uploads brauchen GitHub Advanced Security. Ohne GHAS werden
sie übersprungen und die SARIF-Dateien als Artefakte gespeichert. Mit GHAS die Repository-Variable
`CODE_SCANNING_ENABLED=true` setzen. Öffentliche Repositories laden immer hoch.

## Pentest lokal / running the pentest locally

```bash
go build -o /tmp/tuxmail ./cmd/tuxmail
security/pentest/run-server.sh /tmp/tuxmail /tmp/tuxmail-pentest     # 127.0.0.1:18080, admin + alice + bob
python3 security/pentest/harness.py --base http://127.0.0.1:18080 --json /tmp/pentest.json
kill "$(cat /tmp/tuxmail-pentest/server.pid)"
```

Nur gegen eigene Testinstanzen ausführen. Only run against your own test instances.
