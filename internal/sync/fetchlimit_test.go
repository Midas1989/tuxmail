// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	imapprov "github.com/Midas1989/Mailclient/internal/providers/imap"
)

// limitSpy records the limit the worker passes down to the provider.
type limitSpy struct {
	domain.MailProvider
	inner *imapprov.Provider
	limit *atomic.Int64
	plain *atomic.Int32
}

func (s *limitSpy) FetchRawLimit(ctx context.Context, ref domain.RemoteRef, limit int64) (io.ReadCloser, error) {
	s.limit.Store(limit)
	return s.inner.FetchRawLimit(ctx, ref, limit)
}

func (s *limitSpy) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	s.plain.Add(1)
	return s.inner.FetchRaw(ctx, ref)
}

// Audit NF-04: the sync size limit reaches the provider, so the download is
// bounded while it is received instead of after it was buffered.
func TestIngestPassesSizeLimitToProvider(t *testing.T) {
	srv := newTestServer(t)
	ext := srv.ext(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	appendMail(t, context.Background(), ext, "INBOX", mail("lim@x", "Carol <carol@example.org>", "Alice <alice@example.org>", "Hi", base, "", "body"))
	var limit atomic.Int64
	var plain atomic.Int32
	e := newEnv(t, srv, func(c *Config) {
		c.MaxBodySize = 123456
		reg := NewRegistry()
		reg.Register(domain.KindIMAP, func(ctx context.Context, a domain.Account, cr Credentials, o ProviderOptions) (domain.MailProvider, error) {
			p, err := IMAPFactory(ctx, a, cr, o)
			if err != nil {
				return nil, err
			}
			return &limitSpy{MailProvider: p, inner: p.(*imapprov.Provider), limit: &limit, plain: &plain}, nil
		})
		c.Providers = reg
	})
	e.eventually("message synced", func() bool { return e.has("lim@x") })
	e.eventually("body fetched with limit", func() bool { return limit.Load() == 123456 })
	if plain.Load() != 0 {
		t.Errorf("unbounded FetchRaw used %d times", plain.Load())
	}
}
