#!/bin/bash
# The t_* target functions are called by name through run_target.
# shellcheck disable=SC2317,SC2329
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Builds all TuxMail variants locally on Linux, without GitHub Actions. It only
# calls the same scripts the release workflow uses (build/package-server.sh,
# build/linux/*, build/windows/*, mobile/scripts/build.sh, build/docker/Dockerfile).
#
#   build/build-all.sh --check                 which targets can be built on this machine?
#   build/build-all.sh                         build every target that is possible here
#   build/build-all.sh server windows          only these groups
#   build/build-all.sh --strict                a missing prerequisite is an error, not a skip
#   build/build-all.sh --offline server        no downloads (see below)
#
# Target groups (default: all):
#   frontend         web/dist (npm ci + vite build), needed by everything else
#   server           server/CLI archives, linux+windows+macOS, amd64+arm64 (pure Go)
#   linux-desktop    .deb, .rpm, portable tar.gz, AppImage for the HOST architecture
#                    (needs GTK 3 + WebKitGTK 4.1 development packages, CGO)
#   windows-desktop  portable ZIP and TuxMail-Setup.exe, amd64+arm64 (cross build, pure Go)
#   macos-desktop    TuxMail.app + .dmg, amd64+arm64 (macOS host only: needs Xcode command line tools)
#   android          foss APK (debug, or release when signing variables are set)
#   docker           container image tuxmail:local (needs docker or podman)
#   checksums        dist/checksums.txt over everything in dist/
#
# Options:
#   --arch LIST      server/windows architectures, comma separated (default: amd64,arm64)
#   --android-tasks  Gradle tasks for the Android build (default: fossDebug, or fossRelease
#                    when TUXMAIL_KEYSTORE_FILE is set), e.g. "fossRelease playRelease"
#   --strict         fail when a target cannot be built
#   --offline        build without network access: GOPROXY=off, npm offline, signal-cli not
#                    bundled (TUXMAIL_SIGNAL_CLI=skip), no tool downloads. Go modules and
#                    npm packages must already be in the local caches; the AppImage needs
#                    LINUXDEPLOY, LINUXDEPLOY_PLUGIN_GTK and APPIMAGETOOL pointing to files.
#   --keep-going     continue with the next target after a failure (default for "all")
#   -h, --help       this text
#
# Environment: TUXMAIL_VERSION, DIST (default dist/), TUXMAIL_SIGNAL_CLI=bundle|auto|skip,
# TUXMAIL_SKIP_PLUGINS=1, signing variables as documented in docs/RELEASE.md and
# mobile/README.md. Results are in dist/, intermediate files in build/out/.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$here/.." && pwd)
cd "$ROOT" || exit 1

NFPM_VERSION=${NFPM_VERSION:-v2.43.0}
GO_WINRES_VERSION=${GO_WINRES_VERSION:-v0.3.3}

check_only=0
strict=0
offline=0
archs="amd64,arm64"
android_tasks=""
targets=()

usage() { sed -n '2,/^set -/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
	case "$1" in
	--check) check_only=1 ;;
	--strict) strict=1 ;;
	--offline) offline=1 ;;
	--keep-going) ;;
	--arch)
		archs=${2:?--arch needs a value}
		shift
		;;
	--arch=*) archs=${1#--arch=} ;;
	--android-tasks)
		android_tasks=${2:?--android-tasks needs a value}
		shift
		;;
	--android-tasks=*) android_tasks=${1#--android-tasks=} ;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		echo "unknown option $1 (see --help)" >&2
		exit 2
		;;
	*) targets+=("$1") ;;
	esac
	shift
done

all_targets=(frontend server linux-desktop windows-desktop macos-desktop android docker checksums)
[ ${#targets[@]} -eq 0 ] && targets=("${all_targets[@]}")
for t in "${targets[@]}"; do
	case " ${all_targets[*]} " in
	*" $t "*) ;;
	*)
		echo "unknown target '$t' (known: ${all_targets[*]})" >&2
		exit 2
		;;
	esac
done
case "$archs" in
amd64 | arm64 | amd64,arm64 | arm64,amd64) ;;
*)
	echo "--arch must be amd64, arm64 or amd64,arm64" >&2
	exit 2
	;;
esac
IFS=, read -r -a arch_list <<<"$archs"

