// SPDX-License-Identifier: AGPL-3.0-or-later

// Package send implements TuxMail's send pipeline (docs/KONZEPT.md 8.2,
// 8.6, 8.12, 8.13 and 12):
//
//  1. resolve identity, account and outgoing transport;
//  2. run the mail.before_send hooks (send checks) — blocking findings abort
//     unless the user confirmed them;
//  3. optionally delay the send ("send later", "undo send") through a
//     persistent queue;
//  4. upload cloud-link attachments and insert the link block, deliver the
//     share passwords as requested (in the mail, separate mail, messenger,
//     or returned to the caller);
//  5. compose the message, sign/encrypt it with PGP/MIME (protected headers,
//     encrypt-to-self) and add the Autocrypt header;
//  6. submit it, store it in the Sent folder, record the compose relations,
//     flag the original, schedule a follow-up reminder and publish
//     mail.sent.
//
// All collaborators are interfaces so that the sync engine, the providers
// and the app wire their implementations in; only Identities and Transports
// are required.
package send

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/providers/smtp"
	"github.com/Midas1989/Mailclient/internal/workflow/followup"
)

// OpKind is the operations-queue kind of delayed sends; the op worker
// passes such operations to Service.HandleOp.
const OpKind = "send.scheduled"

// Errors.
var (
	ErrNoIdentity       = errors.New("send: no identity")
	ErrNotCancellable   = errors.New("send: message is no longer cancellable")
	ErrPGPUnavailable   = errors.New("send: OpenPGP is not configured")
	ErrNoSigningKey     = errors.New("send: no secret key for the sender identity")
	ErrPGPKeyLocked     = errors.New("send: the OpenPGP key of the sender is locked, its passphrase is needed")
	ErrCannotEncrypt    = errors.New("send: not all recipients have a usable OpenPGP key")
	ErrNoCloudUploader  = errors.New("send: cloud links requested but no uploader configured")
	ErrNoMessenger      = errors.New("send: password delivery by messenger requested but no messenger configured")
	ErrBlockedByChecks  = errors.New("send: blocked by send checks")
	ErrAutoReplyBlocked = errors.New("send: automatic reply to an automatic message suppressed")
)

// BlockedError is returned when blocking send-check findings were not
// confirmed.
type BlockedError struct {
	Findings []events.Finding
}

func (e *BlockedError) Error() string {
	var ids []string
	for _, f := range e.Findings {
		ids = append(ids, f.Check)
	}
	return "send: blocked by send checks: " + strings.Join(ids, ", ")
}

// Unwrap allows errors.Is(err, ErrBlockedByChecks).
func (e *BlockedError) Unwrap() error { return ErrBlockedByChecks }

// TooLargeError is returned when the server rejects the message size.
type TooLargeError struct {
	Size  int64
	Limit int64 // 0 if unknown
	// Suggestion is an English hint for the UI; i18n key SuggestionKey.
	Suggestion    string
	SuggestionKey string
	Err           error
}

func (e *TooLargeError) Error() string {
	if e.Limit > 0 {
		return fmt.Sprintf("send: message too large (%d bytes, server limit %d bytes); %s", e.Size, e.Limit, e.Suggestion)
	}
	return fmt.Sprintf("send: message too large (%d bytes); %s", e.Size, e.Suggestion)
}

func (e *TooLargeError) Unwrap() error { return e.Err }

// PGPMode selects signing or encryption.
type PGPMode string

const (
	// PGPAuto: encrypt when Autocrypt recommends it and all keys are
	// available; sign when encrypting. This is the default.
	PGPAuto PGPMode = ""
	PGPOn   PGPMode = "on"
	PGPOff  PGPMode = "off"
)

// PasswordDelivery selects how cloud-link passwords reach the recipient
// (KONZEPT 12.2: the client asks on every send).
type PasswordDelivery string

