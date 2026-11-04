#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds the Windows desktop app (Wails + WebView2, pure Go, no CGO):
#   build/out/windows-<arch>/TuxMail.exe
# with icon, manifest (per-monitor DPI v2, common controls v6) and version
# information (CompanyName/ProductName "TuxMail", copyright) from
# build/windows/winres.json, compiled by go-winres into a .syso next to main.go
# (removed again after the build, ignored by git).
#
# Runs on the Windows CI runner (Git Bash) and as cross build on Linux/macOS.
# go-winres: go install github.com/tc-hib/go-winres@v0.3.3
#
# Usage: build/windows/build.sh amd64|arm64
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

goarch=${1:-amd64}
case "$goarch" in amd64 | arm64) ;; *) die "unsupported architecture $goarch" ;; esac
command -v go-winres > /dev/null 2>&1 || die "go-winres not found (go install github.com/tc-hib/go-winres@v0.3.3)"
check_frontend

out=$ROOT/build/out/windows-$goarch
mkdir -p "$out"
cp "$ROOT/assets/branding/icons/tuxmail.ico" "$ROOT/build/windows/icon.ico"

numeric=$(sh "$ROOT/build/version.sh" --numeric)
syso=$ROOT/cmd/tuxmail/rsrc_windows_$goarch.syso
trap 'rm -f "$syso"' EXIT
log "go-winres ($goarch, file version $numeric, product version $VERSION)"
go-winres make --in "$ROOT/build/windows/winres.json" --out "$ROOT/cmd/tuxmail/rsrc" --arch "$goarch" \
	--file-version "$numeric" --product-version "$VERSION"

log "building TuxMail.exe windows/$goarch version $VERSION"
# -H windowsgui: no console window. The CLI ("tuxmail serve", "tuxmail user")
# is shipped separately as console binary in tuxmail_<version>_windows_<arch>.zip.
(cd "$ROOT" && CGO_ENABLED=0 GOOS=windows GOARCH=$goarch \
	go build -trimpath -tags desktop,production -ldflags "$(go_ldflags -H windowsgui)" \
	-o "$out/TuxMail.exe" ./cmd/tuxmail)
log "wrote $out/TuxMail.exe"
