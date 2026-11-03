// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestNormalizeMessageID(t *testing.T) {
	for in, want := range map[string]string{
		"<abc@Example.COM>":         "abc@example.com",
		"  < abc@example.com >\r\n": "abc@example.com",
		"abc@EXAMPLE.com":           "abc@example.com",
		"<CAF=x+Y@mail.Gmail.com>":  "CAF=x+Y@mail.gmail.com",
		"":                          "",
		"<>":                        "",
		"<a@b> trailing":            "a@b",
	} {
		if got := NormalizeMessageID(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestParseMessageIDList(t *testing.T) {
	got := ParseMessageIDList("<a@x.org>\r\n\t<b@X.org> <a@x.org> comment <c@x.org>")
	if want := []string{"a@x.org", "b@x.org", "c@x.org"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if got := ParseMessageIDList("a@x.org b@x.org"); !reflect.DeepEqual(got, []string{"a@x.org", "b@x.org"}) {
		t.Errorf("unbracketed: %v", got)
	}
	if got := ParseMessageIDList(""); got != nil {
		t.Errorf("empty: %v", got)
	}
}

func TestStateRoundTrip(t *testing.T) {
	s := &folderState{UIDValidity: 42, UIDNext: 100001, ModSeq: 0}
	for u := uint32(1); u <= 100000; u++ {
		if u%7 == 0 {
			continue
		}
		s.UIDs = append(s.UIDs, u)
		fp := flagPrint{Mask: maskSeen}
		if u%13 == 0 {
			fp = fingerprint([]imap.Flag{"$Label", imap.FlagFlagged})
		}
		s.Flags = append(s.Flags, fp)
	}
	enc := s.encode()
	if len(enc) > 20000 {
		t.Errorf("state too large: %d bytes for %d UIDs", len(enc), len(s.UIDs))
	}
	got, err := decodeState(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Error("round trip mismatch")
	}

	cs := &folderState{UIDValidity: 1, UIDNext: 5, ModSeq: 1 << 40, UIDs: []uint32{1, 3, 4}}
	got, err = decodeState(cs.encode())
	if err != nil || got.ModSeq != cs.ModSeq || got.Flags != nil || !reflect.DeepEqual(got.UIDs, cs.UIDs) {
		t.Errorf("condstore state = %+v, %v", got, err)
	}
	if got.index(3) != 1 || got.index(2) != -1 {
		t.Error("index")
	}

	for _, bad := range []domain.SyncState{"x", statePrefix + "!!!", statePrefix + "AAAA"} {
		if _, err := decodeState(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	if s, err := decodeState(""); err != nil || s.UIDValidity != 0 {
		t.Error("empty state")
	}
}

func TestFingerprint(t *testing.T) {
	a := fingerprint([]imap.Flag{`\SEEN`, "$Work", "$home"})
	b := fingerprint([]imap.Flag{"$HOME", `\Seen`, "$work", `\Recent`})
	if a != b {
		t.Errorf("fingerprints differ: %+v %+v", a, b)
	}
	if fingerprint([]imap.Flag{"$Forwarded"}).Mask != maskForwarded {
		t.Error("forwarded")
	}
	if fingerprint([]imap.Flag{"$a"}) == fingerprint([]imap.Flag{"$b"}) {
		t.Error("keyword hash collision")
	}
}

func TestConvFlags(t *testing.T) {
	got := convFlags([]imap.Flag{`\SEEN`, `\recent`, "$forwarded", "custom"})
	want := []domain.Flag{domain.FlagSeen, domain.FlagForwarded, "custom"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestAssignRoles(t *testing.T) {
	list := []*imap.ListData{
		{Mailbox: "INBOX", Delim: '.'},
		{Mailbox: "INBOX.Sent", Delim: '.'}, // name match below INBOX
		{Mailbox: "Sent Items", Delim: '.', Attrs: []imap.MailboxAttr{imap.MailboxAttrSent}}, // special-use wins
		{Mailbox: "[Gmail].All Mail", Delim: '.', Attrs: []imap.MailboxAttr{`\ALL`}},
		{Mailbox: "Papierkorb", Delim: '.'},
		{Mailbox: "Deleted Items", Delim: '.'}, // second trash candidate: ignored (longer)
		{Mailbox: "Projekte.Archiv", Delim: '.'},
		{Mailbox: "Junk", Delim: '.', Attrs: []imap.MailboxAttr{`\junk`}},
		{Mailbox: "Entwürfe", Delim: '.'},
	}
	got := assignRoles(list)
	want := []domain.FolderRole{
		domain.RoleInbox, domain.RoleNone, domain.RoleSent, domain.RoleArchive, domain.RoleTrash,
		domain.RoleNone, domain.RoleNone, domain.RoleJunk, domain.RoleDrafts,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	// explicit \Archive beats \All
	got = assignRoles([]*imap.ListData{
		{Mailbox: "All", Attrs: []imap.MailboxAttr{imap.MailboxAttrAll}},
		{Mailbox: "Arch", Attrs: []imap.MailboxAttr{imap.MailboxAttrArchive}},
	})
	if got[0] != domain.RoleNone || got[1] != domain.RoleArchive {
		t.Errorf("all/archive: %v", got)
	}
}

func TestHasAttachments(t *testing.T) {
	text := &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"}
	sig := &imap.BodyStructureSinglePart{Type: "application", Subtype: "pgp-signature"}
	pdf := &imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf"}
	inlineImg := &imap.BodyStructureSinglePart{Type: "image", Subtype: "png",
		Extended: &imap.BodyStructureSinglePartExt{Disposition: &imap.BodyStructureDisposition{Value: "inline"}}}
	namedImg := &imap.BodyStructureSinglePart{Type: "image", Subtype: "png", Params: map[string]string{"name": "a.png"}}
	cases := []struct {
		bs   imap.BodyStructure
		want bool
	}{
		{text, false},
		{&imap.BodyStructureMultiPart{Subtype: "signed", Children: []imap.BodyStructure{text, sig}}, false},
		{&imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{text, pdf}}, true},
		{&imap.BodyStructureMultiPart{Subtype: "related", Children: []imap.BodyStructure{text, inlineImg}}, false},
		{&imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{text, namedImg}}, true},
		{nil, false},
	}
	for i, c := range cases {
		if got := hasAttachments(c.bs); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestMapCopyUID(t *testing.T) {
	m := mapCopyUID(imap.UIDSetNum(3, 1, 7), imap.UIDSetNum(10, 11, 12))
	if m[1] != 10 || m[3] != 11 || m[7] != 12 {
		t.Errorf("mapping = %v", m)
	}
	if mapCopyUID(imap.UIDSetNum(1, 2), imap.UIDSetNum(5)) != nil {
		t.Error("mismatched sizes must yield nil")
	}
}

func TestEncodeMailboxUTF7(t *testing.T) {
	for in, want := range map[string]string{
		"INBOX":             "INBOX",
		"Entwürfe":          "Entw&APw-rfe",
		"Gelöschte Objekte": "Gel&APY-schte Objekte",
		"A&B":               "A&-B",
		"日本語":               "&ZeVnLIqe-",
	} {
		if got := encodeMailboxUTF7(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestParseIDFetch(t *testing.T) {
	uid, ids, ok := parseIDFetch(`12 FETCH (UID 345 EMAILID (M6d99ac3275bb4e) THREADID (T64b478a75b7ea9))`)
	if !ok || uid != 345 || ids.object != "M6d99ac3275bb4e" || ids.thread != "T64b478a75b7ea9" {
		t.Errorf("objectid: %d %+v %v", uid, ids, ok)
	}
	uid, ids, ok = parseIDFetch(`1 FETCH (X-GM-THRID 1266894439832287888 X-GM-MSGID 1278455344230334865 UID 7)`)
	if !ok || uid != 7 || ids.object != "1278455344230334865" || ids.thread != "1266894439832287888" {
		t.Errorf("gmail: %d %+v %v", uid, ids, ok)
	}
	if _, ids, ok = parseIDFetch(`2 FETCH (UID 8 EMAILID (Mx) THREADID NIL)`); !ok || ids.thread != "" || ids.object != "Mx" {
		t.Errorf("NIL thread: %+v", ids)
	}
	if _, _, ok = parseIDFetch(`3 EXISTS`); ok {
		t.Error("non-fetch line parsed")
	}
}

// fakeIDServer is a scripted IMAP server for the object-id side connection.
type fakeIDServer struct {
	caps     string
	mu       sync.Mutex
	commands []string
}

func (f *fakeIDServer) serve(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return ln.Addr().String()
}

func (f *fakeIDServer) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	w := func(s string) { c.Write([]byte(s + "\r\n")) }
	w("* OK fake ready")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		tag, cmd, _ := strings.Cut(line, " ")
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()
		up := strings.ToUpper(cmd)
		switch {
		case up == "CAPABILITY":
			w("* CAPABILITY " + f.caps)
			w(tag + " OK done")
		case strings.HasPrefix(up, "AUTHENTICATE XOAUTH2") && !strings.Contains(f.caps, "SASL-IR"):
			w("+ ")
			resp, _ := br.ReadString('\n')
			dec, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(resp))
			if strings.Contains(string(dec), "auth=Bearer tok") {
				w(tag + " OK authenticated")
			} else {
				w(tag + " NO bad token")
			}
		case strings.HasPrefix(up, "AUTHENTICATE PLAIN "):
			dec, _ := base64.StdEncoding.DecodeString(strings.Fields(cmd)[2])
			if string(dec) == "\x00alice\x00secret" {
				w(tag + " OK authenticated")
			} else {
				w(tag + " NO auth failed")
			}
		case strings.HasPrefix(up, "LOGIN "):
			if cmd == `LOGIN "alice" "sec\"ret"` {
				w(tag + " OK logged in")
			} else {
				w(tag + " NO login failed")
			}
		case strings.HasPrefix(up, "EXAMINE "):
			w("* 2 EXISTS")
			w(tag + " OK [READ-ONLY] done")
		case strings.HasPrefix(up, "UID FETCH "):
			if strings.Contains(up, "X-GM-MSGID") {
				w("* 1 FETCH (UID 1 X-GM-MSGID 1001 X-GM-THRID 2001)")
				w("* 2 FETCH (X-GM-THRID 2001 X-GM-MSGID 1002 UID 2)")
			} else {
				w("* 1 FETCH (UID 1 EMAILID (Ma) THREADID (Ta))")
				w(`* 2 FETCH (UID 2 EMAILID (Mb) THREADID NIL BODY[] {5}`)
				c.Write([]byte("hello)\r\n"))
			}
			w(tag + " OK done")
		case up == "LOGOUT":
			w("* BYE")
			w(tag + " OK bye")
			return
		default:
			w(tag + " BAD unknown")
		}
	}
}

func fakeProvider(t *testing.T, addr string, caps imap.CapSet, mutate func(*Config)) *Provider {
	host, port, _ := net.SplitHostPort(addr)
	cfg := Config{
		Server:   domain.ServerConfig{Host: host, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "alice"},
		Password: "secret",
	}
	cfg.Server.Port = atoi(port)
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.caps = caps
	t.Cleanup(func() { p.Close() })
	return p
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestServerIDsSidecar(t *testing.T) {
	c := context.Background()

	f := &fakeIDServer{caps: "IMAP4rev1 AUTH=PLAIN SASL-IR X-GM-EXT-1"}
	p := fakeProvider(t, f.serve(t), imap.CapSet{capGmail: {}}, nil)
	got := p.serverIDsLocked(c, "Entwürfe", []uint32{1, 2})
	want := map[uint32]serverIDs{1: {"1001", "2001"}, 2: {"1002", "2001"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gmail ids = %+v", got)
	}
	// second call reuses the connection and the selected folder
	p.serverIDsLocked(c, "Entwürfe", []uint32{1})
	f.mu.Lock()
	cmds := strings.Join(f.commands, "|")
	f.mu.Unlock()
	if strings.Count(cmds, "EXAMINE") != 1 || !strings.Contains(cmds, `EXAMINE "Entw&APw-rfe"`) {
		t.Errorf("commands = %s", cmds)
	}

	f2 := &fakeIDServer{caps: "IMAP4rev1 OBJECTID"} // no AUTH=PLAIN: LOGIN with quoting
	p2 := fakeProvider(t, f2.serve(t), imap.CapSet{imap.CapObjectID: {}}, func(c *Config) { c.Password = `sec"ret` })
	got = p2.serverIDsLocked(c, "INBOX", []uint32{1, 2})
	want = map[uint32]serverIDs{1: {"Ma", "Ta"}, 2: {"Mb", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("objectid ids = %+v", got)
	}

	f3 := &fakeIDServer{caps: "IMAP4rev1 AUTH=XOAUTH2 OBJECTID"} // continuation-based SASL
	p3 := fakeProvider(t, f3.serve(t), imap.CapSet{imap.CapObjectID: {}}, func(c *Config) {
		c.Server.Auth = domain.AuthOAuth2
		c.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "tok", nil })
	})
	if got := p3.serverIDsLocked(c, "INBOX", []uint32{1}); got[1].object != "Ma" {
		t.Errorf("xoauth2 ids = %+v", got)
	}
	p4 := fakeProvider(t, f3.serve(t), imap.CapSet{imap.CapObjectID: {}}, func(c *Config) {
		c.Server.Auth = domain.AuthOAuth2
		c.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "bad", nil })
	})
	if got := p4.serverIDsLocked(c, "INBOX", []uint32{1}); got != nil {
		t.Errorf("expected nil on auth failure, got %+v", got)
	}

	// no capability: no connection at all
	p5 := fakeProvider(t, "127.0.0.1:1", imap.CapSet{}, nil)
	if got := p5.serverIDsLocked(c, "INBOX", []uint32{1}); got != nil {
		t.Errorf("got %+v", got)
	}
}

func TestMailCaps(t *testing.T) {
	mc := mailCaps(imap.CapSet{imap.CapIMAP4rev2: {}, imap.CapQResync: {}, "THREAD=REFERENCES": {}, capGmail: {}})
	if !mc.Move || !mc.Idle || !mc.CondStore || !mc.Thread || !mc.ObjectID {
		t.Errorf("caps = %+v", mc)
	}
}