const (
	PasswordInMail       PasswordDelivery = "in_mail"
	PasswordSeparateMail PasswordDelivery = "separate_mail"
	PasswordMessenger    PasswordDelivery = "messenger"
	PasswordNone         PasswordDelivery = "none" // returned to the caller
)

// CloudOptions describe cloud-link attachments.
type CloudOptions struct {
	// Files are uploaded instead of attached.
	Files []compose.Attachment `json:"files,omitempty"`
	// AccountID is the cloud account (Nextcloud, OpenCloud, Vaultwarden).
	AccountID int64 `json:"accountId,omitempty"`
	// FileAccounts optionally selects the cloud account per file (parallel
	// to Files; 0 = AccountID), so every attachment can use its own
	// delivery (KONZEPT 12.1 #3).
	FileAccounts []int64 `json:"fileAccounts,omitempty"`
	// Expiry of the shares (default 14 days).
	Expiry time.Duration `json:"expiry,omitempty"`
	// Delivery of the passwords (default in_mail).
	Delivery PasswordDelivery `json:"delivery,omitempty"`
	// MessengerAccountID is passed to MessengerSender.
	MessengerAccountID int64 `json:"messengerAccountId,omitempty"`
}

// PGPOptions configure OpenPGP for one send.
type PGPOptions struct {
	Sign    PGPMode `json:"sign,omitempty"`
	Encrypt PGPMode `json:"encrypt,omitempty"`
	// HideSubject moves the subject into the encrypted part ("...").
	HideSubject bool `json:"hideSubject,omitempty"`
	// NoAutocrypt suppresses the Autocrypt header.
	NoAutocrypt bool `json:"noAutocrypt,omitempty"`
	// ReplyToEncrypted is true when answering an encrypted message.
	ReplyToEncrypted bool `json:"replyToEncrypted,omitempty"`
}

// Request is a send request.
type Request struct {
	// PreferPGP keeps S/MIME out when OpenPGP would sign or encrypt anyway.
	PreferPGP bool `json:"preferPgp,omitempty"`
	// IdentityID selects the sender identity; if 0, Draft.Identity must be
	// complete (ID, AccountID, Email).
	IdentityID int64         `json:"identityId,omitempty"`
	Draft      compose.Draft `json:"draft"`
	// DraftKey is the composer's draft key (relation draft_of, hooks).
	DraftKey string `json:"draftKey,omitempty"`
	// OriginalKey is the message key of the replied/forwarded message
	// (default: Message-ID of Draft.Original). OriginalAccountID defaults
	// to the identity's account.
	OriginalKey       string `json:"originalKey,omitempty"`
	OriginalAccountID int64  `json:"originalAccountId,omitempty"`
	// ConversationLastDate is the date of the newest message of the
	// conversation (for the old-conversation check).
	ConversationLastDate time.Time `json:"conversationLastDate,omitempty"`

	// Override confirms the blocking findings whose Check is in Confirm.
	Override bool     `json:"override,omitempty"`
	Confirm  []string `json:"confirm,omitempty"`
	// ConfirmWarnings makes warnings (severity warn) block like blocking
	// findings until they are confirmed (Override + Confirm). Interactive
	// sends from the composer set it, so that hints such as "forgotten
	// attachment" are shown before the mail leaves (KONZEPT 8.13).
	ConfirmWarnings bool `json:"confirmWarnings,omitempty"`

	Cloud CloudOptions `json:"cloud"`
	PGP   PGPOptions   `json:"pgp"`
	// SMIME configures S/MIME (smime.go); when it signs or encrypts,
	// OpenPGP is not applied to the same message.
	SMIME SMIMEOptions `json:"smime"`

	// SendAt delays the send until the given time ("send later").
	SendAt time.Time `json:"sendAt,omitempty"`
	// UndoDelay delays the send so that it can be cancelled ("undo send").
	UndoDelay time.Duration `json:"undoDelay,omitempty"`

	// FollowUpAfter / FollowUpDue schedule a "waiting for reply" reminder.
	FollowUpAfter time.Duration `json:"followUpAfter,omitempty"`
	FollowUpDue   time.Time     `json:"followUpDue,omitempty"`

	// Source and Chain are propagated to events (loop protection).
	Source string   `json:"source,omitempty"`
	Chain  []string `json:"chain,omitempty"`
}

