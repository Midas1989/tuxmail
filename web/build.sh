#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Builds the frontend and copies it into web/dist for go:embed.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../frontend"
# Vite 8 / Rolldown need Node 22.12+ (or 20.19+); on an older Node npm silently skips the native
# Rolldown package and the build fails with "Cannot find native binding".
node -e 'const [a,b]=process.versions.node.split(".").map(Number); process.exit(a>22||(a===22&&b>=12)||(a===20&&b>=19)?0:1)' || {
	echo "error: Node 22.12 or newer is required (found $(node -v 2>/dev/null || echo 'none')); on Ubuntu 26.04: sudo apt install nodejs npm" >&2
	exit 1
}
[ -d node_modules ] || npm ci
npm run build
rm -rf "$here/dist"
mkdir -p "$here/dist"
cp -R dist/. "$here/dist/"
touch "$here/dist/.keep"
