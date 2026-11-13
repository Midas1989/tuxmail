// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// hammer calls SyncNow repeatedly against a provider whose login always
// fails with err and returns the number of login attempts (audit M-7).
func hammer(t *testing.T, err error) int32 {
	t.Helper()
	srv := newTestServer(t)
	var attempts atomic.Int32
	e := newEnv(t, srv, func(c *Config) {
		reg := NewRegistry()
		reg.Register(domain.KindIMAP, func(ctx context.Context, a domain.Account, cr Credentials, o ProviderOptions) (domain.MailProvider, error) {
			attempts.Add(1)
			return nil, err
		})
		c.Providers = reg
		c.MinBackoff, c.MaxBackoff = time.Hour, time.Hour
	})
	e.eventually("first attempt", func() bool { return attempts.Load() >= 1 })
	for i := 0; i < 100; i++ {
		e.mgr.SyncNow(e.acc)
		time.Sleep(5 * time.Millisecond)
	}
	return attempts.Load()
}

func TestSyncNowDoesNotCutAuthBackoff(t *testing.T) {
	if n := hammer(t, errors.New("imap: authenticate PLAIN: AUTHENTICATIONFAILED invalid credentials")); n > 2 {
		t.Fatalf("login attempts = %d, want at most 2", n)
	}
}

func TestSyncNowCutsNetworkBackoffOnlyOnce(t *testing.T) {
	if n := hammer(t, errors.New("dial tcp: connection refused")); n > 3 {
		t.Fatalf("login attempts = %d, want at most 3", n)
	}
}

func TestIsAuthError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("imap: authenticate PLAIN: AUTHENTICATIONFAILED"), true},
		{ErrNoCredentials, true},
		{errors.New("authenticate PLAIN: EOF"), false},
		{errors.New("dial tcp: connection refused"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsAuthError(c.err); got != c.want {
			t.Errorf("IsAuthError(%v) = %v", c.err, got)
		}
	}
}
