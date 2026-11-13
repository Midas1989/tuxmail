// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// OriginalLoader loads the raw message a workflow action refers to
// (implemented by the sync engine / blob store).
type OriginalLoader interface {
	LoadRaw(ctx context.Context, ref workflow.ItemRef) ([]byte, error)
}

// DraftSaver stores a composed message as draft (local and server Drafts
// folder); used by ReplyTemplate without Send.
type DraftSaver interface {
	SaveDraft(ctx context.Context, identity domain.Identity, raw []byte) error
}

// Actions implements the send-related methods of workflow.MailActions
// (Forward, ReplyTemplate, ReplyAutomatic) on top of Service. The sync
// engine embeds it into its full MailActions implementation.
type Actions struct {
	Service *Service
	Loader  OriginalLoader
	Drafts  DraftSaver // optional
	// Locale for quotes and forward headers (default: Service locale).
	Locale string
	// Parse options (e.g. a key ring to decrypt originals).
	Parse parse.Options
	// Replies remembers whom an automatic reply was sent to (optional; the
	// store implements it). Without it every mail is answered (tests).
	Replies ReplyLog
	// Now defaults to time.Now.
	Now func() time.Time
}

// ReplyLog is the sent-to log of automatic replies (store.ClaimVacationReply).
type ReplyLog interface {
	ClaimVacationReply(ctx context.Context, scope, sender string, now time.Time, window time.Duration) (bool, error)
	ForgetVacationReply(ctx context.Context, scope, sender string) error
}

// autoReplyWindow is the period in which a sender gets at most one automatic
// reply from a workflow.
const autoReplyWindow = 24 * time.Hour

// ErrAutoForwardBlocked is returned when an automated or looping original
// must not be forwarded by a workflow (audit M-10).
var ErrAutoForwardBlocked = errors.New("send: automatic forward of an automatic message suppressed")

// ErrAutoReplyRepeated is returned when the sender already got an automatic
// reply within the period.
var ErrAutoReplyRepeated = errors.New("send: sender already got an automatic reply recently")

var loopHeaders = []string{"Auto-Submitted", "Precedence", "X-Loop", "X-Auto-Response-Suppress", "Return-Path",
	"List-Id", "List-Unsubscribe", "List-Post", "X-Mailing-List"}

func (a *Actions) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// isOwn reports whether the sender of orig is the receiving identity itself
// (mail loop).
func isOwn(orig *parse.Message, ident domain.Identity) bool {
	return ident.Email != "" && strings.EqualFold(strings.TrimSpace(fromAddr(orig).Email), strings.TrimSpace(ident.Email))
}

// automated reports whether orig is bulk, list or machine generated mail
// (RFC 3834), reusing the vacation responder's rules.
func automated(orig *parse.Message) bool {
	m := &events.Mail{From: fromAddr(orig), Headers: map[string][]string{}}
	for _, h := range loopHeaders {
		if v := orig.Headers.Get(h); v != "" {
			m.Headers[h] = []string{v}
		}
	}
	auto, _ := vacation.IsAutomated(m)
	return auto || orig.IsAutomated()
}

// loop reports whether forwarding orig could feed a mail loop: automatic
// mail (Auto-Submitted), junk precedence, X-Loop or an own sender.
func loop(orig *parse.Message, ident domain.Identity) bool {
	if isOwn(orig, ident) || orig.Headers.Get("X-Loop") != "" {
		return true
	}
	if orig.AutoSubmitted != "" && orig.AutoSubmitted != "no" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(orig.Headers.Get("Precedence")), "junk")
}

// ErrNoDraftSaver is returned when a draft should be created without saver.
var ErrNoDraftSaver = errors.New("send: no draft saver configured")

func (a *Actions) load(ctx context.Context, ref workflow.ItemRef) (*parse.Message, error) {
	if a.Loader == nil {
		return nil, errors.New("send: no original loader configured")
	}
	raw, err := a.Loader.LoadRaw(ctx, ref)
	if err != nil {
		return nil, err
	}
	return parse.ParseContext(ctx, raw, a.Parse)
}

