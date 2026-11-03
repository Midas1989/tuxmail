// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

type fixtureMessage struct {
	Header             domain.MessageHeaderInfo `json:"header"`
	Namespace          string                   `json:"namespace"`
	CalendarUIDs       []string                 `json:"calendarUids"`
	AttachedMessageIDs []string                 `json:"attachedMessageIds"`
}

type threadFixture struct {
	Description string           `json:"description"`
	Messages    []fixtureMessage `json:"messages"`
	Expect      [][]string       `json:"expect"`
}

// partition returns the conversations of the given keys as sorted groups.
func partition(t *testing.T, repo *MemoryRepository) [][]string {
	t.Helper()
	var out [][]string
	for _, id := range repo.Conversations() {
		m, _ := repo.Members(context.Background(), id)
		if len(m) == 0 {
			t.Errorf("conversation %d is empty but not deleted", id)
			continue
		}
		out = append(out, m)
	}
	return sortGroups(out)
}

func sortGroups(groups [][]string) [][]string {
	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		g = append([]string(nil), g...)
		sort.Strings(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func TestEngineGoldenFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "threads", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var fx threadFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			// Every permutation order must give the same partition; test
			// forward and reverse order.
			for _, reverse := range []bool{false, true} {
				repo := NewMemoryRepository()
				e := NewEngine(repo, Config{})
				msgs := append([]fixtureMessage(nil), fx.Messages...)
				if reverse {
					for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
						msgs[i], msgs[j] = msgs[j], msgs[i]
					}
				}
				for _, m := range msgs {
					_, err := e.AddMessage(context.Background(), MessageInput{
						Header:             m.Header,
						Namespace:          m.Namespace,
						CalendarUIDs:       m.CalendarUIDs,
						AttachedMessageIDs: m.AttachedMessageIDs,
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				got := partition(t, repo)
				if want := sortGroups(fx.Expect); !reflect.DeepEqual(got, want) {
					t.Errorf("reverse=%v partition =\n%q\nwant\n%q", reverse, got, want)
				}
			}
		})
	}
}

type harness struct {
	t    *testing.T
	ctx  context.Context
	repo *MemoryRepository
	e    *Engine
}

func newHarness(t *testing.T, cfg Config) *harness {
	repo := NewMemoryRepository()
	return &harness{t: t, ctx: context.Background(), repo: repo, e: NewEngine(repo, cfg)}
}

func (h *harness) add(id string, parents ...string) Result {
	h.t.Helper()
	res, err := h.e.AddMessage(h.ctx, MessageInput{Header: domain.MessageHeaderInfo{MessageID: id, References: parents, Subject: "Subject " + id}})
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) conv(key string) int64 {
	h.t.Helper()
	id, err := h.repo.ConversationOf(h.ctx, key)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) expect(groups ...[]string) {
	h.t.Helper()
	got := partition(h.t, h.repo)
	if want := sortGroups(groups); !reflect.DeepEqual(got, want) {
		h.t.Fatalf("partition =\n%q\nwant\n%q", got, want)
	}
}

func TestEngineBasicThread(t *testing.T) {
	h := newHarness(t, Config{})
	r1 := h.add("a@x")
	if r1.ConversationID == 0 || r1.Key != "a@x" || len(r1.Created) != 1 {
		t.Fatalf("unexpected result %+v", r1)
	}
	r2 := h.add("b@x", "a@x")
	if r2.ConversationID != r1.ConversationID || len(r2.Created) != 0 {
		t.Fatalf("reply not in parent's conversation: %+v", r2)
	}
	h.add("c@x", "a@x", "b@x")
	h.add("z@x")
	h.expect([]string{"a@x", "b@x", "c@x"}, []string{"z@x"})

	c, err := h.repo.GetConversation(h.ctx, r1.ConversationID)
	if err != nil || c.Title != "Subject a@x" || c.Kind != ConversationEmailThread {
		t.Fatalf("conversation = %+v, %v", c, err)
	}
}

func TestEnginePendingRelation(t *testing.T) {
	h := newHarness(t, Config{})
	// Reply arrives first, parent not present.
	r := h.add("reply@x", "parent@x")
	// A second, unrelated message.
	h.add("other@x")
	// Parent arrives without any headers referring to the reply.
	p := h.add("parent@x")
	if p.ConversationID != r.ConversationID {
		t.Fatalf("pending relation not applied: parent %d, reply %d", p.ConversationID, r.ConversationID)
	}
	h.expect([]string{"parent@x", "reply@x"}, []string{"other@x"})
}

