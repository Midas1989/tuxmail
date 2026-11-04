// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/unsubscribe"
)

// Subscription is a row of the "Abonnements" overview.
type Subscription struct {
	Key          string           `json:"key"` // list id or sender address
	Sender       string           `json:"sender"`
	Name         string           `json:"name,omitempty"`
	Count        int              `json:"count"`
	Unread       int              `json:"unread"`
	InInbox      int              `json:"inInbox"`
	PerMonth     float64          `json:"perMonth"`
	FirstMail    string           `json:"firstMail"`
	LastMail     string           `json:"lastMail"`
	Unsubscribe  unsubscribe.Info `json:"unsubscribe"`
	Unsubscribed string           `json:"unsubscribed,omitempty"` // time of the unsubscription
	AutoArchive  bool             `json:"autoArchive"`
	WorkflowID   string           `json:"workflowId,omitempty"`
}

type subState struct {
	unsubscribedAt int64
}

func subscriptionKey(in store.Insight) string {
	if in.ListID != "" {
		return in.ListID
	}
	return strings.ToLower(in.Sender)
}

func (a *App) subscriptionState(ctx context.Context, key string) (subState, error) {
	st, err := a.st.GetSubscription(ctx, key)
	if err != nil {
		return subState{}, err
	}
	var s subState
	if !st.UnsubscribedAt.IsZero() {
		s.unsubscribedAt = st.UnsubscribedAt.Unix()
	}
	return s, nil
}

// ListSubscriptions returns the newsletter overview.
func (a *App) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := a.st.ListSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Subscription, 0, len(rows))
	for _, r := range rows {
		s := Subscription{Key: r.Key, Sender: r.Sender, Count: r.Count, Unread: r.Unread, InInbox: r.InboxMail,
			FirstMail: r.First.Format(time.RFC3339), LastMail: r.Last.Format(time.RFC3339)}
		months := math.Max(r.Last.Sub(r.First).Hours()/(24*30), 1)
		s.PerMonth = math.Round(float64(r.Count)/months*10) / 10
		if in, err := a.st.GetInsight(ctx, r.LatestID); err == nil {
			_ = json.Unmarshal([]byte(in.Unsubscribe), &s.Unsubscribe)
		}
		if addrs, err := a.st.MessageAddresses(ctx, r.LatestID); err == nil {
			for _, ad := range addrs {
				if ad.Role == store.RoleFrom {
					s.Name = ad.Name
				}
			}
		}
		if !r.State.UnsubscribedAt.IsZero() {
			s.Unsubscribed = r.State.UnsubscribedAt.Format(time.RFC3339)
		}
		if r.State.WorkflowID != 0 {
			s.AutoArchive, s.WorkflowID = true, fmt.Sprint(r.State.WorkflowID)
		}
		out = append(out, s)
	}
	return out, nil
}

// UnsubscribeResult is the result of an unsubscription.
type UnsubscribeResult = unsubscribe.Result

// Unsubscribe unsubscribes from a newsletter (key from ListSubscriptions).
func (a *App) Unsubscribe(ctx context.Context, key string) (UnsubscribeResult, error) {
	ids, err := a.st.SubscriptionMessages(ctx, key, "")
	if err != nil {
		return UnsubscribeResult{}, err
	}
	if len(ids) == 0 {
		return UnsubscribeResult{}, errNotFound
	}
	return a.UnsubscribeMessage(ctx, ids[0])
}

// UnsubscribeMessage unsubscribes using the List-Unsubscribe header of a mail.
func (a *App) UnsubscribeMessage(ctx context.Context, messageID int64) (UnsubscribeResult, error) {
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return UnsubscribeResult{}, err
	}
	in, err := a.st.GetInsight(ctx, messageID)
	if err != nil || in.Unsubscribe == "" {
		if _, err := a.MessageInsights(ctx, messageID); err != nil {
			return UnsubscribeResult{}, err
		}
		if in, err = a.st.GetInsight(ctx, messageID); err != nil {
			return UnsubscribeResult{}, err
		}
	}
	var info unsubscribe.Info
	if err := json.Unmarshal([]byte(in.Unsubscribe), &info); err != nil || !info.Available() {
		return UnsubscribeResult{}, invalid("the mail has no unsubscribe link")
	}
	res := a.comfort.unsub.Unsubscribe(ctx, m.AccountID, info)
	if res.Done {
		st, _ := a.st.GetSubscription(ctx, subscriptionKey(in))
		st.Key, st.UnsubscribedAt, st.Method = subscriptionKey(in), a.now(), string(res.Method)
		if err := a.st.PutSubscription(ctx, st); err != nil {
			return res, err
		}
	}
	return res, nil
}

