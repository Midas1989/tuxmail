// SPDX-License-Identifier: AGPL-3.0-or-later

package wbxml

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Token assignments from [MS-ASWBXML]; checking the last tag of every page
// (and a few in between) guards the table alignment.
func TestCodePageTokens(t *testing.T) {
	want := map[string]uint16{
		"AirSync:Sync":                                      0x0005,
		"AirSync:SyncKey":                                   0x000B,
		"AirSync:ApplicationData":                           0x001D,
		"AirSync:HeartbeatInterval":                         0x0029,
		"Contacts:Email1Address":                            0x011B,
		"Contacts:WeightedRank":                             0x013E,
		"Email:Read":                                        0x0215,
		"Email:Flag":                                        0x023A,
		"Email:Status":                                      0x023B,
		"Email:DisallowNewTimeProposal":                     0x023F,
		"AirNotify:DeviceFriendlyName":                      0x0317,
		"Calendar:UID":                                      0x0428,
		"Calendar:AttendeeType":                             0x042A,
		"Calendar:DisallowNewTimeProposal":                  0x0433,
		"Calendar:ClientUid":                                0x043C,
		"Move:DstMsgId":                                     0x050C,
		"ItemEstimate:Status":                               0x060E,
		"FolderHierarchy:FolderSync":                        0x0716,
		"FolderHierarchy:Version":                           0x0718,
		"MeetingResponse:InstanceId":                        0x080E,
		"MeetingResponse:SendResponse":                      0x0812,
		"Tasks:FirstDayOfWeek":                              0x0926,
		"ResolveRecipients:MaxPictures":                     0x0A1D,
		"ValidateCert:Status":                               0x0B0A,
		"Contacts2:MMS":                                     0x0C0E,
		"Ping:MaxFolders":                                   0x0D0D,
		"Provision:RemoteWipe":                              0x0E0C,
		"Provision:MaxEmailHTMLBodyTruncationSize":          0x0E2C,
		"Provision:AccountOnlyRemoteWipe":                   0x0E3B,
		"Search:MaxPictures":                                0x0F23,
		"GAL:Data":                                          0x1012,
		"AirSyncBase:Body":                                  0x110A,
		"AirSyncBase:InstanceId":                            0x112D,
		"Settings:SendDisabled":                             0x1229,
		"Settings:RightsManagementInformation":              0x122B,
		"DocumentLibrary:ContentType":                       0x130C,
		"ItemOperations:MoveAlways":                         0x1419,
		"ComposeMail:Mime":                                  0x1510,
		"ComposeMail:AccountId":                             0x1513,
		"ComposeMail:ForwardeeEmail":                        0x1518,
		"Email2:ConversationId":                             0x1609,
		"Email2:MeetingMessageType":                         0x1613,
		"Email2:Send":                                       0x1617,
		"Notes:Category":                                    0x1709,
		"RightsManagement:RemoveRightsManagementProtection": 0x1818,
	}
	for name, tok := range want {
		got, ok := LookupTag(name)
		if !ok {
			t.Errorf("%s: unknown", name)
			continue
		}
		if uint16(got) != tok {
			t.Errorf("%s = 0x%04x, want 0x%04x", name, uint16(got), tok)
		}
		if got.String() != name {
			t.Errorf("String(%s) = %s", name, got)
		}
	}
	if NumPages != 25 {
		t.Errorf("NumPages = %d", NumPages)
	}
	for p, cp := range codePages {
		if firstToken+len(cp.tags)-1 > 0x3f {
			t.Errorf("page %s has %d tags (exceeds 0x3F)", cp.name, len(cp.tags))
		}
		if cp.name == "" {
			t.Errorf("page %d has no name", p)
		}
	}
}

// Golden vectors: well-known EAS request bodies.
var golden = []struct {
	name string
	doc  *Node
	hex  string
}{
	{
		// FolderSync with SyncKey 0 ([MS-ASCMD] 4.5.1).
		"FolderSync initial",
		E("FolderHierarchy:FolderSync", S("FolderHierarchy:SyncKey", "0")),
		"03 01 6a 00 00 07 56 52 03 30 00 01 01",
	},
	{
		// Provision phase 1 with policy type.
		"Provision",
		E("Provision:Provision", E("Provision:Policies", E("Provision:Policy",
			S("Provision:PolicyType", "MS-EAS-Provisioning-WBXML")))),
		"03 01 6a 00 00 0e 45 46 47 48 03 4d 53 2d 45 41 53 2d 50 72 6f 76 69 73 69 6f 6e 69 6e 67 2d 57 42 58 4d 4c 00 01 01 01 01",
	},
	{
		// Sync with page switches inside the tree and an empty element.
		"Sync",
		E("AirSync:Sync", E("AirSync:Collections", E("AirSync:Collection",
			S("AirSync:SyncKey", "1"), S("AirSync:CollectionId", "5"), E("AirSync:DeletesAsMoves"),
			E("AirSync:Options", E("AirSyncBase:BodyPreference", S("AirSyncBase:Type", "4")))))),
		"03 01 6a 00 45 5c 4f 4b 03 31 00 01 52 03 35 00 01 1e 57 00 11 45 46 03 34 00 01 01 01 01 01 01",
	},
	{
		// Opaque data (SendMail MIME).
		"SendMail opaque",
		E("ComposeMail:SendMail", S("ComposeMail:ClientId", "c1"), E("ComposeMail:SaveInSentItems"),
			Op("ComposeMail:Mime", []byte("Hi"))),
		"03 01 6a 00 00 15 45 51 03 63 31 00 01 08 50 c3 02 48 69 01 01",
	},
}