func TestEngineMergeOlderIDSurvives(t *testing.T) {
	h := newHarness(t, Config{})
	a := h.add("a@x")
	b := h.add("b@x")
	h.add("b2@x", "b@x")
	if a.ConversationID >= b.ConversationID {
		t.Fatal("IDs must increase")
	}
	res := h.add("c@x", "b2@x", "a@x")
	if res.ConversationID != a.ConversationID {
		t.Fatalf("merged into %d, want older %d", res.ConversationID, a.ConversationID)
	}
	if !reflect.DeepEqual(res.Deleted, []int64{b.ConversationID}) {
		t.Fatalf("Deleted = %v, want [%d]", res.Deleted, b.ConversationID)
	}
	moved := map[string]Move{}
	for _, m := range res.Moves {
		moved[m.Key] = m
	}
	if moved["b@x"].From != b.ConversationID || moved["b@x"].To != a.ConversationID || moved["c@x"].From != 0 {
		t.Fatalf("Moves = %+v", res.Moves)
	}
	h.expect([]string{"a@x", "b2@x", "b@x", "c@x"})
}

func TestEngineRemoveKeepsRelations(t *testing.T) {
	h := newHarness(t, Config{})
	root := h.add("root@x")
	h.add("mid@x", "root@x")
	// The leaf only references mid (In-Reply-To style), not root.
	h.add("leaf@x", "mid@x")

	res, err := h.e.RemoveMessage(h.ctx, "mid@x")
	if err != nil {
		t.Fatal(err)
	}
	if res.ConversationID != 0 || len(res.Moves) != 1 {
		t.Fatalf("remove result %+v", res)
	}
	// The removed key still links root and leaf.
	h.expect([]string{"leaf@x", "root@x"})
	if len(h.repo.AllRelations()) != 2 {
		t.Fatalf("relations lost: %+v", h.repo.AllRelations())
	}
	// A new reply to the removed message still joins.
	h.add("late@x", "mid@x")
	h.expect([]string{"late@x", "leaf@x", "root@x"})

	// Re-appearing (e.g. via import) puts it straight back.
	back := h.add("mid@x", "root@x")
	if back.ConversationID != root.ConversationID {
		t.Fatalf("re-added message in %d, want %d", back.ConversationID, root.ConversationID)
	}

	// Removing everything deletes the conversation; relations survive.
	for _, k := range []string{"root@x", "mid@x", "leaf@x", "late@x"} {
		if _, err := h.e.RemoveMessage(h.ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if ids := h.repo.Conversations(); len(ids) != 0 {
		t.Fatalf("conversations left: %v", ids)
	}
	if len(h.repo.AllRelations()) != 3 {
		t.Fatalf("relations = %+v", h.repo.AllRelations())
	}
	// Removing an unknown message is a no-op.
	if res, err := h.e.RemoveMessage(h.ctx, "nope@x"); err != nil || len(res.Moves) != 0 {
		t.Fatalf("remove unknown: %+v %v", res, err)
	}
	// Re-import of two members restores the conversation via stored relations.
	h.add("leaf@x")
	h.add("root@x")
	h.expect([]string{"leaf@x", "root@x"})
}

func TestEngineSplit(t *testing.T) {
	h := newHarness(t, Config{})
	orig := h.add("a@x")
	h.add("b@x", "a@x")
	h.add("c@x", "b@x") // c only connected through b

	res, err := h.e.Split(h.ctx, "b@x")
	if err != nil {
		t.Fatal(err)
	}
	if res.ConversationID == orig.ConversationID || res.ConversationID == 0 {
		t.Fatalf("split result %+v", res)
	}
	h.expect([]string{"a@x"}, []string{"b@x"}, []string{"c@x"})
	if h.conv("a@x") != orig.ConversationID {
		t.Fatal("larger/first part must keep the old ID")
	}
	o, ok, _ := h.repo.Override(h.ctx, "b@x")
	if !ok || o.Action != OverrideSplit || o.ConversationID != res.ConversationID {
		t.Fatalf("override = %+v %v", o, ok)
	}

	// New replies to b do not undo the split; b stays alone.
	h.add("d@x", "b@x", "a@x")
	h.expect([]string{"a@x", "d@x"}, []string{"b@x"}, []string{"c@x"})

	// Overrides survive removal and re-import.
	if _, err := h.e.RemoveMessage(h.ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	h.add("b@x", "a@x")
	h.expect([]string{"a@x", "d@x"}, []string{"b@x"}, []string{"c@x"})

	// Clearing the override re-threads automatically.
	if _, err := h.e.ClearOverride(h.ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	h.expect([]string{"a@x", "b@x", "c@x", "d@x"})
	if h.conv("a@x") != orig.ConversationID {
		t.Fatal("original conversation ID must survive")
	}

	if _, err := h.e.Split(h.ctx, "unknown@x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("split unknown: %v", err)
	}
}

func TestEngineSplitKeepsOtherLinks(t *testing.T) {
	h := newHarness(t, Config{})
	h.add("a@x")
	h.add("b@x", "a@x")
	h.add("c@x", "a@x", "b@x") // c references both
	if _, err := h.e.Split(h.ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	h.expect([]string{"a@x", "c@x"}, []string{"b@x"})
}

func TestEnginePinTo(t *testing.T) {
	h := newHarness(t, Config{})
	t1 := h.add("t1@x")
	h.add("t1b@x", "t1@x")
	h.add("t2@x")
	if _, err := h.e.PinTo(h.ctx, "t2@x", t1.ConversationID); err != nil {
		t.Fatal(err)
	}
	h.expect([]string{"t1@x", "t1b@x", "t2@x"})

	// Pinning one message elsewhere does not drag its thread along.
	other := h.add("o@x")
	if _, err := h.e.PinTo(h.ctx, "t1b@x", other.ConversationID); err != nil {
		t.Fatal(err)
	}
	h.expect([]string{"t1@x", "t2@x"}, []string{"o@x", "t1b@x"})

	// Pin a message that is not present yet: applied on arrival.
	if _, err := h.e.PinTo(h.ctx, "future@x", other.ConversationID); err != nil {
		t.Fatal(err)
	}
	h.add("future@x", "t1@x")
	h.expect([]string{"t1@x", "t2@x"}, []string{"future@x", "o@x", "t1b@x"})

	if _, err := h.e.PinTo(h.ctx, "t1@x", 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pin to unknown conversation: %v", err)
	}
}

func TestEngineManualMerge(t *testing.T) {
	h := newHarness(t, Config{})
	a := h.add("a@x")
	h.add("a2@x", "a@x")
	b := h.add("b@x")
	h.add("b2@x", "b@x")

	// Merge the older conversation into the newer: the user's choice wins.
	res, err := h.e.Merge(h.ctx, a.ConversationID, b.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Deleted, []int64{a.ConversationID}) {
		t.Fatalf("Deleted = %v", res.Deleted)
	}
	h.expect([]string{"a2@x", "a@x", "b2@x", "b@x"})
	if h.conv("a@x") != b.ConversationID {
		t.Fatal("manual merge must use the chosen target")
	}

	// A late reply to a merged message follows the merge.
	if r := h.add("a3@x", "a2@x"); r.ConversationID != b.ConversationID {
		t.Fatalf("late reply in %d, want %d", r.ConversationID, b.ConversationID)
	}
	// Even if it also connects to an older conversation, the anchor wins.
	h.add("old@x")
	h.add("x@x", "old@x", "a3@x")
	if h.conv("old@x") != b.ConversationID {
		t.Fatal("merge anchor must win over the older conversation ID")
	}

	if _, err := h.e.Merge(h.ctx, b.ConversationID, b.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Merge(h.ctx, 4242, b.ConversationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("merge unknown: %v", err)
	}
}

func TestEngineLinkAndAddRelations(t *testing.T) {
	h := newHarness(t, Config{})
	h.add("a@x")
	h.add("b@x")
	if _, err := h.e.Link(h.ctx, "b@x", "a@x"); err != nil {
		t.Fatal(err)
	}
	h.expect([]string{"a@x", "b@x"})

	// Compose-time relation recorded after sending, target not present yet.
	h.add("sent@x")
	if _, err := h.e.AddRelations(h.ctx, ComposeRelations("sent@x", ComposeContext{Action: ComposeForward, OriginalKeys: []string{"orig@x"}})...); err != nil {
		t.Fatal(err)
	}
	h.add("orig@x")
	h.expect([]string{"a@x", "b@x"}, []string{"orig@x", "sent@x"})

	// Invalid relations are ignored.
	if _, err := h.e.AddRelations(h.ctx, Relation{SourceKey: "a@x", TargetKey: "a@x", Kind: KindManual, Confidence: 1}, Relation{}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineThreshold(t *testing.T) {
	h := newHarness(t, Config{})
	h.add("a@x")
	h.add("b@x")
	// Below threshold: stored but not grouping.
	low := Relation{SourceKey: "b@x", TargetKey: "a@x", Kind: KindForward, Origin: OriginInlineForward, Confidence: 0.79}
	if _, err := h.e.AddRelations(h.ctx, low); err != nil {
		t.Fatal(err)
	}
	h.expect([]string{"a@x"}, []string{"b@x"})
	// Inline forward at exactly 0.8 groups.
	if _, err := h.e.AddRelations(h.ctx, InlineForwardRelation("b@x", "a@x")); err != nil {
		t.Fatal(err)
	}
	h.expect([]string{"a@x", "b@x"})

	// Lower threshold engine on the same repo with Recompute.
	h2 := newHarness(t, Config{Threshold: 0.99})
	h2.add("p@x")
	if _, err := h2.e.AddMessage(h2.ctx, MessageInput{Namespace: "1", Header: domain.MessageHeaderInfo{MessageID: "q@x", ServerThreadID: "T"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.e.AddMessage(h2.ctx, MessageInput{Namespace: "1", Header: domain.MessageHeaderInfo{MessageID: "r@x", ServerThreadID: "T"}}); err != nil {
		t.Fatal(err)
	}
	h2.expect([]string{"p@x"}, []string{"q@x"}, []string{"r@x"})
	e3 := NewEngine(h2.repo, Config{})
	if _, err := e3.Recompute(h2.ctx, "q@x"); err != nil {
		t.Fatal(err)
	}
	h2.expect([]string{"p@x"}, []string{"q@x", "r@x"})
}

func TestEngineHeuristic(t *testing.T) {
	base := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	me := domain.Address{Email: "me@home.de"}
	msg := func(id, subject string, d time.Duration, peers ...string) MessageInput {
		h := domain.MessageHeaderInfo{MessageID: id, Subject: subject, Date: base.Add(d), From: []domain.Address{me}}
		for _, p := range peers {
			h.To = append(h.To, domain.Address{Email: p})
		}
		return MessageInput{Header: h}
	}
	inputs := []MessageInput{
		msg("h1@x", "Wohnungsübergabe", 0, "vermieter@example.de"),
		msg("h2@x", "AW: Wohnungsübergabe", 48*time.Hour, "Vermieter@example.de"),     // same peer, 2 days
		msg("h3@x", "Wohnungsübergabe", 20*24*time.Hour, "vermieter@example.de"),      // too late
		msg("h4@x", "Re: Wohnungsübergabe", 24*time.Hour, "fremd@example.de"),         // other peer
		msg("h5@x", "Hi", time.Hour, "vermieter@example.de"),                          // too short subject
		msg("h6@x", "Re: Wohnungsübergabe", 72*time.Hour, "vermieter@example.de", ""), // matches closest (h2)
	}

	off := newHarness(t, Config{})
	for _, in := range inputs {
		if _, err := off.e.AddMessage(off.ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(off.repo.Conversations()); n != len(inputs) {
		t.Fatalf("heuristic must be off by default: %d conversations", n)
	}

	on := newHarness(t, Config{Heuristic: HeuristicConfig{Enabled: true, SelfAddresses: []string{"ME@home.de"}}})
	for _, in := range inputs {
		if _, err := on.e.AddMessage(on.ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	on.expect([]string{"h1@x", "h2@x", "h6@x"}, []string{"h3@x"}, []string{"h4@x"}, []string{"h5@x"})
	var heur []Relation
	for _, r := range on.repo.AllRelations() {
		if r.Kind == KindHeuristic {
			heur = append(heur, r)
			if r.Confidence != ConfidenceHeuristic || r.Origin != OriginHeuristic {
				t.Errorf("bad heuristic relation %+v", r)
			}
		}
	}
	if len(heur) != 2 || heur[1].SourceKey != "h6@x" || heur[1].TargetKey != "h2@x" {
		t.Fatalf("heuristic relations = %+v", heur)
	}

	// Messages with strong relations do not get a heuristic relation.
	if _, err := on.e.AddMessage(on.ctx, MessageInput{Header: domain.MessageHeaderInfo{
		MessageID: "h7@x", InReplyTo: []string{"h3@x"}, Subject: "Re: Wohnungsübergabe",
		Date: base.Add(49 * time.Hour), To: []domain.Address{{Email: "vermieter@example.de"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if on.conv("h7@x") != on.conv("h3@x") {
		t.Fatal("h7 must follow its In-Reply-To")
	}

	// Disabling the heuristic later ignores the stored heuristic relations.
	off2 := NewEngine(on.repo, Config{})
	if _, err := off2.Recompute(on.ctx, "h1@x"); err != nil {
		t.Fatal(err)
	}
	on.expect([]string{"h1@x"}, []string{"h2@x"}, []string{"h3@x", "h7@x"}, []string{"h4@x"}, []string{"h5@x"}, []string{"h6@x"})
}

func TestEngineKeys(t *testing.T) {
	h := newHarness(t, Config{})
	if _, err := h.e.AddMessage(h.ctx, MessageInput{}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
	if _, err := h.e.AddMessage(h.ctx, MessageInput{Key: "~thread:1:2"}); err == nil {
		t.Fatal("virtual key must be rejected")
	}
	// Message without Message-ID, threaded by fingerprint and In-Reply-To.
	h.add("p@x")
	res, err := h.e.AddMessage(h.ctx, MessageInput{Fingerprint: "abc", Header: domain.MessageHeaderInfo{InReplyTo: []string{"<p@x>"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "~fp:abc" || res.ConversationID != h.conv("p@x") {
		t.Fatalf("fingerprint message: %+v", res)
	}
	comp, err := h.e.Component(h.ctx, "p@x")
	if err != nil || !reflect.DeepEqual(comp, []string{"p@x", "~fp:abc"}) {
		t.Fatalf("Component = %v, %v", comp, err)
	}
}

func TestEngineIdempotent(t *testing.T) {
	h := newHarness(t, Config{})
	h.add("a@x")
	r1 := h.add("b@x", "a@x")
	r2 := h.add("b@x", "a@x")
	if r1.ConversationID != r2.ConversationID || len(r2.Moves) != 0 || len(r2.Created) != 0 {
		t.Fatalf("re-adding must be a no-op: %+v", r2)
	}
}

// txRepo wraps MemoryRepository to verify that Transactor is used.
type txRepo struct {
	*MemoryRepository
	calls int
}

func (r *txRepo) InTx(ctx context.Context, fn func(Repository) error) error {
	r.calls++
	return fn(r.MemoryRepository)
}

func TestEngineUsesTransactor(t *testing.T) {
	repo := &txRepo{MemoryRepository: NewMemoryRepository()}
	e := NewEngine(repo, Config{})
	if _, err := e.AddMessage(context.Background(), MessageInput{Key: "a@x"}); err != nil {
		t.Fatal(err)
	}
	if repo.calls != 1 {
		t.Fatalf("InTx calls = %d", repo.calls)
	}
}

func TestEngineConcurrent(t *testing.T) {
	h := newHarness(t, Config{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				id := string(rune('a'+i)) + string(rune('a'+j)) + "@x"
				parent := string(rune('a'+i)) + "a@x"
				if _, err := h.e.AddMessage(h.ctx, MessageInput{Header: domain.MessageHeaderInfo{MessageID: id, InReplyTo: []string{parent}}}); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	if n := len(h.repo.Conversations()); n != 8 {
		t.Fatalf("conversations = %d, want 8", n)
	}
}

func TestUnionFind(t *testing.T) {
	u := NewUnionFind()
	u.Union("a", "b")
	u.Union("c", "d")
	u.Union("b", "d")
	u.Add("e")
	if u.Find("a") != u.Find("c") || u.Find("a") == u.Find("e") {
		t.Fatal("union-find broken")
	}
	if got := u.Sets(); !reflect.DeepEqual(got, [][]string{{"a", "b", "c", "d"}, {"e"}}) {
		t.Fatalf("Sets = %v", got)
	}
}
