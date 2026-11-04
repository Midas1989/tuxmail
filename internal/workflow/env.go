// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
)

// Env is the typed environment conditions are evaluated against. Field names
// in expressions are the `expr` tags, e.g. mail.subject, mail.attachments,
// chat.sender, time.hour, vars.x. All nested structs are always present
// (zero-valued when the event has no such payload), so expressions never fail
// on missing payloads.
type Env struct {
	Event    string            `expr:"event"`
	Source   string            `expr:"source"`
	Ref      string            `expr:"ref"`
	Mail     MailEnv           `expr:"mail"`
	Chat     ChatEnv           `expr:"chat"`
	Calendar CalendarEnv       `expr:"calendar"`
	Contact  ContactEnv        `expr:"contact"`
	File     FileEnv           `expr:"file"`
	Schedule string            `expr:"schedule"`
	Webhook  WebhookEnv        `expr:"webhook"`
	NoReply  NoReplyEnv        `expr:"noreply"`
	Data     map[string]string `expr:"data"`
	Vars     map[string]string `expr:"vars"`
	Time     TimeEnv           `expr:"time"`

	// Tags, projects and search folders (organize.go).
	Item         ItemEnv         `expr:"item"`
	Tag          TagEnv          `expr:"tag"`
	Project      ProjectEnv      `expr:"project"`
	SearchFolder SearchFolderEnv `expr:"searchfolder"`

	rt     *envRuntime // backends for query conditions (querycond.go)
	budget *evalBudget // sandbox limits of the running evaluation (sandbox.go)
}

// MailEnv describes a mail (or, for mail.before_send, the draft).
type MailEnv struct {
	Key             string            `expr:"key"`
	MessageID       string            `expr:"messageId"`
	From            string            `expr:"from"` // lower-case address
	FromName        string            `expr:"fromName"`
	FromDomain      string            `expr:"fromDomain"`
	To              []string          `expr:"to"` // lower-case addresses
	Cc              []string          `expr:"cc"`
	Bcc             []string          `expr:"bcc"`
	Recipients      []string          `expr:"recipients"` // To+Cc+Bcc
	Subject         string            `expr:"subject"`
	Body            string            `expr:"body"`
	Headers         map[string]string `expr:"headers"` // lower-case names, first value
	Account         string            `expr:"account"`
	AccountID       int64             `expr:"accountId"`
	Folder          string            `expr:"folder"`
	OldFolder       string            `expr:"oldFolder"`
	Category        string            `expr:"category"`
	Flags           []string          `expr:"flags"`
	Labels          []string          `expr:"labels"`
	Size            int64             `expr:"size"`
	Date            time.Time         `expr:"date"`
	Attachments     []AttachmentEnv   `expr:"attachments"`
	AttachmentNames []string          `expr:"attachmentNames"`
	HasAttachments  bool              `expr:"hasAttachments"`
	MaxAttachment   int64             `expr:"maxAttachmentSize"`
	IsInvoice       bool              `expr:"isInvoice"`
	PGP             PGPEnv            `expr:"pgp"`
	ReplyAll        bool              `expr:"replyAll"` // drafts only
	// Comfort analysis: spam score 0..100, spam decision, e-invoice data.
	SpamScore float64    `expr:"spamScore"`
	IsSpam    bool       `expr:"isSpam"`
	Invoice   InvoiceEnv `expr:"invoice"`
}

// InvoiceEnv is a structured e-invoice in conditions (mail.invoice.*);
// Present is false when the mail has none.
type InvoiceEnv struct {
	Present  bool      `expr:"present"`
	Format   string    `expr:"format"`
	Number   string    `expr:"number"`
	Issued   time.Time `expr:"issued"`
	Due      time.Time `expr:"due"`
	Amount   float64   `expr:"amount"`
	Currency string    `expr:"currency"`
	Seller   string    `expr:"seller"`
	IBAN     string    `expr:"iban"`
}

// AttachmentEnv is an attachment in conditions.
type AttachmentEnv struct {
	Name string `expr:"name"`
	MIME string `expr:"mime"`
	Size int64  `expr:"size"`
}

// PGPEnv is the OpenPGP status in conditions.
type PGPEnv struct {
	Encrypted bool `expr:"encrypted"`
	Signed    bool `expr:"signed"`
	Verified  bool `expr:"verified"`
}

// ChatEnv describes a messenger message.
type ChatEnv struct {
	Channel  string `expr:"channel"`
	Chat     string `expr:"chat"` // chat title
	ChatID   string `expr:"chatId"`
	Group    bool   `expr:"group"`
	Sender   string `expr:"sender"`
	SenderID string `expr:"senderId"`
	Text     string `expr:"text"`
	HasMedia bool   `expr:"hasMedia"`
}

