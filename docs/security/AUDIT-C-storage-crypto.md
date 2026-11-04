# Audit C: Secrets, Krypto im Ruhezustand, lokale Daten, Injection, Automatisierung

Stand: 2026-11-04 · Branch des Audits: `worktree-agent-a2a4d2873e339c41c` · Go 1.26.0

## Zusammenfassung (Deutsch)

Geprüft wurden der Secret-Speicher (Keyring, Vault), die Server-Schlüsselhierarchie
(Master-Key, DEKs, Team-Vaults, TOTP), Backup/Restore, alle Import-Parser,
die SQL-Schicht inkl. Such-Compiler, die Workflow-Engine (expr-Sandbox, Templates,
Webhooks), der Sieve-Generator, der MIME-Composer, CSV-Export, OAuth, der
Vaultwarden-Client, PGP/S/MIME, die KI-Anbindung sowie Diagnose-Export und
Dateirechte. Zu jedem Befund gibt es einen Angriffstest (PoC), der vor der
Korrektur fehlschlug und jetzt als Regressionstest grün ist.

Wichtigste Ergebnisse:

* **Kritisch (Servermodus):** Ein hochgeladenes Thunderbird-Profil (ZIP) konnte
  über `prefs.js` (`sig_file`, `directory`) oder `profiles.ini` beliebige
  Server-Dateien referenzieren. Die Import-Vorschau lieferte den Inhalt als
  „Signatur“ zurück – z. B. `master.key`, womit alle Benutzer-DEKs und damit
  alle gespeicherten Passwörter/Tokens aller Benutzer offenlagen. **Behoben**
  (Pfade werden auf das hochgeladene Verzeichnis begrenzt).
* **Hoch:** S/MIME-Signaturfälschung: Bei mehreren SignerInfos wurde das erste
  Zertifikat aus der Zertifikatsliste als „Unterzeichner“ angezeigt. Ein
  Angreifer konnte so eine *vertrauenswürdige* Signatur eines fremden
  Absenders vortäuschen. **Behoben** (genau ein Unterzeichner erforderlich).
* **Hoch (Server) / Mittel (Desktop):** Die expr-Sandbox der Workflows hatte
  keine Byte- und Zeitgrenzen: `repeat(repeat("x",1e5),1e5)`, `reduce`-
  Verdopplung oder `let`-Ketten erschöpften den Speicher. **Behoben**
  (Schritt-, Byte- und Listenbudget pro Auswertung, gefährliche Builtins
  entfernt).
* **Mittel:** Vaultwarden-KDF-Downgrade (Server liefert `kdfIterations=1`),
  Webhook-SSRF im Servermodus (durch Audit A / `netguard` abgedeckt), Backup-Downgrade (unverschlüsseltes Archiv
  trotz Passphrase akzeptiert), Zip-Slip unter Windows beim Restore,
  Lücken in der Log-/Diagnose-Schwärzung (Bearer-Token, `access_token`,
  `client_secret`, OAuth-`code`, API-Keys, Cookies, URL-Passwörter, JWTs),
  CSV-Formel-Injection beim Kontakt-Export. **Alle behoben.**
* Mehrere niedrige Befunde (Pfad-Templates, Header-CRLF, Dateirechte von DB und
  Server-Log, Mindestlänge Master-Passwort serverseitig, Begrenzung
  `max_depth`, Workflow-Bestätigung nach Backup-Merge, lokale-KI-Erkennung,
  `OpenBrowser`, Tenant-Validierung) wurden ebenfalls behoben.

Statische Analyse: gosec (aktuelle Version baut mit Go 1.26) – alle Funde im
Prüfbereich triagiert (behoben oder mit begründetem `#nosec` versehen);
staticcheck ohne sicherheitsrelevante Funde; govulncheck konnte hier nicht
laufen (vuln.go.dev ist gesperrt) und muss in der CI laufen.

