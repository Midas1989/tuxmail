// SPDX-License-Identifier: AGPL-3.0-or-later

// Package winsetup holds the logic of the Windows setup program
// (cmd/tuxmail-setup): install scopes and folders, the registry plan
// (Apps & Features, mailto: / Default Programs, autostart), payload
// extraction, upgrade detection, uninstall and command line parsing.
//
// Everything that touches the operating system goes through small
// interfaces (Registry, Linker, ...) so that the logic is tested on every
// platform; the real implementations live in the *_windows.go files.
package winsetup

import (
	"path"
	"strings"
)

// Windows path helpers that behave identically on every GOOS (the setup
// logic is unit-tested on Linux). They accept "/" and "\" as separators and
// always return "\".

// splitVolume splits "C:\x" into "C:" and "\x", and "\\srv\share\x" into
// "\\srv\share" and "\x".
func splitVolume(p string) (vol, rest string) {
	p = strings.ReplaceAll(p, "/", `\`)
	if len(p) >= 2 && p[1] == ':' && isLetter(p[0]) {
		return strings.ToUpper(p[:1]) + ":", p[2:]
	}
	if strings.HasPrefix(p, `\\`) {
		parts := strings.SplitN(p[2:], `\`, 3)
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			vol = `\\` + parts[0] + `\` + parts[1]
			if len(parts) == 3 {
				return vol, `\` + parts[2]
			}
			return vol, ""
		}
	}
	return "", p
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// WinClean is filepath.Clean with Windows semantics.
func WinClean(p string) string {
	if p == "" {
		return ""
	}
	vol, rest := splitVolume(p)
	if rest == "" {
		if strings.HasPrefix(vol, `\\`) {
			return vol + `\`
		}
		return vol
	}
	c := path.Clean(strings.ReplaceAll(rest, `\`, "/"))
	if c == "." && vol != "" {
		c = ""
	}
	return vol + strings.ReplaceAll(c, "/", `\`)
}

// WinJoin joins path elements with "\" and cleans the result.
func WinJoin(elem ...string) string {
	var parts []string
	for _, e := range elem {
		if e != "" {
			parts = append(parts, e)
		}
	}
	return WinClean(strings.Join(parts, `\`))
}

// WinIsAbs reports whether p is a fully qualified path (drive letter with
// root, or UNC).
func WinIsAbs(p string) bool {
	vol, rest := splitVolume(p)
	if strings.HasPrefix(vol, `\\`) {
		return true
	}
	return vol != "" && strings.HasPrefix(rest, `\`)
}

// WinIsUNC reports whether p is a \\server\share path.
func WinIsUNC(p string) bool {
	vol, _ := splitVolume(p)
	return strings.HasPrefix(vol, `\\`)
}

// WinBase returns the last element of p.
func WinBase(p string) string {
	c := WinClean(p)
	if i := strings.LastIndex(c, `\`); i >= 0 {
		return c[i+1:]
	}
	return c
}

// WinDir returns p without its last element.
func WinDir(p string) string {
	c := WinClean(p)
	vol, rest := splitVolume(c)
	i := strings.LastIndex(rest, `\`)
	if i < 0 {
		return vol
	}
	if i == 0 {
		return vol + `\`
	}
	return vol + rest[:i]
}

// WinIsRoot reports whether p is a drive or share root ("C:\", "\\srv\share").
func WinIsRoot(p string) bool {
	vol, rest := splitVolume(WinClean(p))
	return vol != "" && (rest == `\` || rest == "")
}

// WinSame compares two paths case-insensitively after cleaning.
func WinSame(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(WinClean(a), WinClean(b))
}

// WinWithin reports whether child is parent or below it.
func WinWithin(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	p, c := strings.ToLower(WinClean(parent)), strings.ToLower(WinClean(child))
	if p == c {
		return true
	}
	if !strings.HasSuffix(p, `\`) {
		p += `\`
	}
	return strings.HasPrefix(c, p)
}
