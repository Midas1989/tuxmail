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
