// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"sort"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// The in-memory server of go-imap cannot announce RFC 6154 SPECIAL-USE
// attributes. When Config.SpecialUse is set, sessions are wrapped by
// specialUseSession, which answers LIST itself (with the attributes, the
// subscription state and LIST-STATUS) and forwards everything else.

const memDelim = '/'

// mailboxBook tracks the mailbox names and subscriptions of one user (the
// in-memory server keeps them private).
type mailboxBook struct {
	mu         sync.Mutex
	names      map[string]bool // name -> subscribed
	specialUse map[string]imap.MailboxAttr
}

func newMailboxBook(folders []string, specialUse map[string]imap.MailboxAttr) *mailboxBook {
	b := &mailboxBook{names: map[string]bool{"INBOX": true}, specialUse: specialUse}
	for _, f := range folders {
		b.names[f] = true
	}
	return b
}

type specialUseSession struct {
	*imapmemserver.UserSession // nil until login
	s                          *Server
	book                       *mailboxBook
	user                       *imapmemserver.User
}

var _ imapserver.SessionIMAP4rev2 = (*specialUseSession)(nil)

func (sess *specialUseSession) Close() error {
	if sess.UserSession == nil {
		return nil
	}
	return sess.UserSession.Close()
}

func (sess *specialUseSession) Login(username, password string) error {
	a := sess.s.account(username)
	if a == nil {
		return imapserver.ErrAuthFailed
	}
	if err := a.mem.Login(username, password); err != nil {
		return err
	}
	sess.user, sess.book = a.mem, a.book
	sess.UserSession = imapmemserver.NewUserSession(a.mem)
	return nil
}

func (sess *specialUseSession) Create(name string, options *imap.CreateOptions) error {
	if err := sess.UserSession.Create(name, options); err != nil {
		return err
	}
	name = strings.TrimRight(name, string(memDelim))
	sess.book.mu.Lock()
	sess.book.names[name] = false
	if options != nil && len(options.SpecialUse) > 0 {
		sess.book.specialUse[name] = options.SpecialUse[0]
	}
	sess.book.mu.Unlock()
	return nil
}

func (sess *specialUseSession) Delete(name string) error {
	if err := sess.UserSession.Delete(name); err != nil {
		return err
	}
	sess.book.mu.Lock()
	delete(sess.book.names, name)
	delete(sess.book.specialUse, name)
	sess.book.mu.Unlock()
	return nil
}

func (sess *specialUseSession) Rename(oldName, newName string, options *imap.RenameOptions) error {
	if err := sess.UserSession.Rename(oldName, newName, options); err != nil {
		return err
	}
	newName = strings.TrimRight(newName, string(memDelim))
	sess.book.mu.Lock()
	sess.book.names[newName] = sess.book.names[oldName]
	delete(sess.book.names, oldName)
	if a, ok := sess.book.specialUse[oldName]; ok {
		sess.book.specialUse[newName] = a
		delete(sess.book.specialUse, oldName)
	}
	sess.book.mu.Unlock()
	return nil
}

func (sess *specialUseSession) Subscribe(name string) error {
	if err := sess.UserSession.Subscribe(name); err != nil {
		return err
	}
	sess.book.mu.Lock()
	sess.book.names[name] = true
	sess.book.mu.Unlock()
	return nil
}

func (sess *specialUseSession) Unsubscribe(name string) error {
	if err := sess.UserSession.Unsubscribe(name); err != nil {
		return err
	}
	sess.book.mu.Lock()
	sess.book.names[name] = false
	sess.book.mu.Unlock()
	return nil
}

func (sess *specialUseSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if len(patterns) == 0 {
		return w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: memDelim})
	}
	sess.book.mu.Lock()
	var names []string
	subscribed := map[string]bool{}
	for name, sub := range sess.book.names {
		for _, p := range patterns {
			if imapserver.MatchList(name, memDelim, ref, p) {
				names = append(names, name)
				subscribed[name] = sub
				break
			}
		}
	}
	attrs := map[string]imap.MailboxAttr{}
	for k, v := range sess.book.specialUse {
		attrs[k] = v
	}
	sess.book.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		su, hasSU := attrs[name]
		if options.SelectSubscribed && !subscribed[name] {
			continue
		}
		if options.SelectSpecialUse && !hasSU {
			continue
		}
		data := imap.ListData{Mailbox: name, Delim: memDelim}
		if subscribed[name] {
			data.Attrs = append(data.Attrs, imap.MailboxAttrSubscribed)
		}
		if hasSU && (options.ReturnSpecialUse || options.SelectSpecialUse) {
			data.Attrs = append(data.Attrs, su)
		}
		if options.ReturnStatus != nil {
			st, err := sess.user.Status(name, options.ReturnStatus)
			if err != nil {
				continue // deleted meanwhile
			}
			data.Status = st
		}
		if err := w.WriteList(&data); err != nil {
			return err
		}
	}
	return nil
}