// ShareResult describes one created cloud link.
type ShareResult struct {
	FileName string    `json:"fileName"`
	URL      string    `json:"url"`
	Expires  time.Time `json:"expires,omitempty"`
	// Password is only returned for PasswordNone (the caller hands it on).
	Password string `json:"password,omitempty"`
	LinkID   int64  `json:"linkId,omitempty"`
}

// Result is the outcome of Send.
type Result struct {
	MessageID string           `json:"messageId,omitempty"` // normalized
	Findings  []events.Finding `json:"findings,omitempty"`
	Blocked   bool             `json:"blocked,omitempty"`

	Scheduled bool      `json:"scheduled,omitempty"`
	OpID      int64     `json:"opId,omitempty"`
	DueAt     time.Time `json:"dueAt,omitempty"`

	Sent      bool `json:"sent,omitempty"`
	Encrypted bool `json:"encrypted,omitempty"`
	Signed    bool `json:"signed,omitempty"`
	// SMIME is true when Encrypted/Signed refer to S/MIME instead of OpenPGP.
	SMIME bool `json:"smime,omitempty"`
	// SentAppended is true if the message was stored in the Sent folder
	// (or the provider stores it itself: SentByProvider).
	SentAppended   bool `json:"sentAppended,omitempty"`
	SentByProvider bool `json:"sentByProvider,omitempty"`

	Shares                []ShareResult `json:"shares,omitempty"`
	PasswordMailMessageID string        `json:"passwordMailMessageId,omitempty"`
	FollowUpID            int64         `json:"followUpId,omitempty"`
	Size                  int64         `json:"size,omitempty"`
	// Raw is the submitted message (as stored in Sent).
	Raw []byte `json:"-"`
	// Warnings are non-fatal problems after submission (Sent append,
	// relations, flags, follow-up).
	Warnings []string `json:"warnings,omitempty"`
}

// Config wires the service.
type Config struct {
	Identities Identities       // required
	Transports TransportFactory // required
	Hooks      HookRunner
	Events     Publisher
	Cloud      CloudUploader
	CloudLinks CloudLinkStore
	Secrets    domain.SecretStore // stores cloud-link passwords (optional)
	Messenger  MessengerSender
	Sent       MailboxAppender
	Relations  RelationRecorder
	Flags      FlagSetter
	FollowUps  FollowUpScheduler
	Queue      Queue
	Blobs      BlobResolver
	// PGP enables OpenPGP (Planner.Ring must hold the own secret keys).
	PGP *pgp.Planner
	// PGPPrefs returns the user's current OpenPGP preferences (optional).
	PGPPrefs func() PGPPrefs
	// SMIME provides S/MIME identities and recipient certificates (optional).
	SMIME SMIMEKeys
	// Locale for generated texts (cloud link block, password mail) when
	// the draft has none.
	Locale string
	Now    func() time.Time
	Logger *slog.Logger
	// ComposeOptions are passed to compose.Compose (Now is filled in).
	ComposeOptions compose.Options
}

// Service sends mail. It is safe for concurrent use.
type Service struct {
	cfg Config
}

// New creates a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Identities == nil || cfg.Transports == nil {
		return nil, errors.New("send: Identities and Transports are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Locale == "" {
		cfg.Locale = "en"
	}
	return &Service{cfg: cfg}, nil
}

// scheduledPayload is the queue payload of a delayed send.
type scheduledPayload struct {
	Request Request `json:"request"`
}

// Send runs the pipeline. A blocked send returns the findings both in the
// Result and as *BlockedError.
func (s *Service) Send(ctx context.Context, req Request) (*Result, error) {
	return s.send(ctx, req, false)
}

