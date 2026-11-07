// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gmailtest is an in-memory fake of the Gmail API (labels,
// messages, history, send, vacation) plus minimal Google Calendar and
// People endpoints for tests.
package gmailtest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message is a stored message.
type Message struct {
	ID, ThreadID, Raw string
	Labels            map[string]bool
	Date              time.Time
}

type record struct {
	id      int
	kind    string // added, deleted, labelAdded, labelRemoved
	msg     string
	labels  []string
	current []string
}

// Server is the fake.
type Server struct {
	*httptest.Server
	Token string

	mu       sync.Mutex
	Labels   map[string]string // id -> name
	Messages map[string]*Message
	history  []record
	hid      int
	minHID   int // history before this id expired
	seq      int
	Sent     []string
	Vacation map[string]any
	Events   []map[string]any
}

// New starts the fake.
func New(token string) *Server {
	s := &Server{Token: token, Labels: map[string]string{"INBOX": "INBOX", "SENT": "SENT", "DRAFT": "DRAFT", "TRASH": "TRASH",
		"SPAM": "SPAM", "STARRED": "STARRED", "UNREAD": "UNREAD", "IMPORTANT": "IMPORTANT", "CATEGORY_SOCIAL": "CATEGORY_SOCIAL"},
		Messages: map[string]*Message{}, hid: 100}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// AddLabel creates a user label.
func (s *Server) AddLabel(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Labels[id] = name
}

func (s *Server) rec(kind, msg string, labels []string) {
	s.hid++
	m := s.Messages[msg]
	var cur []string
	if m != nil {
		for l := range m.Labels {
			cur = append(cur, l)
		}
	}
	s.history = append(s.history, record{id: s.hid, kind: kind, msg: msg, labels: labels, current: cur})
}

// AddMessage stores a message with labels (e.g. INBOX, UNREAD).
func (s *Server) AddMessage(raw string, labels ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("18c%04x", s.seq)
	m := &Message{ID: id, ThreadID: "thr-" + id, Raw: raw, Labels: map[string]bool{}, Date: time.Date(2026, 11, 2, 8, s.seq, 0, 0, time.UTC)}
	if pm, err := mail.ReadMessage(strings.NewReader(raw)); err == nil {
		if t := pm.Header.Get("X-Thread"); t != "" {
			m.ThreadID = t
		}
	}
	for _, l := range labels {
		m.Labels[l] = true
	}
	s.Messages[id] = m
	s.rec("added", id, nil)
	return id
}

// Modify changes labels like another client.
func (s *Server) Modify(id string, add, remove []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modify(id, add, remove)
}

func (s *Server) modify(id string, add, remove []string) {
	m := s.Messages[id]
	if m == nil {
		return
	}
	var a, r []string
	for _, l := range add {
		if !m.Labels[l] {
			m.Labels[l] = true
			a = append(a, l)
		}
	}
	for _, l := range remove {
		if m.Labels[l] {
			delete(m.Labels, l)
			r = append(r, l)
		}
	}
	if len(a) > 0 {
		s.rec("labelAdded", id, a)
	}
	if len(r) > 0 {
		s.rec("labelRemoved", id, r)
	}
}

// Get returns a copy of a message.
func (s *Server) Get(id string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.Messages[id]
	if !ok {
		return Message{}, false
	}
	cp := *m
	cp.Labels = map[string]bool{}
	for k, v := range m.Labels {
		cp.Labels[k] = v
	}
	return cp, true
}

// ExpireHistory makes all current history ids invalid.
func (s *Server) ExpireHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minHID = s.hid + 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func gerr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "status": http.StatusText(status)}})
}

func (s *Server) labelList(m *Message) []string {
	var out []string
	for l := range m.Labels {
		out = append(out, l)
	}
	return out
}

