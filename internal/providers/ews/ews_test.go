// SPDX-License-Identifier: AGPL-3.0-or-later

package ews

import (
	"context"
	"encoding/base64"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(got) {
		t.Errorf("%s mismatch:\n got: %s\nwant: %s", name, got, want)
	}
}

func TestGoldenRequests(t *testing.T) {
	c, _ := NewClient(Config{URL: "https://x/EWS/Exchange.asmx", Auth: AuthBasic, Mailbox: "info@firma.example"})
	p := &Provider{Client: c}
	golden(t, "syncfolderitems.xml", c.Envelope(p.SyncFolderItemsRequest("inbox", "H4sIAAA=", 512)))
	v := Vacation{C: c, Address: "anna@firma.example"}
	golden(t, "setoof.xml", v.SetOofRequest(vacation.Settings{Enabled: true, InternalText: "Bin im Urlaub & weg",
		Start: time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 1, 6, 0, 0, 0, 0, time.UTC)}))
}

const soapHead = `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`
const soapTail = `</s:Body></s:Envelope>`
const ns = `xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"`

const mime1 = "From: Bob <bob@example.org>\r\nSubject: Hi\r\nMessage-ID: <e1@example.org>\r\n\r\nBody\r\n"

// fakeEWS answers by the operation element in the request.
type fakeEWS struct {
	mu     sync.Mutex
	bodies []string
	auth   []string
	syncs  int
}

var opRE = regexp.MustCompile(`<soap:Body><m:(\w+)`)