func (s *Service) send(ctx context.Context, req Request, checked bool) (*Result, error) {
	ident, err := s.identity(ctx, &req)
	if err != nil {
		return nil, err
	}
	req.Draft.Identity = ident
	res := &Result{}

	if !checked {
		blocked, err := s.runChecks(ctx, &req, res)
		if err != nil {
			return res, err
		}
		if blocked != nil {
			return res, blocked
		}
	}

	now := s.cfg.Now()
	due := req.SendAt
	if req.UndoDelay > 0 && now.Add(req.UndoDelay).After(due) {
		due = now.Add(req.UndoDelay)
	}
	if !checked && due.After(now) {
		return s.schedule(ctx, req, due, res)
	}
	return s.deliver(ctx, req, res)
}

// identity resolves the sender identity.
func (s *Service) identity(ctx context.Context, req *Request) (domain.Identity, error) {
	if req.IdentityID != 0 {
		id, err := s.cfg.Identities.GetIdentity(ctx, req.IdentityID)
		if err != nil {
			return domain.Identity{}, fmt.Errorf("send: identity %d: %w", req.IdentityID, err)
		}
		return id, nil
	}
	id := req.Draft.Identity
	if id.Email == "" || id.AccountID == 0 {
		return domain.Identity{}, ErrNoIdentity
	}
	return id, nil
}

// runChecks runs the mail.before_send hooks. It returns a *BlockedError
// when blocking findings are not confirmed.
func (s *Service) runChecks(ctx context.Context, req *Request, res *Result) (*BlockedError, error) {
	if s.cfg.Hooks == nil {
		return nil, nil
	}
	d := s.eventDraft(ctx, req)
	hr := s.cfg.Hooks.RunHooks(ctx, events.Event{
		Type: events.MailBeforeSend, AccountID: req.Draft.Identity.AccountID, Ref: req.DraftKey,
		Source: req.Source, Chain: req.Chain, Draft: d,
	})
	res.Findings = hr.Findings
	needsConfirm := func(f events.Finding) bool {
		return f.Severity == events.SeverityBlock || (req.ConfirmWarnings && f.Severity == events.SeverityWarn)
	}
	if !hr.Vetoed && !slices.ContainsFunc(hr.Findings, needsConfirm) {
		return nil, nil
	}
	confirmed := map[string]bool{}
	if req.Override {
		for _, c := range req.Confirm {
			confirmed[c] = true
		}
	}
	var open []events.Finding
	for _, f := range hr.Findings {
		if needsConfirm(f) && !confirmed[f.Check] {
			open = append(open, f)
		}
	}
	if hr.Vetoed && len(open) == 0 && !hasBlock(hr.Findings) {
		// Vetoed without a blocking finding (explicit hook veto).
		if !req.Override || !confirmed["veto"] {
			open = append(open, events.Finding{Check: "veto", Severity: events.SeverityBlock, Key: "send.vetoed",
				DE: "Der Versand wurde von einer Prüfung verhindert.", EN: "Sending was vetoed by a check."})
		}
	}
	if len(open) == 0 {
		return nil, nil
	}
	res.Blocked = true
	return &BlockedError{Findings: open}, nil
}

func hasBlock(fs []events.Finding) bool {
	for _, f := range fs {
		if f.Severity == events.SeverityBlock {
			return true
		}
	}
	return false
}