// ArchiveSubscription moves all inbox mails of a newsletter to the archive.
func (a *App) ArchiveSubscription(ctx context.Context, key string) (int, error) {
	ids, err := a.st.SubscriptionMessages(ctx, key, string(domain.RoleInbox))
	if err != nil {
		return 0, err
	}
	byAcc := map[int64][]int64{}
	for _, id := range ids {
		if m, err := a.st.GetMessage(ctx, id); err == nil {
			byAcc[m.AccountID] = append(byAcc[m.AccountID], id)
		}
	}
	n := 0
	for acc, list := range byAcc {
		f, err := a.st.FolderByRole(ctx, acc, domain.RoleArchive)
		if err != nil {
			continue
		}
		if err := a.Move(ctx, list, f.ID); err != nil {
			return n, err
		}
		n += len(list)
	}
	return n, nil
}

// AutoArchiveSubscription creates the workflow "archive future mails of
// this newsletter automatically" and returns it.
func (a *App) AutoArchiveSubscription(ctx context.Context, key string) (Workflow, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Workflow{}, invalid("subscription key required")
	}
	cond := fmt.Sprintf(`mail.from == %q`, strings.ToLower(key))
	if !strings.Contains(key, "@") {
		cond = fmt.Sprintf(`mail.headers["list-id"] contains %q`, key)
	}
	wf, err := a.SaveWorkflow(ctx, Workflow{Name: "Newsletter archivieren: " + key, Enabled: true,
		Trigger:   WorkflowTrigger{Event: "mail.received"},
		Condition: &WorkflowCondition{Expr: cond},
		Actions:   []WorkflowAction{{Type: "archive", Params: map[string]any{}}}})
	if err != nil {
		return wf, err
	}
	st, _ := a.st.GetSubscription(ctx, key)
	st.Key = key
	var id int64
	fmt.Sscan(wf.ID, &id)
	st.WorkflowID = id
	if st.WorkflowID == 0 {
		st.WorkflowID = 1 // non-numeric workflow ids: remember that one exists
	}
	return wf, a.st.PutSubscription(ctx, st)
}

// comfortUnsubMail sends the automatic mailto: unsubscribe mail through
// the send pipeline, from the default identity of the mail's account.
type comfortUnsubMail struct{ a *App }

func (u comfortUnsubMail) SendUnsubscribeMail(ctx context.Context, accountID int64, req unsubscribe.MailtoRequest) error {
	ms, ok := u.a.opts.Sender.(*MailSender)
	if !ok {
		return Errorf(501, CodeNotSupported, "sending is not configured")
	}
	ids, err := u.a.st.ListIdentities(ctx, accountID)
	if err != nil || len(ids) == 0 {
		return invalid("no identity for account %d", accountID)
	}
	ident := ids[0]
	for _, i := range ids {
		if i.IsDefault {
			ident = i
		}
	}
	_, err = ms.Service.Send(ctx, send.Request{IdentityID: ident.ID, Source: "unsubscribe",
		PGP: send.PGPOptions{Sign: send.PGPOff, Encrypt: send.PGPOff, NoAutocrypt: true}, SMIME: send.SMIMEOptions{Sign: send.PGPOff, Encrypt: send.PGPOff},
		Draft: compose.Draft{Identity: ident, To: []domain.Address{{Email: req.To}}, Subject: req.Subject, TextBody: req.Body,
			SignatureMode: compose.SignatureNone, KeepSubject: true}})
	return err
}
