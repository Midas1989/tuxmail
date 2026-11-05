#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds the Linux desktop binary (Wails, WebKitGTK 4.1, CGO) for the host
# architecture and stages the FHS tree used by all Linux packages:
#
#   build/out/linux-<arch>/root/usr/bin/tuxmail
#   build/out/linux-<arch>/root/usr/lib/tuxmail/plugins/tuxmail-plugin-*   (messenger plugins)
#   build/out/linux-<arch>/root/usr/share/applications/org.tuxmail.TuxMail.desktop
#   build/out/linux-<arch>/root/usr/share/metainfo/org.tuxmail.TuxMail.metainfo.xml
#   build/out/linux-<arch>/root/usr/share/icons/hicolor/<size>/apps/org.tuxmail.TuxMail.png
#
# Build dependencies (Debian/Ubuntu): golang, gcc, pkg-config, libwebkit2gtk-4.1-dev, libgtk-3-dev
# The desktop binary also contains the CLI and "tuxmail serve" (see docs/RELEASE.md).
#
# Usage: build/linux/build.sh            (binary + staging)
#        TUXMAIL_SKIP_GO_BUILD=1 ...     (only re-stage an existing binary)
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

# Desktop builds are the only ones that link the Google OAuth client secret (build/lib.sh).
TUXMAIL_LINK_CLIENT_SECRET=1
arch=$(go env GOARCH)
out=$ROOT/build/out/linux-$arch
stage=$out/root
bin=$out/tuxmail

if [ "${TUXMAIL_SKIP_GO_BUILD:-}" != 1 ]; then
	check_frontend
	log "building desktop binary linux/$arch version $VERSION"
	mkdir -p "$out"
	(cd "$ROOT" && CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 \
		-ldflags "$(go_ldflags)" -o "$bin" ./cmd/tuxmail)
fi
[ -x "$bin" ] || die "missing $bin"
"$bin" version

log "staging $stage"
rm -rf "$stage"
install -Dm0755 "$bin" "$stage/usr/bin/tuxmail"
# Messenger plugins (static, CGO_ENABLED=0): /usr/lib/tuxmail/plugins is
# <exe dir>/../lib/tuxmail/plugins for the plugin host (internal/messenger/host).
stage_plugins linux "$arch" "$stage/usr/lib/tuxmail/plugins"
install -Dm0644 "$ROOT/build/linux/org.tuxmail.TuxMail.desktop" \
	"$stage/usr/share/applications/org.tuxmail.TuxMail.desktop"
date=$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%d 2>/dev/null || date -u +%Y-%m-%d)
mkdir -p "$stage/usr/share/metainfo"
sed -e "s/@VERSION@/$VERSION/" -e "s/@DATE@/$date/" \
	"$ROOT/build/linux/org.tuxmail.TuxMail.metainfo.xml" \
	> "$stage/usr/share/metainfo/org.tuxmail.TuxMail.metainfo.xml"
for size in 16 32 48 64 128 256 512; do
	install -Dm0644 "$ROOT/assets/branding/icons/icon-$size.png" \
		"$stage/usr/share/icons/hicolor/${size}x$size/apps/org.tuxmail.TuxMail.png"
done
install -Dm0644 "$ROOT/LICENSE" "$stage/usr/share/licenses/tuxmail/LICENSE"
install -Dm0644 "$ROOT/NOTICE" "$stage/usr/share/doc/tuxmail/NOTICE"
install -Dm0644 "$ROOT/README.md" "$stage/usr/share/doc/tuxmail/README.md"

if command -v desktop-file-validate > /dev/null 2>&1; then
	desktop-file-validate "$stage/usr/share/applications/org.tuxmail.TuxMail.desktop"
fi
if command -v appstreamcli > /dev/null 2>&1; then
	appstreamcli validate --no-net --explain "$stage/usr/share/metainfo/org.tuxmail.TuxMail.metainfo.xml"
fi
log "staged desktop files for linux/$arch"
