# Sicherheitsaudit A – Server-Modus, Authentifizierung, Autorisierung, HTTP/WebSocket-API

Stand: 2026-11-04 · Umfang: `internal/server` (inkl. `api/`, `push/`, `sealed/`, Hintergrund-Apps), `internal/auth`,
`internal/config`, `internal/team`, autorisierungsrelevante Teile von `internal/app`, `tuxmail serve`,
nginx-Vorlagen, KONZEPT 15.6/15.8/17.

## Zusammenfassung (Deutsch)

Das Audit kombinierte ein Bedrohungsmodell (anonymer Angreifer im Internet, angemeldeter Benutzer mit
böswilligen Absichten, Angreifer mit gestohlenem Geräte-Token, Phishing gegen Benutzer) mit Code-Review und
praktischen Angriffstests (httptest-basierte Go-Tests gegen den echten Server-Handler, Node für das Login-Skript).
Jeder Befund hat einen Regressionstest (`internal/server/audit_test.go`, `internal/auth/audit_test.go`,
`internal/netguard/netguard_test.go`); die Tests wurden zusätzlich gegen den unveränderten Stand ausgeführt
und schlagen dort fehl (Ausnahme A-08, siehe dort).

Die Grundlagen sind solide: Argon2id (RFC 9106), zeitkonstante Vergleiche, Dummy-Hash gegen
Benutzer-Enumeration, nur SHA-256-Hashes von 256-Bit-Tokens in der Datenbank, `__Host-`-Cookies mit
`HttpOnly`/`Secure`/`SameSite`, doppelter CSRF-Schutz (Token + Origin), WebSocket-Origin-Prüfung,
TOTP mit Replay-Schutz, OIDC mit state/nonce/PKCE, strikt getrennte Datenbanken pro Benutzer (keine
klassischen IDORs über IDs), signierte Bild-Proxy-URLs mit Benutzerbindung.

Gefunden und behoben wurden vor allem:

* **A-01 (Hoch):** Nach dem Löschen eines Benutzers bekam der nächste neu angelegte Benutzer dieselbe ID
  (SQLite verwendet die höchste ID wieder) – und damit die noch im Speicher laufende App bzw. das
  Datenverzeichnis des gelöschten Benutzers. Der neue Benutzer sah **alle Mails des gelöschten Benutzers**.
  IDs werden jetzt nie wiederverwendet, die App wird beim Löschen beendet.
* **A-02 (Hoch):** SSRF – jeder Benutzer konnte den Server Verbindungen zu Loopback, internen Netzen und
  Cloud-Metadaten aufbauen lassen (Mail-/DAV-/Cloud-Server, Webhooks, KI-Anbieter, Web-Push-Endpunkte …).
  Neu: zentrale Richtlinie `internal/netguard` (Prüfung nach DNS-Auflösung, auch gegen DNS-Rebinding) mit den
  Konfigurationsschlüsseln `security.allow_private_networks` (Standard `false`) und `security.allowed_networks`.
* **A-03 (Mittel):** Die Rolle `readonly` wurde nirgends durchgesetzt. Jetzt: keine schreibenden API-Aufrufe,
  höchstens Leserecht in Team-Postfächern.
* **A-04 (Mittel):** Web-Push-Abos überlebten Abmelden/Sitzungswiderruf – ein abgemeldetes Gerät bekam
  weiter Mail-Vorschauen. Jetzt endet das Abo mit der Sitzung.
* **A-05 (Mittel):** OIDC-Kontoübernahme durch Vorregistrierung: Wer per offenem Einladungslink ein Konto mit
  fremder Adresse anlegte, bekam die spätere OIDC-Anmeldung des Adressinhabers. Verknüpft wird jetzt nur
  noch mit vom Admin gesetzten bzw. verifizierten Adressen.
* **A-11 (Mittel):** Offene Weiterleitung nach dem Login (`/login?next=/\evil.example`).
* Mehrere niedrige Befunde (IPv6-Ratenbegrenzung, Sperrzähler-Umgehung über E-Mail-Alias,
  nicht-atomarer MFA-Versuchszähler, fehlendes Upload-Limit ohne nginx, Phishing-Hinweis bei der
  Gerätebestätigung).

