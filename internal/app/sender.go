// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/followup"
	"github.com/Midas1989/Mailclient/internal/workflow/sendcheck"
)

// SendOptions enable sending through internal/send.
type SendOptions struct {
	// Configure may adjust the send configuration (cloud uploader, PGP,
	// transports ...) before the service is created.
	Configure func(*send.Config)
}

// MailSender is the Sender on internal/send (mail only; messenger channels
// need the messenger plugins).
type MailSender struct {
	a       *App
	Service *send.Service
	Actions *send.Actions
}

// newMailSender builds the send service with adapters on this App.
func newMailSender(a *App, opts *SendOptions) (*MailSender, error) {
	ad := &sendAdapters{a: a}
	cfg := send.Config{Identities: a.st, Transports: a.transports(), Hooks: a.bus, Events: a.bus,
		Cloud: cloudUploader{a: a}, CloudLinks: a.st, Secrets: a.opts.Secrets, Sent: ad, Relations: ad, Flags: ad,
		FollowUps: followup.New(a.st, a.bus, followup.Options{Logger: a.log}), Queue: send.StoreQueue{Store: a.st}, Blobs: ad,
		Now: a.opts.Now, Logger: a.log}
	a.configureOAuthSend(&cfg)
	if opts != nil && opts.Configure != nil {
		opts.Configure(&cfg)
	}
	svc, err := send.New(cfg)
	if err != nil {
		return nil, err
	}
	// Built-in send checks (KONZEPT 8.13) on mail.before_send.
	loc := a.opts.Location
	a.bus.RegisterHook(events.MailBeforeSend, "sendcheck", sendcheck.Hook(func() sendcheck.Config {
		c := sendcheck.DefaultConfig()
		c.WorkingHours.Location = loc
		return c
	}, sendcheck.Deps{}, a.opts.Now))
	return &MailSender{a: a, Service: svc, Actions: &send.Actions{Service: svc, Loader: ad, Drafts: ad}}, nil
}

// Send implements Sender.
func (s *MailSender) Send(ctx context.Context, r SendRequest) (SendResult, error) {
	msg := r.Message
	if msg.Channel != "email" {
		return SendResult{}, Errorf(http.StatusNotImplemented, CodeNotSupported, "sending %s messages needs the messenger plugin", msg.Channel)
	}
	if r.Identity == nil {
		return SendResult{}, invalid("identity required")
	}
	d := compose.Draft{Identity: *r.Identity, To: msg.To, Cc: msg.Cc, Bcc: msg.Bcc, Subject: msg.Subject, TextBody: msg.Text,
		HTMLBody: msg.HTML, KeepSubject: msg.Subject != ""}
	// Composer sends: warnings must be confirmed before the mail leaves;
	// the UI resends with ConfirmChecks after the user confirmed them.
	req := send.Request{IdentityID: r.Identity.ID, Source: "ui", ConfirmWarnings: !r.Unattended,
		Override: len(msg.ConfirmChecks) > 0, Confirm: msg.ConfirmChecks}
	orig := r.InReplyTo
	if orig != nil {
		d.Kind = compose.KindReply
	} else if r.ForwardOf != nil {
		orig, d.Kind = r.ForwardOf, compose.KindForward
	}
	if orig != nil {
		raw, err := s.a.rawMessage(*orig)
		if err == nil {
			d.OriginalRaw = raw
		}
		req.OriginalKey, req.OriginalAccountID = orig.Key(), orig.AccountID
		if msg.ConversationID != 0 && d.Kind.IsReply() {
			d.Quote.Mode = compose.QuoteNone // chat mode: the bubble shows the thread
		}
	}
	if msg.ConversationID != 0 && msg.SignatureID == nil {
		d.SignatureMode = compose.SignatureNone // resolved by the UI in chat mode
	}
	for _, at := range r.Attachments {
		u, err := s.a.st.GetUpload(ctx, at.UploadToken)
		if err != nil {
			return SendResult{}, invalid("unknown upload token")
		}
		ca := compose.Attachment{Filename: at.Filename, ContentType: at.MIMEType, BlobRef: u.BlobHash}
		if at.Delivery == "" || at.Delivery == "attach" {
			d.Attachments = append(d.Attachments, ca)
		} else {
			acc, err := s.a.deliveryAccount(ctx, at.Delivery)
			if err != nil {
				return SendResult{}, err
			}
			req.Cloud.Files = append(req.Cloud.Files, ca)
			req.Cloud.FileAccounts = append(req.Cloud.FileAccounts, acc)
		}
	}
	if pd := msg.PasswordDelivery; pd != nil {
		switch pd.Mode {
		case "inline":
			req.Cloud.Delivery = send.PasswordInMail
		case "separate_mail":
			req.Cloud.Delivery = send.PasswordSeparateMail
		case "messenger":
			req.Cloud.Delivery, req.Cloud.MessengerAccountID = send.PasswordMessenger, pd.MessengerAccountID
		case "none":
			req.Cloud.Delivery = send.PasswordNone
		}
	}
	req.Draft = d
	res, err := s.Service.Send(ctx, req)
	var blocked *send.BlockedError
	var tooLarge *send.TooLargeError
	switch {
	case errors.As(err, &blocked):
		return SendResult{Blocked: true, Findings: findings(blocked.Findings)}, nil
	case errors.As(err, &tooLarge):
		return SendResult{}, &Error{Code: "too_large", Status: http.StatusRequestEntityTooLarge, Message: tooLarge.Suggestion, Err: err}
	case err != nil:
		return SendResult{}, err
	}
	out := SendResult{Queued: res.Scheduled || !res.Sent, ConversationID: msg.ConversationID, Findings: findings(res.Findings)}
	defer func() { s.a.linkSharesToMessage(ctx, res.Shares, out.MessageID) }()
	for _, sh := range res.Shares {
		if sh.Password != "" {
			out.SharePasswords = append(out.SharePasswords, SharePassword{Filename: sh.FileName, Password: sh.Password})
		}
	}
	if res.MessageID != "" {
		if ms, err := s.a.st.MessagesByKey(ctx, res.MessageID); err == nil {
			for _, m := range ms {
				if m.AccountID == r.Identity.AccountID {
					out.MessageID, out.ItemID = m.ID, mailItemID(m.ID)
					if out.ConversationID == 0 {
						out.ConversationID = m.ConversationID
					}
				}
			}
		}
	}
	return out, nil
}