func (a *Actions) locale() string {
	if a.Locale != "" {
		return a.Locale
	}
	return a.Service.cfg.Locale
}

// receivingIdentity picks the identity the original was addressed to
// (To, Cc, Delivered-To, X-Original-To); else the default identity.
func (a *Actions) receivingIdentity(ctx context.Context, accountID int64, orig *parse.Message) (domain.Identity, []string, error) {
	ids, err := a.Service.cfg.Identities.ListIdentities(ctx, accountID)
	if err != nil {
		return domain.Identity{}, nil, err
	}
	if len(ids) == 0 {
		return domain.Identity{}, nil, fmt.Errorf("%w for account %d", ErrNoIdentity, accountID)
	}
	var self []string
	byEmail := map[string]domain.Identity{}
	for _, id := range ids {
		e := strings.ToLower(id.Email)
		self = append(self, e)
		byEmail[e] = id
	}
	var candidates []string
	for _, l := range [][]domain.Address{orig.Header.To, orig.Header.Cc} {
		for _, ad := range l {
			candidates = append(candidates, ad.Email)
		}
	}
	for _, h := range []string{"Delivered-To", "X-Original-To", "Envelope-To"} {
		for _, v := range orig.Headers.Values(h) {
			candidates = append(candidates, strings.Trim(strings.TrimSpace(v), "<>"))
		}
	}
	for _, c := range candidates {
		if id, ok := byEmail[strings.ToLower(c)]; ok {
			return id, self, nil
		}
	}
	for _, id := range ids {
		if id.IsDefault {
			return id, self, nil
		}
	}
	return ids[0], self, nil
}

func parseAddrs(list []string) ([]domain.Address, error) {
	var out []domain.Address
	for _, s := range list {
		if strings.TrimSpace(s) == "" {
			continue
		}
		as, err := mail.ParseAddressList(s)
		if err != nil {
			return nil, fmt.Errorf("send: invalid address %q: %w", s, err)
		}
		for _, ad := range as {
			out = append(out, domain.Address{Name: ad.Name, Email: ad.Address})
		}
	}
	return out, nil
}

// Forward implements workflow.MailActions.Forward.
func (a *Actions) Forward(ctx context.Context, ref workflow.ItemRef, req workflow.ForwardRequest) error {
	orig, err := a.load(ctx, ref)
	if err != nil {
		return err
	}
	ident, _, err := a.receivingIdentity(ctx, ref.AccountID, orig)
	if err != nil {
		return err
	}
	if loop(orig, ident) {
		return ErrAutoForwardBlocked
	}
	to, err := parseAddrs(req.To)
	if err != nil {
		return err
	}
	kind := compose.KindForward
	if req.AsAttachment {
		kind = compose.KindForwardAsAttachment
	}
	_, err = a.Service.Send(ctx, Request{
		Draft: compose.Draft{
			Identity: ident, Kind: kind, Original: orig, To: to, TextBody: req.Note, Locale: a.locale(),
			AutoSubmitted: "auto-forwarded",
		},
		OriginalKey: ref.Key, OriginalAccountID: ref.AccountID, Source: "workflow",
	})
	return err
}

// replyDraft builds the draft for ReplyTemplate / ReplyAutomatic.
func (a *Actions) replyDraft(ctx context.Context, ref workflow.ItemRef, req workflow.ReplyRequest) (*parse.Message, compose.Draft, error) {
	orig, err := a.load(ctx, ref)
	if err != nil {
		return nil, compose.Draft{}, err
	}
	ident, self, err := a.receivingIdentity(ctx, ref.AccountID, orig)
	if err != nil {
		return nil, compose.Draft{}, err
	}
	kind := compose.KindReply
	if req.ReplyAll {
		kind = compose.KindReplyAll
	}
	d := compose.ReplyDraft(orig, kind, ident, self)
	d.TextBody, d.Locale = req.Body, a.locale()
	if req.Subject != "" {
		d.Subject = req.Subject
	}
	if req.To != "" {
		to, err := parseAddrs([]string{req.To})
		if err != nil {
			return nil, compose.Draft{}, err
		}
		d.To, d.Cc = to, nil
	}
	for k, v := range req.Headers {
		if strings.EqualFold(k, "Auto-Submitted") {
			d.AutoSubmitted = v
			continue
		}
		d.Headers = append(d.Headers, compose.Header{Name: k, Value: v})
	}
	return orig, *d, nil
}

