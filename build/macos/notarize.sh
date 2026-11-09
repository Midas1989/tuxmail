#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# CI only (release.yml, macos-desktop): Apple notarization of a Developer-ID
# signed .dmg with an App Store Connect API key, then stapling the ticket so
# Gatekeeper opens it also without a network.
#   MACOS_NOTARY_KEY        base64 of the AuthKey_<id>.p8
#   MACOS_NOTARY_KEY_ID     key id
#   MACOS_NOTARY_ISSUER_ID  issuer id
#
# Usage: build/macos/notarize.sh dist/TuxMail-<v>-macos-<arch>.dmg
set -eu
dmg=${1:?usage: notarize.sh FILE.dmg}
[ -f "$dmg" ] || { echo "notarize.sh: $dmg missing" >&2 && exit 1; }
: "${MACOS_NOTARY_KEY:?}" "${MACOS_NOTARY_KEY_ID:?}" "${MACOS_NOTARY_ISSUER_ID:?}"
[ -n "${TUXMAIL_MACOS_IDENTITY:-}" ] || { echo "notarize.sh: only a Developer-ID signed app can be notarized" >&2 && exit 1; }

key=$(mktemp)
trap 'rm -f "$key"' EXIT
printf '%s' "$MACOS_NOTARY_KEY" | base64 --decode > "$key"
xcrun notarytool submit "$dmg" --key "$key" --key-id "$MACOS_NOTARY_KEY_ID" --issuer "$MACOS_NOTARY_ISSUER_ID" --wait --timeout 30m
xcrun stapler staple "$dmg"
xcrun stapler validate "$dmg"
