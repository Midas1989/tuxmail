// SPDX-License-Identifier: AGPL-3.0-or-later

// Package graphtest is an in-memory fake of the Microsoft Graph mail,
// calendar and mailbox-settings endpoints for tests (httptest).
package graphtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"time"
)

// Message is a stored message.
type Message struct {
	ID, Folder, Raw   string
	IsRead, Flagged   bool
	Categories        []string
	ConversationID    string
	Subject, From     string
	InternetMessageID string
	Received          time.Time
}

// Server is the fake Graph server. Paths are served below /v1.0.
type Server struct {
	*httptest.Server
	Token string

	mu        sync.Mutex
	Folders   map[string]string // id -> displayName (well-known ids equal their names)
	Messages  map[string]*Message
	seq       int
	changes   map[string][]string // folder -> changed ids since the delta token
	removed   map[string][]string
	tokens    map[string]int // delta token -> generation
	Sent      []string       // raw MIME of sendMail calls
	SentPaths []string
	Settings  map[string]any
	Events    []map[string]any
	Throttle  int // number of 429 answers to give before succeeding
	Requests  []string
}

// New starts a fake with inbox, sentitems, drafts, deleteditems, junkemail.
func New(token string) *Server {
	s := &Server{Token: token, Folders: map[string]string{"inbox": "Inbox", "sentitems": "Sent Items", "drafts": "Drafts",
		"deleteditems": "Deleted Items", "junkemail": "Junk Email"}, Messages: map[string]*Message{},
		changes: map[string][]string{}, removed: map[string][]string{}, tokens: map[string]int{}, Settings: map[string]any{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL returns the Graph base URL (…/v1.0).
func (s *Server) BaseURL() string { return s.Server.URL + "/v1.0" }

// AddMessage stores a message in folder (raw MIME); header fields are parsed.
func (s *Server) AddMessage(folder, raw string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("AAMk-%03d", s.seq)
	m := &Message{ID: id, Folder: folder, Raw: raw, Received: time.Date(2026, 11, 1, 10, s.seq, 0, 0, time.UTC)}
	if pm, err := mail.ReadMessage(strings.NewReader(raw)); err == nil {
		m.Subject = pm.Header.Get("Subject")
		m.From = pm.Header.Get("From")
		m.InternetMessageID = pm.Header.Get("Message-Id")
		m.ConversationID = "conv-" + pm.Header.Get("X-Conv")
	}
	s.Messages[id] = m
	s.changes[folder] = append(s.changes[folder], id)
	return id
}

// SetRead changes isRead like another client would.
func (s *Server) SetRead(id string, read bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.Messages[id]
	m.IsRead = read
	s.changes[m.Folder] = append(s.changes[m.Folder], id)
}

// Remove deletes a message like another client would.
func (s *Server) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.Messages[id]
	delete(s.Messages, id)
	s.removed[m.Folder] = append(s.removed[m.Folder], id)
}

// Get returns a copy of a message.
func (s *Server) Get(id string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.Messages[id]
	if !ok {
		return Message{}, false
	}
	return *m, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func gerr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func addr(h string) map[string]any {
	a, err := mail.ParseAddress(h)
	if err != nil {
		return map[string]any{"emailAddress": map[string]string{"address": h}}
	}
	return map[string]any{"emailAddress": map[string]string{"name": a.Name, "address": a.Address}}
}

func (s *Server) msgJSON(m *Message) map[string]any {
	flag := "notFlagged"
	if m.Flagged {
		flag = "flagged"
	}
	cats := m.Categories
	if cats == nil {
		cats = []string{}
	}
	return map[string]any{"id": m.ID, "subject": m.Subject, "from": addr(m.From), "internetMessageId": m.InternetMessageID,
		"conversationId": m.ConversationID, "isRead": m.IsRead, "flag": map[string]string{"flagStatus": flag},
		"categories": cats, "receivedDateTime": m.Received.Format(time.RFC3339), "sentDateTime": m.Received.Format(time.RFC3339),
		"parentFolderId": m.Folder, "toRecipients": []any{}, "ccRecipients": []any{}}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		gerr(w, 401, "InvalidAuthenticationToken")
		return
	}
	s.mu.Lock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	if s.Throttle > 0 {
		s.Throttle--
		s.mu.Unlock()
		w.Header().Set("Retry-After", "0")
		gerr(w, 429, "TooManyRequests")
		return
	}
	s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.0")
	if path == "/$batch" {
		s.batch(w, r)
		return
	}
	s.route(w, r, path)
}

func (s *Server) batch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Requests []struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			URL    string          `json:"url"`
			Body   json.RawMessage `json:"body"`
		} `json:"requests"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	var out []map[string]any
	for _, q := range in.Requests {
		rec := httptest.NewRecorder()
		sub, _ := http.NewRequest(q.Method, "http://x/v1.0"+q.URL, bytes.NewReader(q.Body))
		u := sub.URL
		s.route(rec, sub, strings.TrimPrefix(u.Path, "/v1.0"))
		var body any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		out = append(out, map[string]any{"id": q.ID, "status": rec.Code, "body": body, "headers": map[string]string{}})
	}
	writeJSON(w, 200, map[string]any{"responses": out})
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = strings.TrimPrefix(path, "/me")
	if strings.HasPrefix(path, "/users/") {
		rest := strings.TrimPrefix(path, "/users/")
		_, after, _ := strings.Cut(rest, "/")
		path = "/" + after
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/mailFolders" && r.Method == "GET":
		var v []any
		for id, name := range s.Folders {
			v = append(v, map[string]any{"id": id, "displayName": name, "childFolderCount": 0})
		}
		writeJSON(w, 200, map[string]any{"value": v})
	case len(parts) == 2 && parts[0] == "mailFolders" && r.Method == "GET":
		if name, ok := s.Folders[parts[1]]; ok {
			n, unread := 0, 0
			for _, m := range s.Messages {
				if m.Folder == parts[1] {
					n++
					if !m.IsRead {
						unread++
					}
				}
			}
			writeJSON(w, 200, map[string]any{"id": parts[1], "displayName": name, "totalItemCount": n, "unreadItemCount": unread})
			return
		}
		gerr(w, 404, "ErrorItemNotFound")
	case path == "/mailFolders" && r.Method == "POST":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		id := "fld-" + strings.ToLower(in["displayName"])
		s.Folders[id] = in["displayName"]
		writeJSON(w, 201, map[string]any{"id": id, "displayName": in["displayName"]})
	case len(parts) == 4 && parts[0] == "mailFolders" && parts[2] == "messages" && parts[3] == "delta":
		s.delta(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "mailFolders" && parts[2] == "messages" && r.Method == "POST":
		raw, _ := io.ReadAll(r.Body)
		dec, _ := base64.StdEncoding.DecodeString(string(raw))
		s.mu.Unlock()
		id := s.AddMessage(parts[1], string(dec))
		s.mu.Lock()
		writeJSON(w, 201, map[string]any{"id": id})
	case len(parts) == 3 && parts[0] == "messages" && parts[2] == "$value":
		m, ok := s.Messages[parts[1]]
		if !ok {
			gerr(w, 404, "ErrorItemNotFound")
			return
		}
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = io.WriteString(w, m.Raw)
	case len(parts) == 3 && parts[0] == "messages" && (parts[2] == "move" || parts[2] == "copy"):
		m, ok := s.Messages[parts[1]]
		if !ok {
			gerr(w, 404, "ErrorItemNotFound")
			return
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		dest := in["destinationId"]
		if parts[2] == "move" {
			s.removed[m.Folder] = append(s.removed[m.Folder], m.ID)
			m.Folder = dest
			s.changes[dest] = append(s.changes[dest], m.ID)
			writeJSON(w, 201, s.msgJSON(m))
			return
		}
		s.seq++
		cp := *m
		cp.ID, cp.Folder = fmt.Sprintf("AAMk-%03d", s.seq), dest
		s.Messages[cp.ID] = &cp
		s.changes[dest] = append(s.changes[dest], cp.ID)
		writeJSON(w, 201, s.msgJSON(&cp))
	case len(parts) == 2 && parts[0] == "messages":
		m, ok := s.Messages[parts[1]]
		if !ok {
			gerr(w, 404, "ErrorItemNotFound")
			return
		}
		switch r.Method {
		case "GET":
			writeJSON(w, 200, s.msgJSON(m))
		case "PATCH":
			var in map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&in)
			if v, ok := in["isRead"]; ok {
				_ = json.Unmarshal(v, &m.IsRead)
			}
			if v, ok := in["flag"]; ok {
				var f map[string]string
				_ = json.Unmarshal(v, &f)
				m.Flagged = f["flagStatus"] == "flagged"
			}
			if v, ok := in["categories"]; ok {
				_ = json.Unmarshal(v, &m.Categories)
			}
			s.changes[m.Folder] = append(s.changes[m.Folder], m.ID)
			writeJSON(w, 200, s.msgJSON(m))
		case "DELETE":
			delete(s.Messages, m.ID)
			s.removed[m.Folder] = append(s.removed[m.Folder], m.ID)
			w.WriteHeader(204)
		}
	case path == "/sendMail" && r.Method == "POST":
		raw, _ := io.ReadAll(r.Body)
		dec, err := base64.StdEncoding.DecodeString(string(raw))
		if err != nil || r.Header.Get("Content-Type") != "text/plain" {
			gerr(w, 400, "ErrorMimeContentInvalidBase64String")
			return
		}
		s.Sent = append(s.Sent, string(dec))
		s.SentPaths = append(s.SentPaths, r.URL.Path)
		w.WriteHeader(202)
	case path == "/mailboxSettings":
		if r.Method == "PATCH" {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			for k, v := range in {
				s.Settings[k] = v
			}
			writeJSON(w, 200, in)
			return
		}
		writeJSON(w, 200, s.Settings)
	case path == "/calendars":
		writeJSON(w, 200, map[string]any{"value": []any{map[string]any{"id": "cal1", "name": "Calendar", "canEdit": true,
			"isDefaultCalendar": true, "hexColor": "#0078d4"}}})
	case len(parts) == 3 && parts[0] == "calendars" && parts[2] == "calendarView":
		writeJSON(w, 200, map[string]any{"value": s.Events})
	case len(parts) == 3 && parts[0] == "calendars" && parts[2] == "events" && r.Method == "POST":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		in["id"] = fmt.Sprintf("ev%d", len(s.Events)+1)
		if in["isOnlineMeeting"] == true {
			in["onlineMeeting"] = map[string]string{"joinUrl": "https://teams.microsoft.com/l/meetup-join/x"}
		}
		s.Events = append(s.Events, in)
		writeJSON(w, 201, in)
	case len(parts) == 3 && parts[0] == "events" && (parts[2] == "accept" || parts[2] == "decline" || parts[2] == "tentativelyAccept"):
		for _, e := range s.Events {
			if e["id"] == parts[1] {
				e["responseStatus"] = map[string]string{"response": parts[2]}
				w.WriteHeader(202)
				return
			}
		}
		gerr(w, 404, "ErrorItemNotFound")
	case path == "/calendar/getSchedule":
		writeJSON(w, 200, map[string]any{"value": []any{map[string]any{"scheduleId": "boss@contoso.example",
			"scheduleItems": []any{map[string]any{"status": "busy", "start": map[string]string{"dateTime": "2026-11-04T09:00:00.0000000", "timeZone": "UTC"},
				"end": map[string]string{"dateTime": "2026-11-04T10:00:00.0000000", "timeZone": "UTC"}}}}}})
	case path == "/contactFolders", path == "/todo/lists":
		writeJSON(w, 200, map[string]any{"value": []any{}})
	case path == "/contacts":
		writeJSON(w, 200, map[string]any{"value": []any{map[string]any{"id": "c1", "displayName": "Bob Boss",
			"emailAddresses": []any{map[string]string{"address": "boss@contoso.example"}}}}})
	case path == "" || path == "/":
		writeJSON(w, 200, map[string]any{"mail": "anna@contoso.example", "displayName": "Anna"})
	default:
		gerr(w, 404, "NotImplementedInFake:"+r.Method+" "+path)
	}
}

// delta serves a simplified delta query: the first round returns all
// messages of the folder, later rounds the changes since the token.
func (s *Server) delta(w http.ResponseWriter, r *http.Request, folder string) {
	tok := r.URL.Query().Get("$deltatoken")
	var items []any
	if tok == "" {
		for _, m := range s.Messages {
			if m.Folder == folder {
				items = append(items, s.msgJSON(m))
			}
		}
	} else {
		if _, ok := s.tokens[tok]; !ok {
			gerr(w, 410, "SyncStateNotFound")
			return
		}
		seen := map[string]bool{}
		for _, id := range s.changes[folder] {
			if m, ok := s.Messages[id]; ok && m.Folder == folder && !seen[id] {
				seen[id] = true
				items = append(items, s.msgJSON(m))
			}
		}
		for _, id := range s.removed[folder] {
			items = append(items, map[string]any{"id": id, "@removed": map[string]string{"reason": "deleted"}})
		}
	}
	s.changes[folder], s.removed[folder] = nil, nil
	nt := fmt.Sprintf("tok%d", len(s.tokens)+1)
	s.tokens[nt] = 1
	if items == nil {
		items = []any{}
	}
	writeJSON(w, 200, map[string]any{"value": items,
		"@odata.deltaLink": s.Server.URL + "/v1.0/me/mailFolders/" + folder + "/messages/delta?$deltatoken=" + nt})
}
