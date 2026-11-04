# Third-party notices

TuxMail is licensed under AGPL-3.0-or-later. It bundles or links the
following third-party components that are not plain Go module
dependencies listed in `go.mod` / `package.json` (whose licenses are
recorded there and in the module caches).

## Spell-checking dictionaries (`internal/spell/dicts`)

The dictionaries are embedded gzip-compressed and unchanged.

| File | Source | Version | License |
|---|---|---|---|
| `de_DE.aff.gz`, `de_DE.dic.gz` | npm package `dictionary-de` 3.0.0 (igerman98 by Björn Jacke, Hunspell build) | 20161207 | GPL-2.0 OR GPL-3.0 — **used under GPL-3.0**, which is compatible with AGPL-3.0 (AGPL-3.0 §13 / GPL-3.0 §13). Notice: `internal/spell/dicts/de_DE.LICENSE`, GPL-3.0 text: `internal/spell/dicts/GPL-3.0.txt`. |
| `en_US.aff.gz`, `en_US.dic.gz` | npm package `dictionary-en` 4.0.0 (SCOWL / wordlist.sourceforge.net, en_US Hunspell build 2020.12.07) | 2020.12.07 | MIT AND BSD (see `internal/spell/dicts/en_US.LICENSE`) |

Modified versions of the German dictionary may only be distributed under
the GPL (igerman98 licence terms); TuxMail does not modify it.

## Fonts (`golang.org/x/image/font/gofont`)

The PDF/A renderer (`internal/pdf`) embeds the Go fonts (Go Regular, Go
Bold) by Bigelow & Holmes Inc., licensed under the BSD-3-Clause-style Go
license (see `golang.org/x/image/font/gofont/ttfs/README`).

## Go modules added for the mail comfort features

| Module | Version | License | Used for |
|---|---|---|---|
| `github.com/smallstep/pkcs7` | v0.2.3 | MIT | S/MIME CMS signing, encryption, verification (`internal/crypto/smime`) |
| `software.sslmate.com/src/go-pkcs12` | v0.7.4 | BSD-3-Clause | PKCS#12 import/export (.p12/.pfx) |
| `golang.org/x/image` | v0.27.0 | BSD-3-Clause | TrueType parsing and Go fonts for the PDF renderer |

All of them require Go ≤ 1.24 (`go 1.14`, `go 1.19`, `go 1.23`).
`pdfcpu` was evaluated for the PDF/A enrichment but its current releases
require Go 1.26; TuxMail uses an own minimal PDF writer instead.

## signal-cli and Java runtime (Signal messenger plugin)

The Signal plugin (`internal/messenger/plugins/signal`) starts **signal-cli**
as a separate program and talks to it over JSON-RPC on stdin/stdout; it is
neither linked into nor derived from TuxMail ("aggregate" in the sense of
GPL-3.0 §5 / AGPL-3.0 §5). Release packages ship it unmodified in
`plugins/signal-cli/` (`/usr/lib/tuxmail/plugins/signal-cli` in .deb/.rpm and
the AppImage, `/usr/local/lib/tuxmail/plugins/signal-cli` in the Docker
image); builds without it download the same pinned files on demand into
`<data dir>/plugins/signal-cli`. Versions, download URLs and SHA-256
checksums: `internal/messenger/signalcli/pins.env`.

| Component | Where | Source | License |
|---|---|---|---|
| signal-cli (JVM build, or GraalVM native build on Linux x86_64) | `plugins/signal-cli/` (`signal-cli` or `bin/`, `lib/`) | https://github.com/AsamK/signal-cli (release archives) | GPL-3.0-or-later; text shipped as `plugins/signal-cli/LICENSE-signal-cli.txt` (from `internal/messenger/signalcli/licenses/GPL-3.0.txt`), sources and checksums in `plugins/signal-cli/NOTICE.txt` |
| libsignal-client and the other libraries bundled by signal-cli | inside `lib/` resp. the native binary | https://github.com/signalapp/libsignal and the signal-cli distribution | AGPL-3.0-only (libsignal) and the licenses listed in the signal-cli distribution |
| Eclipse Temurin JRE 25 (Linux ARM64, Windows x64/ARM64 packages) | `plugins/signal-cli/jre/` | https://adoptium.net, https://github.com/adoptium/temurin25-binaries (release archives, unmodified) | GPL-2.0-only WITH Classpath-exception-2.0, plus the third-party notices in `jre/legal/` and `jre/NOTICE` (shipped with the runtime) |

Windows on ARM64 uses the x64 runtime under the x64 emulation of Windows 11,
because libsignal-client ships no native Windows ARM64 library. The complete
corresponding source of signal-cli and Temurin is available from the
upstream projects at the pinned versions; on request the TuxMail project
provides it as well (GPL-3.0 §6, GPL-2.0 §3).