// eventDraft converts the request for the hooks.
func (s *Service) eventDraft(ctx context.Context, req *Request) *events.Draft {
	d := &req.Draft
	ed := &events.Draft{
		Key: req.DraftKey, AccountID: d.Identity.AccountID,
		Identity: events.Identity{Name: d.Identity.Name, Email: d.Identity.Email},
		To:       d.To, Cc: d.Cc, Bcc: d.Bcc, Subject: d.Subject,
		Body:     d.TextBody,
		ReplyAll: d.Kind == compose.KindReplyAll,
		SendAt:   req.SendAt,
	}
	if ed.Body == "" && d.HTMLBody != "" {
		ed.Body = htmlToText(d.HTMLBody)
	}
	if d.From != nil && d.From.Email != "" {
		ed.Identity.Email = d.From.Email
	}
	if d.OnBehalfOf != nil && d.OnBehalfOf.Email != "" {
		ed.Identity = events.Identity{Name: d.OnBehalfOf.Name, Email: d.OnBehalfOf.Email}
	}
	for _, a := range d.Attachments {
		ed.Attachments = append(ed.Attachments, events.Attachment{Name: a.Filename, MIME: a.ContentType, Size: attachmentSize(a)})
	}
	if d.Original != nil {
		if d.Kind.IsReply() {
			ed.InReplyTo = originalKey(req)
			ed.InReplyToDate = d.Original.Header.Date
		}
		if d.Kind == compose.KindForward && !d.OmitOriginalAttachments {
			for _, a := range d.Original.Attachments {
				if !a.Inline {
					ed.Attachments = append(ed.Attachments, events.Attachment{Name: a.Filename, MIME: a.MIMEType, Size: a.Size})
				}
			}
		}
	}
	if !req.ConversationLastDate.IsZero() {
		ed.InReplyToDate = req.ConversationLastDate
	}
	if d.Kind == compose.KindForwardAsAttachment {
		ed.Attachments = append(ed.Attachments, events.Attachment{Name: "message.eml", MIME: "message/rfc822", Size: int64(len(originalRaw(d)))})
	}
	// Planned PGP state, so that the encryption checks see the truth.
	if plan, err := s.planPGP(ctx, req, false); err == nil {
		ed.Encrypt, ed.Sign = plan.encrypt, plan.sign
	} else {
		ed.Encrypt, ed.Sign = req.PGP.Encrypt == PGPOn, req.PGP.Sign == PGPOn
	}
	return ed
}

func attachmentSize(a compose.Attachment) int64 {
	if a.Data != nil {
		return int64(len(a.Data))
	}
	return 0
}

func originalRaw(d *compose.Draft) []byte {
	if d.Original != nil {
		return d.Original.Raw
	}
	return d.OriginalRaw
}

// originalKey returns the message key of the original.
func originalKey(req *Request) string {
	if req.OriginalKey != "" {
		return req.OriginalKey
	}
	if req.Draft.Original != nil {
		return req.Draft.Original.Header.MessageID
	}
	if len(req.Draft.OriginalRaw) > 0 {
		return threading.NormalizeMessageID(headerValue(req.Draft.OriginalRaw, "Message-Id"))
	}
	return ""
}

// schedule enqueues a delayed send.
func (s *Service) schedule(ctx context.Context, req Request, due time.Time, res *Result) (*Result, error) {
	if s.cfg.Queue == nil {
		return res, errors.New("send: delayed send requested but no queue configured")
	}
	// Make the payload self-contained: original as raw bytes, attachment
	// contents read, Message-ID fixed so that the UI can track it.
	d := &req.Draft
	if d.Original != nil && len(d.OriginalRaw) == 0 {
		d.OriginalRaw = d.Original.Raw
		if req.OriginalKey == "" {
			req.OriginalKey = d.Original.Header.MessageID
		}
	}
	if err := materialize(ctx, d.Attachments, s.cfg.Blobs, false); err != nil {
		return res, err
	}
	if err := materialize(ctx, req.Cloud.Files, s.cfg.Blobs, false); err != nil {
		return res, err
	}
	if d.MessageID == "" {
		id, err := compose.NewMessageID(s.cfg.ComposeOptions.Rand, messageIDDomain(d))
		if err != nil {
			return res, err
		}
		d.MessageID = id
	}
	payload, err := json.Marshal(scheduledPayload{Request: req})
	if err != nil {
		return res, fmt.Errorf("send: encode scheduled request: %w", err)
	}
	id, err := s.cfg.Queue.EnqueueAt(ctx, OpKind, string(payload), due)
	if err != nil {
		return res, err
	}
	res.Scheduled, res.OpID, res.DueAt, res.MessageID = true, id, due, d.MessageID
	return res, nil
}