func (s *Server) meta(m *Message, format string) map[string]any {
	out := map[string]any{"id": m.ID, "threadId": m.ThreadID, "labelIds": s.labelList(m),
		"internalDate": strconv.FormatInt(m.Date.UnixMilli(), 10), "sizeEstimate": len(m.Raw), "historyId": strconv.Itoa(s.hid),
		"snippet": snippet(m.Raw)}
	switch format {
	case "raw":
		out["raw"] = base64.URLEncoding.EncodeToString([]byte(m.Raw))
	case "metadata":
		var hs []map[string]string
		if pm, err := mail.ReadMessage(strings.NewReader(m.Raw)); err == nil {
			for k, v := range pm.Header {
				hs = append(hs, map[string]string{"name": k, "value": v[0]})
			}
		}
		out["payload"] = map[string]any{"mimeType": "text/plain", "headers": hs}
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		gerr(w, 401, "Request had invalid authentication credentials.")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/gmail/v1/users/me"):
		s.gmail(w, r, strings.TrimPrefix(p, "/gmail/v1/users/me"))
	case strings.HasPrefix(p, "/calendar/v3"):
		s.calendar(w, r, strings.TrimPrefix(p, "/calendar/v3"))
	case strings.HasPrefix(p, "/people/v1"):
		writeJSON(w, 200, map[string]any{"connections": []any{map[string]any{"resourceName": "people/c1", "etag": "e1",
			"names":          []any{map[string]string{"displayName": "Carla Kunde", "givenName": "Carla", "familyName": "Kunde"}},
			"emailAddresses": []any{map[string]string{"value": "carla@kunde.example"}}}}})
	default:
		gerr(w, 404, "not found "+p)
	}
}

