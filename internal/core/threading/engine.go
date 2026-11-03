// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// DefaultThreshold is the minimum confidence for a relation to join two
// messages into one conversation (KONZEPT 6.4).
const DefaultThreshold = 0.8

// DefaultHeuristicWindow is the maximum distance in time for subject matching.
const DefaultHeuristicWindow = 14 * 24 * time.Hour

// confidenceEpsilon absorbs floating point noise from persisted confidences.
const confidenceEpsilon = 1e-9

// ErrNoKey is returned when a message has neither Message-ID, server object
// ID nor fingerprint.
var ErrNoKey = errors.New("threading: message has no stable key")

// HeuristicConfig configures the optional subject heuristic.
type HeuristicConfig struct {
	// Enabled turns subject matching on (default off). It only works if the
	// Repository also implements HeuristicIndex.
	Enabled bool
	// Window is the maximum time distance (default 14 days).
	Window time.Duration
	// MinConfidence is the minimum confidence at which heuristic relations
	// are honored while Enabled (default 0.5). Heuristic relations stay
	// stored when the heuristic is disabled later, but are then ignored.
	MinConfidence float64
	// SelfAddresses are the user's own addresses; they are ignored when
	// checking for overlapping participants.
	SelfAddresses []string
}

// Config configures an Engine.
type Config struct {
	// Threshold is the minimum confidence for automatic grouping (default 0.8).
	Threshold float64
	Heuristic HeuristicConfig
	// Now returns the current time (default time.Now); used for created_at.
	Now func() time.Time
}

// Move records that a message changed its conversation. From or To is 0 if
// the message was not present before / is not present anymore.
type Move struct {
	Key  string
	From int64
	To   int64
}

// Result describes the effects of an engine operation.
type Result struct {
	// Key is the message key the operation was about (if any).
	Key string
	// ConversationID is the resulting conversation of Key (0 if not present).
	ConversationID int64
	Moves          []Move
	Created        []int64
	Deleted        []int64
}

// Engine assigns messages to conversations incrementally. All public
// methods are safe for concurrent use; operations are serialized and, if the
// Repository implements Transactor, run inside one transaction each.
//
// Algorithm: the relation graph contains message keys and virtual thread
// keys. Relations with sufficient confidence are edges. A conversation is a
// connected component (computed with union-find) restricted to present
// messages. Absent keys (not yet received or removed messages) still connect
// their neighbors, like JWZ's empty containers, so a reply arriving before
// its parent and a removed message in the middle of a thread are handled
// without losing structure. When a component spans several conversations,
// the oldest (lowest) conversation ID survives. Manual overrides win:
// split and pin_to make a message a cut node, merge makes it an anchor that
// pulls its component into the chosen conversation.
type Engine struct {
	mu   sync.Mutex
	repo Repository
	cfg  Config
}

