// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import "testing"

const selfSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

// Audit ND-13: the decision of the Windows trust check, on realistic
// security descriptors (as SECURITY_DESCRIPTOR.String prints them).
func TestCheckSDDL(t *testing.T) {
	cases := []struct {
		name  string
		sddl  string
		trust bool
	}{
		{"Program Files (TrustedInstaller, users read only)",
			"O:S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464G:S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464D:PAI(A;;FA;;;S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464)(A;CIIO;GA;;;S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464)(A;;0x1301bf;;;SY)(A;OICIIO;GA;;;SY)(A;;0x1301bf;;;BA)(A;OICIIO;GA;;;BA)(A;;0x1200a9;;;BU)(A;OICIIO;GXGR;;;BU)(A;;0x1200a9;;;AC)(A;OICIIO;GXGR;;;AC)",
			true},
		{"installed by an administrator, users may read and run",
			"O:BAG:SYD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)(A;OICI;0x1200a9;;;AC)", true},
		{"private folder of the current user", "O:S-1-5-21-1111111111-2222222222-3333333333-1001G:DUD:PAI(A;OICIID;FA;;;SY)(A;OICIID;FA;;;BA)(A;OICIID;FA;;;S-1-5-21-1111111111-2222222222-3333333333-1001)", true},
		{"CREATOR OWNER template is inherit-only and trusted anyway",
			"O:BAG:SYD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICIIO;FA;;;CO)", true},
		{"Users may modify (C:\\ default for new folders)",
			"O:BAG:SYD:AI(A;OICIID;FA;;;SY)(A;OICIID;FA;;;BA)(A;OICIIOID;GA;;;CO)(A;OICIID;0x1301bf;;;AU)(A;CIID;0x100004;;;BU)", false},
		{"Users may create files in the folder",
			"O:BAG:SYD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200ab;;;BU)", false},
		{"Everyone full control", "O:BAG:SYD:PAI(A;;FA;;;WD)", false},
		{"Authenticated Users modify", "O:BAG:SYD:PAI(A;;0x1301bf;;;AU)(A;;FA;;;BA)", false},
		{"Users may write (generic)", "O:BAG:SYD:PAI(A;;GW;;;BU)(A;;FA;;;BA)", false},
		{"Users may change the DACL", "O:BAG:SYD:PAI(A;;0x120089;;;BU)(A;;RCWDWO;;;BU)", false},
		{"another user", "O:BAG:SYD:PAI(A;;FA;;;S-1-5-21-1111111111-2222222222-3333333333-1002)", false},
		{"Power Users modify", "O:BAG:SYD:PAI(A;;0x1301bf;;;PU)", false},
		{"owned by another user", "O:S-1-5-21-1111111111-2222222222-3333333333-1002G:DUD:PAI(A;;FA;;;SY)", false},
		{"owned by Users", "O:BUG:SYD:PAI(A;;FA;;;SY)", false},
		{"deny does not rescue a grant", "O:BAG:SYD:PAI(D;;FA;;;BG)(A;;FA;;;WD)", false},
		{"null DACL", "O:BAG:SY", false},
		{"explicit null DACL", "O:BAG:SYD:NO_ACCESS_CONTROL", false},
		{"unparsable entry", "O:BAG:SYD:PAI(A;;FA)", false},
		{"unknown right", "O:BAG:SYD:PAI(A;;QQ;;;BU)", false},
		{"inherit-only flag in either order", "O:BAG:SYD:PAI(A;CIOI;FA;;;SY)(A;CIOIIO;FA;;;BU)", true},
		{"with SACL", "O:BAG:SYD:PAI(A;;FA;;;SY)(A;;FA;;;BA)S:AI(ML;;NW;;;LW)", true},
	}
	for _, c := range cases {
		err := checkSDDL(c.sddl, selfSID)
		if c.trust && err != nil {
			t.Errorf("%s: rejected: %v", c.name, err)
		}
		if !c.trust && err == nil {
			t.Errorf("%s: accepted, want an error", c.name)
		}
	}
	// Without a known current user only the administrators count.
	if err := checkSDDL("O:S-1-5-21-1111111111-2222222222-3333333333-1001G:DUD:PAI(A;;FA;;;SY)", ""); err == nil {
		t.Error("a user-owned file was trusted without a user SID")
	}
}
