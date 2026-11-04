#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# End-to-end test of the mail journey against real protocol servers
# (docs/ENTWICKLUNG.md): builds the frontend and tuxmail, starts
# `tuxmail devserver` (IMAP/SMTP/autoconfig, in memory) and `tuxmail serve`
# with a temporary configuration and runs frontend/scripts/e2e-mail.cjs with
# Playwright/Chromium. Runs locally and in CI (no root, no Dovecot needed).
#
#   scripts/e2e.sh                       # everything
#   E2E_SKIP_BUILD=1 scripts/e2e.sh      # reuse web/dist and $E2E_BIN
#   E2E_DOVECOT=1 scripts/e2e.sh         # against Dovecot+Postfix (root, scripts/dovecot-dev.sh)
#
# Environment: E2E_DIR (work directory, default: mktemp), E2E_BIN (tuxmail
# binary, default $E2E_DIR/tuxmail), SCREENSHOTS (default: $E2E_DIR/screenshots),
# E2E_HTTP_PORT (18080), E2E_DEV_PORT (1080), NODE_PATH (playwright; default `npm root -g`).
# Exit code: number of failed steps (0 = success).
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
work=${E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/tuxmail-e2e.XXXXXX")}
bin=${E2E_BIN:-$work/tuxmail}
http_port=${E2E_HTTP_PORT:-18080}
dev_port=${E2E_DEV_PORT:-1080}
imap_port=${E2E_IMAP_PORT:-1143}
smtp_port=${E2E_SMTP_PORT:-1587}
shots=${SCREENSHOTS:-$work/screenshots}
mkdir -p "$work" "$shots"
pids=""
cleanup() {
	for p in $pids; do kill "$p" 2>/dev/null || true; done
	if [ "${E2E_DOVECOT:-}" = 1 ]; then "$root/scripts/dovecot-dev.sh" stop || true; fi
}
trap cleanup EXIT INT TERM

if [ "${E2E_SKIP_BUILD:-}" != 1 ]; then
	echo "== building frontend and tuxmail"
	sh "$root/web/build.sh" >"$work/web-build.log" 2>&1 || { tail -40 "$work/web-build.log"; exit 1; }
	(cd "$root" && GOTOOLCHAIN=${GOTOOLCHAIN:-local} go build -o "$bin" ./cmd/tuxmail)
fi

wait_http() { # URL [seconds]
	i=0
	until curl -s --noproxy '*' -o /dev/null "$1"; do
		i=$((i + 1))
		[ "$i" -lt "$((${2:-20} * 4))" ] || { echo "timeout waiting for $1" >&2; return 1; }
		sleep 0.25
	done
}

echo "== starting mail servers"
serve_env=""
if [ "${E2E_DOVECOT:-}" = 1 ]; then
	"$root/scripts/dovecot-dev.sh" start
	"$bin" devserver --http "127.0.0.1:$dev_port" --external-imap 127.0.0.1:993 \
		--external-ca /tmp/tuxmail-dovecot/ca.pem >"$work/devserver.json" 2>"$work/devserver.log" &
	serve_env="SSL_CERT_FILE=/tmp/tuxmail-dovecot/ca.pem"
else
	"$bin" devserver --http "127.0.0.1:$dev_port" --imap "127.0.0.1:$imap_port" --smtp "127.0.0.1:$smtp_port" \
		>"$work/devserver.json" 2>"$work/devserver.log" &
fi
pids="$pids $!"
wait_http "http://127.0.0.1:$dev_port/api/users"

echo "== starting tuxmail serve"
mkdir -p "$work/data"
cat >"$work/config.yaml" <<EOF
listen: 127.0.0.1:$http_port
public_url: http://127.0.0.1:$http_port
data_dir: $work/data
log: {level: debug, format: text, file: $work/serve.log}
limits: {login_rate_per_minute: 60}
# the devserver mail server listens on loopback
security: {allow_private_networks: true}
EOF
password='correct horse battery staple'
echo "$password" | "$bin" user add --config "$work/config.yaml" --username anna --email anna@tuxmail.test \
	--admin --password-stdin --if-not-exists
env $serve_env TUXMAIL_ISPDB_URL="http://127.0.0.1:$dev_port/v1.1/" \
	"$bin" serve --config "$work/config.yaml" >"$work/serve.out" 2>&1 &
pids="$pids $!"
wait_http "http://127.0.0.1:$http_port/login"

echo "== running the journey (screenshots: $shots)"
export NODE_PATH=${NODE_PATH:-$(npm root -g)}
set +e
(cd "$root/frontend" && TUXMAIL_URL="http://127.0.0.1:$http_port" TUXMAIL_PASSWORD="$password" \
	DEVSERVER_URL="http://127.0.0.1:$dev_port" SCREENSHOTS="$shots" node scripts/e2e-mail.cjs)
rc=$?
set -e
[ "$rc" -eq 0 ] || echo "e2e failed ($rc); logs in $work (serve.log, devserver.log)"
exit "$rc"
