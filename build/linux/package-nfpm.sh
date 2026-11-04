#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Packs the staged desktop build (build/linux/build.sh) as .deb and .rpm with nfpm:
#   dist/tuxmail_<version>_<arch>.deb          (version "0.1.0-dev.x" -> "0.1.0~dev.x")
#   dist/tuxmail-<version>-1.<x86_64|aarch64>.rpm
#
# nfpm:  go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0
# Optional signing: NFPM_SIGNING_KEY_FILE=<armored private key> NFPM_PASSPHRASE=<passphrase>
set -eu
# shellcheck source=../lib.sh
. "$(dirname "$0")/../lib.sh"

arch=$(go env GOARCH)
STAGE=$ROOT/build/out/linux-$arch/root
[ -x "$STAGE/usr/bin/tuxmail" ] || die "run build/linux/build.sh first"
command -v nfpm > /dev/null 2>&1 || die "nfpm not found (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0)"
export STAGE VERSION
export ARCH="$arch"
export NFPM_SIGNING_KEY_FILE="${NFPM_SIGNING_KEY_FILE:-}"

mkdir -p "$DIST"
for packager in deb rpm; do
	log "nfpm $packager ($arch, $VERSION)"
	nfpm package --config "$ROOT/build/linux/nfpm.yaml" --packager "$packager" --target "$DIST/"
done

if command -v dpkg-deb > /dev/null 2>&1; then
	for f in "$DIST"/tuxmail_*_"$arch".deb; do
		dpkg-deb --info "$f"
		dpkg-deb --contents "$f"
	done
fi
if command -v rpm > /dev/null 2>&1; then
	for f in "$DIST"/tuxmail-*.rpm; do
		rpm -qip "$f" 2> /dev/null
		rpm -qp --requires "$f" 2> /dev/null
		rpm -qlp "$f" 2> /dev/null
	done
fi
