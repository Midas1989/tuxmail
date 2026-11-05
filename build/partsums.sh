#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Cross-check of the release files between the build jobs and the publish job
# (re-audit ND-09). Every build job writes the SHA-256 of the files it uploads
# right where it built them; the publish job verifies the merged download
# against these lists before it writes (and signs) checksums.txt, so a file
# that changed on its way through the artifact store, or an unexpected extra
# file in the release directory, stops the release.
#
#   build/partsums.sh write OUT FILE...        OUT gets "<sha256>  <file name>" lines
#   build/partsums.sh verify DISTDIR PARTSDIR  PARTSDIR holds the OUT files of all jobs
set -eu

die() {
	printf 'partsums.sh: %s\n' "$*" >&2
	exit 1
}

hash_of() {
	if command -v sha256sum > /dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

case ${1:-} in
write)
	out=${2:?usage: partsums.sh write OUT FILE...}
	shift 2
	[ $# -gt 0 ] || die "no files to list"
	: > "$out"
	for f in "$@"; do
		[ -f "$f" ] || die "$f is not a regular file"
		name=$(basename "$f")
		case $name in
		*[!A-Za-z0-9._+~-]*) die "unexpected character in the file name $name" ;;
		esac
		printf '%s  %s\n' "$(hash_of "$f")" "$name" >> "$out"
	done
	LC_ALL=C sort -k2 "$out" > "$out.sorted"
	mv "$out.sorted" "$out"
	;;
verify)
	dist=${2:?usage: partsums.sh verify DISTDIR PARTSDIR}
	parts=${3:?usage: partsums.sh verify DISTDIR PARTSDIR}
	listed=$(mktemp)
	trap 'rm -f "$listed"' EXIT
	n=0
	for list in "$parts"/*.txt; do
		[ -f "$list" ] || die "no checksum lists in $parts"
		listname=$(basename "$list")
		while read -r want name; do
			[ -n "$want" ] || continue
			case $name in
			*/* | "" | .*) die "$list: unexpected file name '$name'" ;;
			esac
			[ -f "$dist/$name" ] || die "$name (listed by $listname) is missing in $dist"
			got=$(hash_of "$dist/$name")
			[ "$got" = "$want" ] || die "$name changed after the build job: $got, the job recorded $want"
			printf '%s\n' "$name" >> "$listed"
			n=$((n + 1))
		done < "$list"
	done
	dup=$(sort "$listed" | uniq -d)
	[ -z "$dup" ] || die "listed by more than one job: $dup"
	extra=$(cd "$dist" && find . -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort | grep -vxF -f "$listed" || true)
	[ -z "$extra" ] || die "files in $dist that no build job recorded: $extra"
	printf 'partsums.sh: %s files match the checksums of the build jobs\n' "$n"
	;;
*)
	die "usage: partsums.sh write OUT FILE... | verify DISTDIR PARTSDIR"
	;;
esac
