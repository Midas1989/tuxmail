// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
)

// Chat/hybrid bubbles of mails (items.go): newsletter detection for mails
// whose body is not synced yet, and the background download of missing
// bodies when a conversation is opened.

// noReplyRe matches automated sender mailboxes (noreply@, notifications@,
// newsletter@, mailer-daemon@, ...).
var noReplyRe = regexp.MustCompile(`(?i)^(no[-_.]?reply|do[-_.]?not[-_.]?reply|notifications?|notify|newsletters?|news|mailer[-_.]?daemon|bounces?|marketing|alerts?|mailings?)([-+._].*)?@`)

// noReplyFrom reports whether the sender of a mail is an automated mailbox.
func noReplyFrom(addrs []store.MessageAddress) bool {
	for _, a := range addrs {
		if a.Role == store.RoleFrom {
			return noReplyRe.MatchString(strings.TrimSpace(a.Email))
		}
	}
	return false
}

// bodyPrefetch downloads missing bodies of the mails shown in an opened
// conversation (header-only initial sync) in the background, newest first,
// at most a few at a time; the timeline is refreshed with
// conversation.updated when they arrive.
//
// The bookkeeping lives in the App: message ids are per-user store ids, so a
// process-wide map would let one user's prefetch block another user's (NH-04).
type bodyPrefetch struct {
	mu      sync.Mutex
	pending map[int64]bool
}

// claim marks id as being downloaded; false when it already is.
func (p *bodyPrefetch) claim(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = map[int64]bool{}
	}
	if p.pending[id] {
		return false
	}
	p.pending[id] = true
	return true
}

func (p *bodyPrefetch) release(id int64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

const maxPrefetchPerOpen = 30

func (a *App) prefetchBodies(convID int64, msgs []store.Message) {
	bf, ok := a.sync.(bodyFetcher)
	if !ok || a.mgr == nil {
		return
	}
	var ids []int64
	for i := len(msgs) - 1; i >= 0 && len(ids) < maxPrefetchPerOpen; i-- { // newest first
		m := msgs[i]
		if m.BlobHash != "" {
			continue
		}
		if a.prefetch.claim(m.ID) {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		got := 0
		for _, id := range ids {
			if _, err := bf.FetchBody(ctx, id); err == nil {
				got++
			} else {
				a.log.Debug("app: prefetch body", "message", id, "err", err)
			}
			a.prefetch.release(id)
		}
		if got > 0 {
			a.NotifyConversationUpdated(ctx, convID, "")
		}
	}()
}