// ReplyTemplate implements workflow.MailActions.ReplyTemplate: it sends the
// reply (req.Send) or stores it as draft.
func (a *Actions) ReplyTemplate(ctx context.Context, ref workflow.ItemRef, req workflow.ReplyRequest) error {
	orig, d, err := a.replyDraft(ctx, ref, req)
	if err != nil {
		return err
	}
	if !req.Send {
		if a.Drafts == nil {
			return ErrNoDraftSaver
		}
		msg, err := compose.Compose(ctx, &d, a.Service.composeOptions())
		if err != nil {
			return err
		}
		return a.Drafts.SaveDraft(ctx, d.Identity, msg.Bytes())
	}
	if automated(orig) || isOwn(orig, d.Identity) {
		return ErrAutoReplyBlocked
	}
	if d.AutoSubmitted == "" {
		d.AutoSubmitted = "auto-replied"
	}
	_, err = a.Service.Send(ctx, Request{Draft: d, OriginalKey: ref.Key, OriginalAccountID: ref.AccountID, Source: "workflow"})
	return err
}

// ReplyAutomatic implements workflow.MailActions.ReplyAutomatic: an
// automatic reply (RFC 3834) from the receiving identity, without quote and
// signature, with "Auto-Submitted: auto-replied". As a last line of loop
// protection, originals that are themselves automatic are not answered
// (ErrAutoReplyBlocked).
func (a *Actions) ReplyAutomatic(ctx context.Context, ref workflow.ItemRef, req workflow.ReplyRequest) error {
	orig, d, err := a.replyDraft(ctx, ref, req)
	if err != nil {
		return err
	}
	if automated(orig) || isOwn(orig, d.Identity) {
		return ErrAutoReplyBlocked
	}
	scope, sender := "workflow:"+strconv.FormatInt(ref.AccountID, 10), replyTarget(orig, d)
	if a.Replies != nil && sender != "" {
		ok, err := a.Replies.ClaimVacationReply(ctx, scope, sender, a.now(), autoReplyWindow)
		if err != nil {
			return err
		}
		if !ok {
			return ErrAutoReplyRepeated
		}
	}
	d.AutoSubmitted = "auto-replied"
	d.Quote.Mode = compose.QuoteNone
	d.SignatureMode = compose.SignatureNone
	d.Cc = nil
	_, err = a.Service.Send(ctx, Request{Draft: d, OriginalKey: ref.Key, OriginalAccountID: ref.AccountID, Source: "workflow"})
	if err != nil && a.Replies != nil && sender != "" {
		_ = a.Replies.ForgetVacationReply(ctx, scope, sender)
	}
	return err
}

func fromAddr(m *parse.Message) domain.Address {
	if len(m.Header.From) > 0 {
		return m.Header.From[0]
	}
	return domain.Address{}
}

// replyTarget is the address an automatic reply goes to (key of the sent-to log).
func replyTarget(orig *parse.Message, d compose.Draft) string {
	if len(d.To) > 0 {
		return d.To[0].Email
	}
	return fromAddr(orig).Email
}

func (s *Service) composeOptions() compose.Options {
	o := s.cfg.ComposeOptions
	o.Now = s.cfg.Now
	if o.Blobs == nil && s.cfg.Blobs != nil {
		o.Blobs = s.cfg.Blobs
	}
	return o
}
