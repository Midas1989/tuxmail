// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events implements TuxMail's typed in-process event bus (KONZEPT 4.2
// principle 5 and 8.5).
//
// Producers (sync engine, messenger plugins, calendar, scheduler, UI) publish
// Events; consumers (UI push, workflow engine, search indexer) subscribe with
// a topic filter. In addition the bus carries synchronous hooks for events that
// need an answer before the producer may continue (mail.before_send).
package events

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Type is an event topic.
type Type string

// Event types. The names are part of the workflow YAML format; do not rename.
const (
	MailReceived          Type = "mail.received"
	MailSent              Type = "mail.sent"
	MailMoved             Type = "mail.moved"
	MailFlagged           Type = "mail.flagged"
	MailBeforeSend        Type = "mail.before_send" // synchronous hook, see Bus.RunHooks
	ChatReceived          Type = "chat.received"
	CalendarEventStarting Type = "calendar.event_starting"
	CalendarInviteAnswer  Type = "calendar.invite_answered"
	ContactCreated        Type = "contact.created"
	FileCreated           Type = "file.created"
	ScheduleTick          Type = "schedule.tick"
	Manual                Type = "manual"
	Webhook               Type = "webhook"
	NoReplyTimeout        Type = "noreply.timeout"
)

// AllTypes lists every known event type in a stable order.
var AllTypes = []Type{
	MailReceived, MailSent, MailMoved, MailFlagged, MailBeforeSend,
	ChatReceived, CalendarEventStarting, CalendarInviteAnswer, ContactCreated,
	FileCreated, ScheduleTick, Manual, Webhook, NoReplyTimeout,
	TagAdded, TagRemoved, ProjectItemAdded, ProjectStatusChanged, ProjectDueSoon, SearchFolderMatched,
}

// Valid reports whether t is a known event type.
func (t Type) Valid() bool {
	for _, k := range AllTypes {
		if k == t {
			return true
		}
	}
	return false
}

// IsHook reports whether events of this type are delivered synchronously
// through hooks instead of (or in addition to) asynchronous subscriptions.
func (t Type) IsHook() bool { return t == MailBeforeSend }

// Event is a single occurrence. Exactly one of the typed payload pointers is
// normally set, matching Type. Events are JSON-serializable so that the
// workflow engine can persist them in the operations queue.
type Event struct {
	ID        string    `json:"id"`
	Type      Type      `json:"type"`
	Time      time.Time `json:"time"`
	AccountID int64     `json:"accountId,omitempty"`
	// Ref is a stable key of the item the event is about (message key, chat
	// message key, event UID, file path ...). It is used for loop protection.
	Ref string `json:"ref,omitempty"`
	// Source names the producer ("sync", "ui", "scheduler", "workflow:<id>").
	Source string `json:"source,omitempty"`
	// Chain lists the workflow ids whose actions (transitively) caused this
	// event. Producers that act on behalf of a workflow must propagate it.
	Chain []string `json:"chain,omitempty"`

	Mail     *Mail             `json:"mail,omitempty"`
	Draft    *Draft            `json:"draft,omitempty"`
	Chat     *Chat             `json:"chat,omitempty"`
	Calendar *CalendarItem     `json:"calendar,omitempty"`
	Contact  *Contact          `json:"contact,omitempty"`
	File     *File             `json:"file,omitempty"`
	Schedule *Schedule         `json:"schedule,omitempty"`
	Webhook  *WebhookCall      `json:"webhook,omitempty"`
	NoReply  *NoReply          `json:"noReply,omitempty"`
	Data     map[string]string `json:"data,omitempty"` // free-form extras (manual runs, webhook query ...)

	// Payloads of the tag, project and search folder topics (organize.go).
	Tag          *TagChange          `json:"tag,omitempty"`
	Project      *ProjectChange      `json:"project,omitempty"`
	SearchFolder *SearchFolderChange `json:"searchFolder,omitempty"`
}

// NewID returns a random event id.
func NewID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Attachment describes a mail attachment (metadata only).
type Attachment struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}

// PGPStatus describes the OpenPGP state of a message.
type PGPStatus struct {
	Encrypted bool `json:"encrypted"`
	Signed    bool `json:"signed"`
	Verified  bool `json:"verified"`
}

// Mail is the payload of mail.* events.
type Mail struct {
	AccountID   int64               `json:"accountId"`
	Account     string              `json:"account,omitempty"` // display name
	Key         string              `json:"key"`               // stable message key (KONZEPT 6.2)
	MessageID   string              `json:"messageId,omitempty"`
	Folder      string              `json:"folder,omitempty"` // folder name or path
	FolderRole  string              `json:"folderRole,omitempty"`
	OldFolder   string              `json:"oldFolder,omitempty"` // mail.moved: source folder
	RemoteID    string              `json:"remoteId,omitempty"`
	From        domain.Address      `json:"from"`
	To          []domain.Address    `json:"to,omitempty"`
	Cc          []domain.Address    `json:"cc,omitempty"`
	Bcc         []domain.Address    `json:"bcc,omitempty"`
	ReplyTo     []domain.Address    `json:"replyTo,omitempty"`
	Subject     string              `json:"subject"`
	Body        string              `json:"body,omitempty"` // plain text
	Headers     map[string][]string `json:"headers,omitempty"`
	Date        time.Time           `json:"date"`
	Size        int64               `json:"size,omitempty"`
	Category    string              `json:"category,omitempty"`
	Flags       []string            `json:"flags,omitempty"`
	Labels      []string            `json:"labels,omitempty"`
	Attachments []Attachment        `json:"attachments,omitempty"`
	IsInvoice   bool                `json:"isInvoice,omitempty"`
	PGP         PGPStatus           `json:"pgp"`
}

