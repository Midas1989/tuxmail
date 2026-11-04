// SPDX-License-Identifier: AGPL-3.0-or-later

package team_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/team"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// dir adapts auth.Service (as the server does).
type dir struct{ a *auth.Service }

func (d dir) User(ctx context.Context, id int64) (team.DirUser, error) {
	u, err := d.a.GetUser(ctx, id)
	if err != nil {
		return team.DirUser{}, team.ErrNotFound
	}
	return team.DirUser{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Admin: u.IsAdmin(), Disabled: u.Disabled}, nil
}

func (d dir) Users(ctx context.Context) ([]team.DirUser, error) {
	us, err := d.a.ListUsers(ctx)
	var out []team.DirUser
	for _, u := range us {
		out = append(out, team.DirUser{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Admin: u.IsAdmin(), Disabled: u.Disabled})
	}
	return out, err
}

func (d dir) UserGroupIDs(ctx context.Context, id int64) ([]int64, error) {
	return d.a.UserGroupIDs(ctx, id)
}
func (d dir) GroupName(ctx context.Context, id int64) (string, error) {
	g, err := d.a.GetGroup(ctx, id)
	if err != nil {
		return "", err
	}
	return g.Name, nil
}
func (d dir) GroupMembers(ctx context.Context, id int64) ([]int64, error) {
	g, err := d.a.GetGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	return g.Members, nil
}

type fixture struct {
	t      *testing.T
	auth   *auth.Service
	svc    *team.Service
	clk    *clock
	mb     team.Mailbox
	users  map[string]int64
	events []team.Event
	mu     sync.Mutex
	audits []string
}

var ctx = context.Background()

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	a, err := auth.Open(ctx, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	f := &fixture{t: t, auth: a, clk: &clock{t: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)}, users: map[string]int64{}}
	f.svc = team.New(a.DB(), dir{a}, team.Options{Now: f.clk.Now, PresenceTTL: 30 * time.Second,
		Audit: func(_ context.Context, ac team.Actor, action, target, details string) {
			f.mu.Lock()
			f.audits = append(f.audits, action+" "+target)
			f.mu.Unlock()
		}})
	f.svc.Subscribe(func(ev team.Event) {
		f.mu.Lock()
		f.events = append(f.events, ev)
		f.mu.Unlock()
	})
	for _, n := range []string{"anna", "ben", "carla", "dora", "eve"} {
		role := auth.RoleUser
		if n == "eve" {
			role = auth.RoleAdmin
		}
		u, err := a.CreateUser(ctx, auth.NewUser{Username: n, DisplayName: "User " + n, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		f.users[n] = u.ID
	}
	f.mb, err = f.svc.CreateMailbox(ctx, team.Actor{UserID: f.users["eve"], Name: "eve"}, team.MailboxInput{Name: "Support", Address: "Support@Example.org"})
	if err != nil {
		t.Fatal(err)
	}
	// anna: reply (direct), ben: read (direct), carla: assign through the group "Leads", dora: none.
	g, err := a.CreateGroup(ctx, "Leads", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddGroupMember(ctx, g.ID, f.users["carla"]); err != nil {
		t.Fatal(err)
	}
	f.grant(f.users["anna"], 0, team.RoleReply)
	f.grant(f.users["ben"], 0, team.RoleRead)
	f.grant(0, g.ID, team.RoleAssign)
	return f
}

func (f *fixture) grant(uid, gid int64, r team.Role) {
	f.t.Helper()
	if err := f.svc.SetGrant(ctx, team.Actor{Name: "eve"}, f.mb.ID, uid, gid, r); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) actor(n string) team.Actor {
	return team.Actor{UserID: f.users[n], Name: "User " + n}
}

func (f *fixture) role(n string) team.Role {
	r, err := f.svc.RoleOf(ctx, f.users[n], f.mb.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) eventKinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, e.Kind)
	}
	return out
}

