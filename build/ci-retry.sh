#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Runs a command up to N times with a growing pause. CI steps that download
# modules or tools (go install, go mod download, npm ci) use it so that a
# transient proxy or registry error does not skip a whole security job (a
# gitleaks run failed on a proxy.golang.org stream error on 2026-11-04 and the
# push went unscanned).
#
#   build/ci-retry.sh [-n ATTEMPTS] [-d SECONDS] -- command [args...]
#
# Defaults: 4 attempts, 5 s pause doubling after each failure. The exit status
# of the last attempt is returned.
set -eu

attempts=4
delay=5
while [ $# -gt 0 ]; do
	case $1 in
	-n)
		attempts=${2:?-n needs a number}
		shift 2
		;;
	-d)
		delay=${2:?-d needs a number}
		shift 2
		;;
	--)
		shift
		break
		;;
	*) break ;;
	esac
done
[ $# -gt 0 ] || {
	echo "usage: ci-retry.sh [-n ATTEMPTS] [-d SECONDS] -- command [args...]" >&2
	exit 2
}
case $attempts$delay in
*[!0-9]*)
	echo "ci-retry.sh: -n and -d must be numbers" >&2
	exit 2
	;;
esac

n=1
while :; do
	if "$@"; then
		exit 0
	else
		status=$?
	fi
	if [ "$n" -ge "$attempts" ]; then
		echo "ci-retry.sh: '$*' failed after $n attempt(s)" >&2
		exit "$status"
	fi
	echo "ci-retry.sh: attempt $n of '$*' failed (exit $status), retrying in ${delay}s" >&2
	sleep "$delay"
	delay=$((delay * 2))
	n=$((n + 1))
done