// CalendarEnv describes a calendar event.
type CalendarEnv struct {
	UID           string    `expr:"uid"`
	Summary       string    `expr:"summary"`
	Location      string    `expr:"location"`
	Start         time.Time `expr:"start"`
	End           time.Time `expr:"end"`
	Organizer     string    `expr:"organizer"`
	Attendees     []string  `expr:"attendees"`
	Answer        string    `expr:"answer"`
	Responder     string    `expr:"responder"`
	MinutesBefore int       `expr:"minutesBefore"`
}

// ContactEnv describes a contact.
type ContactEnv struct {
	DisplayName string   `expr:"displayName"`
	Emails      []string `expr:"emails"`
	Phones      []string `expr:"phones"`
	Groups      []string `expr:"groups"`
}

// FileEnv describes a cloud file.
type FileEnv struct {
	Provider string `expr:"provider"`
	Path     string `expr:"path"`
	Name     string `expr:"name"`
	MIME     string `expr:"mime"`
	Size     int64  `expr:"size"`
}

// WebhookEnv describes an incoming webhook call.
type WebhookEnv struct {
	Name    string            `expr:"name"`
	Body    string            `expr:"body"`
	Headers map[string]string `expr:"headers"`
}

// NoReplyEnv describes a follow-up timeout.
type NoReplyEnv struct {
	MessageKey string    `expr:"messageKey"`
	Subject    string    `expr:"subject"`
	Recipients []string  `expr:"recipients"`
	SentAt     time.Time `expr:"sentAt"`
	DueAt      time.Time `expr:"dueAt"`
	DaysWaited int       `expr:"daysWaited"`
}

// TimeEnv contains time helpers for conditions (in the engine's time zone).
type TimeEnv struct {
	Now     time.Time `expr:"now"`
	Hour    int       `expr:"hour"`
	Minute  int       `expr:"minute"`
	Clock   string    `expr:"clock"`   // "15:04", compare with >= "08:00"
	Weekday int       `expr:"weekday"` // 1 = Monday ... 7 = Sunday
	Weekend bool      `expr:"weekend"`
	Workday bool      `expr:"workday"`
	Date    string    `expr:"date"` // "2006-01-02"
}

// NewTimeEnv builds the time helpers for t.
func NewTimeEnv(t time.Time) TimeEnv {
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	return TimeEnv{
		Now: t, Hour: t.Hour(), Minute: t.Minute(), Clock: t.Format("15:04"),
		Weekday: wd, Weekend: wd >= 6, Workday: wd <= 5, Date: t.Format("2006-01-02"),
	}
}

// NewEnv builds the condition environment for an event. now is the
// evaluation time in the desired time zone; vars may be nil.
func NewEnv(ev events.Event, now time.Time, vars map[string]string) *Env {
	e := &Env{
		Event: string(ev.Type), Source: ev.Source, Ref: ev.Ref,
		Data: nonNilMap(ev.Data), Vars: nonNilMap(vars), Time: NewTimeEnv(now),
	}
	e.Mail.Headers = map[string]string{}
	e.Webhook.Headers = map[string]string{}
	if m := ev.Mail; m != nil {
		e.Mail = mailEnv(m)
	}
	if d := ev.Draft; d != nil {
		e.Mail = draftEnv(d)
	}
	if c := ev.Chat; c != nil {
		e.Chat = ChatEnv{Channel: c.Channel, Chat: c.ChatTitle, ChatID: c.ChatID, Group: c.Group,
			Sender: c.Sender, SenderID: c.SenderID, Text: c.Text, HasMedia: c.HasMedia}
	}
	if c := ev.Calendar; c != nil {
		e.Calendar = CalendarEnv{UID: c.UID, Summary: c.Summary, Location: c.Location, Start: c.Start, End: c.End,
			Organizer: strings.ToLower(c.Organizer), Attendees: lowerAll(c.Attendees), Answer: c.Answer,
			Responder: strings.ToLower(c.Responder), MinutesBefore: c.MinutesBefore}
	}
	if c := ev.Contact; c != nil {
		e.Contact = ContactEnv{DisplayName: c.DisplayName, Emails: lowerAll(c.Emails), Phones: c.Phones, Groups: c.Groups}
	}
	if f := ev.File; f != nil {
		e.File = FileEnv{Provider: f.Provider, Path: f.Path, Name: f.Name, MIME: f.MIME, Size: f.Size}
	}
	if s := ev.Schedule; s != nil {
		e.Schedule = s.Name
	}
	if w := ev.Webhook; w != nil {
		e.Webhook = WebhookEnv{Name: w.Name, Body: w.Body, Headers: map[string]string{}}
		for k, v := range w.Headers {
			e.Webhook.Headers[strings.ToLower(k)] = v
		}
	}
	if n := ev.NoReply; n != nil {
		e.NoReply = NoReplyEnv{MessageKey: n.MessageKey, Subject: n.Subject, Recipients: lowerAll(n.Recipients),
			SentAt: n.SentAt, DueAt: n.DueAt}
		if !n.SentAt.IsZero() {
			e.NoReply.DaysWaited = int(now.Sub(n.SentAt).Hours() / 24)
		}
		if ev.Mail == nil {
			e.Mail.Key, e.Mail.Subject = n.MessageKey, n.Subject
			e.Mail.To, e.Mail.Recipients = e.NoReply.Recipients, e.NoReply.Recipients
			e.Mail.AccountID = n.AccountID
		}
	}
	fillOrganizeEnv(e, ev, now)
	nonNilSlices(&e.Mail)
	return e
}

