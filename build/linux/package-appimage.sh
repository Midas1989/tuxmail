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

# Sources: versioned releases, pinned by SHA-256 (re-audit ND-07; these
# were rolling "continuous" downloads before). Overrides:
#   LINUXDEPLOY / LINUXDEPLOY_PLUGIN_GTK / APPIMAGETOOL   local files
#   LINUXDEPLOY_VERSION, APPIMAGETOOL_VERSION, LINUXDEPLOY_PLUGIN_GTK_REF
#     (release tag / commit) together with LINUXDEPLOY_SHA256,
#     APPIMAGETOOL_SHA256, LINUXDEPLOY_PLUGIN_GTK_SHA256
# A download without a pin is refused unless TUXMAIL_ALLOW_UNPINNED_TOOLS=1
# (development only). Refresh the defaults deliberately: download the new
# release, compare with the upstream checksum or review the change, update
# the version and the hashes together.
LD_VERSION_DEFAULT=1-alpha-20250213-2
AT_VERSION_DEFAULT=1.9.0
GTK_REF_DEFAULT=7a3fbc31a9e5075073ff8790f26effbac5f84453
GTK_SHA_DEFAULT=b0f4cbc684a0103a9651f0955b635eaea0096b3a66c0f5a2c2aa337960375171
case $arch in
x86_64)
	LD_SHA_DEFAULT=4648f278ab3ef31f819e67c30d50f462640e5365a77637d7e6f2ad9fd0b4522a
	AT_SHA_DEFAULT=46fdd785094c7f6e545b61afcfb0f3d98d8eab243f644b4b17698c01d06083d1
	;;
aarch64)
	LD_SHA_DEFAULT=06706ac8189797dccd36bd384105892cb5e6e71f784f4df526cc958adc223cd6
	AT_SHA_DEFAULT=04f45ea45b5aa07bb2b071aed9dbf7a5185d3953b11b47358c1311f11ea94a96
	;;
esac
LINUXDEPLOY_VERSION=${LINUXDEPLOY_VERSION:-$LD_VERSION_DEFAULT}
APPIMAGETOOL_VERSION=${APPIMAGETOOL_VERSION:-$AT_VERSION_DEFAULT}
LINUXDEPLOY_PLUGIN_GTK_REF=${LINUXDEPLOY_PLUGIN_GTK_REF:-$GTK_REF_DEFAULT}
# The built-in hashes belong to the built-in versions only.
[ "$LINUXDEPLOY_VERSION" = "$LD_VERSION_DEFAULT" ] && : "${LINUXDEPLOY_SHA256:=$LD_SHA_DEFAULT}"
[ "$APPIMAGETOOL_VERSION" = "$AT_VERSION_DEFAULT" ] && : "${APPIMAGETOOL_SHA256:=$AT_SHA_DEFAULT}"
[ "$LINUXDEPLOY_PLUGIN_GTK_REF" = "$GTK_REF_DEFAULT" ] && : "${LINUXDEPLOY_PLUGIN_GTK_SHA256:=$GTK_SHA_DEFAULT}"
fetch() { # fetch URL FILE [EXPECTED_SHA256]
	if [ ! -s "$2" ]; then
		if [ -z "${3:-}" ] && [ "${TUXMAIL_ALLOW_UNPINNED_TOOLS:-}" != 1 ]; then
			die "refusing to download $1 without a SHA-256 pin (set the matching *_SHA256 variable)"
		fi
		log "downloading $1"
		curl --proto '=https' --tlsv1.2 -fsSL --retry 3 -o "$2.tmp" "$1"
		mv "$2.tmp" "$2"
	fi
	chmod +x "$2"
	sha256 "$2" >&2
	if [ -n "${3:-}" ]; then
		got=$(sha256 "$2" | cut -d' ' -f1)
		[ "$got" = "$3" ] || die "$2: SHA-256 $got does not match the pinned $3 (stale download? remove it from build/out/tools)"
	fi
}
LINUXDEPLOY=${LINUXDEPLOY:-$tools/linuxdeploy-$arch-$LINUXDEPLOY_VERSION.AppImage}
LINUXDEPLOY_PLUGIN_GTK=${LINUXDEPLOY_PLUGIN_GTK:-$tools/linuxdeploy-plugin-gtk.sh}  # the name linuxdeploy looks for
APPIMAGETOOL=${APPIMAGETOOL:-$tools/appimagetool-$arch-$APPIMAGETOOL_VERSION.AppImage}
fetch "https://github.com/linuxdeploy/linuxdeploy/releases/download/$LINUXDEPLOY_VERSION/linuxdeploy-$arch.AppImage" "$LINUXDEPLOY" "${LINUXDEPLOY_SHA256:-}"
fetch "https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/$LINUXDEPLOY_PLUGIN_GTK_REF/linuxdeploy-plugin-gtk.sh" "$LINUXDEPLOY_PLUGIN_GTK" "${LINUXDEPLOY_PLUGIN_GTK_SHA256:-}"
fetch "https://github.com/AppImage/appimagetool/releases/download/$APPIMAGETOOL_VERSION/appimagetool-$arch.AppImage" "$APPIMAGETOOL" "${APPIMAGETOOL_SHA256:-}"
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
# appimagetool sets the squashfs time itself; mksquashfs refuses it together
# with SOURCE_DATE_EPOCH ("can't be used at the same time").
env -u SOURCE_DATE_EPOCH "$APPIMAGETOOL" --no-appstream "$appdir" "$out"
chmod +x "$out"

log "smoke test: $out version"
"$out" version
