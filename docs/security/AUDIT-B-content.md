<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Audit B – Untrusted message content → client

**English** · [Deutsch](#deutsch-zusammenfassung)

Scope B of the TuxMail security audit: everything an attacker can put into an
e-mail, chat message, calendar invitation, vCard, attachment or e-invoice and
have the client parse, render, store or act upon. Goal: no script execution,
no data exfiltration (IP/tracking leaks included), no spoofing, no
denial-of-service from a single message.

Other scopes are covered by separate auditors: server/auth (A),
storage/crypto-at-rest/injection (C), desktop/installer/Android/plugins (D),
CI/dynamic (E).

## What was reviewed

`internal/mime/{parse,sanitize,compose,quote}`, `internal/app`
(rendering/rendercache/messages/items/conversations, attachment & cid
endpoints, iTIP, contacts, PDF/print), `internal/imageproxy`,
`internal/crypto/{pgp,smime}`, `internal/pim/{ical,itip,vcard,rrule}`,
`internal/einvoice`, `internal/unsubscribe`, `internal/messenger`, the PDF
renderers (`internal/pdf`), and the Svelte frontend components that render
message content (`MailFrame`, `MailWindows`, `ReadingPane`, `StandaloneMail`,
`OriginalMailDialog`, `MessageBubble`, link previews, `InvitationCard`,
contacts/projects screens), `frontend/src/pwa/sw.js` and the Wails desktop
asset handler.

## Architecture (what already protects us)

The core design is sound and defeats the large majority of the test corpus
out of the box:

- **HTML mail never runs in the app origin.** The Go sanitizer
  (`internal/mime/sanitize`) is an allowlist sanitizer built on bluemonday
  plus a pre-pass that drops `script/iframe/object/embed/form/svg/math/
  base/meta/link/template/...` and *all* namespaced (foreign-content) nodes,
  strips event handlers, filters `style`/`<style>` declaration by declaration
  against a property allowlist, rejects CSS escapes (`\`), `expression()`,
  `@import`, `-moz-binding`, `position:fixed/sticky`, and rewrites every
  resource URL. It wraps the result in a document with a strict CSP
  (`default-src 'none'; script-src 'none'; object-src 'none'; frame-src
  'none'; base-uri 'none'; form-action 'none'`). The output is then rendered
  only inside a sandboxed iframe (`MailFrame.svelte`) with its own CSP.
- **The autoHeight iframe uses `sandbox="allow-same-origin"` without
  `allow-scripts`.** Confirmed safe: a sandbox without `allow-scripts`
  disables scripting regardless of same-origin, and the srcdoc CSP has
  `script-src 'none'`. There is no script path. The main-window-XSS →
  Wails-binding escalation does not exist because mail content is never
  rendered outside the sandboxed frame.
- **Remote images** are blocked by default, routed through the signed
  SSRF-guarded image proxy (`internal/imageproxy`: post-DNS address checks,
  no SVG, size/type limits) when the user allows them; the sender never sees
  the reader's IP/cookies/referrer.
- **PGP/S-MIME EFAIL defenses** are in place: `decryptMessage` reads
  `md.UnverifiedBody` and returns an error on an MDC/AEAD integrity failure
  (never releases unauthenticated plaintext); the decrypted part replaces the
  whole body tree (isolated rendering); inline-PGP text outside the armored
  block is dropped from the rendered content; signature status is mapped
  precisely (`valid/invalid/unknown_key/expired_key/revoked_key`,
  S/MIME `trusted/untrusted/mismatch/expired/invalid`).
- Attachments are served with `Content-Disposition: attachment` unless the
  type is a known-safe inline type (images except SVG, PDF, audio, video),
  with `X-Content-Type-Options: nosniff` and a sandbox CSP; the SPA CSP and
  the service worker (`/api/*` never cached, same-origin only, no custom
  postMessage trust) are correct.

The fuzzed sanitizer corpus (`sanitize_test.go`, OWASP cheat sheet +
mXSS/namespace confusion) passes, and the attacker-host network invariant
holds for the whole corpus.

## Findings

| ID | Severity | Area | Issue | Status |
|----|----------|------|-------|--------|
| B-01 | High | iTIP REPLY | A forged `METHOD:REPLY` could change (or add) *any* attendee's PARTSTAT on the organizer's stored event; the REPLY was applied without binding the attendee to the sending address. | Fixed |
| B-02 | High | iTIP REQUEST/CANCEL | Any sender who knew a UID could update/move or cancel a stored event; incoming REQUEST/ADD/CANCEL were applied without checking the ORGANIZER against the sender. | Fixed |
| B-03 | High | Frontend links | Links from untrusted content (invitation `meetingUrl`, vCard URLs, chat link previews, project/cloud URLs) were rendered as `<a href>` in the **app window** with no scheme filtering; `javascript:`/`data:`/`file:` would run in / open from the app origin (Wails bindings) if the CSP ever regressed. | Fixed |
| B-04 | Medium | PDF/print | Chromium/Gotenberg rendered the mail HTML *and* the surrounding header block in one document; a mail's `<style>`/`position:absolute` could overlay or hide the real From/Subject shown above it (spoofing the archived/printed record). | Fixed |
| B-05 | Medium | Remote images | `MailFrame`/`MailWindows`/`StandaloneMail`/`OriginalMailDialog` restored blocked remote images to their **direct** URLs on "Load images" (frontend `restoreRemoteImages`), bypassing the proxy → IP/tracking leak. Only the ReadingPane used the proxy. | Fixed |
| B-06 | Medium | CSS exfiltration | CSS values could smuggle a remote fetch through URL-taking functions other than `url()` (`src()`, `image()`, `image-set()`, `cross-fade()`) and the `url (…)` (space) token — bypassing url() rewriting/blocking. | Fixed |
| B-07 | Medium | vCard photo | A contact's `PHOTO` with a remote URI was rendered directly as the avatar `src`, fetching an attacker URL when the contact is viewed (tracking, IP leak). | Fixed |
| B-08 | Medium | ICS DoS | `RRULE` expansion walked every FREQ period from DTSTART; `DTSTART:19000101, FREQ=SECONDLY` (or an impossible rule) kept a CPU busy for a very long time on *every* calendar render of the invitation. | Fixed |
| B-09 | Low | mailto | `mailto:` links kept non-standard query fields; `?attach=/etc/passwd` is honoured by some mail programs to attach local files. | Fixed |
| B-10 | Low/Med | e-invoice | Invoice XML/PDF (parsed automatically for every mail) had no depth/element/size/inflate bounds → billion-laughs-style and Flate-bomb DoS (XXE itself is not possible: `encoding/xml` never resolves external entities). | Fixed |
| B-11 | Low | PDF render | Chromium PDF rendering disabled network via a dead proxy but did not disable the loopback proxy bypass or JavaScript; defense-in-depth only (page is sanitized and offline-CSP'd). | Hardened |
| B-12 | Low | filenames | Attachment filenames were cleaned of path separators and control chars but not of Unicode bidi overrides (RTLO `invoice‮fdp.exe`), trailing dots/spaces, or length. Messenger media names were not cleaned at all. | Fixed |

No Critical findings: the central "mail HTML runs only in a scriptless
sandboxed iframe, never near the Wails bindings" invariant holds.

### B-01 / B-02 – iTIP spoofing

`internal/app/pim_itip.go`, `internal/pim/itip/itip.go`

A received iTIP object is matched to the user's stored events by UID only.
Before this audit, `applyIncoming` applied:

- `REPLY` via `itip.ApplyReply`, which set the PARTSTAT of every ATTENDEE in
  the message — so a message from `mallory@evil` could set
  `jonas@…;PARTSTAT=DECLINED` on the organizer's event;
- `REQUEST`/`ADD` and `CANCEL` to the stored copies without checking who
  sent them — anyone who learned a UID (they travel in replies, read
  receipts, published calendars) could rewrite the event's time/body or
  cancel it in the victim's calendar.

Fix:

- New `itip.ApplyReplyFrom(stored, reply, sender)` applies only the ATTENDEE
  line whose address equals the sending address; `ApplyReply` keeps the old
  behaviour for trusted callers. REPLY is additionally only applied to events
  the user actually organizes (`me[Organizer(...)]`).
- REQUEST/ADD and CANCEL are only applied to stored objects whose ORGANIZER
  equals the sender (`fromOrganizer`).
- `Invitation.OrganizerMismatch` is set when the claimed ORGANIZER
  (REPLY/COUNTER: the answering attendee) differs from the sending address;
  `InvitationCard.svelte` shows a warning (`calendar.invite.spoofWarning`).
  Display names and the ORGANIZER property are attacker-chosen, so the card
  derives trust from the envelope sender, not from the ICS.

Regression: `internal/app/pim_test.go` (`TestPIMInvitations`) now feeds a
forged REPLY (from a different attendee) and a spoofed CANCEL (not from the
organizer) and asserts they are flagged and not applied.

### B-03 – Unsafe links in the app window

`frontend/src/lib/safeurl.ts` (new) + call sites. `safeHref()` returns the URL
only for `https/http/mailto/tel/sip(s)` and rejects control characters;
everything else renders no `href`. Applied to `InvitationCard`, `Reminders`,
`EventDialog`, `MessageBubble` (link preview), `InfoPanel`, `ContactsScreen`,
`ProjectsScreen`. Newsletter/subscription "open link" buttons now go through
`openExternal()` (which already enforces `^https?:`) instead of a raw
`window.open`. On the Go side `linkURL()` (`internal/app/safeurl.go`) filters
`CalendarEvent`/`Invitation` `url`/`meetingUrl` at the source.

Regression: `frontend/src/lib/safeurl.test.ts`.

### B-04 / B-12-scope – Style confinement in PDF/print

`internal/mime/sanitize` gains an `Options.Scope`: every selector of the
mail's `<style>` sheet is prefixed with `.<scope>` (html/body/:root collapse
to the scope element, leading combinators are neutralised). `comfort_pdf.go`
now wraps each mail in `<div class="tm-mail tm-mail-N">` (with
`position:relative; overflow:hidden; contain:paint`) and renders the fragment
scoped, so a mail cannot restyle or overlay the header block printed above it.
The print/PDF HTML also carries an explicit CSP.

Regression: `internal/mime/sanitize/audit_test.go` (`TestStyleSheetScope`).

### B-05 – Remote images always through the proxy

`frontend/src/lib/remote.ts`: `restoreRemoteImages` is no longer used by the
mail frame; `proxiedRemoteHtml()` fetches the server-rendered HTML (remote
images already rewritten to signed proxy URLs). `MailFrame.buildSrcdoc` no
longer restores direct URLs. All four readers load remote content through the
proxy.

Regression: `frontend/src/lib/mailframe.test.ts`.

### B-06 – CSS URL-function exfiltration

`internal/mime/sanitize/css.go`: `src(`, `image(`, `image-set(` variants,
`cross-fade(` and the `url (` (whitespace) token are added to the dangerous
substring list / rejected. Regression:
`internal/mime/sanitize/audit_test.go` (`TestCSSURLFunctionsBlocked`).

### B-07 – vCard remote photo

`internal/app/pim_contacts.go`: a `PHOTO` URI is only used as the avatar when
it is an inline `data:image/*` (not SVG); remote photo URIs are dropped
(`isDataImage`).

### B-08 – RRULE expansion DoS

`internal/pim/rrule/expand.go`: `build()` now takes the window end, caps
`UNTIL` to it, and rejects a rule whose number of FREQ periods between
DTSTART and the window end exceeds `maxPeriods` (1,000,000) with
`ErrTooExpensive`. Regression: `internal/pim/rrule/audit_dos_test.go`.

### B-09 – mailto fields

`internal/mime/sanitize/sanitize.go`: `safeMailto()` keeps only
`to/cc/bcc/subject/body/in-reply-to`. Regression:
`internal/mime/sanitize/audit_test.go` (`TestMailtoFieldsFiltered`).

### B-10 – e-invoice resource bounds

`internal/einvoice/einvoice.go`: inflate budget per stream (16 MiB) and per
PDF (64 MiB), XML input ≤ 16 MiB, depth ≤ 64, elements ≤ 100 000.
`encoding/xml` is confirmed not to resolve DTDs/external entities (no XXE).
Regression: `internal/einvoice/audit_test.go` (verified to hang/OOM against
the pre-fix code).

### B-11 – Chromium PDF hardening

`internal/pdf/chromium.go`: added `--proxy-bypass-list=<-loopback>`,
`--disable-javascript`, `--blink-settings=scriptEnabled=false`. The input is
already sanitized and carries an offline CSP; this is defense-in-depth.

### B-12 – Attachment / media filenames

`internal/mime/parse/parse.go`: `CleanFilename` (exported) additionally
strips Unicode bidi/format controls (RTLO and friends), C1 controls, trailing
dots/spaces, and bounds the length to 255 bytes keeping the extension.
`internal/messenger/ingest.go` now runs incoming media names through it.
Regression: `internal/mime/parse/audit_filename_test.go`.

## Residual risk / not changed

- **Phishing link detection** stays heuristic (domain mismatch / IP /
  userinfo). Punycode/IDN homograph display is not normalised here; the links
  still open externally (never in the app) and are shown with a warning. A
  follow-up could add IDN-homograph flagging to `phishingHint`.
- **Signature UI depth**: the backend status model is correct and the
  decrypted body is isolated. A dedicated "partially signed" badge (signed
  part + unsigned trailer) for PGP/MIME is out of this change's scope; the
  inline-PGP unprotected-text case already drops the trailer from the body.
- **End-to-end Playwright corpus**: a headless-browser corpus runner
  (load each malicious mail through the devserver/mock, assert no
  `window.__pwned`, no attacker-host request, no navigation) was prepared but
  is not committed, because this environment's Svelte+vitest browser harness
  is flaky under load (see the pre-existing `capacitor.test.ts` cold-compile
  timeout). The invariants it would check are covered by the Go sanitizer
  fuzz/corpus tests and the new frontend unit tests
  (`mailframe.test.ts`, `safeurl.test.ts`). Recommend wiring a
  `scripts/e2e.sh`-style content-XSS stage in scope E.
- **Gotenberg** runs server-side in the operator's container; its own network
  isolation (no access to internal services) is a deployment concern (scope
  A/D). The HTML handed to it is sanitized and offline-CSP'd.

---

## Deutsch (Zusammenfassung)

Prüfbereich B des Sicherheitsaudits: alle Inhalte, die ein Angreifer per
E-Mail, Chat, Termineinladung, vCard, Anhang oder E-Rechnung einschleusen
kann. Ziel: keine Skriptausführung, kein Datenabfluss (auch keine
Tracking-/IP-Lecks), keine Fälschung, kein Denial-of-Service durch eine
einzelne Nachricht.

Die Grundarchitektur ist tragfähig: HTML-Mails werden ausschließlich im
Go-Sanitizer (Allowlist + strenge CSP) gesäubert und nur in einem
**sandboxed iframe ohne Skripte** dargestellt – nie im App-Fenster mit Zugriff
auf die Wails-Bindings. Der autoHeight-iframe nutzt `allow-same-origin` **ohne**
`allow-scripts`; es gibt keinen Skriptpfad (als sicher bestätigt). Remote-Bilder
laufen über den signierten, SSRF-geschützten Bild-Proxy. Die EFAIL-Abwehr
(MDC/AEAD-Pflicht, isoliertes Rendern entschlüsselter Teile, kein Anzeigen
ungesignierter Teile als signiert) ist korrekt.

Gefundene und behobene Punkte (keine kritischen):

- **B-01/B-02 (hoch):** Gefälschte iTIP-`REPLY`/`REQUEST`/`CANCEL` konnten
  fremde Antwortstatus setzen bzw. Termine im Kalender des Opfers ändern oder
  absagen, weil ORGANIZER/Attendee nicht gegen den Absender geprüft wurden.
  Jetzt absendergebunden; die Einladungskarte warnt bei Abweichung.
- **B-03 (hoch):** Links aus unsicheren Inhalten wurden ohne Schema-Filter als
  `<a href>` im App-Fenster gerendert (`javascript:`/`data:`/`file:`). Neu:
  `safeHref()` / `linkURL()`.
- **B-04 (mittel):** Mail-CSS konnte im PDF/Druck den echten Kopfbereich
  überdecken – Stylesheet wird jetzt auf die Mail eingegrenzt.
- **B-05 (mittel):** Remote-Bilder wurden teils direkt (ohne Proxy)
  nachgeladen → IP-/Tracking-Leck. Jetzt immer über den Proxy.
- **B-06 (mittel):** CSS-Exfiltration über `src()/image()/image-set()/
  cross-fade()/url (…)` geschlossen.
- **B-07 (mittel):** Remote-`PHOTO` einer vCard wird nicht mehr geladen.
- **B-08 (mittel):** DoS durch bösartige `RRULE` (z. B. `FREQ=SECONDLY` ab
  1900) begrenzt.
- **B-09 (niedrig):** `mailto:`-Felder wie `?attach=` entfernt.
- **B-10 (niedrig/mittel):** E-Rechnungs-XML/PDF gegen Billion-Laughs- und
  Flate-Bomben begrenzt (XXE ist mit `encoding/xml` ohnehin nicht möglich).
- **B-11 (niedrig):** Chromium-PDF zusätzlich ohne Loopback-Proxy-Umgehung und
  ohne JavaScript (Tiefenverteidigung).
- **B-12 (niedrig):** Dateinamen gegen RTLO-Spoofing, Steuerzeichen,
  End-Punkte/Leerzeichen und Überlänge gehärtet; auch für Messenger-Medien.

Restrisiko: IDN-Homograph-Erkennung und ein dediziertes „teilweise signiert“-
Abzeichen sind nicht Teil dieser Änderung; ein Playwright-Korpuslauf wird für
Prüfbereich E empfohlen (die hiesige Browser-Testumgebung ist unter Last
instabil – siehe der bereits bestehende Timeout in `capacitor.test.ts`).