func mailEnv(m *events.Mail) MailEnv {
	me := MailEnv{
		Key: m.Key, MessageID: m.MessageID, From: strings.ToLower(m.From.Email), FromName: m.From.Name,
		FromDomain: Domain(m.From.Email), To: emails(m.To), Cc: emails(m.Cc), Bcc: emails(m.Bcc),
		Subject: m.Subject, Body: m.Body, Headers: map[string]string{}, Account: m.Account, AccountID: m.AccountID,
		Folder: m.Folder, OldFolder: m.OldFolder, Category: m.Category, Flags: m.Flags, Labels: m.Labels,
		Size: m.Size, Date: m.Date, IsInvoice: m.IsInvoice,
		PGP:       PGPEnv{Encrypted: m.PGP.Encrypted, Signed: m.PGP.Signed, Verified: m.PGP.Verified},
		SpamScore: m.SpamScore, IsSpam: m.IsSpam,
	}
	if inv := m.Invoice; inv != nil {
		me.IsInvoice = true
		me.Invoice = InvoiceEnv{Present: true, Format: inv.Format, Number: inv.Number, Issued: inv.Issued, Due: inv.Due,
			Amount: inv.Amount, Currency: inv.Currency, Seller: inv.Seller, IBAN: inv.IBAN}
	}
	for k, v := range m.Headers {
		if len(v) > 0 {
			me.Headers[strings.ToLower(k)] = v[0]
		}
	}
	me.Recipients = append(append(append([]string{}, me.To...), me.Cc...), me.Bcc...)
	setAttachments(&me, m.Attachments)
	return me
}

func draftEnv(d *events.Draft) MailEnv {
	me := MailEnv{
		Key: d.Key, From: strings.ToLower(d.Identity.Email), FromName: d.Identity.Name,
		FromDomain: Domain(d.Identity.Email), To: emails(d.To), Cc: emails(d.Cc), Bcc: emails(d.Bcc),
		Subject: d.Subject, Body: d.Body, Headers: map[string]string{}, AccountID: d.AccountID,
		PGP: PGPEnv{Encrypted: d.Encrypt, Signed: d.Sign}, ReplyAll: d.ReplyAll,
	}
	me.Recipients = append(append(append([]string{}, me.To...), me.Cc...), me.Bcc...)
	setAttachments(&me, d.Attachments)
	for _, a := range d.Attachments {
		me.Size += a.Size
	}
	return me
}

func setAttachments(me *MailEnv, as []events.Attachment) {
	for _, a := range as {
		me.Attachments = append(me.Attachments, AttachmentEnv{Name: a.Name, MIME: strings.ToLower(a.MIME), Size: a.Size})
		me.AttachmentNames = append(me.AttachmentNames, a.Name)
		if a.Size > me.MaxAttachment {
			me.MaxAttachment = a.Size
		}
	}
	me.HasAttachments = len(as) > 0
}

func nonNilSlices(m *MailEnv) {
	for _, p := range []*[]string{&m.To, &m.Cc, &m.Bcc, &m.Recipients, &m.Flags, &m.Labels, &m.AttachmentNames} {
		if *p == nil {
			*p = []string{}
		}
	}
	if m.Attachments == nil {
		m.Attachments = []AttachmentEnv{}
	}
}

func emails(as []domain.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, strings.ToLower(strings.TrimSpace(a.Email)))
	}
	return out
}

func lowerAll(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = strings.ToLower(v)
	}
	return out
}

func nonNilMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Domain returns the lower-case domain part of an address ("" if none).
func Domain(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return strings.ToLower(strings.TrimSuffix(addr[i+1:], ">"))
	}
	return ""
}