**Wichtig für Betreiber:** Nach dem Update können Benutzer keine Server in privaten Netzen oder auf
`localhost` mehr erreichen (z. B. Mailserver im LAN, Ollama auf dem Server). Abhilfe:
`security.allowed_networks` (gezielt, z. B. `"192.168.10.5:993"`) oder
`security.allow_private_networks: true` (gilt dann für alle Benutzer). Das Ansible-Playbook trägt den
lokalen Matrix-Homeserver der Bridges automatisch ein; Gotenberg und der OIDC-Provider (vom Admin
konfiguriert) sind ausgenommen.

Verbleibende Risiken stehen am Ende des Dokuments.

## Findings

| ID | Severity | Title | Status |
|----|----------|-------|--------|
| A-01 | High | New user inherits the data and running App of a deleted user (id reuse) | Fixed |
| A-02 | High | SSRF to loopback, internal networks and metadata services via user-configured endpoints | Fixed |
| A-03 | Medium | Role `readonly` not enforced (full write access, any team role) | Fixed |
| A-04 | Medium | Web Push subscriptions survive logout, session revocation and password change | Fixed |
| A-05 | Medium | OIDC account pre-hijacking via an unverified local e-mail address | Fixed |
| A-11 | Medium | Open redirect after login (`next=/\host`) | Fixed |
| A-06 | Low | Login rate limit per IPv6 /128 (trivially bypassed with a /64) | Fixed |
| A-07 | Low | Lockout counter keyed by the login string (username and e-mail alias counted separately) | Fixed |
| A-08 | Low | MFA challenge attempt counter checked and incremented non-atomically | Fixed |
| A-09 | Low | `limits.max_upload_mb` not enforced by the server (8 GiB uploads without nginx) | Fixed |
| A-10 | Low | Device approval: no anti-phishing hint, user-code lookup not rate limited | Fixed (hardened) |
| I-01 | Info | Device tokens have full account scope (incl. admin API) for 365 days | Fixed: scopes, no administration, rotation, 90 days (`internal/auth/devicetokens.go`) |
| I-02 | Info | No idle timeout for web sessions (absolute lifetime only) | Fixed: 2 h idle, 12 h absolute |
| I-03 | Info | Sub-path deployment shares the origin with the host application | Documented |
| I-04 | Info | Push subscription upsert by endpoint can move a subscription between users | Documented |
| I-05 | Info | Gotenberg (admin endpoint) renders user HTML; its own egress is not covered | Documented |
| I-06 | Info | Egress through an environment proxy is checked before the proxy (best effort) | Documented |
| I-07 | Info | Invite registration reveals whether a username/e-mail exists | Documented |
| I-08 | Info | CSP keeps `style-src 'unsafe-inline'` | Documented |

Severity follows CVSS 3.1 reasoning (network attack vector; privileges as stated).

---

## Technical details (English)

### A-01 – Deleted user's data handed to the next new user (High, ~CVSS 7.1)

`users.id` is `INTEGER PRIMARY KEY` without `AUTOINCREMENT`, so SQLite reuses the largest id after that row is
deleted. `DELETE /api/admin/users/{id}` deleted the row (and with `keepData=false` the directory) but never
stopped the user's `app.App` in the server's pool (`appPool`, keyed by user id). The next created user got the
same id and was served the cached App – including its open store (deleted files stay readable through open
descriptors). With `keepData=true` even a server restart did not help: the new user opened the old
`data_dir/users/<id>/tuxmail.db`. Team mailboxes had the same id-reuse pattern.

* PoC: `TestAuditA01DeletedUserDataNotInherited` – victim creates a tag, admin deletes the victim, creates a new
  user; on the old code the new user got id 2 and listed `secret-of-victim`.
* Fix: migration `0004_security.sql` adds `id_highwater`; `auth.nextUserID` allocates `max(highwater, max(id))+1`
  and skips ids whose data directory still exists; `team.CreateMailbox` uses the same high-water mark;
  `handleAdminDeleteUser` drops the App before and after deleting.
* Regression: `TestAuditA01DeletedUserDataNotInherited`, `TestAuditA01UserIDsNotReused`.

### A-02 – Server-side request forgery (High, ~CVSS 7.7, PR:L, S:C)

