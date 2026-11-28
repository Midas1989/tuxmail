#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Test of build/release-optional.sh together with build/partsums.sh verify:
# a complete macOS job stays, an incomplete or missing one is dropped and the
# release still verifies, a missing mandatory file still stops it.
#   build/release-optional.test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
fail() {
	echo "FAIL: $*" >&2
	exit 1
}

setup() {
	rm -rf "$t/dist" "$t/parts"
	mkdir -p "$t/dist" "$t/parts"
	echo linux > "$t/dist/tuxmail_1.1.0_linux_amd64.tar.gz"
	"$here/partsums.sh" write "$t/parts/partsums-server-linux-amd64.txt" "$t/dist/tuxmail_1.1.0_linux_amd64.tar.gz"
	for a in arm64 amd64; do
		echo "mac $a" > "$t/dist/TuxMail-1.1.0-macos-$a.dmg"
		"$here/partsums.sh" write "$t/parts/partsums-macos-desktop-$a.txt" "$t/dist/TuxMail-1.1.0-macos-$a.dmg"
	done
}

# 1. both macOS jobs complete: everything stays
setup
"$here/release-optional.sh" "$t/dist" "$t/parts" macos > /dev/null
if [ ! -f "$t/dist/TuxMail-1.1.0-macos-arm64.dmg" ] || [ ! -f "$t/dist/TuxMail-1.1.0-macos-amd64.dmg" ]; then
	fail "complete macOS files were removed"
fi
"$here/partsums.sh" verify "$t/dist" "$t/parts" > /dev/null || fail "verify after complete macOS"

# 2. amd64 failed after its list was uploaded (dmg missing): its list goes, arm64 stays
setup
rm "$t/dist/TuxMail-1.1.0-macos-amd64.dmg"
"$here/release-optional.sh" "$t/dist" "$t/parts" macos | grep -q "macos-desktop-amd64 incomplete" || fail "incomplete list not reported"
[ ! -f "$t/parts/partsums-macos-desktop-amd64.txt" ] || fail "incomplete list kept"
[ -f "$t/dist/TuxMail-1.1.0-macos-arm64.dmg" ] || fail "arm64 removed"
"$here/partsums.sh" verify "$t/dist" "$t/parts" > /dev/null || fail "verify after incomplete amd64"

# 3. a dmg without list (list upload failed): the dmg goes
setup
rm "$t/parts/partsums-macos-desktop-arm64.txt"
"$here/release-optional.sh" "$t/dist" "$t/parts" macos | grep -q "no checksum list" || fail "unlisted dmg not reported"
[ ! -f "$t/dist/TuxMail-1.1.0-macos-arm64.dmg" ] || fail "unlisted dmg kept"
"$here/partsums.sh" verify "$t/dist" "$t/parts" > /dev/null || fail "verify after unlisted dmg"

# 4. the macOS job failed completely: no files, the release still verifies
setup
rm "$t"/dist/*macos* "$t"/parts/*macos*
"$here/release-optional.sh" "$t/dist" "$t/parts" macos | grep -q "no files" || fail "missing macOS not reported"
"$here/partsums.sh" verify "$t/dist" "$t/parts" > /dev/null || fail "verify without macOS"

# 5. mandatory files are never dropped: a missing server archive still stops the release
setup
rm "$t/dist/tuxmail_1.1.0_linux_amd64.tar.gz"
"$here/release-optional.sh" "$t/dist" "$t/parts" macos > /dev/null
[ -f "$t/parts/partsums-server-linux-amd64.txt" ] || fail "mandatory list removed"
if "$here/partsums.sh" verify "$t/dist" "$t/parts" > /dev/null 2>&1; then fail "missing mandatory file passed"; fi

echo "release-optional.test.sh: ok"
