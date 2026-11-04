#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Builds the messenger plugins (KONZEPT 4.4, 11) without CGO.
#
#   build/plugins.sh                 # host platform -> bin/plugins/
#   build/plugins.sh all             # linux/windows x amd64/arm64 -> dist/plugins/<os>-<arch>/
#   OUT=dir build/plugins.sh         # custom output directory (host platform)
#
# Release packaging: ship the binaries of the matching platform in a
# "plugins" directory next to the tuxmail executable:
#   plugins/tuxmail-plugin-whatsapp[.exe]
#   plugins/tuxmail-plugin-telegram[.exe]
#   plugins/tuxmail-plugin-matrix[.exe]    (built with -tags goolm: E2EE in pure Go)
#   plugins/tuxmail-plugin-signal[.exe]    (needs an external signal-cli at runtime)
# TuxMail discovers them there (or in $TUXMAIL_PLUGIN_DIR / bin/plugins for
# development). The Telegram plugin needs TUXMAIL_TELEGRAM_API_ID and
# TUXMAIL_TELEGRAM_API_HASH at runtime (project credentials).
set -eu

cd "$(dirname "$0")/.."

# name:extra-build-tags
PLUGINS="whatsapp: telegram: matrix:goolm signal:"

build() { # os arch outdir
	os=$1 arch=$2 out=$3
	mkdir -p "$out"
	ext=""
	[ "$os" = windows ] && ext=".exe"
	for p in $PLUGINS; do
		name=${p%%:*}
		tags=${p#*:}
		echo "plugin $name ($os/$arch) -> $out/tuxmail-plugin-$name$ext"
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -tags "$tags" \
			-ldflags "-s -w" \
			-o "$out/tuxmail-plugin-$name$ext" "./cmd/plugins/$name"
	done
}

case "${1:-host}" in
all)
	for os in linux windows; do
		for arch in amd64 arm64; do
			build "$os" "$arch" "dist/plugins/$os-$arch"
		done
	done
	;;
host)
	build "$(go env GOOS)" "$(go env GOARCH)" "${OUT:-bin/plugins}"
	;;
*)
	echo "usage: $0 [host|all]" >&2
	exit 2
	;;
esac