func messageIDDomain(d *compose.Draft) string {
	switch {
	case d.OnBehalfOf != nil && d.OnBehalfOf.Email != "":
		return d.OnBehalfOf.Email
	case d.From != nil && d.From.Email != "":
		return d.From.Email
	}
	return d.Identity.Email
}

// Cancel cancels a delayed send that has not started yet and returns its
// request (e.g. to reopen the draft).
func (s *Service) Cancel(ctx context.Context, opID int64) (*Request, error) {
	if s.cfg.Queue == nil {
		return nil, ErrNotCancellable
	}
	payload, ok, err := s.cfg.Queue.CancelPending(ctx, opID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotCancellable
	}
	var p scheduledPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, fmt.Errorf("send: decode scheduled request: %w", err)
	}
	return &p.Request, nil
}

// HandleOp executes a delayed send from the operations queue (kind OpKind).
// The send checks already ran when the send was scheduled.
func (s *Service) HandleOp(ctx context.Context, kind, payload string) error {
	if kind != OpKind {
		return fmt.Errorf("send: unexpected op kind %q", kind)
	}
	var p scheduledPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return fmt.Errorf("send: decode scheduled request: %w", err)
	}
	_, err := s.send(ctx, p.Request, true)
	return err
}

// deliver performs the actual send (after checks and scheduling).
func (s *Service) deliver(ctx context.Context, req Request, res *Result) (*Result, error) {
	d := &req.Draft
	ident := d.Identity
	locale := d.Locale
	if locale == "" {
		locale = s.cfg.Locale
		d.Locale = locale
	}
	account, err := s.cfg.Identities.GetAccount(ctx, ident.AccountID)
	if err != nil {
		return res, fmt.Errorf("send: account %d: %w", ident.AccountID, err)
	}
	transportAccount := account
	if ident.SMTPAccountID != 0 && ident.SMTPAccountID != account.ID {
		if transportAccount, err = s.cfg.Identities.GetAccount(ctx, ident.SMTPAccountID); err != nil {
			return res, fmt.Errorf("send: SMTP account %d: %w", ident.SMTPAccountID, err)
		}
	}
	if err := s.applySignature(ctx, d); err != nil {
		return res, err
	}

	// Cloud links.
	var shares []uploadedShare
	if len(req.Cloud.Files) > 0 {
		if shares, err = s.uploadCloud(ctx, &req, res); err != nil {
			return res, err
		}
		insertLinkBlock(d, shares, req.Cloud.Delivery, locale)
	}

	// Both are possible and the user prefers OpenPGP: S/MIME stays out, unless
	// it was chosen explicitly.
	if req.PreferPGP && req.SMIME.Sign != PGPOn && req.SMIME.Encrypt != PGPOn {
		if pl, err := s.planPGP(ctx, &req, false); err == nil && (pl.encrypt || pl.sign) {
			req.SMIME = SMIMEOptions{Sign: PGPOff, Encrypt: PGPOff}
		}
	}

	// S/MIME plan (takes precedence over OpenPGP when active).
	smPlan, err := s.planSMIME(ctx, &req)
	if err != nil {
		return res, err
	}
	if smPlan.active() {
		req.PGP.Sign, req.PGP.Encrypt = PGPOff, PGPOff
	}

	// PGP plan and Autocrypt.
	plan, err := s.planPGP(ctx, &req, true)
	if err != nil {
		return res, err
	}
	if plan.publicKey() != nil && !req.PGP.NoAutocrypt && !s.pgpPrefs().NoAutocrypt {
		if ac, err := pgp.NewAutocryptHeader(ident.Email, plan.publicKey(), s.ownPrefer(), s.cfg.Now()); err == nil {
			d.Autocrypt = ac
		}
	}

	msg, err := compose.Compose(ctx, d, s.composeOptions())
	if err != nil {
		return res, err
	}
	res.MessageID = msg.MessageID
	if err := s.protect(ctx, msg, plan, req.PGP, res); err != nil {
		return res, err
	}
	if err := s.protectSMIME(msg, smPlan, res); err != nil {
		return res, err
	}
	raw := msg.Bytes()
	res.Raw, res.Size = raw, int64(len(raw))

	if err := s.submit(ctx, transportAccount, msg, raw); err != nil {
		return res, err
	}
	res.Sent = true

	s.afterSend(ctx, &req, account, msg, raw, res)

	if len(shares) > 0 {
		s.deliverPasswords(ctx, &req, msg, shares, res)
	}
	return res, nil
}

