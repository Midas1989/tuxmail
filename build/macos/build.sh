#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds the macOS desktop app (Wails + WKWebView, CGO, needs Xcode command line
# tools, so it runs on macOS only):
#   build/out/macos-<arch>/TuxMail.app
#     Contents/MacOS/tuxmail, Contents/MacOS/plugins/tuxmail-plugin-*,
#     Contents/Resources/TuxMail.icns, Contents/Info.plist
# The plugin host finds plugins/ next to the executable (internal/messenger/host).
# signal-cli is not bundled for macOS (the app offers no on-demand install there either).
# The app is ad-hoc signed (codesign -s -); with TUXMAIL_MACOS_IDENTITY set it is
# signed with that Developer ID and the hardened runtime (notarization is a CI step).
#
# Usage: build/macos/build.sh amd64|arm64
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

TUXMAIL_LINK_CLIENT_SECRET=1
[ "$(uname -s)" = Darwin ] || die "the macOS app can only be built on macOS"
goarch=${1:-$(go env GOARCH)}
case "$goarch" in amd64 | arm64) ;; *) die "unsupported architecture $goarch" ;; esac
check_frontend

out=$ROOT/build/out/macos-$goarch
app=$out/TuxMail.app
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

clangarch=$goarch
[ "$goarch" = amd64 ] && clangarch=x86_64
log "building desktop binary darwin/$goarch version $VERSION"
(cd "$ROOT" && CGO_ENABLED=1 GOOS=darwin GOARCH=$goarch \
	CGO_CFLAGS="-arch $clangarch -mmacosx-version-min=11.0" CGO_LDFLAGS="-arch $clangarch -mmacosx-version-min=11.0" \
	go build -trimpath -tags desktop,production -ldflags "$(go_ldflags)" \
	-o "$app/Contents/MacOS/tuxmail" ./cmd/tuxmail)

# Icon: iconset from the branding PNGs.
iconset=$out/TuxMail.iconset
rm -rf "$iconset"
mkdir -p "$iconset"
b=$ROOT/assets/branding/icons
cp "$b/icon-16.png" "$iconset/icon_16x16.png"
cp "$b/icon-32.png" "$iconset/icon_16x16@2x.png"
cp "$b/icon-32.png" "$iconset/icon_32x32.png"
cp "$b/icon-64.png" "$iconset/icon_32x32@2x.png"
cp "$b/icon-128.png" "$iconset/icon_128x128.png"
cp "$b/icon-256.png" "$iconset/icon_128x128@2x.png"
cp "$b/icon-256.png" "$iconset/icon_256x256.png"
cp "$b/icon-512.png" "$iconset/icon_256x256@2x.png"
cp "$b/icon-512.png" "$iconset/icon_512x512.png"
cp "$b/icon-1024.png" "$iconset/icon_512x512@2x.png"
iconutil -c icns "$iconset" -o "$app/Contents/Resources/TuxMail.icns"
rm -rf "$iconset"

numeric=$(sh "$ROOT/build/version.sh" --numeric)
sed -e "s/@VERSION@/$VERSION/g" -e "s/@NUMERIC@/$numeric/g" "$ROOT/build/macos/Info.plist" > "$app/Contents/Info.plist"

stage_plugins darwin "$goarch" "$app/Contents/MacOS/plugins"

if [ -n "${TUXMAIL_MACOS_IDENTITY:-}" ]; then
	log "signing with Developer ID (hardened runtime)"
	find "$app/Contents/MacOS/plugins" -type f -perm -u+x -exec codesign --force --timestamp --options runtime -s "$TUXMAIL_MACOS_IDENTITY" {} \;
	codesign --force --timestamp --options runtime -s "$TUXMAIL_MACOS_IDENTITY" "$app"
else
	log "ad-hoc signing (no Developer ID: Gatekeeper asks on first start, see README)"
	codesign --force --deep -s - "$app"
fi
"$app/Contents/MacOS/tuxmail" version
log "wrote $app"
