#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds the TuxMail setup program (cmd/tuxmail-setup, Wails + WebView2, pure
# Go, cross-compiles from Linux):
#
#   dist/TuxMail-<version>-windows-<arch>-setup.exe
#
# 1. payload: TuxMail.exe (build/out/windows-<arch>/ or TUXMAIL_EXE), the
#    messenger plugins next to it (plugins/ or TUXMAIL_PLUGINS_DIR),
#    LICENSE.txt and NOTICE.txt -> cmd/tuxmail-setup/payload/payload.zip (zstd,
#    cmd/tuxmail-setup/mkpayload; embedded with go:embed: one self-contained exe)
# 2. optional WebView2 Evergreen bootstrapper (installed when the runtime is
#    missing; without it the setup downloads it):
#      WEBVIEW2_BOOTSTRAPPER=<file>     bundle this file
#      WEBVIEW2_BOOTSTRAPPER=download   fetch it from go.microsoft.com first
# 3. go-winres: icon, manifest (asInvoker - the setup elevates itself only for
#    "all users"; without a manifest Windows would treat "*setup*.exe" as
#    legacy installer and always ask for administrator rights), version info
# 4. go build -tags production -H windowsgui
# The payload and .syso files are removed again afterwards.
#
# Usage: build/windows/package-setup.sh amd64|arm64
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

goarch=${1:-amd64}
case "$goarch" in amd64 | arm64) ;; *) die "unsupported architecture $goarch" ;; esac
exe=${TUXMAIL_EXE:-$ROOT/build/out/windows-$goarch/TuxMail.exe}
[ -f "$exe" ] || die "missing $exe (run build/windows/build.sh $goarch)"
command -v go-winres > /dev/null 2>&1 || die "go-winres not found (go install github.com/tc-hib/go-winres@v0.3.3)"

crlf() { sed -e 's/\r$//' -e 's/$/\r/' "$@"; }

pkg=$ROOT/cmd/tuxmail-setup
payload_dir=$pkg/payload
syso=$pkg/rsrc_windows_$goarch.syso
work=$(mktemp -d)
cleanup() {
	rm -rf "$work" "$syso" "$payload_dir/payload.zip" "$payload_dir/MicrosoftEdgeWebview2Setup.exe"
}
trap cleanup EXIT
cleanup
mkdir -p "$work"

# ---------------------------------------------------------------- payload
p=$work/payload
mkdir -p "$p"
cp "$exe" "$p/TuxMail.exe"
TUXMAIL_PLUGINS_DIR=${TUXMAIL_PLUGINS_DIR:-$(dirname "$exe")/plugins} stage_plugins windows "$goarch" "$p/plugins"
crlf "$ROOT/LICENSE" > "$p/LICENSE.txt"
crlf "$ROOT/NOTICE" > "$p/NOTICE.txt"
# Zip with zstd entries (about a third smaller than deflate), sorted names and
# the commit time as modification time (reproducible).
(cd "$ROOT" && go run ./cmd/tuxmail-setup/mkpayload -o "$payload_dir/payload.zip" -mtime "$SOURCE_DATE_EPOCH" "$p")

# ---------------------------------------------------------------- WebView2 bootstrapper
wv2=${WEBVIEW2_BOOTSTRAPPER:-}
if [ "$wv2" = download ]; then
	wv2=$ROOT/build/out/MicrosoftEdgeWebview2Setup.exe
	if [ ! -s "$wv2" ]; then
		log "downloading WebView2 bootstrapper"
		mkdir -p "$ROOT/build/out"
		curl -fsSL --retry 3 -o "$wv2" "https://go.microsoft.com/fwlink/p/?LinkId=2124703" || {
			log "WARNING: download failed, the setup downloads the bootstrapper when needed"
			rm -f "$wv2"
			wv2=
		}
	fi
fi
if [ -n "$wv2" ]; then
	cp "$wv2" "$payload_dir/MicrosoftEdgeWebview2Setup.exe"
	log "bundled WebView2 bootstrapper ($(du -h "$wv2" | cut -f1))"
fi

# ---------------------------------------------------------------- resources + build
numeric=$(sh "$ROOT/build/version.sh" --numeric)
cp "$ROOT/assets/branding/icons/tuxmail.ico" "$ROOT/build/windows/icon.ico"
log "go-winres setup ($goarch, file version $numeric, product version $VERSION)"
go-winres make --in "$ROOT/build/windows/setup-winres.json" --out "$pkg/rsrc" --arch "$goarch" \
	--file-version "$numeric" --product-version "$VERSION"

out=$DIST/TuxMail-$VERSION-windows-$goarch-setup.exe
mkdir -p "$DIST"
log "building setup windows/$goarch version $VERSION"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=windows GOARCH=$goarch \
	go build -trimpath -tags production -ldflags "-s -w -H windowsgui -X main.version=$VERSION" \
	-o "$out" ./cmd/tuxmail-setup)
log "wrote $out ($(du -h "$out" | cut -f1))"
