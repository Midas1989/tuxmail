#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds dist/TuxMail-<version>-linux-<arch>.AppImage from the staged desktop
# build (build/linux/build.sh), bundling GTK 3 and WebKitGTK 4.1:
#
#  1. AppDir from the staged FHS tree + WebKitGTK helper processes
#     (WebKitWebProcess, WebKitNetworkProcess, injected bundle)
#  2. linuxdeploy + linuxdeploy-plugin-gtk copy all libraries, GTK modules,
#     pixbuf loaders and schemas and set RPATHs; AppRun = build/linux/AppRun
#  3. the bundled libwebkit2gtk is patched so that it starts the bundled
#     helpers ("/usr/lib/<triplet>/webkit2gtk-4.1" -> "././/lib/<triplet>/...",
#     resolved relative to $APPDIR/usr, see AppRun)
#  4. appimagetool packs the AppImage
#
# Tools are downloaded into build/out/tools (override with LINUXDEPLOY,
# LINUXDEPLOY_PLUGIN_GTK, APPIMAGETOOL pointing to local files).
# Host packages (Debian/Ubuntu): libwebkit2gtk-4.1-dev libgtk-3-dev dpkg-dev file
# desktop-file-utils, plus FUSE or APPIMAGE_EXTRACT_AND_RUN=1 (set below).
set -euo pipefail
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

goarch=$(go env GOARCH)
case "$goarch" in
amd64) arch=x86_64 ;;
arm64) arch=aarch64 ;;
*) die "unsupported architecture $goarch" ;;
esac
triplet=$arch-linux-gnu
stage=$ROOT/build/out/linux-$goarch/root
[ -x "$stage/usr/bin/tuxmail" ] || die "run build/linux/build.sh first"

export APPIMAGE_EXTRACT_AND_RUN=1 ARCH=$arch
tools=$ROOT/build/out/tools
mkdir -p "$tools"

# Sources: rolling "continuous" builds (these projects publish no versioned
# release assets). The SHA-256 of each download is printed for the build log.
fetch() { # fetch URL FILE
	if [ ! -s "$2" ]; then
		log "downloading $1"
		curl -fsSL --retry 3 -o "$2.tmp" "$1"
		mv "$2.tmp" "$2"
	fi
	chmod +x "$2"
	sha256 "$2" >&2
}
LINUXDEPLOY=${LINUXDEPLOY:-$tools/linuxdeploy-$arch.AppImage}
LINUXDEPLOY_PLUGIN_GTK=${LINUXDEPLOY_PLUGIN_GTK:-$tools/linuxdeploy-plugin-gtk.sh}
APPIMAGETOOL=${APPIMAGETOOL:-$tools/appimagetool-$arch.AppImage}
fetch "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-$arch.AppImage" "$LINUXDEPLOY"
fetch "https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/master/linuxdeploy-plugin-gtk.sh" "$LINUXDEPLOY_PLUGIN_GTK"
fetch "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-$arch.AppImage" "$APPIMAGETOOL"
# linuxdeploy finds plugins named linuxdeploy-plugin-<name>(.sh) in PATH or next to itself.
PATH="$(dirname "$LINUXDEPLOY_PLUGIN_GTK"):$PATH"
export PATH

appdir=$ROOT/build/out/linux-$goarch/TuxMail.AppDir
rm -rf "$appdir"
mkdir -p "$appdir"
cp -a "$stage/." "$appdir/"
# The messenger plugins are static binaries: keep them away from linuxdeploy
# (dependency deployment / RPATH patching) and add them afterwards.
rm -rf "$appdir/usr/lib/tuxmail"

# WebKitGTK helper processes and injected bundle.
wk_src=/usr/lib/$triplet/webkit2gtk-4.1
[ -x "$wk_src/WebKitWebProcess" ] || die "$wk_src/WebKitWebProcess missing (libwebkit2gtk-4.1-dev installed?)"
wk_dst=$appdir/usr/lib/$triplet/webkit2gtk-4.1
mkdir -p "$wk_dst"
cp -a "$wk_src/." "$wk_dst/"

log "linuxdeploy ($arch)"
DEPLOY_GTK_VERSION=3 "$LINUXDEPLOY" \
	--appdir "$appdir" \
	--executable "$appdir/usr/bin/tuxmail" \
	--desktop-file "$appdir/usr/share/applications/org.tuxmail.TuxMail.desktop" \
	--icon-file "$appdir/usr/share/icons/hicolor/256x256/apps/org.tuxmail.TuxMail.png" \
	--deploy-deps-only "$wk_dst" \
	--deploy-deps-only "$wk_dst/injected-bundle" \
	--custom-apprun "$ROOT/build/linux/AppRun" \
	--plugin gtk

if [ -d "$stage/usr/lib/tuxmail" ]; then
	cp -a "$stage/usr/lib/tuxmail" "$appdir/usr/lib/"
	log "added messenger plugins: $(find "$appdir/usr/lib/tuxmail/plugins" -maxdepth 1 -printf '%f ')"
fi

# Point the bundled WebKit to the bundled helpers (same-length replacement).
lib=$(find "$appdir/usr/lib" -maxdepth 1 -name 'libwebkit2gtk-4.1.so.0*' -type f | head -n 1)
[ -n "$lib" ] || die "libwebkit2gtk-4.1.so.0 was not bundled"
old="/usr/lib/$triplet/webkit2gtk-4.1"
new="././/lib/$triplet/webkit2gtk-4.1"
grep -c -a -F "$old" "$lib" > /dev/null || die "string $old not found in $lib"
OLD=$old NEW=$new perl -pi -e 's/\Q$ENV{OLD}\E/$ENV{NEW}/g' "$lib"
log "patched $(basename "$lib"): $old -> $new"

out=$DIST/TuxMail-$VERSION-linux-$goarch.AppImage
mkdir -p "$DIST"
rm -f "$out"
log "appimagetool -> $out"
"$APPIMAGETOOL" --no-appstream "$appdir" "$out"
chmod +x "$out"

log "smoke test: $out version"
"$out" version