func findings(fs []events.Finding) []SendFinding {
	var out []SendFinding
	for _, f := range fs {
		out = append(out, SendFinding{Check: f.Check, Severity: string(f.Severity), Key: f.Key, Message: f.EN, Params: f.Params,
			Localized: map[string]string{"de": f.DE, "en": f.EN}})
	}
	return out
}

// sendAdapters connects internal/send to the store and the sync engine.
type sendAdapters struct{ a *App }

// AppendSent stores a sent message in the Sent folder (sync Actions.Append).
func (s *sendAdapters) AppendSent(ctx context.Context, acc domain.Account, id domain.Identity, raw []byte, flags []domain.Flag) error {
	return s.appendTo(ctx, acc.ID, id.ID, domain.RoleSent, raw, flags)
}

func (s *sendAdapters) appendTo(ctx context.Context, accountID, identityID int64, role domain.FolderRole, raw []byte, flags []domain.Flag) error {
	if s.a.mgr == nil {
		return nil // no sync engine: nothing to append to
	}
	var folder int64
	if sent, drafts, err := s.a.st.IdentityFolders(ctx, identityID); err == nil {
		if role == domain.RoleSent {
			folder = sent
		} else {
			folder = drafts
		}
	}
	if folder == 0 {
		f, err := s.a.st.FolderByRole(ctx, accountID, role)
		if err != nil {
			return fmt.Errorf("account %d has no %s folder", accountID, role)
		}
		folder = f.ID
	}
	_, err := s.a.mgr.Actions().Append(ctx, folder, raw, flags)
	s.a.invalidateViews()
	return err
}

// SaveDraft stores a draft (DraftSaver).
func (s *sendAdapters) SaveDraft(ctx context.Context, id domain.Identity, raw []byte) error {
	return s.appendTo(ctx, id.AccountID, id.ID, domain.RoleDrafts, raw, []domain.Flag{domain.FlagDraft, domain.FlagSeen})
}

// RecordRelations stores compose relations through the threading engine
// (re-threads the conversation) or directly.
func (s *sendAdapters) RecordRelations(ctx context.Context, rels []threading.Relation) error {
	if s.a.mgr != nil {
		return send.EngineRelations{Engine: s.a.mgr.Engine()}.RecordRelations(ctx, rels)
	}
	return send.StoreRelations{Store: s.a.st}.RecordRelations(ctx, rels)
}

