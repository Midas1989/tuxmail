// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/meeting"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// MeetingInput is the body of POST /api/comfort/meeting.
type MeetingInput struct {
	Provider     string   `json:"provider,omitempty"` // jitsi | nextcloud_talk | teams ("" = settings default)
	Title        string   `json:"title"`
	Participants []string `json:"participants,omitempty"`
	Start        string   `json:"start,omitempty"` // RFC 3339
	End          string   `json:"end,omitempty"`
	Public       bool     `json:"public,omitempty"`
	Password     string   `json:"password,omitempty"`
	AccountID    int64    `json:"accountId,omitempty"`
	Lang         string   `json:"lang,omitempty"`
}

// MeetingLink is a created video conference link plus the texts the event
// dialog inserts (LOCATION, DESCRIPTION, RFC 7986 CONFERENCE).
type MeetingLink struct {
	meeting.Link
	Description string `json:"description"`
	Conference  string `json:"conference"` // CONFERENCE property line
}

// SetTeamsMeetingCreator installs the Microsoft Teams backend (Graph).
func (a *App) SetTeamsMeetingCreator(t meeting.TeamsCreator) { a.comfort.meeting.Teams = t }

// CreateMeeting creates a video conference link.
func (a *App) CreateMeeting(ctx context.Context, in MeetingInput) (MeetingLink, error) {
	s := a.comfortSettings()
	svc := *a.comfort.meeting
	svc.JitsiBaseURL = s.Meeting.JitsiBaseURL
	req := meeting.Request{Provider: in.Provider, Title: in.Title, Participants: in.Participants, Public: in.Public,
		Password: in.Password, AccountID: in.AccountID}
	if req.Provider == "" {
		req.Provider = s.Meeting.DefaultProvider
	}
	if req.Provider == meeting.ProviderNextcloud && req.AccountID == 0 {
		req.AccountID = s.Meeting.NextcloudAccountID
	}
	req.Start, _ = time.Parse(time.RFC3339, in.Start)
	req.End, _ = time.Parse(time.RFC3339, in.End)
	l, err := svc.Create(ctx, req)
	if errors.Is(err, meeting.ErrNotConfigured) {
		return MeetingLink{}, Errorf(501, CodeNotConfigured, "%v", err)
	}
	if err != nil {
		return MeetingLink{}, err
	}
	lang := in.Lang
	if lang == "" {
		lang = "de"
	}
	return MeetingLink{Link: l, Description: meeting.Description(l, lang),
		Conference: "CONFERENCE;VALUE=URI;FEATURE=AUDIO,VIDEO;LABEL=" + l.Label + ":" + l.URL}, nil
}

// ApplyMeetingToICS writes a link into an iCalendar object.
func (a *App) ApplyMeetingToICS(_ context.Context, ics string, l meeting.Link, lang string) (string, error) {
	if !strings.Contains(ics, "BEGIN:VEVENT") {
		return "", invalid("not an iCalendar event")
	}
	return meeting.ApplyToICS(ics, l, lang), nil
}

// nextcloudLogins implements meeting.NextcloudCredentials on the cloud accounts.
type nextcloudLogins struct{ a *App }

func (n nextcloudLogins) NextcloudLogin(ctx context.Context, accountID int64) (string, string, string, error) {
	a := n.a
	var acc domain.Account
	if accountID != 0 {
		var err error
		if acc, err = a.cloudAccount(ctx, accountID); err != nil {
			return "", "", "", err
		}
	} else {
		accs, err := a.st.ListAccounts(ctx)
		if err != nil {
			return "", "", "", err
		}
		for _, x := range accs {
			if x.Kind == domain.KindNextcloud && x.Enabled {
				acc = x
				break
			}
		}
	}
	if acc.Kind != domain.KindNextcloud {
		return "", "", "", Errorf(400, CodeNotConfigured, "no Nextcloud account configured")
	}
	raw, err := a.opts.Secrets.Get(acc.SecretRef)
	if err != nil {
		return "", "", "", err
	}
	var s ncSecret
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", "", "", err
	}
	return acc.Extra["server"], acc.Extra["user"], s.AppPassword, nil
}

// CreateInvoiceTask creates the task "Rechnung bezahlen bis …" for the
// e-invoice of a mail (through the workflow TaskActions).
func (a *App) CreateInvoiceTask(ctx context.Context, messageID int64) (IDResult, error) {
	ins, err := a.MessageInsights(ctx, messageID)
	if err != nil {
		return IDResult{}, err
	}
	inv := ins.Invoice
	if inv == nil {
		return IDResult{}, invalid("the mail has no structured invoice")
	}
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return IDResult{}, err
	}
	title := "Rechnung bezahlen"
	if inv.Seller != "" {
		title += ": " + inv.Seller
	}
	if inv.Amount != "" {
		title += " (" + inv.Amount + " " + inv.Currency + ")"
	}
	var notes []string
	if inv.Number != "" {
		notes = append(notes, "Rechnungsnummer: "+inv.Number)
	}
	if inv.IBAN != "" {
		notes = append(notes, "IBAN: "+inv.IBAN)
	}
	if inv.Reference != "" {
		notes = append(notes, "Verwendungszweck: "+inv.Reference)
	}
	due := inv.Due
	if due.IsZero() && !inv.Issued.IsZero() {
		due = inv.Issued.AddDate(0, 0, 14)
	}
	id, err := (&workflowTasks{a: a}).CreateTask(ctx, workflow.TaskRequest{Title: title, Notes: strings.Join(notes, "\n"), Due: due,
		Ref: workflow.ItemRef{Kind: "mail", Key: m.Key(), AccountID: m.AccountID}})
	if err != nil {
		return IDResult{}, err
	}
	return IDResult{ID: id}, nil
}
