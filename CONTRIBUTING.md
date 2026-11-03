# Contributing to TuxMail

Thank you for helping! TuxMail is licensed under the **GNU Affero General Public License v3.0 or later (AGPLv3)**.
By contributing you agree that your contribution is licensed under the same terms.

## Developer Certificate of Origin (DCO)

Every commit must be signed off to certify the [Developer Certificate of Origin 1.1](https://developercertificate.org/):

```
git commit -s -m "Your message"
```

This adds `Signed-off-by: Your Name <you@example.com>` to the commit message.

## Source file headers

Every source file starts with an SPDX license identifier, for example:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later
```

Use `#` comments for YAML/shell and `<!-- -->` for HTML/Svelte markup.

## Checks before opening a pull request

```
go build ./... && go vet ./... && go test ./...
```

For the frontend: `npm run check && npm test && npm run build` in `frontend/`.