func TestGoldenEncode(t *testing.T) {
	for _, g := range golden {
		got, err := Marshal(g.doc)
		if err != nil {
			t.Fatalf("%s: %v", g.name, err)
		}
		want := mustHex(t, g.hex)
		if !bytes.Equal(got, want) {
			t.Errorf("%s:\n got % x\nwant % x", g.name, got, want)
		}
		back, err := Unmarshal(want)
		if err != nil {
			t.Fatalf("%s: decode: %v", g.name, err)
		}
		if !reflect.DeepEqual(normalize(back), normalize(g.doc)) {
			t.Errorf("%s: roundtrip mismatch:\n%s\nvs\n%s", g.name, back, g.doc)
		}
	}
}

// normalize clears nil/empty differences for comparison.
func normalize(n *Node) *Node {
	if n == nil {
		return nil
	}
	c := &Node{Tag: n.Tag, Text: n.Text, IsOpaque: n.IsOpaque}
	if n.IsOpaque {
		c.Opaque = append([]byte{}, n.Opaque...)
	}
	for _, ch := range n.Children {
		c.Children = append(c.Children, normalize(ch))
	}
	return c
}

func TestDecodeStringTableAndEntities(t *testing.T) {
	// String table "abc\0xyz\0"; FolderSync/SyncKey with STR_T 4 and an entity.
	data := mustHex(t, "03 01 6a 08 61 62 63 00 78 79 7a 00 00 07 56 52 83 04 02 41 01 4c 83 00 01 01")
	n, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := n.TextOf("FolderHierarchy:SyncKey"); got != "xyzA" {
		t.Errorf("SyncKey = %q", got)
	}
	if got := n.TextOf("FolderHierarchy:Status"); got != "abc" {
		t.Errorf("Status = %q", got)
	}
}

func TestDecodeUnknownTagsAndPages(t *testing.T) {
	// Page 30 is unknown; token 0x3f on page 0 is unassigned.
	data := mustHex(t, "03 01 6a 00 45 7f 03 78 00 01 00 1e 06 01")
	n, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Children) != 2 || n.Children[0].Tag.String() != "AirSync:0x3f" || n.Children[0].Text != "x" {
		t.Fatalf("unexpected tree:\n%s", n)
	}
	if !strings.HasPrefix(n.Children[1].Tag.String(), "Page30:") {
		t.Errorf("tag = %s", n.Children[1].Tag)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, h := range []string{
		"03",                         // truncated header
		"03 01 6a 05 00",             // string table longer than data
		"03 01 6a 00 45",             // missing END
		"03 01 6a 00 c5 01",          // attributes
		"03 01 6a 00 45 c3 05 41 01", // opaque length beyond data
		"03 01 6a 00 45 03 41",       // unterminated inline string
		"03 01 6a 00 01",             // END as root
		"03 01 6a 00 45 83 09 01",    // bad string table reference
	} {
		if _, err := Unmarshal(mustHex(t, h)); err == nil {
			t.Errorf("%s: expected error", h)
		}
	}
	if n, err := Unmarshal(nil); n != nil || err != nil {
		t.Errorf("empty input: %v %v", n, err)
	}
}

func TestDepthLimit(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{3, 1, 0x6a, 0})
	for i := 0; i < maxDepth+5; i++ {
		b.WriteByte(0x45)
	}
	for i := 0; i < maxDepth+5; i++ {
		b.WriteByte(1)
	}
	if _, err := Unmarshal(b.Bytes()); err == nil {
		t.Fatal("expected depth error")
	}
}

func TestMBUint(t *testing.T) {
	for _, v := range []uint32{0, 1, 0x7f, 0x80, 0x3fff, 0x4000, 1 << 28, 0xffffffff} {
		var b bytes.Buffer
		writeMBUint(&b, v)
		d := &decoder{data: b.Bytes()}
		got, err := d.mbUint()
		if err != nil || got != v {
			t.Errorf("%d: got %d %v (% x)", v, got, err, b.Bytes())
		}
	}
}

func TestTextWithNULBecomesOpaque(t *testing.T) {
	data, err := Marshal(S("AirSync:SyncKey", "a\x00b"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if n.Value() != "a\x00b" {
		t.Errorf("value %q", n.Value())
	}
}

func TestNodeHelpers(t *testing.T) {
	n := E("AirSync:Sync", E("AirSync:Collections",
		E("AirSync:Collection", S("AirSync:Status", "1"), S("AirSync:CollectionId", "a")),
		E("AirSync:Collection", S("AirSync:Status", "x"), S("AirSync:CollectionId", "b"))))
	cols := n.Path("AirSync:Collections").All("AirSync:Collection")
	if len(cols) != 2 || cols[1].TextOf("AirSync:CollectionId") != "b" {
		t.Fatal("All/TextOf")
	}
	if cols[0].IntOf("AirSync:Status", -1) != 1 || cols[1].IntOf("AirSync:Status", -1) != -1 {
		t.Error("IntOf")
	}
	var nilNode *Node
	if nilNode.Child("AirSync:Sync") != nil || nilNode.TextOf("x") != "" || nilNode.Path("AirSync:Sync") != nil {
		t.Error("nil safety")
	}
	if !strings.Contains(n.String(), "<AirSync:CollectionId>b</AirSync:CollectionId>") {
		t.Errorf("String:\n%s", n)
	}
	defer func() {
		if recover() == nil {
			t.Error("T must panic for unknown names")
		}
	}()
	T("AirSync:Nope")
}
