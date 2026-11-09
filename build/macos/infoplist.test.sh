#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Test of build/macos/infoplist.sh: three-part CFBundleVersion, full display
# version, no mailto handler (the app does not handle mailto: on macOS), and
# versions with sed/XML characters are refused.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
fail() {
	echo "FAIL: $*" >&2
	exit 1
}

"$here/infoplist.sh" 1.1.0-dev+g1a2b3c 1.1.0 "$t/Info.plist"
grep -q '<key>CFBundleVersion</key><string>1.1.0</string>' "$t/Info.plist" || fail "CFBundleVersion"
grep -q '<key>CFBundleShortVersionString</key><string>1.1.0-dev+g1a2b3c</string>' "$t/Info.plist" || fail "short version"
if grep -q '@[A-Z]*@' "$t/Info.plist"; then fail "placeholder left"; fi
if grep -qi mailto "$t/Info.plist"; then fail "mailto handler declared"; fi
if command -v python3 > /dev/null 2>&1; then
	python3 -I -c 'import plistlib,sys; plistlib.load(open(sys.argv[1],"rb"))' "$t/Info.plist" || fail "not a valid plist"
fi
for bad in '1.1.0|x' '1.1.0/x' '1.1.0&x' '1.1.0<x>' '1.1.0 x' ''; do
	if "$here/infoplist.sh" "$bad" 1.1.0 "$t/bad.plist" 2> /dev/null; then fail "accepted version '$bad'"; fi
done
for bad in 1.1.0.7 1.1 '1.1.0-rc1'; do
	if "$here/infoplist.sh" 1.1.0 "$bad" "$t/bad.plist" 2> /dev/null; then fail "accepted bundle version '$bad'"; fi
done
echo "infoplist.test.sh: ok"
