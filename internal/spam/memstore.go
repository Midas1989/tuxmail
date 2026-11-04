// SPDX-License-Identifier: AGPL-3.0-or-later

package spam

import (
	"context"
	"sync"
)

// MemStore is an in-memory TokenStore (tests, demo mode).
type MemStore struct {
	mu       sync.Mutex
	spam     int
	ham      int
	counts   map[int64]Count
	Training int // number of SpamTrain calls
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{counts: map[int64]Count{}} }

// SpamTotals implements TokenStore.
func (s *MemStore) SpamTotals(context.Context) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spam, s.ham, nil
}

// SpamCounts implements TokenStore.
func (s *MemStore) SpamCounts(_ context.Context, hashes []int64) (map[int64]Count, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int64]Count{}
	for _, h := range hashes {
		if c, ok := s.counts[h]; ok {
			out[h] = c
		}
	}
	return out, nil
}

// SpamTrain implements TokenStore.
func (s *MemStore) SpamTrain(_ context.Context, hashes []int64, spam bool, delta int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Training++
	for _, h := range hashes {
		c := s.counts[h]
		if spam {
			c.Spam = max(0, c.Spam+delta)
		} else {
			c.Ham = max(0, c.Ham+delta)
		}
		s.counts[h] = c
	}
	if spam {
		s.spam = max(0, s.spam+delta)
	} else {
		s.ham = max(0, s.ham+delta)
	}
	return nil
}
