// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// ==== fake JMAP server =========================================================

type fEmail struct {
	ID, Blob, Thread string
	Mailboxes        map[string]bool
	Keywords         map[string]bool
	Subject          string
	Raw              string
	Received         time.Time
}

type fake struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	state    int
	changes  map[int][2][]string // state -> created/updated ids (destroyed in [1] prefixed "-")
	mboxes   []mailbox
	emails   map[string]*fEmail
	blobs    map[string]string
	nextID   int
	subs     []map[string]any
	vacation map[string]any
	noES     bool
	events   chan struct{}
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, changes: map[int][2][]string{}, emails: map[string]*fEmail{}, blobs: map[string]string{},
		events: make(chan struct{}, 4), vacation: map[string]any{"id": "singleton", "isEnabled": false}}
	f.mboxes = []mailbox{{ID: "mb-in", Name: "Inbox", Role: "inbox"}, {ID: "mb-sent", Name: "Sent", Role: "sent"},
		{ID: "mb-arch", Name: "Archive", Role: "archive"}, {ID: "mb-sub", Name: "Kunden", ParentID: "mb-arch"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jmap", f.session)
	mux.HandleFunc("POST /api", f.api)
	mux.HandleFunc("POST /upload/{acc}", f.upload)
	mux.HandleFunc("GET /download/{acc}/{blob}/{name}", f.download)
	mux.HandleFunc("GET /events", f.eventSource)
	f.srv = httptest.NewServer(f.auth(mux))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) auth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "anna" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (f *fake) session(w http.ResponseWriter, _ *http.Request) {
	es := "/events?types={types}&closeafter={closeafter}&ping={ping}"
	if f.noES {
		es = ""
	}
	json.NewEncoder(w).Encode(map[string]any{
		"capabilities": map[string]any{CapCore: map[string]any{}, CapMail: map[string]any{}, CapSubmission: map[string]any{}, CapVacation: map[string]any{}},
		"accounts": map[string]any{"A1": map[string]any{"name": "anna", "isPersonal": true,
			"accountCapabilities": map[string]any{CapMail: map[string]any{}, CapSubmission: map[string]any{}, CapVacation: map[string]any{}}}},
		"primaryAccounts": map[string]string{CapMail: "A1", CapSubmission: "A1", CapVacation: "A1"},
		"apiUrl":          "/api", "downloadUrl": f.srv.URL + "/download/{accountId}/{blobId}/{name}?type={type}",
		"uploadUrl": "/upload/{accountId}", "eventSourceUrl": es, "state": "s1",
	})
}

// bump records a change; callers hold f.mu.
func (f *fake) bump(created, updated []string) {
	f.state++
	f.changes[f.state] = [2][]string{created, updated}
	select {
	case f.events <- struct{}{}:
	default:
	}
}

func (f *fake) addEmail(mbox, subject string, kw ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("e%d", f.nextID)
	raw := "From: bob@example.org\r\nSubject: " + subject + "\r\nMessage-ID: <" + id + "@x>\r\n\r\nbody of " + id + "\r\n"
	e := &fEmail{ID: id, Blob: "b" + id, Thread: "t" + id, Mailboxes: map[string]bool{mbox: true}, Keywords: map[string]bool{},
		Subject: subject, Raw: raw, Received: time.Date(2026, 11, 1, 10, f.nextID, 0, 0, time.UTC)}
	for _, k := range kw {
		e.Keywords[k] = true
	}
	f.emails[id] = e
	f.blobs[e.Blob] = raw
	f.bump([]string{id}, nil)
	return id
}

func (f *fake) emailJSON(e *fEmail, props []string) map[string]any {
	all := map[string]any{"id": e.ID, "blobId": e.Blob, "threadId": e.Thread, "mailboxIds": e.Mailboxes, "keywords": e.Keywords,
		"size": len(e.Raw), "receivedAt": e.Received, "messageId": []string{e.ID + "@x"}, "subject": e.Subject,
		"from": []map[string]string{{"name": "Bob", "email": "bob@example.org"}}, "hasAttachment": false, "preview": "body of " + e.ID}
	if props == nil {
		return all
	}
	out := map[string]any{"id": e.ID}
	for _, p := range props {
		if v, ok := all[p]; ok {
			out[p] = v
		}
	}
	return out
}

func (f *fake) api(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MethodCalls [][3]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]any
	for _, c := range req.MethodCalls {
		var name, id string
		var args map[string]any
		json.Unmarshal(c[0], &name)
		json.Unmarshal(c[1], &args)
		json.Unmarshal(c[2], &id)
		resName, res := f.method(name, args)
		out = append(out, []any{resName, res, id})
	}
	json.NewEncoder(w).Encode(map[string]any{"methodResponses": out, "sessionState": "s1"})
}