// applySignature loads the identity's default/reply signature when the draft
// has none (and signatures are not disabled).
func (s *Service) applySignature(ctx context.Context, d *compose.Draft) error {
	if d.Signature != nil || d.SignatureMode == compose.SignatureNone {
		return nil
	}
	if d.SignatureMode == compose.SignatureShort && d.ShortSignature != "" {
		return nil
	}
	id := d.Identity.DefaultSignatureID
	if d.Kind != compose.KindNew && d.Identity.ReplySignatureID != 0 {
		id = d.Identity.ReplySignatureID
	}
	if id == 0 {
		return nil
	}
	sig, err := s.cfg.Identities.GetSignature(ctx, id)
	if err != nil {
		s.cfg.Logger.WarnContext(ctx, "send: signature not found", "id", id, "err", err)
		return nil
	}
	d.Signature = &sig
	return nil
}

// submit sends raw through the account's transport.
func (s *Service) submit(ctx context.Context, account domain.Account, msg *compose.Message, raw []byte) error {
	tr, err := s.cfg.Transports.Transport(ctx, account)
	if err != nil {
		return err
	}
	defer tr.Close()
	err = tr.Send(ctx, domain.OutgoingMessage{
		From: domain.Address{Email: msg.EnvelopeFrom}, Recipients: msg.Recipients, Raw: raw,
	})
	var tooLarge *smtp.ErrMessageTooLarge
	if errors.As(err, &tooLarge) {
		return &TooLargeError{
			Size: int64(len(raw)), Limit: tooLarge.Limit, Err: err,
			Suggestion:    "send large attachments as cloud links instead",
			SuggestionKey: "send.too_large_use_cloud_links",
		}
	}
	return err
}

// ProviderSavesSent reports whether no copy of sent mail is appended to
// the Sent folder because the provider files it itself (Gmail SMTP,
// Exchange/Graph/EAS) or the user turned saving off (Extra["sent_autosave"]).
func ProviderSavesSent(a domain.Account) bool { return providerSavesSent(a) }

