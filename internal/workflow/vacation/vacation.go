// SPDX-License-Identifier: AGPL-3.0-or-later

// Package vacation implements the out-of-office auto reply
// ("Abwesenheitsnotiz", KONZEPT 8.5) on top of the workflow system.
//
// The user edits a simple Settings struct. Workflows(settings) turns it into
// built-in workflows (trigger mail.received / chat.received, action
// vacation_reply), and Responder implements workflow.VacationResponder: it
// decides per message whether to reply (time window, scope, own addresses,
// mailing lists and other automated mail, once per sender per N days) and
// sends the reply through workflow.MailActions.ReplyAutomatic with
// "Auto-Submitted: auto-replied" (RFC 3834).
//
// Providers that can mirror the settings to the server (Sieve vacation
// RFC 5230, Graph automaticRepliesSetting, EAS Oof) implement
// ServerVacationSync; for those accounts the local responder stays silent so
// that senders do not get two replies. Local execution is the fallback for
// all other accounts.
package vacation

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// DefaultOncePerDays is the default dedup window in days.
const DefaultOncePerDays = 4

// Settings are the out-of-office settings.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Start and End bound the absence (zero = open-ended). End is exclusive.
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
	// AccountIDs limits the reply to these accounts (empty = all).
	AccountIDs []int64 `json:"accountIds,omitempty"`
	// Identities limits the reply to mails addressed (To/Cc) to one of these
	// addresses (empty = any).
	Identities []string `json:"identities,omitempty"`
	// Subject is a template, default "Abwesenheitsnotiz: {{betreff}}".
	Subject string `json:"subject,omitempty"`
	// InternalText is sent to senders from OwnDomains, ExternalText to all
	// others. If one is empty, the other is used.
	InternalText string `json:"internalText,omitempty"`
	ExternalText string `json:"externalText,omitempty"`
	// OwnDomains decide internal vs. external senders.
	OwnDomains []string `json:"ownDomains,omitempty"`
	// OwnAddresses are never answered (own identities, aliases).
	OwnAddresses []string `json:"ownAddresses,omitempty"`
	// OncePerDays: reply at most once per sender within this many days
	// (0 = default 4).
	OncePerDays int `json:"oncePerDays,omitempty"`
	// ForwardTo optionally forwards incoming personal mail to a deputy.
	// Every entry must be a plain mail address (Validate); turning the
	// forwarding on or changing it needs ConfirmExternal.
	ForwardTo []string `json:"forwardTo,omitempty"`
	// ConfirmExternal (input only, never stored) says that the user has seen
	// that mail is forwarded to ForwardTo and confirmed it (re-audit NH-10).
	ConfirmExternal bool `json:"confirmExternal,omitempty"`
	// ChatAutoReply enables auto replies in messenger chats (direct chats only).
	ChatAutoReply bool     `json:"chatAutoReply,omitempty"`
	ChatText      string   `json:"chatText,omitempty"` // default ExternalText
	ChatChannels  []string `json:"chatChannels,omitempty"`
}

// MaxForwardTo is the maximum number of forwarding addresses.
const MaxForwardTo = 5

var plainAddr = regexp.MustCompile(`^[^@\s<>"(),:;\[\]\\]+@[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?\.[A-Za-z0-9-]{2,}$`)

