// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secrets provides implementations of domain.SecretStore
// (KONZEPT 17.2): the OS keyring for installed mode, an encrypted vault file
// for portable mode and an in-memory store for tests.
package secrets

import (
	"errors"
	"sync"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// ErrNotFound is returned by Get when no secret is stored under the reference.
var ErrNotFound = errors.New("secrets: not found")

// Memory is an in-memory SecretStore for tests. The zero value is ready to use.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

var _ domain.SecretStore = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{} }

// Get returns a copy of the secret stored under ref.
func (s *Memory) Get(ref string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(v), nil
}

// Set stores a copy of value under ref.
func (s *Memory) Set(ref string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[string][]byte)
	}
	s.m[ref] = clone(value)
	return nil
}

// Delete removes the secret stored under ref; a missing ref is not an error.
func (s *Memory) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ref)
	return nil
}

func clone(b []byte) []byte {
	return append([]byte{}, b...)
}
