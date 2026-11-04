#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Optional GPG signatures for release files (KONZEPT 15.4: GPG-signed Linux packages).
#
#   GPG_PRIVATE_KEY=<armored secret key> [GPG_PASSPHRASE=...] build/sign-gpg.sh FILE...
#
# Writes FILE.asc (detached, ASCII-armored) for every FILE and the public key
# as dist/tuxmail-release-key.asc. Without GPG_PRIVATE_KEY it does nothing.
# (The .deb/.rpm files additionally get embedded signatures from nfpm, see
# build/linux/package-nfpm.sh / NFPM_SIGNING_KEY_FILE.)
#
# Verify:  gpg --import tuxmail-release-key.asc && gpg --verify checksums.txt.asc checksums.txt
set -eu
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"

if [ -z "${GPG_PRIVATE_KEY:-}" ]; then
	log "GPG_PRIVATE_KEY not set - skipping GPG signatures"
	exit 0
fi
[ $# -gt 0 ] || die "usage: sign-gpg.sh FILE..."

GNUPGHOME=$(mktemp -d)
export GNUPGHOME
trap 'gpgconf --kill all > /dev/null 2>&1 || true; rm -rf "$GNUPGHOME"' EXIT
chmod 700 "$GNUPGHOME"
printf '%s\n' "$GPG_PRIVATE_KEY" | gpg --batch --quiet --import
fpr=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "fpr" { print $10; exit }')
[ -n "$fpr" ] || die "no secret key in GPG_PRIVATE_KEY"
log "signing with key $fpr"

for f in "$@"; do
	rm -f "$f.asc"
	if [ -n "${GPG_PASSPHRASE:-}" ]; then
		printf '%s' "$GPG_PASSPHRASE" | gpg --batch --yes --pinentry-mode loopback --passphrase-fd 0 \
			--local-user "$fpr" --armor --detach-sign --output "$f.asc" "$f"
	else
		gpg --batch --yes --local-user "$fpr" --armor --detach-sign --output "$f.asc" "$f"
	fi
	gpg --batch --verify "$f.asc" "$f"
	log "signed $f"
done
mkdir -p "$DIST"
gpg --batch --armor --export "$fpr" > "$DIST/tuxmail-release-key.asc"