Every user can configure outbound endpoints: IMAP/SMTP/POP3/ManageSieve hosts, EAS/EWS/JMAP/CalDAV/CardDAV,
Nextcloud/OpenCloud/Vaultwarden/WebDAV URLs, ICS subscriptions, workflow webhooks, AI provider base URLs,
Web Push endpoints, autoconfig/autodiscover, WKD/VKS. Except for the image proxy and the unsubscribe client none
of these restricted the destination, so a user could probe and talk to `127.0.0.1` (including the TuxMail
listener itself, which trusts loopback as reverse proxy), the LAN, `169.254.169.254` etc. Error messages
("server not reachable or login failed: …") made it a usable port scanner.

* PoC: `TestAuditA02SSRFBlocked` – `POST /api/dav-accounts` with an internal URL; on the old code the internal
  server received the PROPFINDs.
* Fix: new package `internal/netguard`:
  * `Policy` with `AllowPrivate` and an allowlist of `Rule`s (IP/CIDR, optional port); classification blocks
    loopback and private ranges (unless allowed), always blocks link-local (metadata), unspecified, multicast,
    broadcast, documentation/benchmark/reserved ranges, 6to4, Teredo, `fd00:ec2::254`; NAT64 addresses are judged
    by the embedded IPv4 address.
  * The check runs in `net.Dialer.Control`, i.e. on the resolved address of every connection attempt (defeats DNS
    rebinding and redirects).
  * `init()` installs the guarded dialer in `http.DefaultTransport` (covers `http.DefaultClient`, clients without
    a transport and clones of the default transport). Mail protocol dialers (IMAP, SMTP, POP3, Sieve,
    autoconfig probes) use `netguard.Dialer`. Without a policy (desktop mode) nothing changes.
  * Administrator-configured endpoints are exempt: OIDC discovery/token/JWKS use `netguard.TrustedClient`,
    Gotenberg uses `netguard.Trusted(ctx)`. A configured environment proxy may be internal; the target is then
    checked by resolving it before the proxy.
  * Config: `security.allow_private_networks` (default `false`), `security.allowed_networks` (validated);
    `server.New` installs the policy. Ansible: `tuxmail_security_allow_private_networks` (default `false`),
    `tuxmail_security_allowed_networks_extra`, the bridges' local Matrix homeserver (`127.0.0.1:<port>`) is added
    automatically. `scripts/e2e.sh` enables private networks for the loopback devserver.
* Regression: `TestAuditA02SSRFBlocked`, `TestDefaultTransportGuarded`, `TestClassify`, `TestPolicyCheck`,
  `TestParseRule`, `TestSecurityPolicy`.

### A-03 – Role `readonly` not enforced (Medium, ~CVSS 5.4)

KONZEPT 15.6 defines `readonly` as guest role for shared mailboxes. No code checked it: such users had the same
API as `user` and received whatever team role they were granted (up to `manage`).

* PoC: `TestAuditA03ReadonlyRole` – readonly user created a tag (201) on the old code.
* Fix: `appHandler` refuses non-GET/HEAD requests of readonly users except a small allowlist of read-only POSTs
  (search, workflow validation/completion, mention read markers, presence, UI preferences); `team.DirUser.ReadOnly`
  caps the effective team role at `read` in `RoleOf` and `Accessible`. Account self-service (`/api/auth/*`) is
  unaffected.

### A-04 – Push subscriptions outlive the session (Medium, ~CVSS 4.3)

`push_subscriptions.session_id` was `ON DELETE SET NULL`. After logout, "log out other sessions", admin
revocation, password change or expiry the browser kept receiving notifications – by default with sender and
preview – e.g. on a shared/kiosk computer or a stolen device whose session was revoked.

* PoC/Regression: `TestAuditA04PushEndsWithSession`.
* Fix: trigger `sessions_delete_push` (`BEFORE DELETE ON sessions`) deletes the session's subscriptions (also for
  device tokens / FCM).

### A-05 – OIDC account pre-hijacking (Medium, ~CVSS 6.5, AC:H)

On the first OIDC login with `email_verified=true` the identity was linked to any local account with the same
e-mail address. Local addresses are not verified: with an invite without fixed address the invitee chooses the
address freely. An attacker could register `ceo@example.org` with a known password; the CEO's first SSO login then
landed in the attacker's account, and everything the CEO configured (mail accounts with credentials) was readable
by the attacker. In addition, unverified e-mail claims overwrote the stored address on every login.

