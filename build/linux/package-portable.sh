#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Packs the Linux desktop binary as portable archive:
#   dist/TuxMail-<version>-linux-<arch>-portable.tar.gz
#     TuxMail-<version>-linux-<arch>/tuxmail, plugins/tuxmail-plugin-*,
#     portable (empty marker), data/, README-portable.txt, LICENSE, NOTICE
# Needs build/linux/build.sh first. The host needs GTK 3 + WebKitGTK 4.1.
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

arch=$(go env GOARCH)
bin=$ROOT/build/out/linux-$arch/tuxmail
[ -x "$bin" ] || die "run build/linux/build.sh first"

name=TuxMail-$VERSION-linux-$arch-portable
top=TuxMail-$VERSION-linux-$arch
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

d=$work/$top
mkdir -p "$d/data"
install -m0755 "$bin" "$d/tuxmail"
stage_plugins linux "$arch" "$d/plugins"
: > "$d/portable"
: > "$d/data/.keep"
sed -e "s/@VERSION@/$VERSION/" -e "s/@ARCH@/$arch/" "$ROOT/build/linux/README-portable.txt" > "$d/README-portable.txt"
cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$d/"
chmod -R u+rwX,go+rX "$d"

mkdir -p "$DIST"
mktar "$DIST/$name.tar.gz" "$work" "$top"
log "wrote $DIST/$name.tar.gz"
