// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"strconv"
	"strings"
)

// UpgradeKind classifies an installation over an existing one.
type UpgradeKind string

const (
	Fresh     UpgradeKind = "fresh"
	Upgrade   UpgradeKind = "upgrade"
	Reinstall UpgradeKind = "reinstall"
	Downgrade UpgradeKind = "downgrade"
)

// Classify compares the installed version with the version of this setup.
// Versions are build/version.sh strings: "0.1.0" or "0.1.0-dev.<commit>".
func Classify(installed, setup string) UpgradeKind {
	installed, setup = strings.TrimSpace(installed), strings.TrimSpace(setup)
	if installed == "" {
		return Fresh
	}
	switch c := CompareVersions(installed, setup); {
	case c < 0:
		return Upgrade
	case c > 0:
		return Downgrade
	case installed != setup:
		// Same release, different development builds: order unknown.
		return Upgrade
	default:
		return Reinstall
	}
}

// CompareVersions compares two versions: -1 if a < b, 0 if equal in
// precedence, 1 if a > b. A pre-release ("-dev.x") sorts before its
// release; two different pre-releases of the same release compare equal
// unless both are numeric-dotted (semver-like rules).
func CompareVersions(a, b string) int {
	ac, ap := splitPre(a)
	bc, bp := splitPre(b)
	an, bn := numbers(ac), numbers(bc)
	for i := 0; i < len(an) || i < len(bn); i++ {
		var x, y int
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	// Both pre-releases: compare dot-separated identifiers like semver,
	// but treat "dev.<commit>" builds (non-numeric tail) as unordered.
	ai, bi := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		if ai[i] == bi[i] {
			continue
		}
		x, errx := strconv.Atoi(ai[i])
		y, erry := strconv.Atoi(bi[i])
		if errx == nil && erry == nil {
			if x < y {
				return -1
			}
			return 1
		}
		if errx == nil || erry == nil || i > 0 {
			return 0
		}
		if ai[i] < bi[i] {
			return -1
		}
		return 1
	}
	return 0
}

func splitPre(v string) (core, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexAny(v, "-~"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func numbers(core string) []int {
	var out []int
	for _, p := range strings.Split(core, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}
