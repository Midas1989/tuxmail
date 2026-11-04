# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Shared helpers for the build scripts (sourced, not executed).
# Works with bash/dash on Linux and with Git Bash on the Windows CI runner.

# shellcheck shell=sh

ROOT=$(cd "$(dirname "$0")" && while [ ! -f VERSION ] && [ "$(pwd)" != / ]; do cd ..; done && pwd)
export ROOT

# VERSION: computed once per script run (see build/version.sh).
VERSION=${TUXMAIL_VERSION:-$(sh "$ROOT/build/version.sh")}
export VERSION

# Reproducible archive timestamps: commit time (or VERSION file mtime outside git).
if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
	SOURCE_DATE_EPOCH=$(git -C "$ROOT" log -1 --format=%ct 2>/dev/null || date +%s)
fi
export SOURCE_DATE_EPOCH

DIST=${DIST:-$ROOT/dist}
export DIST

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# go_ldflags [extra...]: linker flags shared by all builds.
go_ldflags() {
	local oauth="github.com/Midas1989/Mailclient/internal/oauth" extra=""
	# Project-wide OAuth app registrations (docs/MICROSOFT-GOOGLE.md). They
	# are public client IDs, not secrets; CI passes them from repository
	# variables. Empty values keep the "not configured" behaviour.
	[ -n "${TUXMAIL_BUILD_MS_CLIENT_ID:-}" ] && extra="$extra -X $oauth.builtinMSClientID=$TUXMAIL_BUILD_MS_CLIENT_ID"
	[ -n "${TUXMAIL_BUILD_GOOGLE_CLIENT_ID:-}" ] && extra="$extra -X $oauth.builtinGoogleClientID=$TUXMAIL_BUILD_GOOGLE_CLIENT_ID"
	[ -n "${TUXMAIL_BUILD_GOOGLE_CLIENT_SECRET:-}" ] && extra="$extra -X $oauth.builtinGoogleClientSecret=$TUXMAIL_BUILD_GOOGLE_CLIENT_SECRET"
	printf '%s' "-s -w -X main.version=$VERSION$extra $*"
}

# check_frontend: warn loudly when web/dist holds no built frontend.
check_frontend() {
	if [ ! -f "$ROOT/web/dist/index.html" ]; then
		if [ "${TUXMAIL_ALLOW_NO_FRONTEND:-}" = 1 ]; then
			log "WARNING: web/dist/index.html missing - binary will only show a placeholder page"
		else
			die "web/dist/index.html missing: run 'go generate ./web' (or set TUXMAIL_ALLOW_NO_FRONTEND=1)"
		fi
	fi
}

# mktar OUT.tar.gz DIR ENTRY...: deterministic gzip tarball of ENTRYs relative to DIR.
mktar() {
	out=$1 dir=$2
	shift 2
	tar -C "$dir" --sort=name --owner=0 --group=0 --numeric-owner \
		--mtime="@$SOURCE_DATE_EPOCH" -cf - "$@" | gzip -9n > "$out"
}

# mkzip OUT.zip DIR ENTRY...: zip of ENTRYs relative to DIR (zip, else python).
mkzip() {
	out=$1 dir=$2
	shift 2
	case $out in /*|?:*) ;; *) out=$(pwd)/$out ;; esac
	rm -f "$out"
	if command -v zip > /dev/null 2>&1; then
		(cd "$dir" && zip -q -X -r -9 "$out" "$@")
	elif command -v 7z > /dev/null 2>&1; then
		# Windows runners: 7-Zip is preinstalled, zip is not.
		(cd "$dir" && 7z a -bd -tzip -mx=9 "$(cygpath -w "$out" 2> /dev/null || printf '%s' "$out")" "$@" > /dev/null)
	else
		py=
		for c in python3 python; do
			if "$c" -c 'import zipfile' > /dev/null 2>&1; then py=$c && break; fi
		done
		[ -n "$py" ] || die "need zip, 7z or python to create $out"
		(cd "$dir" && "$py" -I -c '
import os, sys, zipfile
out = sys.argv[1]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for top in sys.argv[2:]:
        if os.path.isdir(top):
            z.write(top, top.replace(os.sep, "/") + "/")
            for d, dirs, files in os.walk(top):
                dirs.sort()
                for name in sorted(dirs):
                    p = os.path.join(d, name)
                    z.write(p, p.replace(os.sep, "/") + "/")
                for name in sorted(files):
                    p = os.path.join(d, name)
                    z.write(p, p.replace(os.sep, "/"))
        else:
            z.write(top, top.replace(os.sep, "/"))
' "$out" "$@")
	fi
}

# sha256 FILE...: prints "<hash>  <name>" lines (sha256sum format).
sha256() {
	if command -v sha256sum > /dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}
