#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# dist/TuxMail-<version>-macos-<arch>.dmg (TuxMail.app + link to /Applications)
# Needs build/macos/build.sh <arch> first. TUXMAIL_MACOS_IDENTITY signs the dmg too.
#
# Usage: build/macos/package-dmg.sh amd64|arm64
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

goarch=${1:-$(go env GOARCH)}
app=$ROOT/build/out/macos-$goarch/TuxMail.app
[ -d "$app" ] || die "missing $app (run build/macos/build.sh $goarch)"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$app" "$work/TuxMail.app"
ln -s /Applications "$work/Applications"
cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$work/"

mkdir -p "$DIST"
name=TuxMail-$VERSION-macos-$goarch.dmg
rm -f "$DIST/$name"
hdiutil create -quiet -volname "TuxMail $VERSION" -srcfolder "$work" -format UDZO -fs HFS+ "$DIST/$name"
[ -z "${TUXMAIL_MACOS_IDENTITY:-}" ] || codesign --force --timestamp -s "$TUXMAIL_MACOS_IDENTITY" "$DIST/$name"
log "wrote $DIST/$name"
