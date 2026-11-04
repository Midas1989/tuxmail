// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/server/push"
	"github.com/Midas1989/Mailclient/internal/server/push/fcm"
)

// Notification triggers (KONZEPT 15.7): new mail in an inbox, new chat
// messages, calendar reminders (events.CalendarEventStarting, published by
// the PIM part of the App), mentions and assignments in team mailboxes
// (team.go) and the workflow action notify with channel "push". The
// push.Sender applies the preferences (enabled, quiet hours, per account
// and channel, privacy mode), sends one notification per login session and
// suppresses repetitions of the same tag.

// notifyMaxAge: older mails (initial sync, catching up after downtime) do
// not notify.
const notifyMaxAge = 2 * time.Hour

// initFCM configures Firebase Cloud Messaging when a service account is set
// (push.fcm.service_account_file or TUXMAIL_FCM_SERVICE_ACCOUNT).
func (s *Server) initFCM(opts Options) error {
	if opts.FCM != nil {
		s.push.FCM = opts.FCM
		return nil
	}
	path := s.cfg.FCMServiceAccountFile()
	if path == "" {
		return nil
	}
	sa, err := fcm.LoadServiceAccount(path)
	if err != nil {
		return err
	}
	s.push.FCM = fcm.New(sa, fcm.Options{})
	s.log.Info("FCM push enabled", "project", sa.ProjectID)
	return nil
}

// fcmEnabled reports whether FCM registrations are accepted.
func (s *Server) fcmEnabled() bool { return s.push.FCM != nil }

// notify sends n to the user's devices, adding the unread badge.
func (s *Server) notify(ctx context.Context, userID int64, n push.Notification) {
	if n.Badge == 0 {
		if a, ok := s.apps.loadedApp(userID); ok {
			n.Badge = unreadBadge(ctx, a)
		}
	}
	err := s.push.Notify(ctx, userID, n)
	if err != nil && !errors.Is(err, push.ErrSuppressed) && !errors.Is(err, push.ErrDuplicate) {
		s.log.Warn("push notification failed", "user", userID, "type", n.Type, "err", err)
	}
}

// unreadBadge is the number of unread mails in inboxes plus unread chats.
func unreadBadge(ctx context.Context, a *app.App) int {
	st := a.Store()
	accs, err := st.ListAccounts(ctx)
	if err != nil {
		return 0
	}
	n := 0
	for _, acc := range accs {
		fs, err := st.ListFolders(ctx, acc.ID)
		if err != nil {
			continue
		}
		for _, f := range fs {
			if f.Role == domain.RoleInbox {
				n += f.Unread
			}
		}
	}
	if chats, err := st.ChatUnreadCounts(ctx, 0); err == nil {
		for _, c := range chats {
			n += c
		}
	}
	return n
}

// displayFrom returns the display name (or address) of a From header.
func displayFrom(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		if a.Name != "" {
			return a.Name
		}
		return a.Address
	}
	return strings.TrimSpace(from)
}

// watchUserApp turns the user's core events into push notifications until
// the App (and with it its bus) is closed.
func (s *Server) watchUserApp(userID int64, a *app.App) {
	sub := a.Bus().Subscribe(context.Background(), events.SubscribeOptions{Name: "push-notify",
		Types: []events.Type{events.MailReceived, events.ChatReceived, events.CalendarEventStarting}, Buffer: 256})
	defer sub.Unsubscribe()
	for ev := range sub.C {
		if n, ok := notificationFor(context.Background(), a, ev); ok {
			s.notify(context.Background(), userID, n)
		}
	}
}

// notificationFor maps a core event to a notification (ok=false: none).
func notificationFor(ctx context.Context, a *app.App, ev events.Event) (push.Notification, bool) {
	switch {
	case ev.Type == events.MailReceived && ev.Mail != nil:
		m := ev.Mail
		if m.FolderRole != "" && m.FolderRole != string(domain.RoleInbox) {
			return push.Notification{}, false
		}
		if m.FolderRole == "" && !strings.EqualFold(m.Folder, "inbox") {
			return push.Notification{}, false
		}
		if ev.Source != "" && ev.Source != "sync" { // imports, restores, workflow copies
			return push.Notification{}, false
		}
		if !m.Date.IsZero() && time.Since(m.Date) > notifyMaxAge { // initial sync of old mail
			return push.Notification{}, false
		}
		from := m.From.Name
		if from == "" {
			from = m.From.Email
		}
		n := push.Notification{Payload: push.Payload{Type: "mail", Title: from, Body: m.Subject, Sender: from,
			AccountID: fmt.Sprint(m.AccountID), Tag: "mail-" + m.Key}, Urgency: push.UrgencyNormal}
		if msg, err := a.Store().FindMessageByKey(ctx, m.AccountID, m.Key); err == nil && msg.ConversationID != 0 {
			n.ConversationID = fmt.Sprint(msg.ConversationID)
			n.ItemID = fmt.Sprintf("m:%d", msg.ID)
			n.URL = fmt.Sprintf("/#/mail?conversation=%d", msg.ConversationID)
			n.Tag = fmt.Sprintf("conv-%d", msg.ConversationID)
		}
		return n, true
	case ev.Type == events.ChatReceived && ev.Chat != nil:
		c := ev.Chat
		title := c.Sender
		if c.Group && c.ChatTitle != "" {
			title = c.ChatTitle
		}
		// Chats wake the device (UnifiedPush/FCM priority high).
		return push.Notification{Payload: push.Payload{Type: "chat", Title: title, Body: c.Text, Sender: c.Sender,
			AccountID: fmt.Sprint(c.AccountID), Tag: "chat-" + c.ChatID}, Urgency: push.UrgencyHigh}, true
	case ev.Type == events.CalendarEventStarting && ev.Calendar != nil:
		return push.Notification{Payload: push.Payload{Type: "calendar", Title: "TuxMail", Body: calendarTitle(ev),
			Tag: "cal-" + ev.Ref, URL: "/#/calendar"}, Urgency: push.UrgencyHigh}, true
	}
	return push.Notification{}, false
}

func calendarTitle(ev events.Event) string {
	if ev.Calendar == nil {
		return ""
	}
	return ev.Calendar.Summary
}

// userWorkflowNotifier delivers the notify action of a user's workflows
// (channel "push"; "desktop" shows an in-app notice).
type userWorkflowNotifier struct {
	s      *Server
	userID int64
}

func (n *userWorkflowNotifier) Notify(ctx context.Context, channel, title, text string) error {
	switch channel {
	case "push":
		n.s.notify(ctx, n.userID, push.Notification{Payload: push.Payload{Type: "workflow", Title: title, Body: text, URL: "/"}})
		return nil
	case "desktop":
		if a, ok := n.s.apps.loadedApp(n.userID); ok {
			a.PublishTeam(app.EventTeamNotice, app.TeamNotice{Kind: "workflow", ActorName: title, Text: text})
		}
		return nil
	}
	return fmt.Errorf("notify: channel %q is not available on this server", channel)
}