// MarkOriginal sets \Answered / $Forwarded on the original.
func (s *sendAdapters) MarkOriginal(ctx context.Context, accountID int64, key string, add []domain.Flag) error {
	ms, err := s.a.st.MessagesByKey(ctx, key)
	if err != nil {
		return err
	}
	var ids []int64
	for _, m := range ms {
		if m.AccountID == accountID {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return s.a.sync.SetFlags(ctx, ids, add, nil)
}

// LoadRaw loads the raw original of a workflow action (OriginalLoader).
func (s *sendAdapters) LoadRaw(ctx context.Context, ref workflow.ItemRef) ([]byte, error) {
	ms, err := s.a.st.MessagesByKey(ctx, ref.Key)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if ref.AccountID == 0 || m.AccountID == ref.AccountID {
			return s.a.rawMessage(m)
		}
	}
	return nil, store.ErrNotFound
}

// OpenBlob opens uploaded attachment content (BlobResolver).
func (s *sendAdapters) OpenBlob(_ context.Context, ref string) (io.ReadCloser, error) {
	if s.a.blobs == nil {
		return nil, errNotFound
	}
	return s.a.blobs.Open(ref)
}

// syncSender implements the sync engine's Sender (workflow forward/reply)
// with send.Actions.
type syncSender struct{ act func() *send.Actions }

func ref(m store.Message) workflow.ItemRef {
	return workflow.ItemRef{Kind: "mail", AccountID: m.AccountID, Key: m.Key()}
}

func (s syncSender) Forward(ctx context.Context, m store.Message, req workflow.ForwardRequest) error {
	a := s.act()
	if a == nil {
		return errNoSender
	}
	return a.Forward(ctx, ref(m), req)
}

func (s syncSender) Reply(ctx context.Context, m store.Message, req workflow.ReplyRequest, automatic bool) error {
	a := s.act()
	if a == nil {
		return errNoSender
	}
	if automatic {
		return a.ReplyAutomatic(ctx, ref(m), req)
	}
	return a.ReplyTemplate(ctx, ref(m), req)
}

// runSendOps executes delayed sends of internal/send (ops of kind
// send.OpKind, account 0).
func (a *App) runSendOps(ctx context.Context) {
	ms, ok := a.opts.Sender.(*MailSender)
	if !ok {
		return
	}
	for i := 0; i < 20; i++ {
		op, found, err := a.st.ClaimNextOpOfKind(ctx, a.now(), send.OpKind)
		if err != nil || !found {
			return
		}
		if err := ms.Service.HandleOp(ctx, op.Kind, op.Payload); err != nil {
			a.log.Warn("app: delayed send failed", "op", op.ID, "err", err)
			_, _ = a.st.MarkOpFailed(ctx, op.ID, err, a.now(), 5, errors.Is(err, send.ErrBlockedByChecks))
			continue
		}
		_ = a.st.MarkOpDone(ctx, op.ID)
		a.invalidateViews()
	}
}

// ReplyToConversation answers a conversation with a short text (Android
// notification action): mails reply to the last incoming mail with the
// identity that received it; chats answer in the chat.
func (a *App) ReplyToConversation(ctx context.Context, convID int64, text, replyToItemID string) (SendResult, error) {
	if strings.TrimSpace(text) == "" {
		return SendResult{}, invalid("text required")
	}
	items, err := a.ListConversationItems(ctx, convID, ItemsQuery{Limit: 50})
	if err != nil {
		return SendResult{}, err
	}
	var target *ConversationItem
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if (replyToItemID != "" && it.ID == replyToItemID) || (replyToItemID == "" && !it.IsOwn && it.ScheduledID == "") {
			target = &items[i]
			break
		}
	}
	if target == nil {
		return SendResult{}, invalid("nothing to reply to")
	}
	msg := OutgoingMessage{Channel: target.Channel, ConversationID: convID, Text: text, To: []Address{}, Cc: []Address{}, Bcc: []Address{},
		Attachments: []OutgoingAttachment{}, ReplyToItemID: target.ID, AccountID: target.AccountID}
	if target.Channel == "email" && target.MessageID != 0 {
		d, err := a.GetMessage(ctx, target.MessageID)
		if err != nil {
			return SendResult{}, err
		}
		msg.InReplyToMessageID, msg.Subject = d.ID, d.Subject
		msg.To = d.ReplyTo
		if len(msg.To) == 0 {
			msg.To = d.From
		}
		id, err := a.identityFor(ctx, d.AccountID, append(append([]Address{}, d.To...), d.Cc...))
		if err != nil {
			return SendResult{}, err
		}
		msg.IdentityID = id
	}
	return a.Send(ctx, msg)
}

// identityFor picks the identity of an account that received a mail.
func (a *App) identityFor(ctx context.Context, accountID int64, rcpts []Address) (int64, error) {
	ids, err := a.st.ListIdentities(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, invalid("account %d has no identity", accountID)
	}
	for _, i := range ids {
		for _, r := range rcpts {
			if strings.EqualFold(i.Email, r.Email) {
				return i.ID, nil
			}
		}
	}
	for _, i := range ids {
		if i.IsDefault {
			return i.ID, nil
		}
	}
	return ids[0].ID, nil
}
