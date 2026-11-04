#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# dist/TuxMail-<version>-windows-<arch>-portable.zip
#   TuxMail/TuxMail.exe, TuxMail/portable (empty marker), TuxMail/data/.keep,
#   TuxMail/README-portable.txt (de+en), TuxMail/LICENSE.txt, TuxMail/NOTICE.txt
# Needs build/windows/build.sh <arch> first (TUXMAIL_EXE overrides the input,
# e.g. a signed copy).
#
# Usage: build/windows/package-portable.sh amd64|arm64
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

goarch=${1:-amd64}
exe=${TUXMAIL_EXE:-$ROOT/build/out/windows-$goarch/TuxMail.exe}
[ -f "$exe" ] || die "missing $exe (run build/windows/build.sh $goarch)"

crlf() { sed -e 's/\r$//' -e 's/$/\r/' "$@"; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
d=$work/TuxMail
mkdir -p "$d/data"
cp "$exe" "$d/TuxMail.exe"
: > "$d/portable"
: > "$d/data/.keep"
sed -e "s/@VERSION@/$VERSION/g" -e "s/@ARCH@/$goarch/g" "$ROOT/build/windows/README-portable.txt" | crlf > "$d/README-portable.txt"
crlf "$ROOT/LICENSE" > "$d/LICENSE.txt"
crlf "$ROOT/NOTICE" > "$d/NOTICE.txt"

mkdir -p "$DIST"
name=TuxMail-$VERSION-windows-$goarch-portable.zip
mkzip "$DIST/$name" "$work" TuxMail
log "wrote $DIST/$name"
