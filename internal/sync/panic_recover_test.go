// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// panicky simulates a protocol client that panics on a malformed server
// response (audit NF-03: go-imap's AllUIDs on "1:*").
type panicky struct {
	domain.MailProvider
	calls *atomic.Int32
}

func (p *panicky) Sync(ctx context.Context, folder string, st domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	p.calls.Add(1)
	panic("imap: SearchData.All is a dynamic number set")
}

// A panic inside a provider must neither terminate the process (this test
// would crash) nor stop the worker: the cycle fails, the error is reported
// and the next attempt starts with a fresh connection.
func TestWorkerSurvivesProviderPanic(t *testing.T) {
	srv := newTestServer(t)
	var calls atomic.Int32
	e := newEnv(t, srv, func(c *Config) {
		reg := NewRegistry()
		reg.Register(domain.KindIMAP, func(ctx context.Context, a domain.Account, cr Credentials, o ProviderOptions) (domain.MailProvider, error) {
			p, err := IMAPFactory(ctx, a, cr, o)
			if err != nil {
				return nil, err
			}
			return &panicky{MailProvider: p, calls: &calls}, nil
		})
		c.Providers = reg
	})
	e.eventually("panic reported as account error", func() bool {
		st, ok := e.mgr.Status(e.acc)
		return ok && strings.Contains(st.LastError, "internal error")
	})
	e.eventually("worker retries after the panic", func() bool { return calls.Load() >= 2 })
}
