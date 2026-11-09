#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Optional platforms in the publish job (release.yml): a build job of an optional
# platform (macOS) may fail without stopping the release. Before
# build/partsums.sh verifies everything, this drops what such a job left
# incomplete, so the release goes out without that platform instead of failing:
#   - a checksum list partsums-<platform>-*.txt whose files are not all there
#     (the list goes, and the files of it that are there),
#   - files of the platform (*-<platform>-*) that no list of it records.
# Mandatory platforms are never touched: a missing file of theirs still stops
# the release in partsums.sh verify.
# Prints one Markdown line per dropped or kept item (job summary).
#
#   build/release-optional.sh DISTDIR PARTSDIR PLATFORM
set -eu

dist=${1:?usage: release-optional.sh DISTDIR PARTSDIR PLATFORM}
parts=${2:?usage: release-optional.sh DISTDIR PARTSDIR PLATFORM}
platform=${3:?usage: release-optional.sh DISTDIR PARTSDIR PLATFORM}
case $platform in
*[!a-z0-9]* | "") echo "release-optional.sh: bad platform '$platform'" >&2 && exit 2 ;;
esac

keep=$(mktemp)
trap 'rm -f "$keep"' EXIT
for list in "$parts"/partsums-"$platform"-*.txt; do
	[ -f "$list" ] || continue
	complete=1
	while read -r _sum name; do
		[ -n "$name" ] || continue
		case $name in */* | .*) complete=0 ;; esac
		[ -f "$dist/$name" ] || complete=0
	done < "$list"
	if [ "$complete" = 1 ]; then
		cut -d' ' -f3- < "$list" >> "$keep"
		echo "- $platform: $(basename "$list" .txt) complete"
	else
		echo "- $platform: $(basename "$list" .txt) incomplete, left out of the release"
		while read -r _sum name; do
			case $name in */* | .* | "") ;; *) rm -f "$dist/$name" ;; esac
		done < "$list"
		rm -f "$list"
	fi
done
for f in "$dist"/*-"$platform"-*; do
	[ -f "$f" ] || continue
	name=$(basename "$f")
	if ! grep -qxF -- "$name" "$keep"; then
		rm -f "$f"
		echo "- $platform: $name has no checksum list, left out of the release"
	fi
done
[ -s "$keep" ] || echo "- $platform: no files in this release"
