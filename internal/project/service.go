// SPDX-License-Identifier: AGPL-3.0-or-later

// Package project implements the service logic for tags, project folders
// and search folders (KONZEPT 8.1, 8.4, 8.5) on top of internal/store:
// it publishes the workflow trigger events (tag.added, tag.removed,
// project.item_added, project.status_changed, project.due_soon,
// searchfolder.matched) and the UI live-update events (tag.changed,
// project.changed, searchfolder.changed), assigns new items to projects by
// rules, detects items that newly match search folders, keeps search folder
// counts fresh, and implements the workflow backends (WorkflowBackend).
//
// The sync pipeline calls ProcessNewItem (or AssignByRules and CheckItem)
// for every new mail, chat message, event, contact or file.
package project

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// DefaultCountTTL is how long cached search folder counts are served.
const DefaultCountTTL = 30 * time.Second

// WorkflowInstaller installs the default workflows of a project template
// for a new project (e.g. by instantiating a workflow template with the
// project key). It is optional.
type WorkflowInstaller interface {
	InstallProjectWorkflow(ctx context.Context, p store.Project, ref string) error
}

// Options configures a Service.
type Options struct {
	Now       func() time.Time
	CountTTL  time.Duration // default DefaultCountTTL
	Logger    *slog.Logger
	Workflows WorkflowInstaller
	// Source is the event source of events published by the service
	// (default "project"); events caused by workflows use "workflow:<id>".
	Source string
}

// Service is the tag/project/search folder service. It is safe for
// concurrent use.
type Service struct {
	st   *store.Store
	bus  *events.Bus
	opts Options

	invalidateMu sync.Mutex
	lastInval    time.Time
}

// New creates a service; bus may be nil (no events are published).
func New(st *store.Store, bus *events.Bus, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CountTTL <= 0 {
		opts.CountTTL = DefaultCountTTL
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Source == "" {
		opts.Source = "project"
	}
	return &Service{st: st, bus: bus, opts: opts}
}

// Store returns the underlying store.
func (s *Service) Store() *store.Store { return s.st }

func (s *Service) now() time.Time { return s.opts.Now() }

// publish sends an event with the workflow chain of ctx.
func (s *Service) publish(ctx context.Context, ev events.Event) {
	if s.bus == nil {
		return
	}
	if ev.ID == "" {
		ev.ID = events.NewID()
	}
	if ev.Time.IsZero() {
		ev.Time = s.now().UTC()
	}
	ev.Source = s.opts.Source
	if chain := workflow.EventChain(ctx); len(chain) > 0 {
		ev.Chain = chain
		ev.Source = "workflow:" + chain[len(chain)-1]
	}
	s.bus.Publish(ev)
}

func itemKey(it store.Item) *events.ItemKey { return &events.ItemKey{Kind: string(it.Kind), ID: it.ID} }

// itemEvent fills the item payload of an event (mail payload for messages,
// chat payload for chat messages), so that workflow conditions and the
// in-memory query matcher can evaluate the item. Errors leave the payload
// empty.
func (s *Service) itemEvent(ctx context.Context, ev *events.Event, it store.Item) {
	ev.Ref = it.String()
	switch it.Kind {
	case store.ItemMessage:
		m, err := s.st.GetMessage(ctx, it.ID)
		if err != nil {
			return
		}
		mail := &events.Mail{AccountID: m.AccountID, Key: m.Key(), MessageID: m.MessageIDHdr, Subject: m.Subject,
			From: domain.Address{Email: m.From}, Date: m.SentAt, Size: m.Size}
		if mail.Date.IsZero() {
			mail.Date = m.ReceivedAt
		}
		if acc, err := s.st.GetAccount(ctx, m.AccountID); err == nil {
			mail.Account = acc.DisplayName
		}
		for _, f := range m.Flags.Domain() {
			mail.Flags = append(mail.Flags, string(f))
		}
		if addrs, err := s.st.MessageAddresses(ctx, it.ID); err == nil {
			for _, a := range addrs {
				da := domain.Address{Name: a.Name, Email: a.Email}
				switch a.Role {
				case store.RoleFrom:
					mail.From = da
				case store.RoleTo:
					mail.To = append(mail.To, da)
				case store.RoleCc:
					mail.Cc = append(mail.Cc, da)
				case store.RoleBcc:
					mail.Bcc = append(mail.Bcc, da)
				}
			}
		}
		if locs, err := s.st.Locations(ctx, it.ID); err == nil && len(locs) > 0 {
			if f, err := s.st.GetFolder(ctx, locs[0].FolderID); err == nil {
				mail.Folder, mail.FolderRole = f.Name, string(f.Role)
			}
		}
		if atts, err := s.st.MessageAttachments(ctx, it.ID); err == nil {
			for _, a := range atts {
				mail.Attachments = append(mail.Attachments, events.Attachment{Name: a.Filename, MIME: a.MIMEType, Size: a.Size})
			}
		}
		if paths, err := s.st.TagPaths(ctx, it); err == nil {
			mail.Labels = paths
		}
		ev.Mail, ev.AccountID = mail, m.AccountID
	case store.ItemChatMessage:
		cm, err := s.st.GetChatMessage(ctx, it.ID)
		if err != nil {
			return
		}
		c, err := s.st.GetChat(ctx, cm.ChatID)
		if err != nil {
			return
		}
		chat := &events.Chat{AccountID: c.AccountID, Key: cm.RemoteID, Channel: c.Channel, ChatID: c.RemoteID, ChatTitle: c.Title,
			Group: c.Kind == store.ChatGroup, Text: cm.Body, HasMedia: cm.MediaBlob != "" || cm.MediaName != ""}
		if cm.SenderPersonID != 0 {
			if p, err := s.st.GetPerson(ctx, cm.SenderPersonID); err == nil {
				chat.Sender = p.DisplayName
			}
		}
		ev.Chat, ev.AccountID = chat, c.AccountID
	}
}

// ProcessNewItem is called by the sync pipeline for every new item: it
// assigns the item to projects by rule and records search folder matches.
func (s *Service) ProcessNewItem(ctx context.Context, it store.Item) error {
	if _, err := s.AssignByRules(ctx, it); err != nil {
		return err
	}
	_, err := s.CheckItem(ctx, it)
	return err
}

func idString(id int64) string { return strconv.FormatInt(id, 10) }

func lowerTrim(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