// NewEngine returns an engine with defaults applied to cfg.
func NewEngine(repo Repository, cfg Config) *Engine {
	if cfg.Threshold <= 0 {
		cfg.Threshold = DefaultThreshold
	}
	if cfg.Heuristic.Window <= 0 {
		cfg.Heuristic.Window = DefaultHeuristicWindow
	}
	if cfg.Heuristic.MinConfidence <= 0 {
		cfg.Heuristic.MinConfidence = ConfidenceHeuristic
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Engine{repo: repo, cfg: cfg}
}

// Config returns the effective configuration.
func (e *Engine) Config() Config { return e.cfg }

// Accepts reports whether a relation joins conversations under the current
// configuration.
func (e *Engine) Accepts(r Relation) bool {
	if r.Confidence+confidenceEpsilon >= e.cfg.Threshold {
		return true
	}
	return e.cfg.Heuristic.Enabled && r.Kind == KindHeuristic &&
		r.Confidence+confidenceEpsilon >= e.cfg.Heuristic.MinConfidence
}

// AddMessage records a new (or re-appearing) message: it extracts and
// stores its relations (also those pointing at messages that are not
// present yet), optionally adds a subject-heuristic relation and assigns
// the message — and everything it connects — to a conversation.
func (e *Engine) AddMessage(ctx context.Context, in MessageInput) (Result, error) {
	key := in.ResolveKey()
	if key == "" {
		return Result{}, ErrNoKey
	}
	if IsVirtualKey(key) {
		return Result{}, fmt.Errorf("threading: %q is not a message key", key)
	}
	rels := ExtractRelations(in)
	return e.run(ctx, key, func(op *operation) error {
		if hi, ok := op.repo.(HeuristicIndex); ok && e.cfg.Heuristic.Enabled {
			cand := candidateFromInput(key, in)
			if err := hi.IndexMessage(ctx, cand); err != nil {
				return err
			}
			strong := false
			for _, r := range rels {
				if r.Confidence+confidenceEpsilon >= e.cfg.Threshold {
					strong = true
					break
				}
			}
			if !strong {
				r, ok, err := e.findHeuristic(ctx, hi, cand)
				if err != nil {
					return err
				}
				if ok {
					rels = append(rels, r)
				}
			}
		}
		if len(rels) > 0 {
			if err := op.repo.SaveRelations(ctx, rels, e.cfg.Now()); err != nil {
				return err
			}
		}
		op.present[key] = true
		if t := NormalizeSubject(in.Header.Subject); t != "" {
			op.titles[key] = t
		}
		return op.recompute(ctx, []string{key})
	})
}

// AddRelations stores additional relations (compose actions recorded at send
// time, matched inline forwards, manual links) and re-threads the affected
// messages. Relations may point at messages that are not present yet.
func (e *Engine) AddRelations(ctx context.Context, rels ...Relation) (Result, error) {
	rels = DedupeRelations(rels)
	var clean []Relation
	for _, r := range rels {
		if r.SourceKey != "" && r.TargetKey != "" && r.SourceKey != r.TargetKey {
			clean = append(clean, r)
		}
	}
	key := ""
	if len(clean) == 1 {
		key = clean[0].SourceKey
	}
	return e.run(ctx, key, func(op *operation) error {
		if len(clean) == 0 {
			return nil
		}
		if err := op.repo.SaveRelations(ctx, clean, e.cfg.Now()); err != nil {
			return err
		}
		var seeds []string
		for _, r := range clean {
			seeds = append(seeds, r.SourceKey, r.TargetKey)
		}
		return op.recompute(ctx, seeds)
	})
}

// Link manually connects two messages (relation kind manual, origin user).
func (e *Engine) Link(ctx context.Context, sourceKey, targetKey string) (Result, error) {
	return e.AddRelations(ctx, ManualRelation(sourceKey, targetKey))
}

// RemoveMessage marks a message as not present (deleted or garbage
// collected). Its relations stay stored, so the key keeps connecting its
// neighbors and the message is threaded correctly if it re-appears.
// Conversations that become empty are deleted.
func (e *Engine) RemoveMessage(ctx context.Context, key string) (Result, error) {
	return e.run(ctx, key, func(op *operation) error {
		old, err := op.conversationOf(ctx, key)
		if err != nil || old == 0 {
			return err
		}
		if err := op.setConversation(ctx, key, 0); err != nil {
			return err
		}
		return op.cleanup(ctx)
	})
}

// Split detaches a message into a new conversation of its own. The message
// stops connecting other messages; the rest of its former conversation is
// re-threaded (and may itself fall apart if the message was the only link).
func (e *Engine) Split(ctx context.Context, key string) (Result, error) {
	return e.run(ctx, key, func(op *operation) error {
		old, err := op.conversationOf(ctx, key)
		if err != nil {
			return err
		}
		if old == 0 {
			return fmt.Errorf("%w: message %q", ErrNotFound, key)
		}
		c, err := op.repo.GetConversation(ctx, old)
		if err != nil {
			return err
		}
		id, err := op.createConversation(ctx, c.Title)
		if err != nil {
			return err
		}
		if err := op.setOverride(ctx, Override{MessageKey: key, ConversationID: id, Action: OverrideSplit}); err != nil {
			return err
		}
		members, err := op.repo.Members(ctx, old)
		if err != nil {
			return err
		}
		return op.recompute(ctx, append(members, key))
	})
}

// PinTo places a message into an existing conversation, overriding automatic
// detection. Like Split, the message stops connecting other messages.
func (e *Engine) PinTo(ctx context.Context, key string, conversationID int64) (Result, error) {
	return e.run(ctx, key, func(op *operation) error {
		if _, err := op.repo.GetConversation(ctx, conversationID); err != nil {
			return err
		}
		old, err := op.conversationOf(ctx, key)
		if err != nil {
			return err
		}
		if err := op.setOverride(ctx, Override{MessageKey: key, ConversationID: conversationID, Action: OverridePinTo}); err != nil {
			return err
		}
		if old == 0 {
			// Not present yet: the override is applied when it arrives.
			return nil
		}
		members, err := op.repo.Members(ctx, old)
		if err != nil {
			return err
		}
		return op.recompute(ctx, append(members, key))
	})
}

// Merge moves all messages of conversation from into conversation into.
// Every moved message becomes an anchor (merge override), so replies to any
// of them follow into the merged conversation.
func (e *Engine) Merge(ctx context.Context, from, into int64) (Result, error) {
	return e.run(ctx, "", func(op *operation) error {
		if from == into {
			return nil
		}
		for _, id := range []int64{from, into} {
			if _, err := op.repo.GetConversation(ctx, id); err != nil {
				return err
			}
		}
		fromMembers, err := op.repo.Members(ctx, from)
		if err != nil {
			return err
		}
		for _, k := range fromMembers {
			if err := op.setOverride(ctx, Override{MessageKey: k, ConversationID: into, Action: OverrideMerge}); err != nil {
				return err
			}
		}
		intoMembers, err := op.repo.Members(ctx, into)
		if err != nil {
			return err
		}
		return op.recompute(ctx, append(fromMembers, intoMembers...))
	})
}

// ClearOverride removes a manual override and re-threads the message
// automatically.
func (e *Engine) ClearOverride(ctx context.Context, key string) (Result, error) {
	return e.run(ctx, key, func(op *operation) error {
		o, ok, err := op.override(ctx, key)
		if err != nil || !ok {
			return err
		}
		if err := op.repo.DeleteOverride(ctx, key); err != nil {
			return err
		}
		op.overrides[key] = nil
		cur, err := op.conversationOf(ctx, key)
		if err != nil {
			return err
		}
		seeds := []string{key}
		for _, id := range []int64{o.ConversationID, cur} {
			if id == 0 {
				continue
			}
			members, err := op.repo.Members(ctx, id)
			if err != nil {
				return err
			}
			seeds = append(seeds, members...)
		}
		return op.recompute(ctx, seeds)
	})
}

// Recompute re-threads the components containing the given keys, e.g.
// after the threshold or heuristic settings changed.
func (e *Engine) Recompute(ctx context.Context, keys ...string) (Result, error) {
	return e.run(ctx, "", func(op *operation) error {
		var seeds []string
		for _, k := range keys {
			seeds = append(seeds, k)
			id, err := op.conversationOf(ctx, k)
			if err != nil {
				return err
			}
			if id != 0 {
				members, err := op.repo.Members(ctx, id)
				if err != nil {
					return err
				}
				seeds = append(seeds, members...)
			}
		}
		return op.recompute(ctx, seeds)
	})
}

// Component returns the keys (messages and virtual keys, present or not)
// that are automatically connected to key, ignoring overrides of key itself.
// It is intended for diagnostics and the UI ("why are these grouped?").
func (e *Engine) Component(ctx context.Context, key string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	op := e.newOperation(e.repo)
	comp, err := op.component(ctx, key)
	if err != nil {
		return nil, err
	}
	sort.Strings(comp)
	return comp, nil
}

func (e *Engine) run(ctx context.Context, key string, fn func(op *operation) error) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var res Result
	body := func(repo Repository) error {
		op := e.newOperation(repo)
		if err := fn(op); err != nil {
			return err
		}
		res = op.res
		res.Key = key
		if key != "" && !IsVirtualKey(key) {
			id, err := repo.ConversationOf(ctx, key)
			if err != nil {
				return err
			}
			res.ConversationID = id
		}
		return nil
	}
	var err error
	if tx, ok := e.repo.(Transactor); ok {
		err = tx.InTx(ctx, body)
	} else {
		err = body(e.repo)
	}
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func (e *Engine) newOperation(repo Repository) *operation {
	return &operation{
		e:         e,
		repo:      repo,
		present:   map[string]bool{},
		convs:     map[string]int64{},
		overrides: map[string]*Override{},
		titles:    map[string]string{},
		touched:   map[int64]bool{},
	}
}

func (e *Engine) findHeuristic(ctx context.Context, hi HeuristicIndex, c Candidate) (Relation, bool, error) {
	subj := SubjectKey(c.Subject)
	if len([]rune(subj)) < 3 || c.Date.IsZero() {
		return Relation{}, false, nil
	}
	w := e.cfg.Heuristic.Window
	cands, err := hi.FindBySubject(ctx, subj, c.Date.Add(-w), c.Date.Add(w))
	if err != nil {
		return Relation{}, false, err
	}
	self := map[string]bool{}
	for _, a := range e.cfg.Heuristic.SelfAddresses {
		self[normalizeEmail(a)] = true
	}
	mine := participantSet(c.Participants, self)
	var (
		best     Candidate
		bestDiff time.Duration = -1
	)
	for _, o := range cands {
		if o.Key == c.Key || SubjectKey(o.Subject) != subj {
			continue
		}
		theirs := participantSet(o.Participants, self)
		overlap := 0
		for p := range theirs {
			if mine[p] {
				overlap++
			}
		}
		need := 2
		if len(mine) < need {
			need = len(mine)
		}
		if len(theirs) < need {
			need = len(theirs)
		}
		if need == 0 || overlap < need {
			continue
		}
		d := absDuration(o.Date.Sub(c.Date))
		if d > w {
			continue
		}
		if bestDiff < 0 || d < bestDiff || (d == bestDiff && o.Key < best.Key) {
			best, bestDiff = o, d
		}
	}
	if bestDiff < 0 {
		return Relation{}, false, nil
	}
	return Relation{SourceKey: c.Key, TargetKey: best.Key, Kind: KindHeuristic, Origin: OriginHeuristic, Confidence: ConfidenceHeuristic}, true, nil
}

func participantSet(list []string, self map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, p := range list {
		p = normalizeEmail(p)
		if p != "" && !self[p] {
			out[p] = true
		}
	}
	return out
}

func candidateFromInput(key string, in MessageInput) Candidate {
	h := in.Header
	c := Candidate{Key: key, Subject: h.Subject, Date: h.Date}
	if len(h.From) > 0 {
		c.From = normalizeEmail(h.From[0].Email)
	}
	for _, list := range [][]string{emails(h.From), emails(h.To), emails(h.Cc)} {
		c.Participants = append(c.Participants, list...)
	}
	return c
}

// operation holds per-operation caches and collects the Result.
type operation struct {
	e         *Engine
	repo      Repository
	res       Result
	present   map[string]bool // keys made present in this operation
	convs     map[string]int64
	overrides map[string]*Override // nil entry = known to have none
	titles    map[string]string
	touched   map[int64]bool // conversations that lost members
}

func (op *operation) conversationOf(ctx context.Context, key string) (int64, error) {
	if IsVirtualKey(key) {
		return 0, nil
	}
	if id, ok := op.convs[key]; ok {
		return id, nil
	}
	id, err := op.repo.ConversationOf(ctx, key)
	if err != nil {
		return 0, err
	}
	op.convs[key] = id
	return id, nil
}

func (op *operation) setConversation(ctx context.Context, key string, id int64) error {
	old, err := op.conversationOf(ctx, key)
	if err != nil {
		return err
	}
	if old == id {
		return nil
	}
	if err := op.repo.SetConversation(ctx, key, id); err != nil {
		return err
	}
	op.convs[key] = id
	if old != 0 {
		op.touched[old] = true
	}
	op.res.Moves = append(op.res.Moves, Move{Key: key, From: old, To: id})
	return nil
}

func (op *operation) override(ctx context.Context, key string) (Override, bool, error) {
	if IsVirtualKey(key) {
		return Override{}, false, nil
	}
	if o, ok := op.overrides[key]; ok {
		if o == nil {
			return Override{}, false, nil
		}
		return *o, true, nil
	}
	o, ok, err := op.repo.Override(ctx, key)
	if err != nil {
		return Override{}, false, err
	}
	if !ok {
		op.overrides[key] = nil
		return Override{}, false, nil
	}
	op.overrides[key] = &o
	return o, true, nil
}

func (op *operation) setOverride(ctx context.Context, o Override) error {
	if err := op.repo.SetOverride(ctx, o); err != nil {
		return err
	}
	op.overrides[o.MessageKey] = &o
	return nil
}

func (op *operation) createConversation(ctx context.Context, title string) (int64, error) {
	id, err := op.repo.CreateConversation(ctx, Conversation{Kind: ConversationEmailThread, Title: title})
	if err != nil {
		return 0, err
	}
	op.res.Created = append(op.res.Created, id)
	return id, nil
}

func (op *operation) isPresent(ctx context.Context, key string) (bool, error) {
	if IsVirtualKey(key) {
		return false, nil
	}
	if op.present[key] {
		return true, nil
	}
	id, err := op.conversationOf(ctx, key)
	return id != 0, err
}

// isCut reports whether key is a cut node (split or pin_to override).
func (op *operation) isCut(ctx context.Context, key string) (bool, error) {
	o, ok, err := op.override(ctx, key)
	if err != nil || !ok {
		return false, err
	}
	return o.Action == OverrideSplit || o.Action == OverridePinTo, nil
}

// component returns all keys connected to start via accepted relations,
// using union-find. Cut nodes other than start are not entered.
func (op *operation) component(ctx context.Context, start string) ([]string, error) {
	uf := NewUnionFind()
	uf.Add(start)
	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		rels, err := op.repo.RelationsOf(ctx, n)
		if err != nil {
			return nil, err
		}
		for _, r := range rels {
			if !op.e.Accepts(r) {
				continue
			}
			other := r.TargetKey
			if other == n {
				other = r.SourceKey
			}
			if other == n || other == "" {
				continue
			}
			if !visited[other] {
				cut, err := op.isCut(ctx, other)
				if err != nil {
					return nil, err
				}
				if cut {
					continue
				}
				visited[other] = true
				queue = append(queue, other)
			}
			uf.Union(n, other)
		}
	}
	root := uf.Find(start)
	var out []string
	for k := range visited {
		if uf.Find(k) == root {
			out = append(out, k)
		}
	}
	return out, nil
}

