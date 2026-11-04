// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// ==== automatic replies (out of office) ======================================

// Vacation mirrors the vacation settings to mailboxSettings/automaticRepliesSetting.
type Vacation struct{ C *Client }

var _ vacation.ServerVacationSync = Vacation{}

// Supported implements vacation.ServerVacationSync.
func (Vacation) Supported() bool { return true }

// Push implements vacation.ServerVacationSync.
func (v Vacation) Push(ctx context.Context, s vacation.Settings) error {
	ext := s.ExternalText
	if ext == "" {
		ext = s.InternalText
	}
	in := s.InternalText
	if in == "" {
		in = ext
	}
	ars := map[string]any{
		"status":               "disabled",
		"externalAudience":     "all",
		"internalReplyMessage": textToHTML(in),
		"externalReplyMessage": textToHTML(ext),
	}
	if s.Enabled {
		ars["status"] = "alwaysEnabled"
		if !s.Start.IsZero() && !s.End.IsZero() {
			ars["status"] = "scheduled"
			ars["scheduledStartDateTime"] = toGDateTime(s.Start, false)
			ars["scheduledEndDateTime"] = toGDateTime(s.End, false)
		}
	}
	return v.C.do(ctx, http.MethodPatch, v.C.user()+"/mailboxSettings", map[string]any{"automaticRepliesSetting": ars}, nil)
}

// AutoReplies is the current server setting.
type AutoReplies struct {
	Status       string    `json:"status"` // disabled | alwaysEnabled | scheduled
	Start, End   time.Time `json:"-"`
	InternalHTML string    `json:"internalReplyMessage"`
	ExternalHTML string    `json:"externalReplyMessage"`
}

// GetAutoReplies reads the automatic replies setting.
func (c *Client) GetAutoReplies(ctx context.Context) (AutoReplies, error) {
	var resp struct {
		AutomaticRepliesSetting struct {
			AutoReplies
			Start gDateTime `json:"scheduledStartDateTime"`
			End   gDateTime `json:"scheduledEndDateTime"`
		} `json:"automaticRepliesSetting"`
	}
	if err := c.get(ctx, c.user()+"/mailboxSettings?$select=automaticRepliesSetting", &resp); err != nil {
		return AutoReplies{}, err
	}
	a := resp.AutomaticRepliesSetting.AutoReplies
	a.Start, a.End = resp.AutomaticRepliesSetting.Start.time(), resp.AutomaticRepliesSetting.End.time()
	return a, nil
}

func textToHTML(s string) string {
	return "<html><body>" + strings.ReplaceAll(html.EscapeString(s), "\n", "<br>") + "</body></html>"
}

// ==== delegation (KONZEPT 7.3a) ==============================================

// CheckMailbox verifies that the signed-in user can open a shared mailbox
// and returns its display name.
func (c *Client) CheckMailbox(ctx context.Context, address string) (string, error) {
	var f gFolder
	if err := c.get(ctx, "/users/"+url.PathEscape(address)+"/mailFolders/inbox?$select=id,displayName", &f); err != nil {
		return "", fmt.Errorf("graph: mailbox %s is not accessible: %w", address, err)
	}
	var u struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.get(ctx, "/users/"+url.PathEscape(address)+"?$select=displayName", &u); err == nil && u.DisplayName != "" {
		return u.DisplayName, nil
	}
	return address, nil
}

// Me returns the signed-in user (mail address, display name).
func (c *Client) Me(ctx context.Context) (email, name string, err error) {
	var u struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
		DisplayName       string `json:"displayName"`
	}
	if err := c.get(ctx, "/me?$select=mail,userPrincipalName,displayName", &u); err != nil {
		return "", "", err
	}
	email = u.Mail
	if email == "" {
		email = u.UserPrincipalName
	}
	return email, u.DisplayName, nil
}

// CalendarPermission is a sharing entry of a calendar.
type CalendarPermission struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email"`
	// Role: freeBusyRead | limitedRead | read | write | delegateWithoutPrivateEventAccess | delegateWithPrivateEventAccess
	Role string `json:"role"`
}