log() { printf '==> %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }
want() { # want TARGET
	local t
	for t in "${targets[@]}"; do [ "$t" = "$1" ] && return 0; done
	return 1
}

if [ "$offline" = 1 ]; then
	export GOPROXY=off GOFLAGS="${GOFLAGS:+$GOFLAGS }-mod=mod" npm_config_offline=true
	export TUXMAIL_SIGNAL_CLI=${TUXMAIL_SIGNAL_CLI:-skip}
fi
gopath=$(go env GOPATH 2>/dev/null || true)
export PATH="$PATH:$gopath/bin"

# ---- prerequisite checks ---------------------------------------------------
# missing_<target> holds the reasons a target cannot be built here.
declare -A missing
add_missing() { missing[$1]+="${missing[$1]:+; }$2"; }

go_need=$(awk '/^go /{print $2; exit}' go.mod)
check_go() {
	have go || {
		add_missing all "go not found (need $go_need or newer, https://go.dev/dl/)"
		return
	}
	local have_v
	have_v=$(go env GOVERSION | sed 's/^go//')
	if [ "$(printf '%s\n%s\n' "$go_need" "$have_v" | sort -V | head -1)" != "$go_need" ]; then
		# Go itself may fetch the matching toolchain (GOTOOLCHAIN=auto); not possible offline.
		[ "$offline" = 1 ] && add_missing all "Go $go_need needed, found $have_v"
	fi
}
check_node() {
	if ! have node || ! have npm; then
		add_missing frontend "node/npm not found (Node 22.12 or newer)"
	fi
	if have node; then
		# Vite 8 / Rolldown need Node 22.12+ (or 20.19+): npm silently skips the native
		# Rolldown package on an older Node and the build then fails with "Cannot find native binding".
		node -e 'const [a,b]=process.versions.node.split(".").map(Number); process.exit(a>22||(a===22&&b>=12)||(a===20&&b>=19)?0:1)' ||
			add_missing frontend "Node 22.12 or newer needed (Vite 8), found $(node -v); on Ubuntu 26.04: sudo apt install nodejs npm"
	fi
}
check_linux_desktop() {
	[ "$(uname -s)" = Linux ] || add_missing linux-desktop "not a Linux host"
	have gcc || add_missing linux-desktop "gcc not found"
	have pkg-config || add_missing linux-desktop "pkg-config not found"
	if have pkg-config; then
		pkg-config --exists webkit2gtk-4.1 || add_missing linux-desktop "libwebkit2gtk-4.1-dev not found"
		pkg-config --exists gtk+-3.0 || add_missing linux-desktop "libgtk-3-dev not found"
	fi
	if ! have nfpm && { [ "$offline" = 1 ] || ! have go; }; then
		add_missing linux-desktop "nfpm not found (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@$NFPM_VERSION)"
	fi
}
check_windows_desktop() {
	if ! have go-winres && { [ "$offline" = 1 ] || ! have go; }; then
		add_missing windows-desktop "go-winres not found (go install github.com/tc-hib/go-winres@$GO_WINRES_VERSION)"
	fi
}
check_macos_desktop() {
	[ "$(uname -s)" = Darwin ] || add_missing macos-desktop "not a macOS host (the app needs cgo and the Apple SDK)"
	have clang || add_missing macos-desktop "clang not found (xcode-select --install)"
	have hdiutil || add_missing macos-desktop "hdiutil not found"
	have iconutil || add_missing macos-desktop "iconutil not found"
}
check_android() {
	have java || add_missing android "JDK 21 not found"
	if have java; then
		local jv
		jv=$(java -version 2>&1 | sed -n 's/.*version "\([0-9]*\).*/\1/p' | head -1)
		[ "${jv:-0}" -ge 21 ] 2>/dev/null || add_missing android "JDK 21 or newer needed, found ${jv:-unknown}"
	fi
	if [ -z "${ANDROID_HOME:-}${ANDROID_SDK_ROOT:-}" ] && ! grep -qs '^sdk.dir=' mobile/android/local.properties; then
		add_missing android "Android SDK not found (set ANDROID_HOME or mobile/android/local.properties, Platform 36 + Build-Tools 36)"
	fi
	have node || add_missing android "node/npm not found"
}
check_docker() {
	have docker || have podman || add_missing docker "docker or podman not found"
}

check_go
# The frontend is built when asked for, or when another target needs it and web/dist is missing.
if want frontend; then
	check_node
elif { want server || want linux-desktop || want windows-desktop || want macos-desktop || want docker; } && [ ! -f web/dist/index.html ]; then
	check_node
fi
want linux-desktop && check_linux_desktop
want windows-desktop && check_windows_desktop
want macos-desktop && check_macos_desktop
want android && check_android
want docker && check_docker

print_check() {
	local t status
	printf '\nTarget            Status\n'
	for t in "${targets[@]}"; do
		status="ok"
		[ -n "${missing[all]:-}" ] && status="SKIP - ${missing[all]}"
		[ -n "${missing[$t]:-}" ] && status="SKIP - ${missing[$t]}"
		[ "$t" = checksums ] && status="ok"
		printf '%-17s %s\n' "$t" "$status"
	done
	printf '\n'
}

if [ "$check_only" = 1 ]; then
	print_check
	exit 0
fi

# ---- running ---------------------------------------------------------------
declare -a results
failed=0
skipped=0
run_target() { # run_target NAME FUNCTION
	local name=$1 fn=$2 reason=${missing[$1]:-}
	[ -n "${missing[all]:-}" ] && reason=${missing[all]}
	if [ -n "$reason" ]; then
		if [ "$strict" = 1 ]; then
			results+=("$name|FAILED|$reason")
			failed=$((failed + 1))
		else
			results+=("$name|skipped|$reason")
			skipped=$((skipped + 1))
		fi
		return
	fi
	log "=== $name"
	if "$fn"; then
		results+=("$name|ok|")
	else
		results+=("$name|FAILED|see the output above")
		failed=$((failed + 1))
	fi
}

ensure_frontend() {
	[ -f web/dist/index.html ] && return 0
	t_frontend
}
t_frontend() {
	# npm ci + vite build, copied to web/dist (web/build.sh); with --offline npm
	# runs with npm_config_offline=true and uses its local cache.
	go generate ./web
}
t_server() {
	ensure_frontend || return 1
	local a
	for a in "${arch_list[@]}"; do
		build/package-server.sh linux "$a" || return 1
		build/package-server.sh windows "$a" || return 1
		build/package-server.sh darwin "$a" || return 1
	done
}
t_linux_desktop() {
	ensure_frontend || return 1
	have nfpm || go install "github.com/goreleaser/nfpm/v2/cmd/nfpm@$NFPM_VERSION" || return 1
	build/linux/build.sh || return 1
	build/linux/package-nfpm.sh || return 1
	build/linux/package-portable.sh || return 1
	# The AppImage needs linuxdeploy/appimagetool (downloaded and checksum-pinned
	# unless the *_ variables point to local files) and FUSE-less extraction.
	if [ "$offline" = 1 ] && { [ -z "${LINUXDEPLOY:-}" ] || [ -z "${LINUXDEPLOY_PLUGIN_GTK:-}" ] || [ -z "${APPIMAGETOOL:-}" ]; }; then
		log "AppImage skipped: --offline needs LINUXDEPLOY, LINUXDEPLOY_PLUGIN_GTK and APPIMAGETOOL"
		return 0
	fi
	build/linux/package-appimage.sh || return 1
}
t_windows_desktop() {
	ensure_frontend || return 1
	have go-winres || go install "github.com/tc-hib/go-winres@$GO_WINRES_VERSION" || return 1
	local a
	for a in "${arch_list[@]}"; do
		build/windows/build.sh "$a" || return 1
		build/windows/package-portable.sh "$a" || return 1
		build/windows/package-setup.sh "$a" || return 1
	done
}
t_macos_desktop() {
	ensure_frontend || return 1
	local a
	for a in "${arch_list[@]}"; do
		build/macos/build.sh "$a" || return 1
		build/macos/package-dmg.sh "$a" || return 1
	done
}
t_android() {
	local tasks=${android_tasks:-}
	if [ -z "$tasks" ]; then
		if [ -n "${TUXMAIL_KEYSTORE_FILE:-}" ]; then tasks=fossRelease; else tasks=fossDebug; fi
	fi
	mkdir -p build/out "${DIST:-$ROOT/dist}"
	touch build/out/.android-start
	# shellcheck disable=SC2086
	mobile/scripts/build.sh $tasks || return 1
	find mobile/android/app/build/outputs \( -name '*.apk' -o -name '*.aab' \) -newer build/out/.android-start \
		-exec cp -v {} "${DIST:-$ROOT/dist}/" \;
}
t_docker() {
	local engine=docker
	have docker || engine=podman
	ensure_frontend || return 1
	"$engine" build -f build/docker/Dockerfile --build-arg FRONTEND=prebuilt \
		--build-arg "VERSION=$(sh build/version.sh)" -t tuxmail:local . || return 1
}
t_checksums() {
	[ -d "${DIST:-$ROOT/dist}" ] || {
		log "nothing in dist/ - no checksums"
		return 0
	}
	build/checksums.sh
}

want frontend && run_target frontend t_frontend
want server && run_target server t_server
want linux-desktop && run_target linux-desktop t_linux_desktop
want windows-desktop && run_target windows-desktop t_windows_desktop
want macos-desktop && run_target macos-desktop t_macos_desktop
want android && run_target android t_android
want docker && run_target docker t_docker
want checksums && run_target checksums t_checksums

# ---- summary ---------------------------------------------------------------
printf '\n== Summary (version %s)\n' "$(sh build/version.sh)"
for r in "${results[@]}"; do
	IFS='|' read -r name state note <<<"$r"
	printf '  %-17s %-8s %s\n' "$name" "$state" "$note"
done
if [ -d "${DIST:-$ROOT/dist}" ]; then
	printf '\nFiles in %s:\n' "${DIST:-$ROOT/dist}"
	(cd "${DIST:-$ROOT/dist}" && find . -maxdepth 1 -type f -printf '  %f (%s bytes)\n' | LC_ALL=C sort)
fi
[ "$skipped" -gt 0 ] && printf '\n%d target(s) skipped; run "build/build-all.sh --check" for the reasons.\n' "$skipped"
[ "$failed" -gt 0 ] && exit 1
exit 0
