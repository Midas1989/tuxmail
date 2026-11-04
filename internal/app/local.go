// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// localSyncer is the Syncer fallback until internal/sync is wired in: it
// applies actions to the local store only (nothing reaches a server).
type localSyncer struct{ st *store.Store }

func (l *localSyncer) AddAccount(context.Context, int64) error    { return nil }
func (l *localSyncer) RemoveAccount(context.Context, int64) error { return nil }

func (l *localSyncer) Status(ctx context.Context) ([]AccountStatus, error) {
	accs, err := l.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountStatus, 0, len(accs))
	for _, a := range accs {
		out = append(out, AccountStatus{AccountID: a.ID, State: "offline"})
	}
	return out, nil
}

func (l *localSyncer) Move(ctx context.Context, ids []int64, to int64) error {
	target, err := l.st.GetFolder(ctx, to)
	if err != nil {
		return err
	}
	for _, id := range ids {
		m, err := l.st.GetMessage(ctx, id)
		if err != nil {
			return err
		}
		if m.AccountID != target.AccountID {
			return invalid("message %d belongs to another account", id)
		}
		locs, err := l.st.Locations(ctx, id)
		if err != nil {
			return err
		}
		moved := false
		for _, loc := range locs {
			if loc.FolderID == to {
				moved = true
				continue
			}
			if !moved {
				if err := l.st.MoveLocation(ctx, loc.FolderID, loc.RemoteRef, to, "local:"+fmt.Sprint(id)); err != nil {
					return err
				}
				moved = true
				continue
			}
			if _, err := l.st.RemoveLocation(ctx, loc.FolderID, loc.RemoteRef); err != nil {
				return err
			}
		}
		if !moved {
			if err := l.st.AddLocation(ctx, store.Location{MessageID: id, FolderID: to, RemoteRef: "local:" + fmt.Sprint(id)}); err != nil {
				return err
			}
		}
	}
	return l.st.RecountFolders(ctx, target.AccountID)
}

func (l *localSyncer) SetFlags(ctx context.Context, ids []int64, add, remove []domain.Flag) error {
	addBits, rmBits := store.FlagsFromDomain(add), store.FlagsFromDomain(remove)
	accounts := map[int64]bool{}
	for _, id := range ids {
		m, err := l.st.GetMessage(ctx, id)
		if err != nil {
			return err
		}
		if err := l.st.SetMessageFlags(ctx, id, (m.Flags|addBits)&^rmBits); err != nil {
			return err
		}
		if m.ConversationID != 0 {
			_ = l.st.RefreshConversationStats(ctx, m.ConversationID)
		}
		accounts[m.AccountID] = true
	}
	for acc := range accounts {
		if err := l.st.RecountFolders(ctx, acc); err != nil {
			return err
		}
	}
	return nil
}

func (l *localSyncer) Delete(ctx context.Context, ids []int64, permanent bool) error {
	for _, id := range ids {
		m, err := l.st.GetMessage(ctx, id)
		if err != nil {
			return err
		}
		trash, terr := l.st.FolderByRole(ctx, m.AccountID, domain.RoleTrash)
		inTrash := false
		if terr == nil {
			locs, _ := l.st.Locations(ctx, id)
			inTrash = len(locs) > 0
			for _, loc := range locs {
				if loc.FolderID != trash.ID {
					inTrash = false
				}
			}
		}
		if permanent || terr != nil || inTrash {
			if err := l.st.DeleteMessage(ctx, id); err != nil {
				return err
			}
			_ = l.st.RecountFolders(ctx, m.AccountID)
			continue
		}
		if err := l.Move(ctx, []int64{id}, trash.ID); err != nil {
			return err
		}
	}
	return nil
}

// ==== Workflow backends ========================================================

// workflowMail implements workflow.MailActions on top of the Syncer for the
// local operations; sending actions need a Sender (step B).
type workflowMail struct{ a *App }

func (w *workflowMail) message(ctx context.Context, ref workflow.ItemRef) (store.Message, error) {
	m, err := w.a.st.FindMessageByKey(ctx, ref.AccountID, ref.Key)
	if err != nil {
		return m, workflow.Permanent(fmt.Errorf("mail %q not found: %w", ref.Key, err))
	}
	return m, nil
}

func (w *workflowMail) folderByName(ctx context.Context, accountID int64, name string) (domain.Folder, error) {
	folders, err := w.a.st.ListFolders(ctx, accountID)
	if err != nil {
		return domain.Folder{}, err
	}
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) || strings.EqualFold(f.RemoteID, name) || strings.EqualFold(string(f.Role), name) {
			return f, nil
		}
	}
	return domain.Folder{}, workflow.Permanent(fmt.Errorf("folder %q not found", name))
}