// validAddress reports whether a is a plain addr-spec (no display name, no
// control characters, no list syntax).
func validAddress(a string) bool {
	if len(a) > 254 || strings.TrimSpace(a) != a {
		return false
	}
	for _, r := range a {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return plainAddr.MatchString(a)
}

// SameForwardTo reports whether two forwarding lists name the same addresses.
func SameForwardTo(a, b []string) bool {
	norm := func(l []string) []string {
		out := make([]string, 0, len(l))
		for _, x := range l {
			out = append(out, strings.ToLower(strings.TrimSpace(x)))
		}
		sort.Strings(out)
		return out
	}
	return slices.Equal(norm(a), norm(b))
}

// DefaultSubject is used when Settings.Subject is empty.
const DefaultSubject = "Abwesenheitsnotiz: {{betreff}}"

// Validate checks the settings.
func (s Settings) Validate() error {
	var errs []error
	if !s.End.IsZero() && !s.Start.IsZero() && !s.End.After(s.Start) {
		errs = append(errs, errors.New("end must be after start"))
	}
	if s.Enabled && strings.TrimSpace(s.InternalText) == "" && strings.TrimSpace(s.ExternalText) == "" {
		errs = append(errs, errors.New("a reply text is required"))
	}
	if s.OncePerDays < 0 {
		errs = append(errs, errors.New("oncePerDays must not be negative"))
	}
	if len(s.ForwardTo) > MaxForwardTo {
		errs = append(errs, fmt.Errorf("forwardTo: at most %d addresses", MaxForwardTo))
	}
	for _, a := range s.ForwardTo {
		if !validAddress(a) {
			errs = append(errs, fmt.Errorf("forwardTo: %q is not a mail address", a))
		}
	}
	for name, t := range map[string]string{"subject": s.Subject, "internalText": s.InternalText,
		"externalText": s.ExternalText, "chatText": s.ChatText} {
		if err := workflow.CheckTemplate(t); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Window returns the dedup window.
func (s Settings) Window() time.Duration {
	d := s.OncePerDays
	if d <= 0 {
		d = DefaultOncePerDays
	}
	return time.Duration(d) * 24 * time.Hour
}

// ActiveAt reports whether the absence is enabled and t is inside [Start, End).
func (s Settings) ActiveAt(t time.Time) bool {
	if !s.Enabled {
		return false
	}
	if !s.Start.IsZero() && t.Before(s.Start) {
		return false
	}
	if !s.End.IsZero() && !t.Before(s.End) {
		return false
	}
	return true
}

func (s Settings) isOwn(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	for _, o := range s.OwnAddresses {
		if strings.EqualFold(strings.TrimSpace(o), addr) {
			return true
		}
	}
	for _, o := range s.Identities {
		if strings.EqualFold(strings.TrimSpace(o), addr) {
			return true
		}
	}
	return false
}

func (s Settings) internal(addr string) bool {
	d := workflow.Domain(addr)
	for _, o := range s.OwnDomains {
		o = strings.ToLower(strings.TrimPrefix(o, "@"))
		if d != "" && (d == o || strings.HasSuffix(d, "."+o)) {
			return true
		}
	}
	return false
}

// Decision reasons.
const (
	ReasonReplied       = "replied"
	ReasonInactive      = "not active"
	ReasonOutOfScope    = "out of scope"
	ReasonOwnAddress    = "own address"
	ReasonAutomated     = "automated or bulk mail"
	ReasonAlreadySent   = "already answered within the window"
	ReasonServerHandled = "handled by the server"
	ReasonNoSender      = "no sender"
	ReasonGroupChat     = "group chat"
	ReasonChatDisabled  = "chat auto reply disabled"
)

// Decision is the stateless part of the reply decision.
type Decision struct {
	Reply    bool
	Forward  bool // forward to deputy
	Reason   string
	To       string // reply recipient
	Internal bool
	Scope    string // dedup scope
}

// Decide applies time window, scope, own-address and bulk rules to ev. It
// does not consult the sent-to log.
func (s Settings) Decide(ev events.Event, now time.Time) Decision {
	if !s.ActiveAt(now) {
		return Decision{Reason: ReasonInactive}
	}
	if c := ev.Chat; c != nil {
		switch {
		case !s.ChatAutoReply:
			return Decision{Reason: ReasonChatDisabled}
		case c.Group:
			return Decision{Reason: ReasonGroupChat}
		case c.SenderID == "" && c.Sender == "":
			return Decision{Reason: ReasonNoSender}
		}
		if len(s.ChatChannels) > 0 && !containsFold(s.ChatChannels, c.Channel) {
			return Decision{Reason: ReasonOutOfScope}
		}
		if len(s.AccountIDs) > 0 && !containsID(s.AccountIDs, c.AccountID) {
			return Decision{Reason: ReasonOutOfScope}
		}
		to := c.SenderID
		if to == "" {
			to = c.Sender
		}
		return Decision{Reply: true, Reason: ReasonReplied, To: to, Scope: "chat:" + strings.ToLower(c.Channel)}
	}
	m := ev.Mail
	if m == nil {
		return Decision{Reason: ReasonOutOfScope}
	}
	if len(s.AccountIDs) > 0 && !containsID(s.AccountIDs, m.AccountID) {
		return Decision{Reason: ReasonOutOfScope}
	}
	if len(s.Identities) > 0 {
		found := false
		for _, a := range append(append([]string{}, addrs(m.To)...), addrs(m.Cc)...) {
			found = found || containsFold(s.Identities, a)
		}
		if !found {
			return Decision{Reason: ReasonOutOfScope}
		}
	}
	to := strings.ToLower(strings.TrimSpace(m.From.Email))
	if len(m.ReplyTo) > 0 && m.ReplyTo[0].Email != "" {
		to = strings.ToLower(strings.TrimSpace(m.ReplyTo[0].Email))
	}
	if to == "" {
		return Decision{Reason: ReasonNoSender}
	}
	if s.isOwn(m.From.Email) || s.isOwn(to) {
		return Decision{Reason: ReasonOwnAddress}
	}
	if auto, _ := IsAutomated(m); auto {
		return Decision{Reason: ReasonAutomated}
	}
	return Decision{Reply: true, Forward: len(s.ForwardTo) > 0, Reason: ReasonReplied, To: to,
		Internal: s.internal(m.From.Email), Scope: "account:" + strconv.FormatInt(m.AccountID, 10)}
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func containsID(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// noReplyLocalParts are sender local parts that must never get auto replies.
var noReplyLocalParts = []string{"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "do_not_reply",
	"mailer-daemon", "postmaster", "bounce", "bounces", "listserv", "majordomo", "notifications", "notification"}

func header(m *events.Mail, name string) (string, bool) {
	for k, v := range m.Headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return strings.TrimSpace(v[0]), true
		}
	}
	return "", false
}

// IsAutomated reports whether m is a mailing-list, bulk or automatically
// generated mail that must not get an auto reply (RFC 3834 section 2), and
// why.
func IsAutomated(m *events.Mail) (bool, string) {
	if v, ok := header(m, "Auto-Submitted"); ok && !strings.EqualFold(v, "no") {
		return true, "Auto-Submitted: " + v
	}
	if v, ok := header(m, "Precedence"); ok {
		switch strings.ToLower(v) {
		case "bulk", "list", "junk":
			return true, "Precedence: " + v
		}
	}
	for _, h := range []string{"List-Id", "List-Unsubscribe", "List-Post", "X-Mailing-List", "X-Loop"} {
		if _, ok := header(m, h); ok {
			return true, h
		}
	}
	if v, ok := header(m, "X-Auto-Response-Suppress"); ok {
		l := strings.ToLower(v)
		if strings.Contains(l, "all") || strings.Contains(l, "oof") {
			return true, "X-Auto-Response-Suppress: " + v
		}
	}
	if v, ok := header(m, "Return-Path"); ok && (v == "<>" || v == "") {
		return true, "empty Return-Path"
	}
	from := strings.ToLower(m.From.Email)
	local := from
	if i := strings.LastIndexByte(from, '@'); i >= 0 {
		local = from[:i]
	}
	for _, p := range noReplyLocalParts {
		if local == p || strings.HasPrefix(local, p+"+") || strings.HasPrefix(local, p+".") || strings.HasPrefix(local, p+"-") {
			return true, "no-reply sender"
		}
	}
	return false, ""
}

func addrs(as []domain.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Email)
	}
	return out
}