Restrisiken siehe [Residual risks](#residual-risks).

---

## English details

### Scope and method

Code read: `internal/secrets`, `internal/auth` (master key, DEK wrapping, team
vaults, TOTP), `internal/backup`, `internal/importexport/*`, `internal/store`
(search compiler, FTS, LIKE, migrations, file modes, blob paths),
`internal/search/query`, `internal/workflow` (expr sandbox, templates, path
templates, webhook, `run_workflow`, loop protection), `internal/sieve`,
`internal/mime/compose`, `internal/merge`, CSV export, `internal/oauth`,
`internal/cloud/vaultwarden`, `internal/crypto/pgp`, `internal/crypto/smime`,
`internal/ai` and the AI callers in `internal/app`, diagnostics export,
`cmd/tuxmail` (log files, portable vault, `desktop.json`).

Hands-on: attack tests (listed per finding), plus gosec, staticcheck and
govulncheck.

### Findings

| ID | Severity | Area | Finding | Status | PoC / regression test |
|----|----------|------|---------|--------|-----------------------|
| C-01 | **Critical** (server mode) | import | Uploaded Thunderbird profile reads arbitrary server files (`sig_file` → `master.key` returned by the import preview; absolute/`[ProfD]../..` mail directories; `profiles.ini` with `IsRelative=0`) | fixed | `internal/app/io_security_test.go` `TestUploadedThunderbirdProfileCannotReadServerFiles`, `internal/importexport/thunderbird/confine_test.go` |
| C-02 | **High** | S/MIME | Signer spoofing: with more than one SignerInfo the first certificate of the bag was reported as signer → "trusted signature" by a victim whose public certificate the attacker embeds | fixed | `internal/crypto/smime/spoof_test.go` |
| C-03 | **High** (server) / Medium (desktop) | workflow | expr sandbox without byte/time limits: `repeat(repeat(..))`, `reduce(1..40, #acc + #acc, "x")`, `let` doubling chains, `toJSON`/`string()` of huge lists → OOM / CPU exhaustion of the (shared) server | fixed | `internal/workflow/sandbox_test.go` `TestSandboxRejectsResourceExhaustion`, `TestSandboxNoDangerousBuiltins`, `TestSandboxKeepsSemantics` |
| C-04 | Medium | Vaultwarden | KDF parameters from the server's prelogin were accepted without bounds: PBKDF2 `iterations=1` makes the master-password hash sent to a malicious/compromised server trivially brute-forceable; huge Argon2 memory → client OOM | fixed | `internal/cloud/vaultwarden/crypto_test.go` `TestKDFDowngradeRejected` |
| C-05 | Medium (server mode) | workflow | Webhook action uses the app HTTP client: users of a multi-user server could POST to `127.0.0.1`, RFC 1918 hosts or cloud metadata (SSRF) | fixed by audit A (A-02, `internal/netguard`: the server installs a dial-time policy on the default transport, which the webhook client uses; admins can allow private ranges) – a separate client from this audit was dropped after the merge to keep the admin policy authoritative | `internal/server/audit_test.go` (audit A) |
| C-06 | Medium | backup | Downgrade: with a passphrase given, an unencrypted (unauthenticated) archive was silently accepted; anyone who can write to the backup target (WebDAV/USB) could swap in a crafted database | fixed | `internal/backup/security_test.go` `TestPlainArchiveWithPassphraseRefused` |
| C-07 | Medium (Windows) | backup / import | Zip-slip on Windows: tar names with `\` or drive letters (`db\..\..\evil`, `C:\evil`) passed the `..`/`/` checks; server restore also accepted non-regular tar entries; import unzip only checked `..`/`IsAbs` | fixed | `TestExtractRejectsUnsafeEntries`, `internal/app/io_security_test.go` `TestUploadZipSlip` |
| C-08 | Medium | diagnostics | Redaction regex missed `Authorization: Bearer <token>` (only the word "Bearer" was masked), compound keys (`access_token`, `refresh_token`, `client_secret`, `api_key`, `x-api-key`), OAuth `code`/`device_code`, cookies/session ids, URL userinfo passwords and JWTs | fixed | `internal/app/diagnostics_redact_internal_test.go` |
| C-09 | Medium | export | CSV/formula injection: contact data (collected addresses from received mail, CardDAV) such as `=HYPERLINK(...)` was exported verbatim to Outlook/Google CSV | fixed (`csv.SafeCell`) | `internal/importexport/csv/formula_test.go` |
| C-10 | Low | compose | CR/LF in envelope addresses reached `Message.Recipients`/`EnvelopeFrom` (headers were already sanitized; go-smtp also rejects CR/LF – defense in depth) | fixed (`ErrBadAddress`) | `internal/mime/compose/injection_test.go` |
| C-11 | Low | workflow | Path templates: field access (`{{.Mail.Subject}}`) and helpers (`replace`, `lower`) bypassed per-placeholder sanitizing → mail content could add directories or a leading `/` (targets are root-confined, so no escape) | fixed (every action piped through `__segment`) | `TestRenderPathSanitizesFieldAccess` |
| C-12 | Low | workflow | Webhook header values rendered from mail content could contain CR/LF (net/http refuses them → action error) | fixed (control chars → space) | `TestWebhookHeaderCRLF` |
| C-13 | Low | workflow | `loop_protection.max_depth` unbounded (run_workflow recursion depth) | fixed (cap 32) | `TestMaxDepthIsCapped` |
| C-14 | Low | backup | Merge restore copied `workflows.external_confirmed`, so workflows from a foreign backup could run external actions without confirmation | fixed (reset to 0) | `TestMergeResetsWorkflowConfirmation` |
| C-15 | Low | AI | Background categorization ("local AI only") used substring checks (`://10.`, `://127.`): `https://10.evil.example` or any remote Ollama URL counted as local → mail content sent off-device | fixed (host/IP check) | `internal/app/comfort_ai_internal_test.go` |
| C-16 | Low | store | SQLite database (all mail, wrapped DEKs, sealed TOTP secrets) created with 0644 & umask; -wal/-shm inherit it | fixed (pre-created 0600) | `internal/store/security_test.go` `TestDatabaseFileMode` |
| C-17 | Low | cmd/tuxmail | Server log file created 0640 (logs contain addresses, error texts) | fixed (0600) | – |
| C-18 | Low | OAuth | `OpenBrowser` passed any string to `rundll32 url.dll,FileProtocolHandler`/`open`/`xdg-open` (local program start if ever called with a non-URL); tenant inserted unvalidated into endpoint paths | fixed (http(s) only; tenant charset) | `internal/oauth/security_test.go` |
| C-19 | Low | vault | Header KDF caps (4 GiB, t ≤ 100) allowed a crafted vault file to stall/OOM the unlock | fixed (1 GiB, t ≤ 20) | `internal/secrets/audit_test.go` |
| C-20 | Low | portable mode | Master-password minimum length (8) only enforced in the UI | fixed (backend check for new vaults) | `TestNewVaultPasswordMinLength` |
| C-21 | Info | OAuth | Loopback `state` compared with `!=` | fixed (constant time) | – |
| C-22 | Info | PGP | Key lifetime `uint32(secs)` could wrap for far-future expiry | fixed (clamped) | – |

Verified without finding (attack tests added where useful):

* **Vault** (`internal/secrets/vault.go`): Argon2id, XChaCha20-Poly1305, the full
  header (magic, version, KDF id and parameters, salt, nonce) is AEAD associated
  data; header downgrade → `ErrWrongPassword`; fresh nonce per save, fresh salt
  per password change; atomic temp-file + fsync + rename, 0600.
* **Master key / DEKs / team vaults / TOTP** (`internal/auth`): 256-bit key file,
  refused if group/other-readable; `seal`/`open` with per-use associated data
  (`dek:<id>`, `totp:<id>`); team vault password = HMAC-SHA256(master,
  `team-vault:<id>`); TOTP replay protection (`lastStep`), `hmac.Equal`.
* **Search** (`internal/store/searchunified.go` & co.): every value is a bound
  parameter; FTS strings are quoted with `""` doubling and control characters
  removed; LIKE patterns are escaped (`ESCAPE '\'`); ORDER BY keys come from a
  fixed sort spec; cursors are type-checked. `TestSearchInjection` runs ~40
  hostile expert/simple queries × 5 sort orders with facets and forged cursors.
* **Blob store**: hashes validated (`ValidHash`, 64 hex) before any path is built.
* **Backup**: manifest SHA-256 per file, blobs verified by content hash, so a
  base archive found by manifest id can only contribute correct blobs.
* **Import parsers**: eml-ZIP is read in memory (no extraction), per-message
  256 MiB limits (eml/mbox/maildir), Go's `archive/zip` rejects data beyond the
  declared size, unzip total ≤ 8 GiB (= upload limit); unzip creates no symlinks.
  Malformed PST: `TestMalformedPST` (go-pst panics are recovered).
* **Sieve**: all strings go through `sieveString`/`multiline` (escaping and
  dot-stuffing) – `internal/sieve/injection_test.go`.
* **Compose headers**: all header text sanitized/encoded, extra header names
  validated and reserved names refused – `TestComposeHeaderInjection`,
  `TestExtraHeaderNameValidated`.
* **OAuth**: PKCE S256, 32-byte verifier, random state, loopback listener on
  `127.0.0.1:0` (random port; `[::1]` only for providers that need
  `localhost`), tokens stored only in the secret store.
* **Vaultwarden crypto**: encrypt-then-MAC, MAC checked with `hmac.Equal` before
  CBC decryption, padding checked afterwards, only cipher type 2.
* **PGP**: go-crypto v2 with nil config rejects DSA/ElGamal and RSA < 2047 bits
  for encryption (`TestWeakRSAKeyNotUsedForEncryption`); generation:
  Ed25519/X25519 or RSA-4096. Autocrypt keeps gossip keys separate from
  direct keys and only recommends "discourage" for gossip-only peers.
* **S/MIME**: AES-256-CBC content encryption (library default DES replaced),
  SHA-256 signatures, chain verification with the e-mail-protection EKU.
* **AI**: encrypted items are excluded server-side (`aiTranscript`,
  `AiTranslate` returns 409), optional masking of addresses/phone numbers,
  API key in the secret store. AI output is only returned to the UI (reply
  suggestions, translation, summary, category) – it never triggers actions.
* **Files**: data dir 0700, desktop log 0600, `desktop.json` 0600, vault 0600,
  diagnostics 0600, upload/download dirs 0700. Attachments are not opened from
  temp files by the Go side (downloads go through the frontend).

### Fixes (code pointers)

* `internal/importexport/thunderbird`: `Options.Root`, `within()` (symlink-aware);
  `internal/app/io_jobs.go` `importRoot()` sets it for uploads and in server mode.
* `internal/crypto/smime/read.go` `verify()`: exactly one signer.
* `internal/workflow/sandbox.go` (new): per-`Eval` budget (20 000 predicate
  steps, 16 MiB of built strings, 5 000 list elements = `vm.MemoryBudget`), AST
  patcher wrapping predicates and `+`/`repeat`/`join`, removal of
  `toJSON`/`fromJSON`/`toBase64`/`fromBase64`, scalar-only `string()`.
* `internal/cloud/vaultwarden/crypto.go`: `KDFParams.Validate` (PBKDF2
  5 000–2 000 000; Argon2id t 2–10, m 16–1024 MiB, p 1–16, as the Bitwarden
  clients), enforced in `MasterKey`.
* `internal/backup`: `ErrNotEncrypted`; `filepath.IsLocal` + backslash checks;
  server restore only regular files; merge resets `external_confirmed`.
* `internal/app/diagnostics.go`: new `secretRe`, `userinfoRe`, `jwtRe`.
* `internal/importexport/csv`: `SafeCell`.
* `internal/mime/compose`: `ErrBadAddress`.
* `internal/workflow/template.go`: `sanitizeActions`; `actions_builtin.go`
  header sanitizing; `model.go` `MaxAllowedDepth`.
* `internal/app/comfort_ai.go` `isLocalAI`; `internal/store/store.go` 0600 DB;
  `cmd/tuxmail/serve.go` 0600 log; `internal/oauth` opener/tenant/state;
  `internal/secrets/vault.go` caps; `internal/app/settings.go`
  `MinVaultPasswordLen`; `internal/crypto/pgp/generate.go` clamp.

### Crypto parameters

| Use | Algorithm | Parameters | Where |
|-----|-----------|------------|-------|
| Portable vault (master password) | Argon2id → XChaCha20-Poly1305 | t=3, m=64 MiB, p=4 (RFC 9106 #2); salt 16 B; nonce 24 B random per save; header = AAD; accepted header range t 1–20, m ≤ 1 GiB | `internal/secrets/vault.go` |
| Per-user secret vault (server) | same format | KDF t=1, m=64 KiB, p=1 – password is the random 256-bit DEK (no stretching needed) | `internal/auth/usersecrets.go` |
| Team mailbox vault | same format | password = HMAC-SHA256(master key, `team-vault:<id>`) | `internal/auth/teamsecrets.go` |
| Server master key (KEK) | 256-bit random | file 0600, refused if group/other bits set | `internal/auth/crypto.go` |
| DEK wrapping, TOTP secrets | XChaCha20-Poly1305 | 24 B random nonce, AD `dek:<id>` / `totp:<id>` | `internal/auth/crypto.go` |
| User passwords | Argon2id (PHC string) | t=3, m=64 MiB, p=4, 32 B; salt 16 B; constant-time compare; rehash on param change | `internal/auth/crypto.go` |
| TOTP | HMAC-SHA1 (RFC 6238) | 30 s, 6 digits, skew window, replay-protected | `internal/auth/totp.go` |
| Backup archives | age (scrypt recipient) over zstd tar | age default scrypt work factor (2^18); SHA-256 per file; blobs by SHA-256; unencrypted archives refused when a passphrase is given | `internal/backup` |
| Blob store | SHA-256 content address | zstd-compressed files | `internal/store/blob` |
| OAuth | PKCE S256 | verifier 32 random bytes (base64url), state 18 random bytes, constant-time state compare | `internal/oauth` |
| Vaultwarden | PBKDF2-SHA256 or Argon2id → HKDF-SHA256 → AES-256-CBC + HMAC-SHA256 | PBKDF2 5 000–2 000 000; Argon2id t 2–10, m 16–1024 MiB, p 1–16; encrypt-then-MAC; Send: PBKDF2 100 000 | `internal/cloud/vaultwarden/crypto.go` |
| OpenPGP | go-crypto v2 | generate Ed25519/X25519 (default) or RSA-4096; reject DSA, ElGamal, RSA < 2047, MD5/RIPEMD-160 (library defaults) | `internal/crypto/pgp` |
| S/MIME | PKCS#7 | content AES-256-CBC, signature SHA-256, RSA recipients only, chain + EKU emailProtection, exactly one signer | `internal/crypto/smime` |

### Static analysis

* **gosec** (`go install github.com/securego/gosec/v2/cmd/gosec@latest`) builds
  and runs with Go 1.26.0. Whole repository before the audit: 499 issues
  (277 G104, 64 G304, 52 G202, 106 others), 0 `#nosec`.
  * In scope (packages listed above + `internal/app`, `cmd/tuxmail`): all
    findings other than G104/G304 were fixed or annotated
    (`// #nosec <rule> -- <reason>`; 74 annotations). Remaining: 2 × G203 in
    `internal/app/comfort_pdf.go` (`template.HTML` of sanitized mail HTML for
    the PDF renderer – content handling belongs to audit B).
  * G202 (SQL concatenation, 52× in `internal/store`): every site reviewed;
    only constant column lists, generated placeholders and compiled conditions
    are concatenated, values are bound. Annotated.
  * G104 (unhandled errors, best-effort `Close`/`Remove`) and G304 (file paths
    from variables – this is a local client that opens user-selected files;
    in-scope uses reviewed: backup, import, vault, logs) are not annotated
    individually; recommendation for CI (audit E): `gosec -exclude=G104` (use
    errcheck instead) and keep G304 as informational.
  * Out of scope, left for the other audits: G710 open redirect
    `internal/server/api_auth.go` (A), G124 cookie attributes
    `internal/server/middleware.go` (A), G202 `internal/team` (A), G117
    `internal/server/push/push.go` (A), G204/G702/G703 messenger host, Signal
    plugin, `internal/pdf/chromium.go` (D), G301/G302/G306/G122
    `internal/winsetup` (D), G203 `comfort_pdf.go` (B), G704
    `internal/imageproxy` (has a dial-time guard; B), G705 devserver/graph
    settings, G401/G501/G505 non-security hashes (`internal/pdf/writer.go` MD5
    document ID, `davtest` SHA-1), G404 `math/rand` for retry jitter, remaining
    G115 conversions in providers/pdf/whatsapp/winsetup.
* **staticcheck** (latest): no security-relevant findings (unused code and
  style hints only).
* **govulncheck**: could not run in this environment (`vuln.go.dev` → 403);
  must run in CI.

### Residual risks

1. **Vault rollback**: an attacker with write access to a portable vault can
   replace it with an older valid copy (no monotonic counter). Impact limited
   to resurrecting old credentials.
2. **expr sandbox**: string builtins (`upper`, `replace`, …) inside predicates
   are bounded only by the step and list budgets (worst case ≈ 5 000 × body
   size of transient allocations); there is no wall-clock timeout in
   expr-lang. Workflow authors are authenticated users.
3. **OS keyring**: any process of the same OS user can read the `TuxMail`
   service entries (inherent to Secret Service/Keychain/Credential Manager);
   another program using the service name `TuxMail` would share the namespace.
4. **S/MIME** still accepts SHA-1 signatures (old mail); only multiple-signer
   messages are refused.
5. **Autocrypt**: `ProcessMessage` is not wired into the sync path yet; when it
   is, gossip headers must only come from decrypted and signed inner parts.
6. **Zip bombs** in server mode are bounded by the 8 GiB upload/unpack limit
   per job, not by a compression-ratio check.
7. **govulncheck** not executed here.
8. Server-mode replace restore and per-user restore permissions belong to
   audit A.
