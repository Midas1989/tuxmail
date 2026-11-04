#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# signal-cli for the Signal messenger plugin (KONZEPT 11): bundling into
# release packages and pin updates. Versions, URLs and SHA-256 checksums live
# in ONE place: internal/messenger/signalcli/pins.env (also embedded in the
# app for the on-demand install, see internal/messenger/components.go).
#
#   build/signal-cli.sh bundle OS ARCH DIR   # DIR = <plugins dir>/signal-cli
#   build/signal-cli.sh plan OS ARCH         # print the pinned downloads
#   build/signal-cli.sh pin [VERSION|latest] [TEMURIN_VERSION]
#
# Per platform (signalcli.PlanFor):
#   linux/amd64    GraalVM native signal-cli (no Java)
#   linux/arm64    signal-cli JVM build + Temurin JRE (linux aarch64)
#   windows/amd64  signal-cli JVM build + Temurin JRE (windows x64)
#   windows/arm64  signal-cli JVM build + Temurin JRE (windows x64, runs under
#                  the x64 emulation of Windows 11 on ARM: libsignal-client has
#                  no Windows ARM64 library)
# Downloads are HTTPS only and checksum-verified before they are unpacked;
# verified files are cached in build/cache/signal-cli (TUXMAIL_SIGNAL_CLI_CACHE).
set -eu

cd "$(dirname "$0")/.."

cache=${TUXMAIL_SIGNAL_CLI_CACHE:-build/cache/signal-cli}

case "${1:-}" in
bundle)
	[ $# -eq 4 ] || { echo "usage: $0 bundle OS ARCH DIR" >&2; exit 2; }
	exec go run ./build/signalcli bundle -os "$2" -arch "$3" -out "$4" -cache "$cache"
	;;
plan)
	[ $# -eq 3 ] || { echo "usage: $0 plan OS ARCH" >&2; exit 2; }
	exec go run ./build/signalcli plan -os "$2" -arch "$3"
	;;
pin)
	exec go run ./build/signalcli pin -signal-cli "${2:-latest}" ${3:+-temurin "$3"}
	;;
*)
	echo "usage: $0 bundle OS ARCH DIR | plan OS ARCH | pin [VERSION|latest] [TEMURIN_VERSION]" >&2
	exit 2
	;;
esac
