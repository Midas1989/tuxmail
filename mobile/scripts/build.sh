#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Build the TuxMail Android app: frontend build -> cap sync -> Gradle.
#
#   scripts/build.sh                     # fossDebug APK
#   scripts/build.sh fossRelease         # unsigned/signed release APK (signing via env, see README)
#   scripts/build.sh playDebug playRelease bundlePlayRelease
#   SKIP_WEB=1 scripts/build.sh          # reuse an existing frontend/dist
#
# Version: single source mobile/package.json ("version"); Gradle derives versionCode.
# Requires: Node 22+, JDK 21, Android SDK (ANDROID_HOME or android/local.properties).

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo="$(cd "$here/.." && pwd)"
tasks=("$@")
[ ${#tasks[@]} -eq 0 ] && tasks=(fossDebug)

if [ "${SKIP_WEB:-0}" != "1" ]; then
  echo "==> frontend build"
  (cd "$repo/frontend" && { [ -d node_modules ] || npm ci; } && npm run build:capacitor)
fi

echo "==> capacitor sync"
cd "$here"
[ -d node_modules ] || npm ci
npx cap sync android

gradle_tasks=()
gms=true
for t in "${tasks[@]}"; do
  case "$t" in
    assemble* | bundle* | lint* | test* | clean) gradle_tasks+=("$t") ;;
    *) gradle_tasks+=("assemble${t^}") ;;
  esac
done
# Pure foss builds (F-Droid): keep the Google Services Gradle plugin off the classpath.
if ! printf '%s\n' "${tasks[@]}" | grep -qi play; then gms=false; fi

echo "==> gradle ${gradle_tasks[*]} (tuxmail.gms=$gms)"
cd "$here/android"
if [ -x ./gradlew ]; then gradle_cmd=(./gradlew); else gradle_cmd=(gradle); fi
"${gradle_cmd[@]}" --no-daemon "-Ptuxmail.gms=$gms" "${gradle_tasks[@]}"

echo "==> outputs"
find app/build/outputs \( -name '*.apk' -o -name '*.aab' \) -print 2>/dev/null || true