func strs(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func (f *fake) method(name string, a map[string]any) (string, any) {
	st := fmt.Sprint(f.state)
	switch name {
	case "Mailbox/get":
		return name, map[string]any{"state": st, "list": f.mboxes}
	case "Mailbox/set":
		created := map[string]any{}
		for k, v := range a["create"].(map[string]any) {
			m := v.(map[string]any)
			f.nextID++
			id := fmt.Sprintf("mb%d", f.nextID)
			parent, _ := m["parentId"].(string)
			f.mboxes = append(f.mboxes, mailbox{ID: id, Name: m["name"].(string), ParentID: parent})
			created[k] = map[string]any{"id": id}
		}
		return name, map[string]any{"created": created}
	case "Email/get":
		var props []string
		if a["properties"] != nil {
			props = strs(a["properties"])
		}
		list := []any{}
		for _, id := range strs(a["ids"]) {
			if e, ok := f.emails[id]; ok {
				list = append(list, f.emailJSON(e, props))
			}
		}
		return name, map[string]any{"state": st, "list": list, "notFound": []string{}}
	case "Email/query":
		mb := a["filter"].(map[string]any)["inMailbox"].(string)
		var ids []string
		for id, e := range f.emails {
			if e.Mailboxes[mb] {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		return name, map[string]any{"ids": ids, "total": len(ids), "position": 0}
	case "Email/changes":
		var since int
		fmt.Sscan(fmt.Sprint(a["sinceState"]), &since)
		if since < f.state-20 {
			return "error", map[string]any{"type": "cannotCalculateChanges"}
		}
		var cr, up []string
		for s := since + 1; s <= f.state; s++ {
			cr = append(cr, f.changes[s][0]...)
			up = append(up, f.changes[s][1]...)
		}
		return name, map[string]any{"oldState": a["sinceState"], "newState": st, "hasMoreChanges": false,
			"created": nonNil(cr), "updated": nonNil(up), "destroyed": []string{}}
	case "Email/set":
		var updated []string
		if u, ok := a["update"].(map[string]any); ok {
			for id, patch := range u {
				e := f.emails[id]
				if e == nil {
					return name, map[string]any{"notUpdated": map[string]any{id: map[string]any{"type": "notFound"}}}
				}
				for path, v := range patch.(map[string]any) {
					kind, key, _ := strings.Cut(path, "/")
					target := e.Keywords
					if kind == "mailboxIds" {
						target = e.Mailboxes
					}
					if v == nil {
						delete(target, key)
					} else {
						target[key] = true
					}
				}
				updated = append(updated, id)
			}
		}
		for _, id := range strs(a["destroy"]) {
			delete(f.emails, id)
			updated = append(updated, id)
		}
		f.bump(nil, updated)
		return name, map[string]any{"newState": fmt.Sprint(f.state)}
	case "Email/import":
		created := map[string]any{}
		for k, v := range a["emails"].(map[string]any) {
			m := v.(map[string]any)
			f.nextID++
			id := fmt.Sprintf("e%d", f.nextID)
			e := &fEmail{ID: id, Blob: m["blobId"].(string), Thread: "t" + id, Mailboxes: map[string]bool{}, Keywords: map[string]bool{},
				Raw: f.blobs[m["blobId"].(string)], Received: time.Now()}
			for mb := range m["mailboxIds"].(map[string]any) {
				e.Mailboxes[mb] = true
			}
			for kw := range m["keywords"].(map[string]any) {
				e.Keywords[kw] = true
			}
			f.emails[id] = e
			created[k] = map[string]any{"id": id, "blobId": e.Blob}
			f.bump([]string{id}, nil)
		}
		return name, map[string]any{"created": created}
	case "Identity/get":
		return name, map[string]any{"list": []map[string]any{{"id": "i1", "name": "Anna", "email": "anna@example.org"},
			{"id": "i2", "name": "Anna Alias", "email": "alias@example.org"}}}
	case "EmailSubmission/set":
		for _, v := range a["create"].(map[string]any) {
			f.subs = append(f.subs, v.(map[string]any))
		}
		return name, map[string]any{"created": map[string]any{"s": map[string]any{"id": "sub1"}}}
	case "VacationResponse/get":
		return name, map[string]any{"list": []any{f.vacation}}
	case "VacationResponse/set":
		for k, v := range a["update"].(map[string]any)["singleton"].(map[string]any) {
			f.vacation[k] = v
		}
		return name, map[string]any{"updated": map[string]any{"singleton": nil}}
	}
	return "error", map[string]any{"type": "unknownMethod"}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (f *fake) upload(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.nextID++
	id := fmt.Sprintf("up%d", f.nextID)
	f.blobs[id] = string(b)
	f.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"blobId": id, "type": r.Header.Get("Content-Type"), "size": len(b)})
}

func (f *fake) download(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	raw, ok := f.blobs[r.PathValue("blob")]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	io.WriteString(w, raw)
}

func (f *fake) eventSource(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("types") != "Email,Mailbox" {
		http.Error(w, "bad types "+r.URL.RawQuery, 400)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	io.WriteString(w, ": hello\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-f.events:
			io.WriteString(w, "event: state\ndata: {\"@type\":\"StateChange\",\"changed\":{\"A1\":{\"Email\":\"x\"}}}\n\n")
			fl.Flush()
		}
	}
}

// ==== tests ====================================================================

func newTestProvider(t *testing.T, f *fake) *Provider {
	p, err := New(Config{SessionURL: f.srv.URL + "/.well-known/jmap", Auth: BasicAuth("anna", "pw"), MinBackoff: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFoldersSyncFlagsMove(t *testing.T) {
	f := newFake(t)
	p := newTestProvider(t, f)
	ctx := context.Background()
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]domain.FolderRole{}
	for _, x := range fs {
		roles[x.RemoteID] = x.Role
	}
	if roles["mb-in"] != domain.RoleInbox || roles["mb-arch"] != domain.RoleArchive {
		t.Fatalf("folders %+v", fs)
	}
	if _, ok := roles["mb-arch/mb-sub"]; !ok {
		t.Fatalf("hierarchy missing: %+v", fs)
	}

	e1 := f.addEmail("mb-in", "Hello", "$seen")
	f.addEmail("mb-in", "Second")
	d, st, err := p.Sync(ctx, "mb-in", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 2 {
		t.Fatalf("added %+v", d.Added)
	}
	var a1 domain.RemoteMessage
	for _, a := range d.Added {
		if a.Ref.ID == e1 {
			a1 = a
		}
	}
	if a1.Header.ServerThreadID != "t"+e1 || a1.Header.MessageID != e1+"@x" || len(a1.Flags) != 1 || a1.Flags[0] != domain.FlagSeen {
		t.Fatalf("message %+v", a1)
	}
	// No changes: no query.
	d, st2, err := p.Sync(ctx, "mb-in", st)
	if err != nil || len(d.Added)+len(d.Removed)+len(d.FlagsChanged) != 0 {
		t.Fatal(err, d)
	}
	// Flag + move.
	ref := domain.RemoteRef{FolderRemoteID: "mb-in", ID: e1}
	if err := p.SetFlags(ctx, []domain.RemoteRef{ref}, []domain.Flag{domain.FlagFlagged, "Kunde"}, []domain.Flag{domain.FlagSeen}); err != nil {
		t.Fatal(err)
	}
	d, st3, err := p.Sync(ctx, "mb-in", st2)
	if err != nil || len(d.FlagsChanged) != 1 {
		t.Fatal(err, d)
	}
	if got := fmt.Sprint(d.FlagsChanged[0].Flags); got != `[Kunde \Flagged]` {
		t.Fatalf("flags %s", got)
	}
	refs, err := p.Move(ctx, []domain.RemoteRef{ref}, "mb-arch/mb-sub")
	if err != nil || refs[0].ID != e1 || refs[0].FolderRemoteID != "mb-arch/mb-sub" {
		t.Fatal(err, refs)
	}
	d, _, err = p.Sync(ctx, "mb-in", st3)
	if err != nil || len(d.Removed) != 1 || d.Removed[0].ID != e1 {
		t.Fatal(err, d)
	}
	d, _, err = p.Sync(ctx, "mb-arch/mb-sub", "")
	if err != nil || len(d.Added) != 1 {
		t.Fatal(err, d)
	}
	// Raw download.
	rc, err := p.FetchRaw(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	if !strings.Contains(string(raw), "Subject: Hello") {
		t.Fatalf("raw %q", raw)
	}
	// Delete.
	if err := p.Delete(ctx, refs); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.emails[e1]; ok {
		t.Fatal("not destroyed")
	}
}

func TestAppendCreateFolderAndSend(t *testing.T) {
	f := newFake(t)
	p := newTestProvider(t, f)
	ctx := context.Background()
	if _, err := p.ListFolders(ctx); err != nil {
		t.Fatal(err)
	}
	nf, err := p.CreateFolder(ctx, "mb-arch", "2026")
	if err != nil || !strings.HasPrefix(nf.RemoteID, "mb-arch/") {
		t.Fatal(err, nf)
	}
	ref, err := p.Append(ctx, nf.RemoteID, strings.NewReader("Subject: appended\r\n\r\nx\r\n"), []domain.Flag{domain.FlagSeen})
	if err != nil || ref.ID == "" {
		t.Fatal(err, ref)
	}
	if e := f.emails[ref.ID]; e == nil || !e.Keywords["$seen"] || !e.Mailboxes[mailboxID(nf.RemoteID)] || !strings.Contains(e.Raw, "appended") {
		t.Fatalf("imported %+v", e)
	}
	err = Transport{P: p}.Send(ctx, domain.OutgoingMessage{From: domain.Address{Email: "alias@example.org"},
		Recipients: []string{"bob@example.org"}, Raw: []byte("Subject: sent\r\n\r\nhi\r\n")})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.subs) != 1 || f.subs[0]["identityId"] != "i2" {
		t.Fatalf("submissions %+v", f.subs)
	}
	ids, err := p.DomainIdentities(ctx, 7)
	if err != nil || len(ids) != 2 || !ids[0].IsDefault || ids[1].Email != "alias@example.org" {
		t.Fatal(err, ids)
	}
}

func TestVacationPush(t *testing.T) {
	f := newFake(t)
	p := newTestProvider(t, f)
	v := &Vacation{P: p, Loc: time.UTC}
	if !v.Supported() {
		t.Fatal("vacation not supported")
	}
	s := vacation.Settings{Enabled: true, Start: time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 1, 6, 0, 0, 0, 0, time.UTC),
		ExternalText: "Ich bin bis 6.1. nicht im Büro.", Subject: vacation.DefaultSubject}
	if err := v.Push(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, err := v.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsEnabled || got.TextBody == nil || *got.TextBody != s.ExternalText || got.Subject != nil || got.FromDate == nil || !got.FromDate.Equal(s.Start) {
		t.Fatalf("vacation %+v", got)
	}
}

func TestWatchEventSourceAndPolling(t *testing.T) {
	f := newFake(t)
	p := newTestProvider(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Watch(ctx, "mb-in")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	f.addEmail("mb-in", "pushed")
	select {
	case h := <-ch:
		if h.FolderRemoteID != "mb-in" {
			t.Fatal(h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no push hint")
	}

	f2 := newFake(t)
	f2.noES = true
	p2, _ := New(Config{SessionURL: f2.srv.URL + "/.well-known/jmap", Auth: BasicAuth("anna", "pw"), PollInterval: 10 * time.Millisecond})
	ch2, err := p2.Watch(ctx, "mb-in")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch2:
	case <-time.After(2 * time.Second):
		t.Fatal("no poll hint")
	}
}

func TestUnauthorizedAndSessionURL(t *testing.T) {
	f := newFake(t)
	p, _ := New(Config{SessionURL: f.srv.URL + "/.well-known/jmap", Auth: BasicAuth("anna", "wrong")})
	if _, err := p.ListFolders(context.Background()); err != ErrUnauthorized {
		t.Fatal(err)
	}
	acc := domain.Account{Incoming: &domain.ServerConfig{Host: "mail.example.org", Port: 443, TLS: domain.TLSImplicit}}
	if u := SessionURLFor(acc); u != "https://mail.example.org/.well-known/jmap" {
		t.Fatal(u)
	}
	acc.Extra = map[string]string{"jmapUrl": "https://api.fastmail.com/jmap/session"}
	if u := SessionURLFor(acc); u != "https://api.fastmail.com/jmap/session" {
		t.Fatal(u)
	}
}

func TestCannotCalculateChangesFallsBack(t *testing.T) {
	f := newFake(t)
	p := newTestProvider(t, f)
	ctx := context.Background()
	id := f.addEmail("mb-in", "x")
	_, st, err := p.Sync(ctx, "mb-in", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		f.addEmail("mb-arch", "noise")
	}
	f.mu.Lock()
	f.emails[id].Keywords["$flagged"] = true
	f.mu.Unlock()
	d, _, err := p.Sync(ctx, "mb-in", st)
	if err != nil || len(d.FlagsChanged) != 1 {
		t.Fatal(err, d)
	}
}
