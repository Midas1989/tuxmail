// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// WorkflowMail implements workflow.MailActions on top of Actions. Item
// references carry the account id and the threading key of the message
// (events.Mail.Key); folders are given as remote id, role name or folder
// name.
type WorkflowMail struct{ m *Manager }

var _ workflow.MailActions = (*WorkflowMail)(nil)

// resolve finds the message (and the folder it was seen in) of ref.
func (w *WorkflowMail) resolve(ctx context.Context, ref workflow.ItemRef) (store.Message, int64, error) {
	st := w.m.st
	if ref.Key == "" {
		return store.Message{}, 0, workflow.Permanent(errors.New("sync: item reference without message key"))
	}
	rows, err := st.MessagesByKey(ctx, ref.Key)
	if err != nil {
		return store.Message{}, 0, err
	}
	var src int64
	if ref.Folder != "" && ref.AccountID != 0 {
		if f, err := st.FolderByRemoteID(ctx, ref.AccountID, ref.Folder); err == nil {
			src = f.ID
		}
	}
	var best *store.Message
	for i, r := range rows {
		if r.State == store.StateDeleted || (ref.AccountID != 0 && r.AccountID != ref.AccountID) {
			continue
		}
		if best == nil {
			best = &rows[i]
		}
	}
	if best == nil {
		return store.Message{}, 0, workflow.Permanent(fmt.Errorf("sync: message %q not found", ref.Key))
	}
	if src != 0 {
		locs, err := st.Locations(ctx, best.ID)
		if err != nil {
			return *best, 0, err
		}
		found := false
		for _, l := range locs {
			found = found || l.FolderID == src
		}
		if !found {
			src = 0
		}
	}
	return *best, src, nil
}

func (w *WorkflowMail) targetFolder(ctx context.Context, accountID int64, folder string) (domain.Folder, error) {
	f, err := w.m.st.FindFolder(ctx, accountID, folder)
	if errors.Is(err, store.ErrNotFound) {
		return f, workflow.Permanent(fmt.Errorf("sync: folder %q not found", folder))
	}
	return f, err
}

func permanentIfNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrCrossAccount) {
		return workflow.Permanent(err)
	}
	return err
}

// Move implements workflow.MailActions.
func (w *WorkflowMail) Move(ctx context.Context, ref workflow.ItemRef, folder string) error {
	msg, src, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	f, err := w.targetFolder(ctx, msg.AccountID, folder)
	if err != nil {
		return err
	}
	return permanentIfNotFound(w.m.actions.MoveFrom(ctx, []int64{msg.ID}, src, f.ID))
}

// Copy implements workflow.MailActions.
func (w *WorkflowMail) Copy(ctx context.Context, ref workflow.ItemRef, folder string) error {
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	f, err := w.targetFolder(ctx, msg.AccountID, folder)
	if err != nil {
		return err
	}
	return permanentIfNotFound(w.m.actions.Copy(ctx, []int64{msg.ID}, f.ID))
}

// ParseFlag maps the flag names used in workflows ("seen", "read",
// "flagged", "answered", "draft", "deleted", "forwarded", system flags
// like `\Seen` or keywords) to protocol flags.
func ParseFlag(s string) domain.Flag {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "seen", "read", `\seen`:
		return domain.FlagSeen
	case "flagged", "starred", "flag", `\flagged`:
		return domain.FlagFlagged
	case "answered", "replied", `\answered`:
		return domain.FlagAnswered
	case "draft", `\draft`:
		return domain.FlagDraft
	case "deleted", `\deleted`:
		return domain.FlagDeleted
	case "forwarded", "$forwarded":
		return domain.FlagForwarded
	}
	return domain.Flag(strings.TrimSpace(s))
}

func parseFlags(ss []string) []domain.Flag {
	out := make([]domain.Flag, 0, len(ss))
	for _, s := range ss {
		if f := ParseFlag(s); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// SetFlags implements workflow.MailActions.
func (w *WorkflowMail) SetFlags(ctx context.Context, ref workflow.ItemRef, add, remove []string) error {
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	return permanentIfNotFound(w.m.actions.SetFlags(ctx, []int64{msg.ID}, parseFlags(add), parseFlags(remove)))
}

// SetLabels implements workflow.MailActions with tags (KONZEPT 8.1a);
// they reach the server through the tag sync (IMAP keywords).
func (w *WorkflowMail) SetLabels(ctx context.Context, ref workflow.ItemRef, add, remove []string) error {
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	it := store.Item{Kind: store.ItemMessage, ID: msg.ID}
	st := w.m.st
	for _, p := range add {
		if t := w.m.cfg.Tags; t != nil {
			if _, err := t.Tag(ctx, p, store.TagSourceWorkflow, it); err != nil {
				return err
			}
			continue
		}
		tag, _, err := st.EnsureTag(ctx, p)
		if err != nil {
			return workflow.Permanent(err)
		}
		if _, err := st.TagItems(ctx, tag.ID, store.TagSourceWorkflow, it); err != nil {
			return err
		}
	}
	for _, p := range remove {
		if t := w.m.cfg.Tags; t != nil {
			if _, err := t.Untag(ctx, p, it); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			continue
		}
		tag, err := st.TagByPath(ctx, p)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := st.UntagItems(ctx, tag.ID, it); err != nil {
			return err
		}
	}
	w.m.wakeAll()
	return nil
}

// Delete implements workflow.MailActions.
func (w *WorkflowMail) Delete(ctx context.Context, ref workflow.ItemRef, permanent bool) error {
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	if permanent {
		return permanentIfNotFound(w.m.actions.DeletePermanently(ctx, []int64{msg.ID}))
	}
	return permanentIfNotFound(w.m.actions.Delete(ctx, []int64{msg.ID}))
}

// Archive implements workflow.MailActions.
func (w *WorkflowMail) Archive(ctx context.Context, ref workflow.ItemRef) error {
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	return permanentIfNotFound(w.m.actions.Archive(ctx, []int64{msg.ID}))
}

func (w *WorkflowMail) sender() (Sender, error) {
	if w.m.cfg.Sender == nil {
		return nil, workflow.Permanent(fmt.Errorf("%w: no send pipeline configured", ErrNotImplemented))
	}
	return w.m.cfg.Sender, nil
}

// Forward implements workflow.MailActions through the configured Sender.
func (w *WorkflowMail) Forward(ctx context.Context, ref workflow.ItemRef, req workflow.ForwardRequest) error {
	s, err := w.sender()
	if err != nil {
		return err
	}
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	return s.Forward(ctx, msg, req)
}

// ReplyTemplate implements workflow.MailActions through the configured Sender.
func (w *WorkflowMail) ReplyTemplate(ctx context.Context, ref workflow.ItemRef, req workflow.ReplyRequest) error {
	s, err := w.sender()
	if err != nil {
		return err
	}
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	return s.Reply(ctx, msg, req, false)
}

// ReplyAutomatic implements workflow.MailActions through the configured Sender.
func (w *WorkflowMail) ReplyAutomatic(ctx context.Context, ref workflow.ItemRef, req workflow.ReplyRequest) error {
	s, err := w.sender()
	if err != nil {
		return err
	}
	msg, _, err := w.resolve(ctx, ref)
	if err != nil {
		return err
	}
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	if _, ok := req.Headers["Auto-Submitted"]; !ok {
		req.Headers["Auto-Submitted"] = "auto-replied"
	}
	return s.Reply(ctx, msg, req, true)
}