// Identity is the sender identity of a draft.
type Identity struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
	// Business marks the identity as a business identity. If unset, send
	// checks derive it from the configured own domains.
	Business *bool `json:"business,omitempty"`
}

// Draft is the payload of mail.before_send (and optionally mail.sent): an
// outgoing message before it is rendered.
type Draft struct {
	Key         string           `json:"key"` // draft key in the composer store
	AccountID   int64            `json:"accountId"`
	Identity    Identity         `json:"identity"`
	To          []domain.Address `json:"to,omitempty"`
	Cc          []domain.Address `json:"cc,omitempty"`
	Bcc         []domain.Address `json:"bcc,omitempty"`
	Subject     string           `json:"subject"`
	Body        string           `json:"body"` // plain text of the new content incl. quote
	Attachments []Attachment     `json:"attachments,omitempty"`
	Encrypt     bool             `json:"encrypt"`
	Sign        bool             `json:"sign"`
	// ReplyAll is true when the draft was created with "reply all".
	ReplyAll bool `json:"replyAll,omitempty"`
	// InReplyTo is the message key of the message being answered, if any.
	InReplyTo string `json:"inReplyTo,omitempty"`
	// InReplyToDate is the date of the newest message of the conversation
	// being answered (zero if not a reply).
	InReplyToDate time.Time `json:"inReplyToDate,omitempty"`
	// SendAt is the scheduled send time; zero means "now".
	SendAt time.Time `json:"sendAt,omitempty"`
}

// Recipients returns To, Cc and Bcc in order.
func (d *Draft) Recipients() []domain.Address {
	out := make([]domain.Address, 0, len(d.To)+len(d.Cc)+len(d.Bcc))
	out = append(out, d.To...)
	out = append(out, d.Cc...)
	return append(out, d.Bcc...)
}

// Chat is the payload of chat.received.
type Chat struct {
	AccountID int64  `json:"accountId"`
	Key       string `json:"key"`
	Channel   string `json:"channel"` // whatsapp, signal, telegram, matrix ...
	ChatID    string `json:"chatId"`
	ChatTitle string `json:"chatTitle,omitempty"`
	Group     bool   `json:"group,omitempty"`
	Sender    string `json:"sender"` // display name or handle
	SenderID  string `json:"senderId,omitempty"`
	Text      string `json:"text"`
	HasMedia  bool   `json:"hasMedia,omitempty"`
}

// CalendarItem is the payload of calendar.* events.
type CalendarItem struct {
	CalendarID int64     `json:"calendarId"`
	UID        string    `json:"uid"`
	Summary    string    `json:"summary"`
	Location   string    `json:"location,omitempty"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Organizer  string    `json:"organizer,omitempty"`
	Attendees  []string  `json:"attendees,omitempty"`
	// Answer is the participation status for calendar.invite_answered
	// (accepted, declined, tentative).
	Answer string `json:"answer,omitempty"`
	// Responder is the attendee who answered.
	Responder string `json:"responder,omitempty"`
	// MinutesBefore is set for calendar.event_starting.
	MinutesBefore int `json:"minutesBefore,omitempty"`
}

// Contact is the payload of contact.created.
type Contact struct {
	ID          int64    `json:"id"`
	DisplayName string   `json:"displayName"`
	Emails      []string `json:"emails,omitempty"`
	Phones      []string `json:"phones,omitempty"`
	Groups      []string `json:"groups,omitempty"`
}

// File is the payload of file.created.
type File struct {
	AccountID int64  `json:"accountId"`
	Provider  string `json:"provider"` // nextcloud, opencloud, webdav, local
	Path      string `json:"path"`
	Name      string `json:"name"`
	MIME      string `json:"mime,omitempty"`
	Size      int64  `json:"size"`
}

// Schedule is the payload of schedule.tick.
type Schedule struct {
	Name string `json:"name"` // schedule name, matched by workflow triggers
}

// WebhookCall is the payload of webhook events (server mode only).
type WebhookCall struct {
	Name    string            `json:"name"`
	Body    string            `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// NoReply is the payload of noreply.timeout (KONZEPT 8.12).
type NoReply struct {
	FollowupID     int64     `json:"followupId"`
	AccountID      int64     `json:"accountId"`
	MessageKey     string    `json:"messageKey"`
	ConversationID int64     `json:"conversationId,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	Recipients     []string  `json:"recipients,omitempty"`
	SentAt         time.Time `json:"sentAt"`
	DueAt          time.Time `json:"dueAt"`
}
