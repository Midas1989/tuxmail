#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Builds the frontend and copies it into web/dist for go:embed.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../frontend"
[ -d node_modules ] || npm ci
npm run build
rm -rf "$here/dist"
mkdir -p "$here/dist"
cp -R dist/. "$here/dist/"
touch "$here/dist/.keep"
