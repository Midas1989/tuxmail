#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Writes Contents/Info.plist from build/macos/Info.plist (build.sh).
# CFBundleVersion: at most three numbers (Apple), so the base version X.Y.Z;
# CFBundleShortVersionString is the full version (may carry -dev+<commit>).
# Both are checked before they go into sed, so no character can change the
# expression or the XML. Runs on any system (test: infoplist.test.sh).
#
# Usage: build/macos/infoplist.sh VERSION BASEVERSION OUT
set -eu
version=${1:?usage: infoplist.sh VERSION BASEVERSION OUT}
base=${2:?usage: infoplist.sh VERSION BASEVERSION OUT}
out=${3:?usage: infoplist.sh VERSION BASEVERSION OUT}
printf '%s' "$base" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || { echo "infoplist.sh: unexpected base version '$base'" >&2 && exit 1; }
printf '%s' "$version" | grep -Eqx '[0-9A-Za-z.+-]+' || { echo "infoplist.sh: unexpected version '$version'" >&2 && exit 1; }
sed -e "s|@VERSION@|${version}|g" -e "s|@BUNDLEVERSION@|${base}|g" "$(dirname "$0")/Info.plist" > "$out"
