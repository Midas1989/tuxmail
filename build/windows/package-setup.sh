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
#    missing; without it the setup downloads it). A bundled file must pass
#    build-time verification (see verify_wv2 below), and the setup checks its
#    Authenticode signature again before running it:
#      WEBVIEW2_BOOTSTRAPPER=<file>     bundle this file
#      WEBVIEW2_BOOTSTRAPPER=download   fetch it from go.microsoft.com first
#      WEBVIEW2_BOOTSTRAPPER_SHA256 / WEBVIEW2_CA_FILE / WEBVIEW2_ALLOW_UNVERIFIED
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
	rm -rf "$work" "$syso" "$payload_dir/payload.zip" "$payload_dir/MicrosoftEdgeWebview2Setup.exe" "$payload_dir/MicrosoftEdgeWebview2Setup.exe.sha256"
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
# The bootstrapper is run by the setup (often elevated), so what gets bundled
# is verified here (re-audit ND-04); the setup verifies the Authenticode
# signature (publisher Microsoft Corporation) again before it runs it, and
# checks the bundled file against the SHA-256 recorded next to it.
#
# verify_wv2 FILE: one of
#   WEBVIEW2_BOOTSTRAPPER_SHA256=<hex>  the file must match this pin
#   osslsigncode + WEBVIEW2_CA_FILE=<pem with Microsoft's code signing roots>
#                                       `osslsigncode verify` must succeed and
#                                       name Microsoft Corporation as signer
#   WEBVIEW2_ALLOW_UNVERIFIED=1         explicit opt-out (warns)
# Anything else: the file is not bundled and the setup downloads (and
# verifies) the bootstrapper itself when WebView2 is missing.
verify_wv2() {
	_f=$1
	_got=$(sha256 "$_f" | cut -d' ' -f1)
	if [ -n "${WEBVIEW2_BOOTSTRAPPER_SHA256:-}" ]; then
		if [ "$_got" = "$(printf '%s' "$WEBVIEW2_BOOTSTRAPPER_SHA256" | tr 'A-F' 'a-f')" ]; then
			log "WebView2 bootstrapper matches WEBVIEW2_BOOTSTRAPPER_SHA256"
			return 0
		fi
		log "ERROR: WebView2 bootstrapper SHA-256 is $_got, expected $WEBVIEW2_BOOTSTRAPPER_SHA256"
		return 1
	fi
	if command -v osslsigncode > /dev/null 2>&1 && [ -n "${WEBVIEW2_CA_FILE:-}" ]; then
		_ossl=$work/osslsigncode.log
		if osslsigncode verify -in "$_f" -CAfile "$WEBVIEW2_CA_FILE" > "$_ossl" 2>&1 && grep -q 'Microsoft Corporation' "$_ossl"; then
			log "WebView2 bootstrapper: Authenticode signature of Microsoft Corporation verified (osslsigncode)"
			return 0
		fi
		log "ERROR: osslsigncode could not verify the WebView2 bootstrapper:"
		cat "$_ossl" >&2
		return 1
	fi
	if [ "${WEBVIEW2_ALLOW_UNVERIFIED:-}" = 1 ]; then
		log "WARNING: WebView2 bootstrapper bundled without build-time verification (WEBVIEW2_ALLOW_UNVERIFIED=1); sha256 $_got"
		return 0
	fi
	log "WARNING: cannot verify the WebView2 bootstrapper (set WEBVIEW2_BOOTSTRAPPER_SHA256, or WEBVIEW2_CA_FILE with osslsigncode); sha256 $_got"
	return 1
}

wv2=${WEBVIEW2_BOOTSTRAPPER:-}
if [ "$wv2" = download ]; then
	wv2=$ROOT/build/out/MicrosoftEdgeWebview2Setup.exe
	if [ ! -s "$wv2" ]; then
		log "downloading WebView2 bootstrapper"
		mkdir -p "$ROOT/build/out"
		# https only (also across redirects), TLS 1.2+, bounded size
		curl --proto '=https' --proto-redir '=https' --tlsv1.2 --max-filesize 67108864 -fsSL --retry 3 \
			-o "$wv2" "https://go.microsoft.com/fwlink/p/?LinkId=2124703" || {
			log "WARNING: download failed, the setup downloads the bootstrapper when needed"
			rm -f "$wv2"
			wv2=
		}
	fi
fi
if [ -n "$wv2" ] && ! verify_wv2 "$wv2"; then
	log "WebView2 bootstrapper NOT bundled: the setup downloads and verifies it when the runtime is missing"
	[ "${WEBVIEW2_BOOTSTRAPPER:-}" = download ] && rm -f "$wv2"
	wv2=
fi
if [ -n "$wv2" ]; then
	cp "$wv2" "$payload_dir/MicrosoftEdgeWebview2Setup.exe"
	sha256 "$wv2" | cut -d' ' -f1 > "$payload_dir/MicrosoftEdgeWebview2Setup.exe.sha256"
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