type componentInfo struct {
	members []string // present message keys, sorted
}

// recompute re-threads all components that contain one of the seeds.
func (op *operation) recompute(ctx context.Context, seeds []string) error {
	seen := map[string]bool{}
	var comps []componentInfo
	var cutSeeds []string
	for _, s := range seeds {
		if s == "" || seen[s] {
			continue
		}
		cut, err := op.isCut(ctx, s)
		if err != nil {
			return err
		}
		if cut {
			seen[s] = true
			cutSeeds = append(cutSeeds, s)
			continue
		}
		comp, err := op.component(ctx, s)
		if err != nil {
			return err
		}
		var members []string
		for _, k := range comp {
			seen[k] = true
			ok, err := op.isPresent(ctx, k)
			if err != nil {
				return err
			}
			if ok {
				members = append(members, k)
			}
		}
		if len(members) > 0 {
			sort.Strings(members)
			comps = append(comps, componentInfo{members: members})
		}
	}

	// Cut nodes go to their override conversation.
	sort.Strings(cutSeeds)
	for _, k := range cutSeeds {
		ok, err := op.isPresent(ctx, k)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		id, err := op.overrideTarget(ctx, k)
		if err != nil {
			return err
		}
		if err := op.setConversation(ctx, k, id); err != nil {
			return err
		}
	}

	// Larger components claim existing conversations first, so that when a
	// conversation falls apart its main part keeps the ID.
	sort.SliceStable(comps, func(i, j int) bool {
		if len(comps[i].members) != len(comps[j].members) {
			return len(comps[i].members) > len(comps[j].members)
		}
		return comps[i].members[0] < comps[j].members[0]
	})
	claimed := map[int64]bool{}
	for _, c := range comps {
		if err := op.assign(ctx, c, claimed); err != nil {
			return err
		}
	}
	return op.cleanup(ctx)
}