func (f *fakeEWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	body := string(b)
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	m := opRE.FindStringSubmatch(body)
	if m == nil {
		http.Error(w, "bad", 400)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	ok := `ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>`
	var resp string
	switch m[1] {
	case "GetFolder":
		var msgs strings.Builder
		for _, id := range []string{"inbox", "sentitems", "drafts", "deleteditems", "junkemail"} {
			msgs.WriteString(`<m:GetFolderResponseMessage ` + ok + `<m:Folders><t:Folder><t:FolderId Id="ID-` + id + `"/></t:Folder></m:Folders></m:GetFolderResponseMessage>`)
		}
		resp = `<m:GetFolderResponse ` + ns + `><m:ResponseMessages>` + msgs.String() + `</m:ResponseMessages></m:GetFolderResponse>`
	case "FindFolder":
		resp = `<m:FindFolderResponse ` + ns + `><m:ResponseMessages><m:FindFolderResponseMessage ` + ok +
			`<m:RootFolder><t:Folders>` +
			`<t:Folder><t:FolderId Id="ID-inbox"/><t:ParentFolderId Id="ROOT"/><t:FolderClass>IPF.Note</t:FolderClass><t:DisplayName>Posteingang</t:DisplayName><t:TotalCount>1</t:TotalCount><t:UnreadCount>1</t:UnreadCount></t:Folder>` +
			`<t:Folder><t:FolderId Id="ID/sub+1"/><t:ParentFolderId Id="ID-inbox"/><t:FolderClass>IPF.Note</t:FolderClass><t:DisplayName>Projekte</t:DisplayName></t:Folder>` +
			`<t:Folder><t:FolderId Id="ID-cal"/><t:ParentFolderId Id="ROOT"/><t:FolderClass>IPF.Appointment</t:FolderClass><t:DisplayName>Kalender</t:DisplayName></t:Folder>` +
			`</t:Folders></m:RootFolder></m:FindFolderResponseMessage></m:ResponseMessages></m:FindFolderResponse>`
	case "SyncFolderItems":
		f.mu.Lock()
		f.syncs++
		n := f.syncs
		f.mu.Unlock()
		changes := `<t:Create><t:Message><t:ItemId Id="I1" ChangeKey="c1"/><t:Subject>Hi</t:Subject>` +
			`<t:DateTimeReceived>2026-11-02T08:00:00Z</t:DateTimeReceived><t:Size>120</t:Size><t:Categories><t:String>Kunde</t:String></t:Categories>` +
			`<t:IsRead>false</t:IsRead><t:InternetMessageId>&lt;e1@example.org&gt;</t:InternetMessageId>` +
			`<t:From><t:Mailbox><t:Name>Bob</t:Name><t:EmailAddress>bob@example.org</t:EmailAddress></t:Mailbox></t:From>` +
			`<t:ConversationId Id="CONV1"/><t:Flag><t:FlagStatus>Flagged</t:FlagStatus></t:Flag></t:Message></t:Create>`
		state := "S1"
		if n > 1 {
			if strings.Contains(body, "<m:SyncState>BROKEN</m:SyncState>") {
				resp = `<m:SyncFolderItemsResponse ` + ns + `><m:ResponseMessages><m:SyncFolderItemsResponseMessage ResponseClass="Error">` +
					`<m:MessageText>bad</m:MessageText><m:ResponseCode>ErrorInvalidSyncStateData</m:ResponseCode></m:SyncFolderItemsResponseMessage></m:ResponseMessages></m:SyncFolderItemsResponse>`
				break
			}
			if strings.Contains(body, "<m:SyncState>S1</m:SyncState>") {
				changes = `<t:ReadFlagChange><t:ItemId Id="I1"/><t:IsRead>true</t:IsRead></t:ReadFlagChange><t:Delete><t:ItemId Id="I0"/></t:Delete>`
				state = "S2"
			}
		}
		resp = `<m:SyncFolderItemsResponse ` + ns + `><m:ResponseMessages><m:SyncFolderItemsResponseMessage ` + ok +
			`<m:SyncState>` + state + `</m:SyncState><m:IncludesLastItemInRange>true</m:IncludesLastItemInRange><m:Changes>` + changes +
			`</m:Changes></m:SyncFolderItemsResponseMessage></m:ResponseMessages></m:SyncFolderItemsResponse>`
	case "GetItem":
		item := `<t:Message><t:ItemId Id="I1"/><t:IsRead>true</t:IsRead></t:Message>`
		if strings.Contains(body, "IncludeMimeContent") {
			item = `<t:Message><t:MimeContent CharacterSet="UTF-8">` + base64.StdEncoding.EncodeToString([]byte(mime1)) + `</t:MimeContent><t:ItemId Id="I1"/></t:Message>`
		}
		resp = `<m:GetItemResponse ` + ns + `><m:ResponseMessages><m:GetItemResponseMessage ` + ok + `<m:Items>` + item +
			`</m:Items></m:GetItemResponseMessage></m:ResponseMessages></m:GetItemResponse>`
	case "MoveItem":
		resp = `<m:MoveItemResponse ` + ns + `><m:ResponseMessages><m:MoveItemResponseMessage ` + ok +
			`<m:Items><t:Message><t:ItemId Id="I1-moved"/></t:Message></m:Items></m:MoveItemResponseMessage></m:ResponseMessages></m:MoveItemResponse>`
	case "UpdateItem", "DeleteItem", "CreateItem":
		resp = `<m:` + m[1] + `Response ` + ns + `><m:ResponseMessages><m:` + m[1] + `ResponseMessage ` + ok +
			`<m:Items><t:Message><t:ItemId Id="NEW1"/></t:Message></m:Items></m:` + m[1] + `ResponseMessage></m:ResponseMessages></m:` + m[1] + `Response>`
	case "SetUserOofSettingsRequest":
		resp = `<SetUserOofSettingsResponse xmlns="http://schemas.microsoft.com/exchange/services/2006/messages"><ResponseMessage ResponseClass="Success"><ResponseCode>NoError</ResponseCode></ResponseMessage></SetUserOofSettingsResponse>`
	case "FindItem":
		resp = `<m:FindItemResponse ` + ns + `><m:ResponseMessages><m:FindItemResponseMessage ` + ok +
			`<m:RootFolder IncludesLastItemInRange="true"><t:Items><t:CalendarItem><t:ItemId Id="CAL1"/><t:UID>uid-1</t:UID>` +
			`<t:Subject>Jour fixe</t:Subject><t:Start>2026-11-04T09:00:00Z</t:Start><t:End>2026-11-04T10:00:00Z</t:End>` +
			`<t:MyResponseType>Tentative</t:MyResponseType></t:CalendarItem></t:Items></m:RootFolder></m:FindItemResponseMessage></m:ResponseMessages></m:FindItemResponse>`
	default:
		resp = `<s:Fault><faultcode>s:Client</faultcode><faultstring>unsupported ` + m[1] + `</faultstring></s:Fault>`
	}
	_, _ = io.WriteString(w, soapHead+resp+soapTail)
}

func TestProviderAgainstFake(t *testing.T) {
	fake := &fakeEWS{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p, err := New(Config{URL: srv.URL, Auth: AuthBasic, Username: "FIRMA\\anna", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].Role != domain.RoleInbox || fs[1].RemoteID != "ID-inbox|ID/sub+1" || fs[1].Name != "Projekte" {
		t.Fatalf("%+v", fs)
	}
	if !strings.HasPrefix(fake.auth[0], "Basic ") {
		t.Fatal(fake.auth)
	}
	d, st, err := p.Sync(ctx, "ID-inbox", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 {
		t.Fatalf("%+v", d)
	}
	a := d.Added[0]
	kw, _ := p.km.ToKeyword("Kunde")
	got := map[domain.Flag]bool{}
	for _, f := range a.Flags {
		got[f] = true
	}
	if a.Header.MessageID != "e1@example.org" || a.Header.ServerThreadID != "CONV1" || a.Header.From[0].Email != "bob@example.org" ||
		!got[domain.FlagFlagged] || got[domain.FlagSeen] || !got[domain.Flag(kw)] || a.Size != 120 {
		t.Fatalf("%+v", a)
	}
	d, st, err = p.Sync(ctx, "ID-inbox", st)
	if err != nil || len(d.Removed) != 1 || len(d.FlagsChanged) != 1 || d.FlagsChanged[0].Flags[0] != domain.FlagSeen || !strings.Contains(string(st), "S2") {
		t.Fatalf("%+v %s %v", d, st, err)
	}
	d, _, err = p.Sync(ctx, "ID-inbox", `{"s":"BROKEN"}`)
	if err != nil || !d.FullResync || len(d.Added) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	rc, err := p.FetchRaw(ctx, domain.RemoteRef{ID: "I1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	if string(raw) != mime1 {
		t.Fatal(string(raw))
	}
	refs, err := p.Move(ctx, []domain.RemoteRef{{FolderRemoteID: "ID-inbox", ID: "I1"}}, "ID-inbox|ID/sub+1")
	if err != nil || refs[0].ID != "I1-moved" {
		t.Fatal(refs, err)
	}
	if err := p.SetFlags(ctx, refs, []domain.Flag{domain.FlagSeen, domain.Flag(kw)}, []domain.Flag{domain.FlagFlagged}); err != nil {
		t.Fatal(err)
	}
	last := fake.bodies[len(fake.bodies)-1]
	if !strings.Contains(last, `<t:IsRead>true</t:IsRead>`) || !strings.Contains(last, "NotFlagged") || !strings.Contains(last, "<t:String>Kunde</t:String>") {
		t.Fatal(last)
	}
	if err := p.Send(ctx, domain.OutgoingMessage{Raw: []byte(mime1), Recipients: []string{"x@example.org"}}); err != nil {
		t.Fatal(err)
	}
	last = fake.bodies[len(fake.bodies)-1]
	if !strings.Contains(last, `MessageDisposition="SendAndSaveCopy"`) || !strings.Contains(last, `DistinguishedFolderId Id="sentitems"`) {
		t.Fatal(last)
	}
	if err := (Vacation{C: p.Client, Address: "anna@firma.example"}).Push(ctx, vacation.Settings{Enabled: true, InternalText: "weg"}); err != nil {
		t.Fatal(err)
	}
	evs, err := p.ListEvents(ctx, "calendar", time.Now(), time.Now().AddDate(0, 1, 0))
	if err != nil || len(evs) != 1 || evs[0].Response != "tentative" || evs[0].UID != "uid-1" {
		t.Fatalf("%+v %v", evs, err)
	}
	if err := p.Respond(ctx, "calendar", "CAL1", pimapi.Accept, "gern", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.bodies[len(fake.bodies)-1], `<t:AcceptItem><t:ReferenceItemId Id="CAL1"/>`) {
		t.Fatal(fake.bodies[len(fake.bodies)-1])
	}
}

func TestSoapFaultAndBearer(t *testing.T) {
	fake := &fakeEWS{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := NewClient(Config{URL: srv.URL, Tokens: staticTok("tok")})
	if err != nil {
		t.Fatal(err)
	}
	err = c.call(context.Background(), `<m:Bogus/>`, nil)
	if !IsCode(err, "s:Client") {
		t.Fatal(err)
	}
	if fake.auth[0] != "Bearer tok" {
		t.Fatal(fake.auth)
	}
}

type staticTok string

func (s staticTok) Token(context.Context) (string, error) { return string(s), nil }
