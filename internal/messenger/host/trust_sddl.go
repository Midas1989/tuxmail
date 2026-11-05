// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The Windows trust check (trust_windows.go) reads the security descriptor
// of a plugin file and its folder as an SDDL string and decides here, so
// that the decision can be tested on every OS (audit ND-13, the equivalent
// of the owner and mode checks of trust_unix.go):
//
//   - the owner must be an administrator, SYSTEM, TrustedInstaller or the
//     user running TuxMail, and
//   - only those principals (and CREATOR OWNER / OWNER RIGHTS) may be
//     granted rights to write, delete or take over the object. Everyone,
//     Users, Authenticated Users, other users and groups with such a right
//     make the file untrusted. A missing DACL (everybody has full access)
//     is untrusted too.
//
// Deny entries are ignored (they cannot make a grant safe).

// wellKnownSID maps the SDDL aliases to SIDs.
var wellKnownSID = map[string]string{
	"WD": "S-1-1-0", "CO": "S-1-3-0", "CG": "S-1-3-1", "OW": "S-1-3-4", "NU": "S-1-5-2", "IU": "S-1-5-4",
	"SU": "S-1-5-6", "AN": "S-1-5-7", "AU": "S-1-5-11", "SY": "S-1-5-18", "LS": "S-1-5-19", "NS": "S-1-5-20",
	"BA": "S-1-5-32-544", "BU": "S-1-5-32-545", "BG": "S-1-5-32-546", "PU": "S-1-5-32-547", "AO": "S-1-5-32-548",
	"SO": "S-1-5-32-549", "PO": "S-1-5-32-550", "BO": "S-1-5-32-551", "RE": "S-1-5-32-552", "RU": "S-1-5-32-554",
	"AC": "S-1-15-2-1",
	"TI": "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464",
}

// trustedPrincipals may own and change plugin files.
var trustedPrincipals = map[string]bool{
	"S-1-5-32-544": true, // Administrators
	"S-1-5-18":     true, // SYSTEM
	"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464": true, // TrustedInstaller
	"S-1-3-0": true, // CREATOR OWNER (inherit-only template for the owner)
	"S-1-3-4": true, // OWNER RIGHTS
}

// rights masks
const (
	rFileWriteData   = 0x00000002 // also "add file" on a folder
	rFileAppendData  = 0x00000004 // also "add subfolder"
	rFileDeleteChild = 0x00000040
	rDelete          = 0x00010000
	rWriteDAC        = 0x00040000
	rWriteOwner      = 0x00080000
	rGenericAll      = 0x10000000
	rGenericWrite    = 0x40000000

	// rWriteLike are the rights that let a principal replace or modify the
	// object or its folder content.
	rWriteLike = rFileWriteData | rFileAppendData | rFileDeleteChild | rDelete | rWriteDAC | rWriteOwner | rGenericAll | rGenericWrite
)

// sddlRights are the right strings of SDDL (two letters each).
var sddlRights = map[string]uint32{
	"GA": 0x10000000, "GR": 0x80000000, "GW": 0x40000000, "GX": 0x20000000,
	"RC": 0x00020000, "SD": 0x00010000, "WD": 0x00040000, "WO": 0x00080000,
	"RP": 0x10, "WP": 0x20, "CC": 0x01, "DC": 0x02, "LC": 0x04, "SW": 0x08, "LO": 0x80, "DT": 0x40, "CR": 0x100,
	"FA": 0x001f01ff, "FR": 0x00120089, "FW": 0x00120116, "FX": 0x001200a0,
	"KA": 0x000f003f, "KR": 0x00020019, "KW": 0x00020006, "KX": 0x00020019,
}

func parseRights(s string) (uint32, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseUint(s[2:], 16, 32)
		return uint32(n), err
	}
	if n, err := strconv.ParseUint(s, 10, 32); err == nil {
		return uint32(n), nil
	}
	var m uint32
	for i := 0; i+2 <= len(s); i += 2 {
		v, ok := sddlRights[s[i:i+2]]
		if !ok {
			return 0, fmt.Errorf("unknown right %q", s[i:i+2])
		}
		m |= v
	}
	if len(s)%2 != 0 {
		return 0, fmt.Errorf("invalid rights %q", s)
	}
	return m, nil
}

func normSID(s string) string {
	if full, ok := wellKnownSID[s]; ok {
		return full
	}
	return strings.ToUpper(s)
}

var aceRe = regexp.MustCompile(`\(([^()]*)\)`)

// checkSDDL returns an error if the object with the security descriptor
// sddl (as returned by SECURITY_DESCRIPTOR.String) could be changed by a
// principal other than the trusted ones and selfSID, the user running
// TuxMail.
func checkSDDL(sddl, selfSID string) error {
	self := strings.ToUpper(selfSID)
	trusted := func(sid string) bool { return trustedPrincipals[sid] || (self != "" && sid == self) }

	owner, dacl, hasDACL := splitSDDL(sddl)
	if o := normSID(owner); o == "" || !trusted(o) {
		return fmt.Errorf("owner %q is not an administrator or the current user", owner)
	}
	if !hasDACL {
		return fmt.Errorf("no DACL: everybody has full access")
	}
	// DACL flags (P, AR, AI, NO_ACCESS_CONTROL) precede the first ACE.
	flags := dacl
	if i := strings.IndexByte(dacl, '('); i >= 0 {
		flags = dacl[:i]
	}
	if strings.Contains(flags, "NO_ACCESS_CONTROL") {
		return fmt.Errorf("null DACL: everybody has full access")
	}
	for _, m := range aceRe.FindAllStringSubmatch(dacl, -1) {
		f := strings.Split(m[1], ";")
		if len(f) < 6 {
			return fmt.Errorf("unparsable access entry %q", m[0])
		}
		aceType, aceFlags, rights, sid := f[0], f[1], f[2], normSID(f[5])
		switch aceType {
		case "D", "OD", "XD", "AU", "OU", "AL", "OL", "ML", "RA", "SP", "TL": // deny, audit, label ...
			continue
		}
		if inheritOnly(aceFlags) { // not an access to this object itself
			continue
		}
		mask, err := parseRights(rights)
		if err != nil {
			return fmt.Errorf("access entry %q: %w", m[0], err)
		}
		if mask&rWriteLike != 0 && !trusted(sid) {
			return fmt.Errorf("%s may modify it (access entry %s)", f[5], m[0])
		}
	}
	return nil
}

// inheritOnly reports whether the ACE flags (two-letter codes) contain IO.
func inheritOnly(flags string) bool {
	for i := 0; i+2 <= len(flags); i += 2 {
		if flags[i:i+2] == "IO" {
			return true
		}
	}
	return false
}

// splitSDDL extracts the owner SID and the DACL part of an SDDL string.
func splitSDDL(sddl string) (owner, dacl string, hasDACL bool) {
	section := func(tag string) (string, bool) {
		i := strings.Index(sddl, tag)
		if i < 0 {
			return "", false
		}
		rest := sddl[i+len(tag):]
		end := len(rest)
		for _, next := range []string{"O:", "G:", "D:", "S:"} {
			if j := indexOutsideParens(rest, next); j >= 0 && j < end {
				end = j
			}
		}
		return rest[:end], true
	}
	owner, _ = section("O:")
	dacl, hasDACL = section("D:")
	return owner, dacl, hasDACL
}

// indexOutsideParens finds sep in s outside of parentheses.
func indexOutsideParens(s, sep string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && strings.HasPrefix(s[i:], sep) {
				return i
			}
		}
	}
	return -1
}