// overrideTarget returns the conversation of an override, re-creating it if
// it was deleted in the meantime (e.g. while the message was absent).
func (op *operation) overrideTarget(ctx context.Context, key string) (int64, error) {
	o, _, err := op.override(ctx, key)
	if err != nil {
		return 0, err
	}
	if o.ConversationID != 0 {
		_, err := op.repo.GetConversation(ctx, o.ConversationID)
		if err == nil {
			return o.ConversationID, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return 0, err
		}
	}
	id, err := op.createConversation(ctx, op.titles[key])
	if err != nil {
		return 0, err
	}
	o.ConversationID = id
	if err := op.setOverride(ctx, o); err != nil {
		return 0, err
	}
	return id, nil
}

func (op *operation) assign(ctx context.Context, c componentInfo, claimed map[int64]bool) error {
	var anchors, existing []int64
	merged := map[string]bool{}
	for _, k := range c.members {
		o, ok, err := op.override(ctx, k)
		if err != nil {
			return err
		}
		if ok && o.Action == OverrideMerge {
			id, err := op.overrideTarget(ctx, k)
			if err != nil {
				return err
			}
			anchors = append(anchors, id)
			merged[k] = true
			if err := op.setConversation(ctx, k, id); err != nil {
				return err
			}
			continue
		}
		id, err := op.conversationOf(ctx, k)
		if err != nil {
			return err
		}
		if id != 0 {
			existing = append(existing, id)
		}
	}

	var target int64
	if len(anchors) > 0 {
		target = minID(anchors)
	} else {
		sort.Slice(existing, func(i, j int) bool { return existing[i] < existing[j] })
		for _, id := range existing {
			if claimed[id] {
				continue
			}
			if _, err := op.repo.GetConversation(ctx, id); err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return err
			}
			target = id
			break
		}
		if target == 0 {
			title := op.titleFor(c.members)
			if title == "" && len(existing) > 0 {
				// A piece split off an existing conversation keeps its title.
				if old, err := op.repo.GetConversation(ctx, existing[0]); err == nil {
					title = old.Title
				}
			}
			id, err := op.createConversation(ctx, title)
			if err != nil {
				return err
			}
			target = id
		}
		claimed[target] = true
	}
	for _, k := range c.members {
		if merged[k] {
			continue
		}
		if err := op.setConversation(ctx, k, target); err != nil {
			return err
		}
	}
	return nil
}

func (op *operation) titleFor(members []string) string {
	for _, k := range members {
		if t := op.titles[k]; t != "" {
			return t
		}
	}
	return ""
}

// cleanup deletes conversations that lost all members.
func (op *operation) cleanup(ctx context.Context) error {
	ids := make([]int64, 0, len(op.touched))
	for id := range op.touched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		members, err := op.repo.Members(ctx, id)
		if err != nil {
			return err
		}
		if len(members) > 0 {
			continue
		}
		if err := op.repo.DeleteConversation(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		op.res.Deleted = append(op.res.Deleted, id)
	}
	op.touched = map[int64]bool{}
	return nil
}

func minID(ids []int64) int64 {
	m := ids[0]
	for _, id := range ids[1:] {
		if id < m {
			m = id
		}
	}
	return m
}

func emails(as []domain.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		if e := normalizeEmail(a.Email); e != "" {
			out = append(out, e)
		}
	}
	return out
}
