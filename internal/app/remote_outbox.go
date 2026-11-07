// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Midas1989/Mailclient/internal/store"
)

// The outbox of the server mode: mails written and marks set without a
// connection wait in the store and are replayed in order on reconnecting.
// A mail keeps its id as Idempotency-Key for all attempts, and the server
// keeps the result of a key, so a lost answer cannot send it twice.

type queueKind int

const (
	queueNo queueKind = iota
	queueSend
	queueAction
)

var (
	reConvRead = regexp.MustCompile(`^/api/conversations/(\d+)/read$`)
)

func queueable(method, path string) queueKind {
	if method != http.MethodPost {
		return queueNo
	}
	switch {
	case path == "/api/send":
		return queueSend
	case path == "/api/messages/flags", reConvRead.MatchString(path):
		return queueAction
	}
	return queueNo
}

const (
	maxOfflineUpload  = 25 << 20
	maxOfflineUploads = 100 << 20
	offlineTokenPref  = "offline_"
	// A server error (not network) is retried this often before the user is asked.
	maxOutboxAttempts = 10
)

// OutboxEntry is shown in the status (no message content beyond subject and recipients).
type OutboxEntry struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title,omitempty"`
	Recipients string `json:"recipients,omitempty"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	// Findings: why the send checks stopped it (state "blocked").
	Findings  []SendFinding `json:"findings,omitempty"`
	CreatedAt string        `json:"createdAt"`
}

func (a *App) outboxEntries(ctx context.Context) ([]OutboxEntry, error) {
	items, err := a.st.ListRemoteOutbox(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OutboxEntry, 0, len(items))
	for _, it := range items {
		e := OutboxEntry{ID: it.ID, Kind: it.Kind, Title: it.Title, Recipients: it.Recipients, State: it.State, Error: it.LastError, CreatedAt: rfc3339(it.CreatedAt)}
		if it.State == store.OutboxBlocked {
			e.Error = ""
			if err := json.Unmarshal([]byte(it.LastError), &e.Findings); err != nil {
				a.log.Warn("app: reading the send check result failed", "err", err)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// enqueue notes an action; id is the key of a first attempt whose answer got lost.
func (a *App) enqueue(ctx context.Context, method string, u *url.URL, body []byte, q queueKind, id string) (*connResult, error) {
	it := store.RemoteOutboxItem{Method: method, Path: u.RequestURI(), Body: body, CreatedAt: a.now(), Kind: "action", ID: id}
	if it.ID == "" {
		it.ID = newID("out_")
	}
	if q == queueSend {
		it.Kind = "send"
		var m OutgoingMessage
		if err := json.Unmarshal(body, &m); err == nil {
			it.Title = m.Subject
			var to []string
			for _, ad := range slices.Concat(m.To, m.Cc, m.Bcc) {
				to = append(to, ad.Email)
			}
			it.Recipients = strings.Join(to, ", ")
			if it.Title == "" {
				it.Title = truncateRunes(m.Text, 60)
			}
		}
	}
	if err := a.st.AddRemoteOutbox(ctx, it); err != nil {
		return nil, err
	}
	a.connNotify()
	if q == queueSend {
		return jsonResult(http.StatusOK, map[string]any{"queued": true, "offline": true, "outboxId": it.ID}, true), nil
	}
	return &connResult{status: http.StatusNoContent, header: http.Header{headerOffline: {"1"}}}, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// offlineUpload keeps an attachment of a mail written offline.
func (a *App) offlineUpload(ctx context.Context, hdr http.Header, body []byte) (*connResult, error) {
	name, err := url.QueryUnescape(hdr.Get("X-Filename"))
	if err != nil || name == "" {
		return jsonResult(http.StatusBadRequest, map[string]string{"error": "bad_request", "message": "X-Filename header required"}, true), nil
	}
	used, err := a.st.RemoteUploadsSize(ctx)
	if err != nil {
		return nil, err
	}
	if len(body) > maxOfflineUpload || used+int64(len(body)) > maxOfflineUploads {
		return jsonResult(http.StatusRequestEntityTooLarge, map[string]string{"error": "offline_too_large", "message": "this attachment is too large to keep until the connection is back"}, true), nil
	}
	token := newID(offlineTokenPref)
	if err := a.st.PutRemoteUpload(ctx, store.RemoteUpload{Token: token, Filename: name, MIMEType: hdr.Get("Content-Type"), Data: body}, a.now()); err != nil {
		return nil, err
	}
	return jsonResult(http.StatusCreated, UploadResult{UploadToken: token}, true), nil
}

// outboxUploads returns the local upload tokens of a queued mail.
func outboxUploads(body []byte) []string {
	var m OutgoingMessage
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	var out []string
	for _, at := range m.Attachments {
		if strings.HasPrefix(at.UploadToken, offlineTokenPref) {
			out = append(out, at.UploadToken)
		}
	}
	return out
}

// replaceUploads uploads local attachments and stores the rewritten mail, so
// that retries repeat exactly the same request (idempotency compares bodies).
func (a *App) replaceUploads(ctx context.Context, c *serverConnection, it *store.RemoteOutboxItem) (uploaded []string, status int, err error) {
	tokens := outboxUploads(it.Body)
	if len(tokens) == 0 {
		return nil, 0, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(it.Body, &doc); err != nil {
		return nil, 0, err
	}
	var atts []map[string]any
	if err := json.Unmarshal(doc["attachments"], &atts); err != nil {
		return nil, 0, err
	}
	for i, at := range atts {
		tok := jsonStr(at["uploadToken"])
		if !strings.HasPrefix(tok, offlineTokenPref) {
			continue
		}
		up, err := a.st.GetRemoteUpload(ctx, tok)
		if err != nil {
			return nil, 0, err
		}
		h := http.Header{"X-Filename": {url.QueryEscape(up.Filename)}}
		if up.MIMEType != "" {
			h.Set("Content-Type", up.MIMEType)
		}
		res, err := a.connDo(ctx, c.URL, http.MethodPost, "/api/uploads", h, up.Data, a.cr.timing.WriteTimeout*5)
		if err != nil {
			return nil, 0, err
		}
		if res.Status/100 != 2 {
			return nil, res.Status, nil
		}
		var ur UploadResult
		if err := json.Unmarshal(res.Body, &ur); err != nil {
			return nil, 0, err
		}
		atts[i]["uploadToken"] = ur.UploadToken
		uploaded = append(uploaded, tok)
	}
	if doc["attachments"], err = json.Marshal(atts); err != nil {
		return nil, 0, err
	}
	if it.Body, err = json.Marshal(doc); err != nil {
		return nil, 0, err
	}
	if err := a.st.SetRemoteOutboxBody(ctx, it.ID, it.Body); err != nil {
		return nil, 0, err
	}
	return uploaded, 0, nil
}

// flushOutbox replays waiting actions, in order. Only one replay runs at a time.
func (a *App) flushOutbox(ctx context.Context) {
	if !a.cr.flushMu.TryLock() {
		return
	}
	defer a.cr.flushMu.Unlock()
	c := a.connCurrent()
	if c == nil || c.Mode != ConnModeThin {
		return
	}
	items, err := a.st.ListRemoteOutbox(ctx)
	if err != nil {
		a.log.Warn("app: reading the outbox failed", "err", err)
		return
	}
	changed := false
	// Attachments of mails that were never finished do not stay for ever.
	var inUse []string
	for i := range items {
		inUse = append(inUse, outboxUploads(items[i].Body)...)
	}
	if err := a.st.PurgeRemoteUploads(ctx, a.now().AddDate(0, 0, -30), inUse); err != nil {
		a.log.Warn("app: cleaning up kept attachments failed", "err", err)
	}
	defer func() {
		if changed {
			a.connNotify()
			// The lists on screen are the server's again: reload them.
			a.emitRemote(Event{Type: EventAccountUpdated})
			a.emitRemote(Event{Type: EventConnection, State: ConnStateOnline})
		}
	}()
	for i := range items {
		it := &items[i]
		if it.State != store.OutboxPending {
			continue
		}
		if !a.flushOne(ctx, c, it) {
			return
		}
		changed = true
	}
}

// flushOne replays one action. It returns false when the replay has to stop.
func (a *App) flushOne(ctx context.Context, c *serverConnection, it *store.RemoteOutboxItem) bool {
	keep := func(state, msg string) {
		if err := a.st.SetRemoteOutboxState(ctx, it.ID, state, msg); err != nil {
			a.log.Warn("app: updating the outbox failed", "err", err)
		}
	}
	// However often it fails: a mail is not repeated without end.
	if it.Kind == "send" && it.Attempts >= maxOutboxAttempts {
		keep(store.OutboxInDoubt, "too many attempts")
		return true
	}
	netFail := func(err error) bool {
		if errors.Is(err, errConnNetwork) || errors.Is(err, errConnLocked) {
			if errors.Is(err, errConnNetwork) {
				keep(store.OutboxPending, "")
			}
			a.connMarkOffline(err)
		} else {
			a.log.Warn("app: replaying an action failed", "kind", it.Kind, "err", err)
			keep(store.OutboxFailed, "internal error")
		}
		return false
	}
	var uploaded []string
	h := http.Header{"Accept": {"application/json"}, "Content-Type": {"application/json"}}
	if it.Kind == "send" {
		h.Set("Idempotency-Key", it.ID)
		var status int
		var err error
		uploaded, status, err = a.replaceUploads(ctx, c, it)
		if err != nil {
			return netFail(err)
		}
		if status != 0 {
			if status == http.StatusUnauthorized {
				a.connMarkExpired(ctx)
				return false
			}
			if connGatewayDown(status) {
				a.connMarkOffline(errConnNetwork)
				return false
			}
			keep(store.OutboxFailed, fmt.Sprintf("attachment refused (%d)", status))
			return true
		}
	}
	res, err := a.connDo(ctx, c.URL, it.Method, it.Path, h, it.Body, a.cr.timing.WriteTimeout)
	if err != nil {
		return netFail(err)
	}
	switch {
	case connGatewayDown(res.Status) && !isServerAnswer(res):
		// From a proxy in front of the server: the key answers the next try.
		keep(store.OutboxPending, "")
		a.connMarkOffline(errConnNetwork)
		return false
	case connIsTokenRejected(res):
		a.connMarkExpired(ctx)
		return false
	case res.Status/100 == 2:
		if it.Kind == "send" {
			var r SendResult
			if json.Unmarshal(res.Body, &r) == nil && r.Blocked {
				// The send checks want a decision: nothing was sent. The mail
				// stays, with the reason, for the user to look at.
				js, err := json.Marshal(r.Findings)
				if err != nil {
					js = []byte("[]")
				}
				keep(store.OutboxBlocked, string(js))
				return true
			}
		}
		if err := a.st.DeleteRemoteOutbox(ctx, it.ID, slices.Concat(uploaded, outboxUploads(it.Body))); err != nil {
			a.log.Warn("app: updating the outbox failed", "err", err)
		}
		a.connSeen()
		return true
	case res.Status == http.StatusConflict && isIdemConflict(res.Body):
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(res.Body, &e)
		if e.Error == "idempotency_in_doubt" {
			keep(store.OutboxInDoubt, "the server may have sent this mail already")
			return true
		}
		return false // still running at the server: next round
	case res.Status >= 500 && it.Kind == "send":
		// The server failed, possibly after the mail relay took the mail.
		// Trying again could deliver it twice: the user decides.
		a.log.Warn("app: the server failed on a replayed mail", "status", res.Status)
		keep(store.OutboxInDoubt, fmt.Sprintf("server error (%d)", res.Status))
		return true
	case res.Status >= 500:
		a.log.Warn("app: the server refused a replayed action", "status", res.Status)
		if it.Attempts+1 >= maxOutboxAttempts {
			keep(store.OutboxFailed, fmt.Sprintf("server error (%d)", res.Status))
			return true
		}
		keep(store.OutboxPending, fmt.Sprintf("server error (%d)", res.Status))
		return false
	case it.Kind == "action":
		// The mail is gone or the mark no longer applies: nothing to ask the user.
		a.log.Info("app: dropping a noted action the server refused", "status", res.Status)
		if err := a.st.DeleteRemoteOutbox(ctx, it.ID, nil); err != nil {
			a.log.Warn("app: updating the outbox failed", "err", err)
		}
		return true
	default:
		msg := fmt.Sprintf("refused by the server (%d)", res.Status)
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(res.Body, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		keep(store.OutboxFailed, msg)
		return true
	}
}

// isServerAnswer tells an error of the server itself from one of a proxy in front of it.
func isServerAnswer(res *connResponse) bool {
	var e struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(res.Body, &e) == nil && e.Error != ""
}

// ServerOutboxMessage returns a waiting mail, to continue it as a draft.
func (a *App) ServerOutboxMessage(ctx context.Context, id string) (OutgoingMessage, error) {
	items, err := a.st.ListRemoteOutbox(ctx)
	if err != nil {
		return OutgoingMessage{}, err
	}
	for _, it := range items {
		if it.ID == id && it.Kind == "send" {
			var m OutgoingMessage
			if err := json.Unmarshal(it.Body, &m); err != nil {
				return OutgoingMessage{}, err
			}
			return m, nil
		}
	}
	return OutgoingMessage{}, errNotFound
}

// ServerOutboxDiscard removes a waiting action.
func (a *App) ServerOutboxDiscard(ctx context.Context, id string) error {
	items, err := a.st.ListRemoteOutbox(ctx)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.ID == id {
			if err := a.st.DeleteRemoteOutbox(ctx, id, outboxUploads(it.Body)); err != nil {
				return err
			}
			a.connNotify()
			return nil
		}
	}
	return errNotFound
}

// ServerOutboxRetry tries an action again that needed a decision. A mail in
// doubt gets a new key: the user decided to risk sending it a second time.
// A mail stopped by the send checks goes with those warnings confirmed, as if
// the user pressed "send anyway" in the composer.
func (a *App) ServerOutboxRetry(ctx context.Context, id string) error {
	items, err := a.st.ListRemoteOutbox(ctx)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.ID != id || it.State != store.OutboxBlocked {
			continue
		}
		var findings []SendFinding
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(it.LastError), &findings); err != nil {
			return err
		}
		if err := json.Unmarshal(it.Body, &doc); err != nil {
			return err
		}
		checks := []string{}
		for _, f := range findings {
			checks = append(checks, f.Check)
		}
		if doc["confirmChecks"], err = json.Marshal(checks); err != nil {
			return err
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if err := a.st.SetRemoteOutboxBody(ctx, id, body); err != nil {
			return err
		}
	}
	if err := a.st.RequeueRemoteOutbox(ctx, id, newID("out_")); err != nil {
		return err
	}
	a.connNotify()
	a.connKick()
	return nil
}

// overlayPending applies noted marks to a kept answer, so that a mail marked
// as read offline stays read on screen.
func (a *App) overlayPending(ctx context.Context, u *url.URL, body []byte) []byte {
	isMsgs := u.Path == "/api/messages" || reMessageDetail.MatchString(u.Path)
	isConvs := u.Path == "/api/conversations"
	if !isMsgs && !isConvs {
		return body
	}
	items, err := a.st.ListRemoteOutbox(ctx)
	if err != nil || len(items) == 0 {
		return body
	}
	type change struct{ add, remove []string }
	flags := map[int64]*change{}
	readConvs := map[int64]bool{}
	for _, it := range items {
		if it.Kind != "action" || it.State != store.OutboxPending {
			continue
		}
		if it.Path == "/api/messages/flags" {
			var in struct {
				IDs    []int64  `json:"ids"`
				Add    []string `json:"add"`
				Remove []string `json:"remove"`
			}
			if json.Unmarshal(it.Body, &in) != nil {
				continue
			}
			for _, id := range in.IDs {
				ch := flags[id]
				if ch == nil {
					ch = &change{}
					flags[id] = ch
				}
				ch.add = slices.DeleteFunc(append(ch.add, in.Add...), func(f string) bool { return slices.Contains(in.Remove, f) })
				ch.remove = slices.DeleteFunc(append(ch.remove, in.Remove...), func(f string) bool { return slices.Contains(in.Add, f) })
			}
		} else if m := reConvRead.FindStringSubmatch(it.Path); m != nil {
			var id int64
			if _, err := fmt.Sscan(m[1], &id); err == nil {
				readConvs[id] = true
			}
		}
	}
	if len(flags) == 0 && len(readConvs) == 0 {
		return body
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return body
	}
	apply := func(o map[string]any) {
		if isConvs {
			if readConvs[jsonInt(o["id"])] {
				o["unreadCount"] = 0
				o["markedUnread"] = false
			}
			return
		}
		ch := flags[jsonInt(o["id"])]
		if ch == nil {
			return
		}
		cur := []string{}
		if l, ok := o["flags"].([]any); ok {
			for _, f := range l {
				if s, ok := f.(string); ok && !slices.Contains(ch.remove, s) {
					cur = append(cur, s)
				}
			}
		}
		for _, f := range ch.add {
			if !slices.Contains(cur, f) {
				cur = append(cur, f)
			}
		}
		o["flags"] = cur
	}
	if l, ok := doc["items"].([]any); ok {
		for _, e := range l {
			if o, ok := e.(map[string]any); ok {
				apply(o)
			}
		}
	} else {
		apply(doc)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return b
}
