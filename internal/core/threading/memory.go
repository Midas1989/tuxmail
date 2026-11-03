// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryRepository is an in-memory Repository and HeuristicIndex. It is
// used in tests and as a reference for the SQLite adapter. It is safe for
// concurrent use.
type MemoryRepository struct {
	mu            sync.Mutex
	relations     map[RelationID]storedRelation
	byKey         map[string]map[RelationID]bool
	assignments   map[string]int64
	conversations map[int64]Conversation
	overrides     map[string]Override
	candidates    map[string]Candidate
	nextID        int64
}

type storedRelation struct {
	Relation
	CreatedAt time.Time
}

// NewMemoryRepository returns an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		relations:     map[RelationID]storedRelation{},
		byKey:         map[string]map[RelationID]bool{},
		assignments:   map[string]int64{},
		conversations: map[int64]Conversation{},
		overrides:     map[string]Override{},
		candidates:    map[string]Candidate{},
	}
}

var (
	_ Repository     = (*MemoryRepository)(nil)
	_ HeuristicIndex = (*MemoryRepository)(nil)
)

// SaveRelations implements Repository.
func (m *MemoryRepository) SaveRelations(_ context.Context, rels []Relation, createdAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rels {
		if r.SourceKey == "" || r.TargetKey == "" {
			return fmt.Errorf("threading: relation with empty key: %+v", r)
		}
		id := r.ID()
		if old, ok := m.relations[id]; ok {
			if r.Confidence > old.Confidence {
				old.Relation = r
				m.relations[id] = old
			}
			continue
		}
		m.relations[id] = storedRelation{Relation: r, CreatedAt: createdAt}
		for _, k := range []string{r.SourceKey, r.TargetKey} {
			if m.byKey[k] == nil {
				m.byKey[k] = map[RelationID]bool{}
			}
			m.byKey[k][id] = true
		}
	}
	return nil
}

// RelationsOf implements Repository.
func (m *MemoryRepository) RelationsOf(_ context.Context, key string) ([]Relation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Relation, 0, len(m.byKey[key]))
	for id := range m.byKey[key] {
		out = append(out, m.relations[id].Relation)
	}
	sortRelations(out)
	return out, nil
}

// AllRelations returns every stored relation (sorted).
func (m *MemoryRepository) AllRelations() []Relation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Relation, 0, len(m.relations))
	for _, r := range m.relations {
		out = append(out, r.Relation)
	}
	sortRelations(out)
	return out
}

func sortRelations(rs []Relation) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.SourceKey != b.SourceKey {
			return a.SourceKey < b.SourceKey
		}
		if a.TargetKey != b.TargetKey {
			return a.TargetKey < b.TargetKey
		}
		return a.Kind < b.Kind
	})
}

// ConversationOf implements Repository.
func (m *MemoryRepository) ConversationOf(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assignments[key], nil
}

// SetConversation implements Repository.
func (m *MemoryRepository) SetConversation(_ context.Context, key string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == 0 {
		delete(m.assignments, key)
		return nil
	}
	if _, ok := m.conversations[id]; !ok {
		return fmt.Errorf("%w: conversation %d", ErrNotFound, id)
	}
	m.assignments[key] = id
	return nil
}

// Members implements Repository.
func (m *MemoryRepository) Members(_ context.Context, id int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k, c := range m.assignments {
		if c == id {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// CreateConversation implements Repository.
func (m *MemoryRepository) CreateConversation(_ context.Context, c Conversation) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	c.ID = m.nextID
	if c.Kind == "" {
		c.Kind = ConversationEmailThread
	}
	m.conversations[c.ID] = c
	return c.ID, nil
}

// GetConversation implements Repository.
func (m *MemoryRepository) GetConversation(_ context.Context, id int64) (Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conversations[id]
	if !ok {
		return Conversation{}, fmt.Errorf("%w: conversation %d", ErrNotFound, id)
	}
	return c, nil
}

// DeleteConversation implements Repository.
func (m *MemoryRepository) DeleteConversation(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.conversations[id]; !ok {
		return fmt.Errorf("%w: conversation %d", ErrNotFound, id)
	}
	delete(m.conversations, id)
	return nil
}

// Conversations returns the IDs of all conversations (sorted).
func (m *MemoryRepository) Conversations() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, 0, len(m.conversations))
	for id := range m.conversations {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Override implements Repository.
func (m *MemoryRepository) Override(_ context.Context, key string) (Override, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.overrides[key]
	return o, ok, nil
}

// SetOverride implements Repository.
func (m *MemoryRepository) SetOverride(_ context.Context, o Override) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides[o.MessageKey] = o
	return nil
}

// DeleteOverride implements Repository.
func (m *MemoryRepository) DeleteOverride(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.overrides, key)
	return nil
}

// IndexMessage implements HeuristicIndex.
func (m *MemoryRepository) IndexMessage(_ context.Context, c Candidate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.candidates[c.Key] = c
	return nil
}

// FindBySubject implements HeuristicIndex. Only present messages match.
func (m *MemoryRepository) FindBySubject(_ context.Context, subjectKey string, from, to time.Time) ([]Candidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Candidate
	for k, c := range m.candidates {
		if m.assignments[k] == 0 || SubjectKey(c.Subject) != subjectKey {
			continue
		}
		if c.Date.Before(from) || c.Date.After(to) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
