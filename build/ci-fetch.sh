#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Downloads a CI tool over https with retries and refuses to keep it unless
# its SHA-256 matches the pin (re-audit ND-07: the tools run with the
# repository checked out and, in release jobs, next to signing secrets).
#
#   build/ci-fetch.sh URL SHA256 OUTFILE
#
# Refresh a pin deliberately: download the new release, compare the hash with
# the upstream checksum file, change URL and hash together.
set -eu

url=${1:?usage: ci-fetch.sh URL SHA256 OUTFILE}
want=${2:?usage: ci-fetch.sh URL SHA256 OUTFILE}
out=${3:?usage: ci-fetch.sh URL SHA256 OUTFILE}
[ "${#want}" -eq 64 ] || {
	echo "ci-fetch.sh: SHA256 must be 64 lower-case hex digits" >&2
	exit 2
}
case $want in
*[!0-9a-f]*)
	echo "ci-fetch.sh: SHA256 must be 64 lower-case hex digits" >&2
	exit 2
	;;
esac

tmp=$out.part
trap 'rm -f "$tmp"' EXIT
n=1
while :; do
	if curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --connect-timeout 20 --max-time 300 -o "$tmp" "$url"; then
		if command -v sha256sum > /dev/null 2>&1; then
			got=$(sha256sum "$tmp" | cut -d' ' -f1)
		else
			got=$(shasum -a 256 "$tmp" | cut -d' ' -f1)
		fi
		if [ "$got" = "$want" ]; then
			mv "$tmp" "$out"
			echo "ci-fetch.sh: $out ok ($got)" >&2
			exit 0
		fi
		# A wrong hash is not transient: do not retry.
		echo "ci-fetch.sh: $url has SHA-256 $got, pinned $want" >&2
		exit 1
	fi
	if [ "$n" -ge 4 ]; then
		echo "ci-fetch.sh: download of $url failed" >&2
		exit 1
	fi
	echo "ci-fetch.sh: download attempt $n failed, retrying" >&2
	sleep $((n * 5))
	n=$((n + 1))
done
