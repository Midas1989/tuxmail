#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds the server/CLI binary (pure Go, CGO_ENABLED=0, no desktop window) and
# packs the release archive:
#
#   build/package-server.sh linux   amd64|arm64  -> dist/tuxmail_<version>_linux_<arch>.tar.gz
#   build/package-server.sh windows amd64|arm64  -> dist/tuxmail_<version>_windows_<arch>.zip
#
# The Linux archive layout is a contract with deploy/ansible (roles/tuxmail):
# the binary "tuxmail" at the top level, plus LICENSE, NOTICE and README.md.
# The messenger plugins (build/plugins.sh) are in plugins/ next to it, where
# the plugin host finds them (Ansible unpacks the whole archive).
# Requires a built frontend in web/dist (go generate ./web).
set -eu
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"

goos=${1:?usage: package-server.sh linux|windows amd64|arm64}
goarch=${2:?usage: package-server.sh linux|windows amd64|arm64}
case "$goos/$goarch" in
linux/amd64 | linux/arm64 | windows/amd64 | windows/arm64) ;;
*) die "unsupported target $goos/$goarch" ;;
esac

check_frontend
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

exe=tuxmail
[ "$goos" = windows ] && exe=tuxmail.exe

log "building server binary $goos/$goarch version $VERSION"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
	go build -trimpath -ldflags "$(go_ldflags_server)" -o "$work/$exe" ./cmd/tuxmail)
cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$ROOT/README.md" "$work/"
stage_plugins "$goos" "$goarch" "$work/plugins"
entries="$exe LICENSE NOTICE README.md"
[ -d "$work/plugins" ] && entries="$entries plugins"

mkdir -p "$DIST"
name=tuxmail_${VERSION}_${goos}_${goarch}
if [ "$goos" = linux ]; then
	chmod 0755 "$work/$exe"
	# shellcheck disable=SC2086 # fixed file names without spaces
	mktar "$DIST/$name.tar.gz" "$work" $entries
	log "wrote $DIST/$name.tar.gz"
else
	# shellcheck disable=SC2086
	mkzip "$DIST/$name.zip" "$work" $entries
	log "wrote $DIST/$name.zip"
fi
