// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"encoding/json"
	"sync"
)

// StateStore persists the account-level EAS state (endpoint after
// redirects, protocol version, policy key, folder hierarchy sync key and
// folder list). Losing it is not fatal but forces a FolderSync with
// SyncKey 0, which makes servers reset the sync state of all collections.
type StateStore interface {
	LoadEASState(ctx context.Context, key string) ([]byte, error) // nil, nil if none
	SaveEASState(ctx context.Context, key string, data []byte) error
}

// MemoryState is a process-local StateStore.
type MemoryState struct {
	mu sync.Mutex
	m  map[string][]byte
}

// LoadEASState implements StateStore.
func (s *MemoryState) LoadEASState(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.m[key]...), nil
}

// SaveEASState implements StateStore.
func (s *MemoryState) SaveEASState(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[key] = append([]byte(nil), data...)
	return nil
}

// processState is used when no StateStore is configured, so that a
// rebuilt provider in the same process keeps its state.
var processState = &MemoryState{}

// folderRec is one entry of the cached folder hierarchy.
type folderRec struct {
	ServerID string `json:"id"`
	ParentID string `json:"p,omitempty"`
	Name     string `json:"n"`
	Type     int    `json:"t"`
}

// accountState is the persisted account-level state.
type accountState struct {
	URL string `json:"url,omitempty"`
	// Base is the configured endpoint URL was learned for (by a 451 redirect,
	// or URL is Base itself). A stored URL is only reused while the account
	// is still configured with this endpoint.
	Base         string      `json:"base,omitempty"`
	Version      string      `json:"v,omitempty"`
	PolicyKey    string      `json:"pk,omitempty"`
	HierarchyKey string      `json:"hk,omitempty"`
	Folders      []folderRec `json:"f,omitempty"`
	// Collections tracks the sync keys of mail collections: R is the key
	// last returned to the sync engine, C the current key (it advances
	// when the provider sends changes on its own).
	Collections map[string]collKeys `json:"c,omitempty"`
}

type collKeys struct {
	R string `json:"r"`
	C string `json:"c"`
}

func (s accountState) clone() accountState {
	s.Folders = append([]folderRec(nil), s.Folders...)
	cols := make(map[string]collKeys, len(s.Collections))
	for k, v := range s.Collections {
		cols[k] = v
	}
	s.Collections = cols
	return s
}

func loadAccountState(ctx context.Context, st StateStore, key string) (accountState, error) {
	if st == nil {
		st = processState
	}
	var s accountState
	data, err := st.LoadEASState(ctx, key)
	if err != nil || len(data) == 0 {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

func saveAccountState(ctx context.Context, st StateStore, key string, s accountState) error {
	if st == nil {
		st = processState
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SaveEASState(ctx, key, data)
}