func TestAuthorizationMatrix(t *testing.T) {
	f := newFixture(t)
	want := map[string]team.Role{"anna": team.RoleReply, "ben": team.RoleRead, "carla": team.RoleAssign, "dora": team.RoleNone, "eve": team.RoleManage}
	for n, r := range want {
		if got := f.role(n); got != r {
			t.Errorf("%s: role %q, want %q", n, got, r)
		}
	}
	type check struct {
		user string
		need team.Role
		err  error
	}
	for _, c := range []check{
		{"ben", team.RoleRead, nil}, {"ben", team.RoleReply, team.ErrForbidden},
		{"anna", team.RoleReply, nil}, {"anna", team.RoleAssign, team.ErrForbidden},
		{"carla", team.RoleAssign, nil}, {"carla", team.RoleManage, team.ErrForbidden},
		{"dora", team.RoleRead, team.ErrNotFound}, {"eve", team.RoleManage, nil},
	} {
		_, err := f.svc.Require(ctx, f.users[c.user], f.mb.ID, c.need)
		if !errors.Is(err, c.err) && !(err == nil && c.err == nil) {
			t.Errorf("%s needs %s: err %v, want %v", c.user, c.need, err, c.err)
		}
	}
	// Accessible lists only mailboxes with access.
	if acc, _ := f.svc.Accessible(ctx, f.users["dora"]); len(acc) != 0 {
		t.Fatalf("dora sees %v", acc)
	}
	if acc, _ := f.svc.Accessible(ctx, f.users["ben"]); len(acc) != 1 || acc[0].Role != team.RoleRead {
		t.Fatalf("ben sees %v", acc)
	}
	// A direct grant and a group grant combine to the higher role.
	f.grant(f.users["carla"], 0, team.RoleRead)
	if f.role("carla") != team.RoleAssign {
		t.Fatal("group role must win over the lower direct role")
	}
	// Disabled users lose access, disabled mailboxes too (admins keep it).
	if err := f.auth.SetDisabled(ctx, f.users["anna"], true); err != nil {
		t.Fatal(err)
	}
	if f.role("anna") != team.RoleNone {
		t.Fatal("disabled user keeps access")
	}
	if _, err := f.svc.UpdateMailbox(ctx, f.actor("eve"), f.mb.ID, team.MailboxInput{Name: "Support", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if f.role("ben") != team.RoleNone || f.role("eve") != team.RoleManage {
		t.Fatal("disabled mailbox: wrong access")
	}
	// Removing a grant.
	f.grant(f.users["ben"], 0, team.RoleNone)
	gs, _ := f.svc.ListGrants(ctx, f.mb.ID)
	for _, g := range gs {
		if g.UserID == f.users["ben"] {
			t.Fatal("grant not removed")
		}
	}
	if err := f.svc.SetGrant(ctx, team.Actor{}, f.mb.ID, f.users["ben"], 0, "owner"); !errors.Is(err, team.ErrInvalidInput) {
		t.Fatal("unknown role accepted:", err)
	}
}

func TestAssignmentRules(t *testing.T) {
	f := newFixture(t)
	conv := int64(42)
	uid := func(n string) *int64 { v := f.users[n]; return &v }
	// read role cannot change anything.
	if _, err := f.svc.Update(ctx, f.actor("ben"), team.RoleRead, f.mb.ID, conv, team.AssignmentPatch{AssigneeID: uid("ben")}); !errors.Is(err, team.ErrForbidden) {
		t.Fatal("read role assigned:", err)
	}
	// reply role: take an unassigned conversation ("Mir zuweisen").
	a, err := f.svc.Update(ctx, f.actor("anna"), team.RoleReply, f.mb.ID, conv, team.AssignmentPatch{AssigneeID: uid("anna")})
	if err != nil || a.AssigneeID != f.users["anna"] || a.Status != team.StatusInProgress || a.AssigneeName != "User anna" {
		t.Fatal(a, err)
	}
	// reply role cannot give it to someone else.
	if _, err := f.svc.Update(ctx, f.actor("anna"), team.RoleReply, f.mb.ID, conv, team.AssignmentPatch{AssigneeID: uid("carla")}); !errors.Is(err, team.ErrForbidden) {
		t.Fatal("reply role reassigned:", err)
	}
	// assign role can, but only to members who can reply (ben is read only).
	if _, err := f.svc.Update(ctx, f.actor("carla"), team.RoleAssign, f.mb.ID, conv, team.AssignmentPatch{AssigneeID: uid("ben")}); !errors.Is(err, team.ErrInvalidInput) {
		t.Fatal("assigned to a reader:", err)
	}
	due, prio, st := "2026-11-05", team.PriorityHigh, team.StatusWaiting
	a, err = f.svc.Update(ctx, f.actor("carla"), team.RoleAssign, f.mb.ID, conv, team.AssignmentPatch{AssigneeID: uid("carla"),
		DueAt: &due, Priority: &prio, Status: &st})
	if err != nil || a.AssigneeID != f.users["carla"] || a.DueAt == nil || a.DueAt.Format("2006-01-02") != "2026-11-05" || a.Priority != "high" {
		t.Fatal(a, err)
	}
	// anna is no longer the assignee: she cannot change the status.
	done := team.StatusDone
	if _, err := f.svc.Update(ctx, f.actor("anna"), team.RoleReply, f.mb.ID, conv, team.AssignmentPatch{Status: &done}); !errors.Is(err, team.ErrForbidden) {
		t.Fatal(err)
	}
	a, err = f.svc.Update(ctx, f.actor("carla"), team.RoleAssign, f.mb.ID, conv, team.AssignmentPatch{Status: &done})
	if err != nil || a.DoneAt == nil {
		t.Fatal(a, err)
	}
	bad := team.Status("closed")
	if _, err := f.svc.Update(ctx, f.actor("carla"), team.RoleAssign, f.mb.ID, conv, team.AssignmentPatch{Status: &bad}); !errors.Is(err, team.ErrInvalidInput) {
		t.Fatal(err)
	}
	// Views.
	ids, ok, _ := f.svc.ViewIDs(ctx, f.users["carla"], f.mb.ID, team.ViewDone)
	if !ok || !slices.Equal(ids, []int64{conv}) {
		t.Fatal(ids)
	}
	ids, _, _ = f.svc.ViewIDs(ctx, f.users["carla"], f.mb.ID, team.ViewMine)
	if len(ids) != 0 {
		t.Fatal("done conversation in mine", ids)
	}
	if team.MatchesView(team.ViewUnassigned, 1, nil) != true || team.MatchesView(team.ViewUnassigned, 1, &a) {
		t.Fatal("unassigned view")
	}
	// New mail reopens a done conversation.
	if err := f.svc.Touch(ctx, f.mb.ID, conv, true); err != nil {
		t.Fatal(err)
	}
	if a, _ = f.svc.Assignment(ctx, f.mb.ID, conv); a.Status != team.StatusOpen || a.AssigneeID != f.users["carla"] {
		t.Fatal("not reopened", a)
	}
	kinds := f.eventKinds()
	if !slices.Contains(kinds, team.EventAssigned) || !slices.Contains(kinds, team.EventUpdated) {
		t.Fatal(kinds)
	}
	if !slices.ContainsFunc(f.audits, func(s string) bool { return s == "team.assigned team=1 conversation=42" }) {
		t.Fatal(f.audits)
	}
}

func TestCommentsAndMentions(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.AddComment(ctx, f.actor("ben"), team.RoleRead, f.mb.ID, 7, "hi"); !errors.Is(err, team.ErrForbidden) {
		t.Fatal("reader commented:", err)
	}
	// @dora has no access, @anna is the author: only carla and ben are mentioned.
	c, err := f.svc.AddComment(ctx, f.actor("anna"), team.RoleReply, f.mb.ID, 7, "@carla bitte prüfen, cc @Ben. @dora @anna x@y.de")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{f.users["ben"], f.users["carla"]}; !slices.Equal(c.Mentions, want) {
		t.Fatalf("mentions %v, want %v", c.Mentions, want)
	}
	f.mu.Lock()
	var mention *team.Event
	for i := range f.events {
		if f.events[i].Kind == team.EventMention {
			mention = &f.events[i]
		}
	}
	f.mu.Unlock()
	if mention == nil || len(mention.UserIDs) != 2 {
		t.Fatal("no mention event", mention)
	}
	ms, _ := f.svc.Mentions(ctx, f.users["carla"], true, 0)
	if len(ms) != 1 || ms[0].MailboxName != "Support" || ms[0].ConversationID != 7 {
		t.Fatal(ms)
	}
	acc, _ := f.svc.Accessible(ctx, f.users["carla"])
	if acc[0].Mentions != 1 {
		t.Fatal(acc)
	}
	if err := f.svc.MarkMentionsRead(ctx, f.users["carla"], f.mb.ID, 7); err != nil {
		t.Fatal(err)
	}
	if ms, _ = f.svc.Mentions(ctx, f.users["carla"], true, 0); len(ms) != 0 {
		t.Fatal("still unread")
	}
	list, _ := f.svc.Comments(ctx, f.mb.ID, 7)
	if len(list) != 1 || list[0].Body == "" || len(list[0].Mentions) != 2 {
		t.Fatal(list)
	}
	// Only the author or a manager may delete.
	if err := f.svc.DeleteComment(ctx, f.actor("carla"), team.RoleAssign, f.mb.ID, c.ID); !errors.Is(err, team.ErrForbidden) {
		t.Fatal(err)
	}
	if err := f.svc.DeleteComment(ctx, f.actor("anna"), team.RoleReply, f.mb.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := team.ParseMentions("mail@anna.de @a.b- (@c_d) @@x"); !slices.Equal(got, []string{"a.b", "c_d"}) {
		t.Fatal(got)
	}
}

func TestPresenceTTL(t *testing.T) {
	f := newFixture(t)
	p := f.svc.Heartbeat(f.actor("anna"), f.mb.ID, 3, team.PresenceViewing)
	if len(p) != 1 || p[0].State != "viewing" {
		t.Fatal(p)
	}
	f.clk.Add(10 * time.Second)
	f.svc.Heartbeat(f.actor("ben"), f.mb.ID, 3, team.PresenceReplying)
	before := len(f.eventKinds())
	f.svc.Heartbeat(f.actor("ben"), f.mb.ID, 3, team.PresenceReplying) // unchanged: no event
	if len(f.eventKinds()) != before {
		t.Fatal("unchanged heartbeat emitted an event")
	}
	f.clk.Add(25 * time.Second) // anna expired (35s > 30s TTL), ben not (25s)
	p = f.svc.Presence(f.mb.ID, 3)
	if len(p) != 1 || p[0].UserName != "User ben" || p[0].State != team.PresenceReplying {
		t.Fatal(p)
	}
	f.svc.SweepPresence()
	if k := f.eventKinds(); k[len(k)-1] != team.EventPresence {
		t.Fatal("sweep did not announce the change", k)
	}
	f.svc.Heartbeat(f.actor("ben"), f.mb.ID, 3, "") // leave
	if len(f.svc.Presence(f.mb.ID, 3)) != 0 {
		t.Fatal("leave failed")
	}
}

func TestDraftsAndStats(t *testing.T) {
	f := newFixture(t)
	body := json.RawMessage(`{"text":"Hallo"}`)
	if err := f.svc.SaveDraft(ctx, f.actor("anna"), f.mb.ID, 5, body); err != nil {
		t.Fatal(err)
	}
	ds, _ := f.svc.Drafts(ctx, f.users["ben"], f.mb.ID, 5)
	if len(ds) != 1 || ds[0].UserName != "User anna" || ds[0].Body != nil {
		t.Fatal("drafts must be hidden without sharing", ds)
	}
	if _, err := f.svc.UpdateMailbox(ctx, f.actor("eve"), f.mb.ID, team.MailboxInput{Name: "Support", SharedDrafts: true}); err != nil {
		t.Fatal(err)
	}
	ds, _ = f.svc.Drafts(ctx, f.users["ben"], f.mb.ID, 5)
	if string(ds[0].Body) != `{"text":"Hallo"}` {
		t.Fatal(ds)
	}
	if own, _ := f.svc.Draft(ctx, f.users["anna"], f.mb.ID, 5); own == nil {
		t.Fatal("own draft missing")
	}
	// First response times 10 min and 30 min -> median 20 min.
	for i, d := range []time.Duration{10 * time.Minute, 30 * time.Minute} {
		conv := int64(100 + i)
		if err := f.svc.Touch(ctx, f.mb.ID, conv, true); err != nil {
			t.Fatal(err)
		}
		f.clk.Add(d)
		if err := f.svc.RecordReply(ctx, f.actor("anna"), f.mb.ID, conv); err != nil {
			t.Fatal(err)
		}
	}
	st, err := f.svc.Stats(ctx, f.mb.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.MedianFirstResponseSeconds != 1200 || st.Responded != 2 || st.Waiting != 2 || st.Total != 5 || st.Open != 3 {
		t.Fatalf("%+v", st)
	}
	if !slices.ContainsFunc(f.audits, func(s string) bool { return s == "team.replied team=1 conversation=100" }) {
		t.Fatal(f.audits)
	}
}

func TestAssignActionRoundRobin(t *testing.T) {
	f := newFixture(t)
	resolve := func(_ context.Context, ref workflow.ItemRef) (int64, error) {
		var id int64
		_, err := fmt.Sscan(ref.Key, &id)
		return id, err
	}
	at := team.AssignActionType(f.svc, f.mb.ID, resolve)
	reg := workflow.NewRegistry()
	if err := reg.Register(at); err != nil {
		t.Fatal(err)
	}
	act, err := at.New(map[string]any{"to": "round_robin", "users": []any{"anna", "carla", "ben"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for conv := int64(1); conv <= 4; conv++ {
		rc := &workflow.RunContext{Workflow: &workflow.Workflow{ID: "rr"}, Event: events.Event{Mail: &events.Mail{Key: fmt.Sprint(conv)}}}
		if _, err := act.Execute(ctx, rc); err != nil {
			t.Fatal(err)
		}
		a, _ := f.svc.Assignment(ctx, f.mb.ID, conv)
		got = append(got, a.AssigneeID)
	}
	// ben (read only) is skipped; anna and carla alternate.
	an, ca := f.users["anna"], f.users["carla"]
	if !slices.Equal(got, []int64{an, ca, an, ca}) {
		t.Fatalf("round robin %v (anna=%d carla=%d)", got, an, ca)
	}
	// Keyword workflows assign to a named user; already assigned ones are kept.
	named, _ := at.New(map[string]any{"to": "carla", "priority": "urgent"})
	rc := &workflow.RunContext{Event: events.Event{Mail: &events.Mail{Key: "1"}}}
	if _, err := named.Execute(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if a, _ := f.svc.Assignment(ctx, f.mb.ID, 1); a.AssigneeID != an || a.Priority != "urgent" {
		t.Fatal("override without override flag", a)
	}
	if _, err := at.New(map[string]any{"to": "dora"}); err != nil {
		t.Fatal(err) // validated at run time
	}
	bad, _ := at.New(map[string]any{"to": "dora", "override": true})
	if _, err := bad.Execute(ctx, rc); err == nil {
		t.Fatal("assigned to a non-member")
	}
	if _, err := at.New(map[string]any{}); err == nil {
		t.Fatal("missing to accepted")
	}
	ev := team.AssignedEvent(team.Event{MailboxID: 1, ConversationID: 2, Assignment: &team.Assignment{AssigneeID: an, Status: "open"}}, "anna")
	if ev.Type != events.TeamAssigned || ev.Data["assignee"] != "anna" || !ev.Type.Valid() {
		t.Fatal(ev)
	}
}

type fakeRuntime struct {
	st     *store.Store
	closed bool
}

func (r *fakeRuntime) Store() *store.Store { return r.st }
func (r *fakeRuntime) Close() error        { r.closed = true; return nil }

func TestManagerLifecycle(t *testing.T) {
	f := newFixture(t)
	var started []int64
	var rts []*fakeRuntime
	m := team.NewManager(f.svc, t.TempDir(), func(_ context.Context, mb team.Mailbox, st *store.Store, d string) (team.Runtime, error) {
		started = append(started, mb.ID)
		rt := &fakeRuntime{st: st}
		rts = append(rts, rt)
		return rt, nil
	}, nil)
	second, err := f.svc.CreateMailbox(ctx, f.actor("eve"), team.MailboxInput{Name: "Bewerbung", Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Running(), []int64{f.mb.ID}) {
		t.Fatal("disabled mailbox started", m.Running())
	}
	rt, err := m.Open(ctx, f.mb.ID)
	if err != nil || rt != team.Runtime(rts[0]) || len(started) != 1 {
		t.Fatal("Open must reuse the running runtime")
	}
	if _, err := rt.Store().ListAccounts(ctx); err != nil {
		t.Fatal("team store not usable:", err)
	}
	if _, err := m.Open(ctx, second.ID); err != nil || len(m.Running()) != 2 {
		t.Fatal("open on demand", err)
	}
	if err := m.Remove(second.ID); err != nil || !rts[1].closed {
		t.Fatal(err)
	}
	if _, err := m.Open(ctx, 999); !errors.Is(err, team.ErrNotFound) {
		t.Fatal(err)
	}
	m.Stop()
	if !rts[0].closed || len(m.Running()) != 0 {
		t.Fatal("stop")
	}
	if _, err := m.Open(ctx, f.mb.ID); !errors.Is(err, team.ErrStopped) {
		t.Fatal(err)
	}
}
