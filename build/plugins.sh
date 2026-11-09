#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Builds the messenger plugins (KONZEPT 4.4, 11) without CGO.
#
#   build/plugins.sh                      # host platform -> bin/plugins/
#   build/plugins.sh target OS ARCH [DIR] # one platform -> DIR (default build/out/plugins/<os>-<arch>/)
#   build/plugins.sh all                  # linux/windows/darwin x amd64/arm64 -> build/out/plugins/<os>-<arch>/
#   OUT=dir build/plugins.sh              # custom output directory (host platform)
#
# Release packaging (build/lib.sh stage_plugins) ships the binaries of the
# matching platform with every desktop and server package:
#   plugins/tuxmail-plugin-whatsapp[.exe]
#   plugins/tuxmail-plugin-telegram[.exe]
#   plugins/tuxmail-plugin-matrix[.exe]    (built with -tags goolm: E2EE in pure Go)
#   plugins/tuxmail-plugin-signal[.exe]    (needs an external signal-cli at runtime)
# in a "plugins" folder next to the tuxmail executable (Windows, portable and
# server archives), or in /usr/lib/tuxmail/plugins (.deb/.rpm, AppImage) and
# /usr/local/lib/tuxmail/plugins (Docker): internal/messenger/host.DefaultDirs
# looks in <exe dir>/plugins and <exe dir>/../lib/tuxmail/plugins
# (plus $TUXMAIL_PLUGIN_DIR and bin/plugins for development). The Telegram
# plugin needs TUXMAIL_TELEGRAM_API_ID and TUXMAIL_TELEGRAM_API_HASH at
# runtime (project credentials).
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
		echo "plugin $name ($os/$arch) -> $out/tuxmail-plugin-$name$ext" >&2
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -tags "$tags" \
			-ldflags "-s -w" \
			-o "$out/tuxmail-plugin-$name$ext" "./cmd/plugins/$name"
	done
}

case "${1:-host}" in
all)
	for os in linux windows darwin; do
		for arch in amd64 arm64; do
			build "$os" "$arch" "build/out/plugins/$os-$arch"
		done
	done
	;;
target)
	[ $# -ge 3 ] || { echo "usage: $0 target OS ARCH [DIR]" >&2; exit 2; }
	build "$2" "$3" "${4:-build/out/plugins/$2-$3}"
	;;
host)
	build "$(go env GOOS)" "$(go env GOARCH)" "${OUT:-bin/plugins}"
	;;
*)
	echo "usage: $0 [host|target OS ARCH [DIR]|all]" >&2
	exit 2
	;;
esac
