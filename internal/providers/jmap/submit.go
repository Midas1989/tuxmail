// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// ==== sending ==================================================================

// Transport sends mail through EmailSubmission/set (RFC 8621 section 7).
type Transport struct{ P *Provider }

var _ domain.Transport = Transport{}

// Send implements domain.Transport: the message is imported into the sent
// mailbox (drafts if there is none) and submitted with an explicit envelope.
func (t Transport) Send(ctx context.Context, msg domain.OutgoingMessage) error {
	p := t.P
	if len(msg.Recipients) == 0 {
		return errors.New("jmap: no recipients")
	}
	ids, err := p.Identities(ctx)
	if err != nil {
		return fmt.Errorf("jmap: identities: %w", err)
	}
	if len(ids) == 0 {
		return errors.New("jmap: the account has no sending identity")
	}
	identity := ids[0].ID
	for _, id := range ids {
		if strings.EqualFold(id.Email, msg.From.Email) {
			identity = id.ID
			break
		}
	}
	boxes, err := p.mailboxes(ctx)
	if err != nil {
		return err
	}
	target := ""
	for _, role := range []string{"sent", "drafts"} {
		for _, m := range boxes {
			if strings.EqualFold(m.Role, role) && target == "" {
				target = m.ID
			}
		}
	}
	if target == "" {
		return errors.New("jmap: neither a sent nor a drafts mailbox exists")
	}
	emailID, err := p.importEmail(ctx, bytes.NewReader(msg.Raw), []string{target}, []domain.Flag{domain.FlagSeen})
	if err != nil {
		return fmt.Errorf("jmap: import: %w", err)
	}
	acc, err := p.submissionAccount(ctx)
	if err != nil {
		return err
	}
	rcpt := make([]map[string]any, 0, len(msg.Recipients))
	for _, r := range msg.Recipients {
		rcpt = append(rcpt, map[string]any{"email": r})
	}
	var res SetResponse
	err = p.call(ctx, []string{CapCore, CapMail, CapSubmission}, "EmailSubmission/set", map[string]any{
		"accountId": acc,
		"create": map[string]any{"s": map[string]any{
			"identityId": identity, "emailId": emailID,
			"envelope": map[string]any{"mailFrom": map[string]any{"email": msg.From.Email}, "rcptTo": rcpt},
		}},
	}, &res)
	if err != nil {
		return err
	}
	if e, ok := res.NotCreated["s"]; ok {
		return fmt.Errorf("jmap: submission: %w", &e)
	}
	return nil
}

// Close implements domain.Transport.
func (t Transport) Close() error { return nil }

// ==== vacation =================================================================

// Vacation mirrors the out-of-office settings to VacationResponse (RFC 8621
// section 8); it implements vacation.ServerVacationSync.
type Vacation struct {
	P   *Provider
	Now func() time.Time
	Loc *time.Location
}

var _ vacation.ServerVacationSync = (*Vacation)(nil)

// Supported implements vacation.ServerVacationSync.
func (v *Vacation) Supported() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := v.P.c.Session(ctx, false)
	return err == nil && s.HasCapability(CapVacation) && s.AccountID(CapVacation) != ""
}

// VacationResponse is the singleton of RFC 8621 section 8.
type VacationResponse struct {
	IsEnabled bool       `json:"isEnabled"`
	FromDate  *time.Time `json:"fromDate"`
	ToDate    *time.Time `json:"toDate"`
	Subject   *string    `json:"subject"`
	TextBody  *string    `json:"textBody"`
	HTMLBody  *string    `json:"htmlBody"`
}

var vacationUsing = []string{CapCore, CapVacation}

// Get reads the current server setting.
func (v *Vacation) Get(ctx context.Context) (VacationResponse, error) {
	s, err := v.P.c.Session(ctx, false)
	if err != nil {
		return VacationResponse{}, err
	}
	var out struct {
		List []VacationResponse `json:"list"`
	}
	if err := v.P.call(ctx, vacationUsing, "VacationResponse/get", map[string]any{"accountId": s.AccountID(CapVacation), "ids": []string{"singleton"}}, &out); err != nil {
		return VacationResponse{}, err
	}
	if len(out.List) == 0 {
		return VacationResponse{}, ErrNotFound
	}
	return out.List[0], nil
}

// Push implements vacation.ServerVacationSync.
func (v *Vacation) Push(ctx context.Context, set vacation.Settings) error {
	s, err := v.P.c.Session(ctx, false)
	if err != nil {
		return err
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	subject, body := vacation.ServerTexts(set, now, v.Loc)
	patch := map[string]any{"isEnabled": set.Enabled, "fromDate": nil, "toDate": nil, "textBody": body, "htmlBody": nil, "subject": nil}
	if !set.Start.IsZero() {
		patch["fromDate"] = set.Start.UTC().Format(time.RFC3339)
	}
	if !set.End.IsZero() {
		patch["toDate"] = set.End.UTC().Format(time.RFC3339)
	}
	if subject != "" {
		patch["subject"] = subject
	}
	_, err = v.P.setOne(ctx, vacationUsing, "VacationResponse/set", map[string]any{
		"accountId": s.AccountID(CapVacation), "update": map[string]any{"singleton": patch}})
	return err
}