* PoC/Regression: `TestAuditA05OIDCPreHijack`.
* Fix: `users.email_verified` (migration; existing users `1`, users registered through invites without fixed
  address `0`); invite registration with a free address stores `0`, admin changes set `1`; linking requires
  `email_verified = 1`; OIDC-created users store the claim's verification state; unverified claims no longer
  change the stored address.

### A-11 – Open redirect after login (Medium, ~CVSS 4.7, UI:R)

`login.js` accepted any `next` starting with `/` but not `//`. Browsers treat `/\evil.example` (and
`/<TAB>/evil.example`) as protocol-relative, so `https://mail.example.org/login?next=/%5Cevil.example` sent the
user to an attacker page right after a successful login – ideal for "session expired, please re-enter your
password" phishing.

* PoC/Regression: `TestAuditA11LoginNextRedirect` (runs the shipped `next()` in Node with a list of payloads).
* Fix: reject `//` and `/\`, resolve with `new URL(n, location.origin)` and require the same origin; only path,
  query and fragment are used.

### A-06 – IPv6 rate limiting per address (Low, ~CVSS 3.7)

The in-memory login limiters were keyed by the full client IP. Any IPv6 host controls at least a /64, so password
spraying across many usernames was not limited at all (the persistent per-username lockout still applies).
Fix: `limiterKey` groups IPv6 by /64. Regression: `TestAuditA06IPv6RateLimitPrefix` (10/10 sprayed attempts passed
before, now `rate_limited` after the burst).

### A-07 – Lockout counter alias (Low, ~CVSS 3.1)

`login_throttle` was keyed by the submitted login string. Alternating username and e-mail address doubled the
number of free attempts and a successful login by username did not reset the e-mail key. Fix: after the user is
resolved, the canonical key `u:<username>` is checked and charged. Regression: `TestAuditA07ThrottleAliases`.

### A-08 – Non-atomic MFA attempt counter (Low, theoretical)

`CompleteMFA` read `attempts`, verified the code and incremented afterwards; concurrent requests could exceed the
5 guesses per challenge. SQLite's write serialisation made it hard to exploit (the PoC did not reproduce it on the
old code), but the check is now a single `UPDATE … WHERE attempts < 5 … RETURNING`. Regression:
`TestAuditA08MFAChallengeAttemptsAtomic` (30 parallel guesses → at most 5 counted, challenge exhausted).

### A-09 – Upload limit not enforced by the server (Low, ~CVSS 4.3, PR:L, A:L)

`limits.max_upload_mb` was only reported to the SPA; nginx enforced it via `client_max_body_size`. Without nginx
(Docker image, `tls.mode: file`) `/api/io/uploads` accepted 8 GiB per request (disk exhaustion). Fix: middleware
`limitBody` (Content-Length check and `MaxBytesReader` for chunked bodies); `api.writeErr` maps
`http.MaxBytesError` to 413. Regression: `TestAuditA09BodyLimit`.

### A-10 – Device authorization phishing resistance (Low)

The RFC 8628 flow is inherently phishable ("device code phishing": the attacker starts a flow and sends the victim
`verification_uri_complete`). The approval page showed only name and IP. Hardening: explicit warning, client user
agent and request time are shown, `GET /api/device/request` is rate limited (user codes have 20^8 values and are
already bound to the approving account). TOTP is required for approval when enabled. Residual risk: see below.

### Info

* **I-01 Device tokens:** a device token (365 days) can call the whole API including `/api/admin/*` for admins
  (TOTP was required at issue time if enabled). Recommendation: scoped tokens or step-up re-authentication for
  admin actions, shorter lifetime with refresh.
* **I-02 Idle timeout:** web sessions only have an absolute lifetime (`auth.session_lifetime`, default 720 h,
  Ansible 168 h). Recommendation: optional idle timeout.
* **I-03 Sub-path deployment:** the nginx snippet mounts TuxMail under `/tuxmail/` of an existing vhost (e.g.
  Nextcloud). Both share one origin: an XSS in the host application can use the TuxMail session (cookies are
  `Path=/`, CSRF token readable by same-origin scripts). nginx adds `X-Frame-Options: SAMEORIGIN`; TuxMail's own CSP
  (`frame-ancestors 'none'`) still applies. Recommend a dedicated (sub)domain.
* **I-04 Push endpoint upsert:** `push_subscriptions.endpoint` is unique; subscribing with an existing endpoint
  moves it to the caller. Exploitation requires the secret endpoint URL of another user's browser.
* **I-05 Gotenberg:** HTML mail is converted by Gotenberg's Chromium; requests made by that Chromium are outside
  netguard. Run Gotenberg without network egress (the Ansible role binds it to localhost; consider
  `--chromium-deny-list`/network isolation).
* **I-06 Environment proxy:** with `HTTP(S)_PROXY` the proxy resolves the target; TuxMail checks the resolved
  addresses beforehand (DNS rebinding between the two lookups remains possible). Restrict the proxy itself.
* **I-07 Enumeration:** `POST /api/auth/register` with a valid invite reports "username or email already in use".
  Login, OIDC and device flow do not enumerate users (dummy hash, uniform errors, throttling of unknown names).
* **I-08 CSP:** `style-src 'unsafe-inline'` remains (Svelte styles); scripts are `'self'` only.

### Verified without findings

Argon2id (t=3, m=64 MiB, p=4, bounded parallelism, rehash on parameter change); constant-time comparisons for
passwords, CSRF tokens, TOTP; session/device/MFA/invite/OIDC-state tokens are 192–256-bit random and stored as
SHA-256 only; new session on every login (no fixation), CSRF token rotated on login; logout and revocation delete
server-side; `__Host-` cookies (`Secure`, `HttpOnly` for the session, `SameSite=Lax`/`Strict`); CSRF double submit
plus Origin/Referer check on every state-changing `/api/` request incl. multipart uploads; bearer-authenticated
requests carry no ambient credentials and never fall back to cookies; WebSocket handshake checks the Origin
(Capacitor origins only with a bearer token), the bearer subprotocol is consumed and not echoed or logged; no CORS
headers; request log omits query strings and headers; `X-Forwarded-*` honoured only from `trusted_proxies`
(right-most untrusted address); TOTP ±1 step with replay protection; recovery codes single-use and hashed; OIDC
state bound to a cookie, nonce, PKCE S256, issuer/audience/expiry verified by go-oidc, local return paths only;
last-admin protection in role change, disable and delete; per-user stores make cross-user IDOR on ids impossible
(messages, attachments, CID, conversations, projects, tags, cloud links, upload/download tokens, I/O jobs, backups,
search folders, workflows); team routes check the role (read < reply < assign < manage) before running on the team
App; upload tokens are validated against path traversal, server mode refuses local paths for import/export/backup;
attachments are served with `Content-Disposition`, `nosniff` and a sandbox CSP; the image proxy verifies an HMAC
over URL, user and expiry and blocks internal addresses; provisioning secrets are sealed (X25519 + HKDF-SHA256 +
XChaCha20-Poly1305) to the key registered with the device token and only for device tokens; account APIs return no
passwords or tokens (secret references only); regular expressions use RE2 (no ReDoS); JSON bodies are limited
(1–4 MiB); `ReadHeaderTimeout`/`MaxHeaderBytes` set.

## Residual risks

* **Device code phishing** (RFC 8628 design) – mitigated by the warning, displayed client details and TOTP.
* **Allowlisting widens access for all users:** `allow_private_networks: true` lets every user reach the LAN and
  loopback services (including TuxMail itself on its listener). Prefer specific `allowed_networks` entries.
* **Team mailbox accounts** configured by administrators are also subject to the policy; internal mail servers need
  an `allowed_networks` entry.
* **Non-HTTP third-party clients** (Telegram MTProto, WhatsApp, Signal) dial their own fixed service endpoints and
  are not routed through netguard.
* **Policy changes** apply to new connections; pooled keep-alive connections opened before (only at startup) are
  not re-checked.
* **CLI deletion** (`tuxmail user delete`) while the server runs: ids are no longer reused, but the server keeps the
  deleted user's App until idle eviction/reconcile (no session can reach it).
* Items I-01 … I-08 above.