func (s *Server) gmail(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case p == "/profile":
		writeJSON(w, 200, map[string]any{"emailAddress": "carla@gmail.com", "historyId": strconv.Itoa(s.hid)})
	case p == "/labels" && r.Method == "GET":
		var ls []any
		for id, name := range s.Labels {
			typ := "system"
			if strings.HasPrefix(id, "Label_") {
				typ = "user"
			}
			ls = append(ls, map[string]any{"id": id, "name": name, "type": typ})
		}
		writeJSON(w, 200, map[string]any{"labels": ls})
	case p == "/labels" && r.Method == "POST":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		id := fmt.Sprintf("Label_%d", len(s.Labels)+1)
		s.Labels[id] = in["name"]
		writeJSON(w, 200, map[string]any{"id": id, "name": in["name"], "type": "user"})
	case p == "/messages" && r.Method == "GET":
		lid := r.URL.Query().Get("labelIds")
		var ms []any
		for id, m := range s.Messages {
			if m.Labels[lid] {
				ms = append(ms, map[string]string{"id": id})
			}
		}
		writeJSON(w, 200, map[string]any{"messages": ms})
	case p == "/messages" && r.Method == "POST":
		var in struct {
			Raw      string   `json:"raw"`
			LabelIDs []string `json:"labelIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		raw, _ := base64.URLEncoding.DecodeString(in.Raw)
		s.mu.Unlock()
		id := s.AddMessage(string(raw), in.LabelIDs...)
		s.mu.Lock()
		writeJSON(w, 200, map[string]any{"id": id})
	case p == "/messages/send":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		raw, err := base64.URLEncoding.DecodeString(in["raw"])
		if err != nil {
			gerr(w, 400, "bad raw")
			return
		}
		s.Sent = append(s.Sent, string(raw))
		s.seq++
		id := fmt.Sprintf("18c%04x", s.seq)
		s.Messages[id] = &Message{ID: id, ThreadID: "thr-" + id, Raw: string(raw), Labels: map[string]bool{"SENT": true}, Date: time.Now()}
		s.rec("added", id, nil)
		writeJSON(w, 200, map[string]any{"id": id})
	case p == "/messages/batchModify":
		var in struct {
			IDs    []string `json:"ids"`
			Add    []string `json:"addLabelIds"`
			Remove []string `json:"removeLabelIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, id := range in.IDs {
			s.modify(id, in.Add, in.Remove)
		}
		w.WriteHeader(204)
	case p == "/messages/batchDelete":
		var in struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, id := range in.IDs {
			delete(s.Messages, id)
			s.rec("deleted", id, nil)
		}
		w.WriteHeader(204)
	case len(parts) == 3 && parts[0] == "messages" && (parts[2] == "trash" || parts[2] == "untrash"):
		if parts[2] == "trash" {
			s.modify(parts[1], []string{"TRASH"}, []string{"INBOX"})
		} else {
			s.modify(parts[1], nil, []string{"TRASH"})
		}
		writeJSON(w, 200, map[string]string{"id": parts[1]})
	case len(parts) == 2 && parts[0] == "messages":
		m, ok := s.Messages[parts[1]]
		if !ok {
			gerr(w, 404, "Requested entity was not found.")
			return
		}
		writeJSON(w, 200, s.meta(m, r.URL.Query().Get("format")))
	case p == "/history":
		start, _ := strconv.Atoi(r.URL.Query().Get("startHistoryId"))
		if start < s.minHID {
			gerr(w, 404, "Requested entity was not found.")
			return
		}
		var hs []any
		for _, h := range s.history {
			if h.id <= start {
				continue
			}
			msg := map[string]any{"id": h.msg, "threadId": "thr", "labelIds": h.current}
			entry := map[string]any{"id": strconv.Itoa(h.id)}
			switch h.kind {
			case "added":
				entry["messagesAdded"] = []any{map[string]any{"message": msg}}
			case "deleted":
				entry["messagesDeleted"] = []any{map[string]any{"message": msg}}
			case "labelAdded":
				entry["labelsAdded"] = []any{map[string]any{"message": msg, "labelIds": h.labels}}
			case "labelRemoved":
				entry["labelsRemoved"] = []any{map[string]any{"message": msg, "labelIds": h.labels}}
			}
			hs = append(hs, entry)
		}
		writeJSON(w, 200, map[string]any{"history": hs, "historyId": strconv.Itoa(s.hid)})
	case p == "/settings/vacation":
		_ = json.NewDecoder(r.Body).Decode(&s.Vacation)
		writeJSON(w, 200, s.Vacation)
	case p == "/drafts":
		var in struct {
			Message struct {
				Raw string `json:"raw"`
			} `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		raw, _ := base64.URLEncoding.DecodeString(in.Message.Raw)
		s.mu.Unlock()
		id := s.AddMessage(string(raw), "DRAFT")
		s.mu.Lock()
		writeJSON(w, 200, map[string]any{"id": "r-" + id, "message": map[string]string{"id": id}})
	default:
		gerr(w, 404, "not implemented "+r.Method+" "+p)
	}
}

func (s *Server) calendar(w http.ResponseWriter, r *http.Request, p string) {
	switch {
	case p == "/users/me/calendarList":
		writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"id": "carla@gmail.com", "summary": "Carla", "primary": true, "accessRole": "owner"}}})
	case strings.HasSuffix(p, "/events") && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"items": s.Events})
	case strings.HasSuffix(p, "/events") && r.Method == "POST":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		in["id"] = fmt.Sprintf("gev%d", len(s.Events)+1)
		in["iCalUID"] = in["id"].(string) + "@google.com"
		if in["conferenceData"] != nil {
			in["hangoutLink"] = "https://meet.google.com/abc-defg-hij"
		}
		in["attendees"] = append(asList(in["attendees"]), map[string]any{"email": "carla@gmail.com", "self": true, "responseStatus": "accepted"})
		s.Events = append(s.Events, in)
		writeJSON(w, 200, in)
	case strings.Contains(p, "/events/"):
		id := p[strings.LastIndex(p, "/")+1:]
		for _, e := range s.Events {
			if e["id"] != id {
				continue
			}
			if r.Method == "PATCH" {
				var in map[string]any
				_ = json.NewDecoder(r.Body).Decode(&in)
				for k, v := range in {
					e[k] = v
				}
			}
			writeJSON(w, 200, e)
			return
		}
		gerr(w, 404, "Not Found")
	case p == "/freeBusy":
		writeJSON(w, 200, map[string]any{"calendars": map[string]any{"x@example.org": map[string]any{"busy": []any{
			map[string]string{"start": "2026-11-05T10:00:00Z", "end": "2026-11-05T11:00:00Z"}}}}})
	default:
		gerr(w, 404, "not implemented "+p)
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// snippet is Gmail's preview text: the start of the body, HTML-escaped.
func snippet(raw string) string {
	_, body, _ := strings.Cut(strings.ReplaceAll(raw, "\r\n", "\n"), "\n\n")
	body = strings.Join(strings.Fields(body), " ")
	if r := []rune(body); len(r) > 200 {
		body = string(r[:200])
	}
	return html.EscapeString(body)
}
