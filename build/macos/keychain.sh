#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# CI only (release.yml, macos-desktop): imports the "Developer ID Application"
# certificate into a temporary keychain and exports TUXMAIL_MACOS_IDENTITY for
# build.sh and package-dmg.sh. The release job deletes the keychain afterwards.
#   MACOS_CERTIFICATE_P12       base64 of the .p12 (certificate + private key)
#   MACOS_CERTIFICATE_PASSWORD  its password
set -eu
: "${MACOS_CERTIFICATE_P12:?}" "${MACOS_CERTIFICATE_PASSWORD:?}" "${RUNNER_TEMP:?}" "${GITHUB_ENV:?}"

kc=$RUNNER_TEMP/tuxmail-signing.keychain-db
p12=$RUNNER_TEMP/tuxmail-signing.p12
kcpass=$(openssl rand -hex 24)
trap 'rm -f "$p12"' EXIT
printf '%s' "$MACOS_CERTIFICATE_P12" | base64 --decode > "$p12"
security create-keychain -p "$kcpass" "$kc"
security set-keychain-settings -lut 3600 "$kc"
security unlock-keychain -p "$kcpass" "$kc"
security import "$p12" -k "$kc" -P "$MACOS_CERTIFICATE_PASSWORD" -T /usr/bin/codesign
security set-key-partition-list -S apple-tool:,apple: -s -k "$kcpass" "$kc" > /dev/null
# shellcheck disable=SC2046 # the existing search list, one keychain per word
security list-keychains -d user -s "$kc" $(security list-keychains -d user | tr -d '"')
identity=$(security find-identity -v -p codesigning "$kc" | awk -F'"' '/Developer ID Application/ { print $2; exit }')
[ -n "$identity" ] || {
	echo "::error::no 'Developer ID Application' identity in MACOS_CERTIFICATE_P12" >&2
	exit 1
}
echo "TUXMAIL_MACOS_IDENTITY=$identity" >> "$GITHUB_ENV"
echo "signing identity: $identity"
