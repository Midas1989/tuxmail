#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds the NSIS installer dist/TuxMail-<version>-windows-<arch>-setup.exe
# from build/out/windows-<arch>/TuxMail.exe (or TUXMAIL_EXE) with
# build/windows/installer.nsi. Runs with makensis on Windows (Git Bash) and
# on Linux (apt install nsis).
#
# WEBVIEW2_BOOTSTRAPPER=<file>  bundle Microsoft's WebView2 bootstrapper
# WEBVIEW2_BOOTSTRAPPER=download  fetch it from go.microsoft.com first
#
# Usage: build/windows/package-installer.sh amd64|arm64
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

goarch=${1:-amd64}
exe=${TUXMAIL_EXE:-$ROOT/build/out/windows-$goarch/TuxMail.exe}
[ -f "$exe" ] || die "missing $exe (run build/windows/build.sh $goarch)"

makensis=$(command -v makensis || true)
if [ -z "$makensis" ]; then
	for p in "/c/Program Files (x86)/NSIS/makensis.exe" "/c/Program Files/NSIS/makensis.exe"; do
		[ -x "$p" ] && makensis=$p && break
	done
fi
[ -n "$makensis" ] || die "makensis not found (Windows: choco install nsis; Debian/Ubuntu: apt install nsis)"

# makensis on Windows needs Windows paths.
winpath() {
	if command -v cygpath > /dev/null 2>&1; then cygpath -w "$1"; else printf '%s' "$1"; fi
}

numeric=$(sh "$ROOT/build/version.sh" --numeric)
out=$DIST/TuxMail-$VERSION-windows-$goarch-setup.exe
mkdir -p "$DIST"

set --
wv2=${WEBVIEW2_BOOTSTRAPPER:-}
if [ "$wv2" = download ]; then
	wv2=$ROOT/build/out/MicrosoftEdgeWebview2Setup.exe
	if [ ! -s "$wv2" ]; then
		log "downloading WebView2 bootstrapper"
		mkdir -p "$ROOT/build/out"
		curl -fsSL --retry 3 -o "$wv2" "https://go.microsoft.com/fwlink/p/?LinkId=2124703" || {
			log "WARNING: download failed, installer will link to the download page instead"
			rm -f "$wv2"
			wv2=
		}
	fi
fi
if [ -n "$wv2" ]; then
	set -- "-DWEBVIEW2_BOOTSTRAPPER=$(winpath "$wv2")"
fi

# On Windows (MSYS) "/D..." arguments would be rewritten as paths; makensis also accepts "-D".
log "makensis ($goarch, $VERSION)"
MSYS2_ARG_CONV_EXCL='*' "$makensis" -V3 -WX \
	"-DVERSION=$VERSION" "-DVERSION_NUMERIC=$numeric" "-DARCH=$goarch" \
	"-DROOT=$(winpath "$ROOT")" "-DEXE=$(winpath "$exe")" "-DOUTFILE=$(winpath "$out")" \
	"$@" "$(winpath "$ROOT/build/windows/installer.nsi")"
log "wrote $out"