// providerSavesSent reports whether the provider stores submitted mail in
// Sent itself (Gmail SMTP, Exchange/Graph/EAS).
func providerSavesSent(a domain.Account) bool {
	switch a.Extra["sent_autosave"] {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	switch a.Kind {
	case domain.KindGmail, domain.KindGraph, domain.KindEAS, domain.KindEWS:
		return true
	}
	if a.Incoming != nil {
		h := strings.ToLower(a.Incoming.Host)
		if strings.HasSuffix(h, "gmail.com") || strings.HasSuffix(h, "googlemail.com") {
			return true
		}
	}
	return false
}

// afterSend runs the post-submission steps; failures become warnings
// because the message is already on its way.
func (s *Service) afterSend(ctx context.Context, req *Request, account domain.Account, msg *compose.Message, raw []byte, res *Result) {
	d := &req.Draft
	ident := d.Identity
	warn := func(step string, err error) {
		res.Warnings = append(res.Warnings, step+": "+err.Error())
		s.cfg.Logger.WarnContext(ctx, "send: post-send step failed", "step", step, "err", err)
	}

	if providerSavesSent(account) {
		res.SentByProvider = true
	} else if s.cfg.Sent != nil {
		if err := s.cfg.Sent.AppendSent(ctx, account, ident, raw, []domain.Flag{domain.FlagSeen}); err != nil {
			warn("append_sent", err)
		} else {
			res.SentAppended = true
		}
	}

	key := msg.MessageID
	origKey := originalKey(req)
	action := threading.ComposeNew
	var flag domain.Flag
	switch {
	case d.Kind.IsReply():
		action, flag = threading.ComposeReply, domain.FlagAnswered
	case d.Kind.IsForward():
		action, flag = threading.ComposeForward, domain.FlagForwarded
	}
	if s.cfg.Relations != nil && (origKey != "" && action != threading.ComposeNew || req.DraftKey != "") {
		cc := threading.ComposeContext{Action: action, DraftKey: req.DraftKey}
		if origKey != "" && action != threading.ComposeNew {
			cc.OriginalKeys = []string{origKey}
		}
		if rels := threading.ComposeRelations(key, cc); len(rels) > 0 {
			if err := s.cfg.Relations.RecordRelations(ctx, rels); err != nil {
				warn("relations", err)
			}
		}
	}
	if s.cfg.Flags != nil && origKey != "" && flag != "" {
		acc := req.OriginalAccountID
		if acc == 0 {
			acc = ident.AccountID
		}
		if err := s.cfg.Flags.MarkOriginal(ctx, acc, origKey, []domain.Flag{flag}); err != nil {
			warn("flag_original", err)
		}
	}
	if s.cfg.FollowUps != nil && (req.FollowUpAfter > 0 || !req.FollowUpDue.IsZero()) {
		var rcpts []string
		for _, a := range append(append([]domain.Address{}, d.To...), d.Cc...) {
			rcpts = append(rcpts, a.Email)
		}
		id, err := s.cfg.FollowUps.Schedule(ctx, followup.Request{
			AccountID: ident.AccountID, MessageKey: key, Subject: msg.Subject, Recipients: rcpts,
			SentAt: msg.Date, Due: req.FollowUpDue, After: req.FollowUpAfter,
		})
		if err != nil {
			warn("follow_up", err)
		} else {
			res.FollowUpID = id
		}
	}
	if s.cfg.Events != nil {
		ev := events.Event{
			Type: events.MailSent, Time: s.cfg.Now().UTC(), AccountID: ident.AccountID, Ref: key,
			Source: req.Source, Chain: req.Chain,
			Mail: &events.Mail{
				AccountID: ident.AccountID, Key: key, MessageID: key, FolderRole: string(domain.RoleSent),
				From: msg.From, To: d.To, Cc: d.Cc, Bcc: d.Bcc, Subject: msg.Subject, Body: msg.Text,
				Date: msg.Date, Size: int64(len(raw)), Flags: []string{string(domain.FlagSeen)},
				PGP: events.PGPStatus{Encrypted: res.Encrypted, Signed: res.Signed},
			},
		}
		for _, a := range d.Attachments {
			ev.Mail.Attachments = append(ev.Mail.Attachments, events.Attachment{Name: a.Filename, MIME: a.ContentType, Size: attachmentSize(a)})
		}
		s.cfg.Events.Publish(ev)
	}
}

// headerValue returns the first value of a header in a raw message.
func headerValue(raw []byte, name string) string {
	lname := strings.ToLower(name) + ":"
	lines := strings.Split(string(raw), "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(l), lname) {
			v := l[len(lname):]
			for j := i + 1; j < len(lines); j++ {
				n := strings.TrimRight(lines[j], "\r")
				if n == "" || (n[0] != ' ' && n[0] != '\t') {
					break
				}
				v += n
			}
			return strings.TrimSpace(v)
		}
	}
	return ""
}