// ListCalendarPermissions lists who can see a calendar ("" = default calendar).
func (c *Client) ListCalendarPermissions(ctx context.Context, calendarID string) ([]CalendarPermission, error) {
	type gp struct {
		ID           string `json:"id"`
		Role         string `json:"role"`
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	}
	ps, err := list[gp](ctx, c, c.calPath(calendarID)+"/calendarPermissions")
	if err != nil {
		return nil, err
	}
	out := make([]CalendarPermission, 0, len(ps))
	for _, p := range ps {
		out = append(out, CalendarPermission{ID: p.ID, Email: p.EmailAddress.Address, Role: p.Role})
	}
	return out, nil
}

// ShareCalendar grants a person access to a calendar (Frei/Gebucht, Details, Bearbeiten).
func (c *Client) ShareCalendar(ctx context.Context, calendarID string, p CalendarPermission) (CalendarPermission, error) {
	in := map[string]any{"emailAddress": map[string]string{"address": p.Email}, "role": p.Role, "isInsideOrganization": true}
	var out struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := c.do(ctx, http.MethodPost, c.calPath(calendarID)+"/calendarPermissions", in, &out); err != nil {
		return CalendarPermission{}, err
	}
	return CalendarPermission{ID: out.ID, Email: p.Email, Role: out.Role}, nil
}

// UnshareCalendar removes a permission.
func (c *Client) UnshareCalendar(ctx context.Context, calendarID, permissionID string) error {
	return c.do(ctx, http.MethodDelete, c.calPath(calendarID)+"/calendarPermissions/"+url.PathEscape(permissionID), nil, nil)
}

// ==== change notifications ====================================================

// Notifier manages Graph change-notification subscriptions for the server
// mode. ServeHTTP must be mounted at NotificationURL (public, unauthenticated;
// requests are matched by subscription id and clientState).
type Notifier struct {
	// NotificationURL is the public URL of the handler.
	NotificationURL string
	// Lifetime of a subscription (default 2 days; renewed before expiry).
	Lifetime time.Duration

	mu   sync.Mutex
	subs map[string]*subscription // subscription id
}

type subscription struct {
	clientState string
	ch          chan struct{}
}

// NewNotifier creates a notifier.
func NewNotifier(url string) *Notifier {
	return &Notifier{NotificationURL: url, subs: map[string]*subscription{}}
}

// Subscribe creates a subscription on resource and returns a channel that
// receives a value per notification; the subscription is renewed until ctx
// is done and then deleted.
func (n *Notifier) Subscribe(ctx context.Context, c *Client, resource string) (<-chan struct{}, error) {
	life := n.Lifetime
	if life <= 0 {
		life = 48 * time.Hour
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	var resp struct {
		ID string `json:"id"`
	}
	in := map[string]any{"changeType": "created,updated,deleted", "notificationUrl": n.NotificationURL, "resource": resource,
		"expirationDateTime": time.Now().Add(life).UTC().Format(time.RFC3339), "clientState": state}
	if err := c.do(ctx, http.MethodPost, "/subscriptions", in, &resp); err != nil {
		return nil, err
	}
	s := &subscription{clientState: state, ch: make(chan struct{}, 1)}
	n.mu.Lock()
	n.subs[resp.ID] = s
	n.mu.Unlock()
	go func() {
		t := time.NewTicker(life * 3 / 4)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				n.mu.Lock()
				delete(n.subs, resp.ID)
				n.mu.Unlock()
				close(s.ch)
				dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = c.do(dctx, http.MethodDelete, "/subscriptions/"+url.PathEscape(resp.ID), nil, nil)
				cancel()
				return
			case <-t.C:
				_ = c.do(ctx, http.MethodPatch, "/subscriptions/"+url.PathEscape(resp.ID),
					map[string]any{"expirationDateTime": time.Now().Add(life).UTC().Format(time.RFC3339)}, nil)
			}
		}
	}()
	return s.ch, nil
}

// ServeHTTP handles the validation handshake and notifications.
func (n *Notifier) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if tok := r.URL.Query().Get("validationToken"); tok != "" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, tok)
		return
	}
	var body struct {
		Value []struct {
			SubscriptionID string `json:"subscriptionId"`
			ClientState    string `json:"clientState"`
		} `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	for _, v := range body.Value {
		if s, ok := n.subs[v.SubscriptionID]; ok && s.clientState == v.ClientState {
			select {
			case s.ch <- struct{}{}:
			default:
			}
		}
	}
	n.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}
