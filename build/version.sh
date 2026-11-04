#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Single source of the TuxMail version: the file VERSION in the repository root.
#
#   build/version.sh                 version for this build:
#                                      release build (TUXMAIL_RELEASE=1 or a v* tag in CI): 0.1.0
#                                      otherwise:                                     0.1.0-dev.<sha7>
#   build/version.sh --base          content of VERSION (0.1.0)
#   build/version.sh --numeric       four-part Windows version (0.1.0.<n>, n = CI run number or 0)
#   build/version.sh --check-tag v0.1.0
#                                    fails unless tag == v$(VERSION) and mobile/package.json
#                                    has the same version (release consistency check)
#
# TUXMAIL_VERSION overrides the computed version (e.g. for local test packages).
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
base=$(tr -d ' \t\r\n' < "$root/VERSION")

if ! printf '%s\n' "$base" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "VERSION must be MAJOR.MINOR.PATCH, got '$base'" >&2
	exit 1
fi

json_version() {
	# Reads "version" from a package.json without requiring node.
	sed -n 's/^  "version": *"\([^"]*\)".*/\1/p' "$1" | head -n 1
}

case "${1:-}" in
--base)
	echo "$base"
	;;
--numeric)
	n=${GITHUB_RUN_NUMBER:-0}
	echo "$base.$((n % 65535))"
	;;
--check-tag)
	tag=${2:?usage: version.sh --check-tag vX.Y.Z}
	rc=0
	if [ "$tag" != "v$base" ]; then
		echo "::error::tag $tag does not match VERSION $base" >&2
		rc=1
	fi
	mobile=$(json_version "$root/mobile/package.json")
	if [ "$mobile" != "$base" ]; then
		echo "::error::mobile/package.json version $mobile does not match VERSION $base" >&2
		rc=1
	fi
	frontend=$(json_version "$root/frontend/package.json")
	if [ "$frontend" != "$base" ]; then
		# The web frontend version is informational only.
		echo "::warning::frontend/package.json version $frontend differs from VERSION $base" >&2
	fi
	[ $rc -eq 0 ] && echo "version $base: tag, VERSION and mobile/package.json agree"
	exit $rc
	;;
"")
	if [ -n "${TUXMAIL_VERSION:-}" ]; then
		echo "$TUXMAIL_VERSION"
	elif [ "${TUXMAIL_RELEASE:-}" = 1 ] || { [ "${GITHUB_REF_TYPE:-}" = tag ] && [ "${GITHUB_REF_NAME:-}" = "v$base" ]; }; then
		echo "$base"
	else
		sha=${GITHUB_SHA:-$(git -C "$root" rev-parse HEAD 2>/dev/null || echo local)}
		echo "$base-dev.$(printf '%s' "$sha" | cut -c1-7)"
	fi
	;;
*)
	echo "usage: $0 [--base | --numeric | --check-tag vX.Y.Z]" >&2
	exit 2
	;;
esac