func (w *workflowMail) Move(ctx context.Context, ref workflow.ItemRef, folder string) error {
	m, err := w.message(ctx, ref)
	if err != nil {
		return err
	}
	f, err := w.folderByName(ctx, m.AccountID, folder)
	if err != nil {
		return err
	}
	if err := w.a.sync.Move(ctx, []int64{m.ID}, f.ID); err != nil {
		return err
	}
	w.a.NotifyMessageUpdated(ctx, m.ID)
	return nil
}

func (w *workflowMail) Copy(ctx context.Context, ref workflow.ItemRef, folder string) error {
	m, err := w.message(ctx, ref)
	if err != nil {
		return err
	}
	f, err := w.folderByName(ctx, m.AccountID, folder)
	if err != nil {
		return err
	}
	return w.a.st.AddLocation(ctx, store.Location{MessageID: m.ID, FolderID: f.ID, RemoteRef: fmt.Sprintf("local-copy:%d", m.ID)})
}

func toFlags(names []string) []domain.Flag {
	var out []domain.Flag
	for _, n := range names {
		switch strings.ToLower(strings.TrimPrefix(n, `\`)) {
		case "seen", "read":
			out = append(out, domain.FlagSeen)
		case "flagged", "star", "starred":
			out = append(out, domain.FlagFlagged)
		case "answered":
			out = append(out, domain.FlagAnswered)
		case "deleted":
			out = append(out, domain.FlagDeleted)
		case "draft":
			out = append(out, domain.FlagDraft)
		case "forwarded", "$forwarded":
			out = append(out, domain.FlagForwarded)
		}
	}
	return out
}

func (w *workflowMail) SetFlags(ctx context.Context, ref workflow.ItemRef, add, remove []string) error {
	m, err := w.message(ctx, ref)
	if err != nil {
		return err
	}
	if err := w.a.sync.SetFlags(ctx, []int64{m.ID}, toFlags(add), toFlags(remove)); err != nil {
		return err
	}
	w.a.NotifyMessageUpdated(ctx, m.ID)
	return nil
}

func (w *workflowMail) SetLabels(ctx context.Context, ref workflow.ItemRef, add, remove []string) error {
	m, err := w.message(ctx, ref)
	if err != nil {
		return err
	}
	it := store.Item{Kind: store.ItemMessage, ID: m.ID}
	for _, p := range add {
		if _, err := w.a.proj.Tag(ctx, p, store.TagSourceWorkflow, it); err != nil {
			return err
		}
	}
	for _, p := range remove {
		if _, err := w.a.proj.Untag(ctx, p, it); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (w *workflowMail) Delete(ctx context.Context, ref workflow.ItemRef, permanent bool) error {
	m, err := w.message(ctx, ref)
	if err != nil {
		return err
	}
	if err := w.a.sync.Delete(ctx, []int64{m.ID}, permanent); err != nil {
		return err
	}
	w.a.NotifyMessageRemoved(ctx, m.ID)
	return nil
}

func (w *workflowMail) Archive(ctx context.Context, ref workflow.ItemRef) error {
	m, err := w.message(ctx, ref)
	if err != nil {
		return err
	}
	ok, err := w.a.archiveMessages(ctx, m.AccountID, []int64{m.ID}) // folderconfig.go: archive mode
	if !ok {
		return workflow.Permanent(fmt.Errorf("account has no archive folder"))
	}
	return err
}

var errNoSender = workflow.Permanent(errors.New("sending is not configured in this build (internal/send not wired)"))

func (w *workflowMail) Forward(context.Context, workflow.ItemRef, workflow.ForwardRequest) error {
	return errNoSender
}
func (w *workflowMail) ReplyTemplate(context.Context, workflow.ItemRef, workflow.ReplyRequest) error {
	return errNoSender
}
func (w *workflowMail) ReplyAutomatic(context.Context, workflow.ItemRef, workflow.ReplyRequest) error {
	return errNoSender
}

// workflowTasks implements workflow.TaskActions with the local calendar.
type workflowTasks struct{ a *App }

func (w *workflowTasks) CreateTask(ctx context.Context, t workflow.TaskRequest) (string, error) {
	cal := w.a.calendarByName(ctx, t.List, "VTODO")
	id, err := w.a.pimCreate(ctx, cal, "todo", t.Title, t.Notes, "", time.Time{}, time.Time{}, t.Due)
	return fmt.Sprint(id), err
}

func (w *workflowTasks) CreateEvent(ctx context.Context, e workflow.EventRequest) (string, error) {
	d := e.Duration
	if d <= 0 {
		d = time.Hour
	}
	cal := w.a.calendarByName(ctx, e.Calendar, "VEVENT")
	id, err := w.a.pimCreate(ctx, cal, "event", e.Title, e.Notes, e.Location, e.Start, e.Start.Add(d), time.Time{})
	return fmt.Sprint(id), err
}
