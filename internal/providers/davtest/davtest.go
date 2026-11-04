// SPDX-License-Identifier: AGPL-3.0-or-later

// Package davtest runs in-process CalDAV and CardDAV servers (built on
// github.com/emersion/go-webdav) for tests: an in-memory store with ETags
// and If-Match/If-None-Match checks, plus optional sync-collection
// (RFC 6578) and a scheduling outbox answering free/busy requests, which
// go-webdav does not implement itself. Only tests import this package.
package davtest

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	goical "github.com/emersion/go-ical"
	govcard "github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// Principal is the user principal of the test servers.
const Principal = "/alice/"

type object struct {
	data    []byte
	etag    string
	modTime time.Time
}

type change struct {
	seq     int
	path    string
	deleted bool
}

// Store is the in-memory resource store shared by the handlers.
type Store struct {
	mu      sync.Mutex
	objects map[string]*object
	log     []change
	seq     int
	// Requests counts requests per method (for assertions).
	Requests map[string]int
}

func newStore() *Store { return &Store{objects: map[string]*object{}, Requests: map[string]int{}} }

func etagOf(b []byte) string {
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:8])
}

func (s *Store) put(p string, data []byte, ifMatch, ifNoneMatch webdav.ConditionalMatch) (*object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.objects[p]
	if ifNoneMatch.IsWildcard() && cur != nil {
		return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("exists"))
	}
	if ifMatch.IsSet() {
		ok := false
		if cur != nil {
			ok, _ = ifMatch.MatchETag(cur.etag)
		}
		if !ok {
			return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("etag mismatch"))
		}
	}
	o := &object{data: data, etag: etagOf(data), modTime: time.Now()}
	s.objects[p] = o
	s.seq++
	s.log = append(s.log, change{seq: s.seq, path: p})
	return o, nil
}

// Put stores a resource directly (server-side change in tests).
func (s *Store) Put(p string, data string) string {
	o, _ := s.put(p, []byte(strings.ReplaceAll(data, "\n", "\r\n")), "", "")
	return o.etag
}

// Delete removes a resource (server-side change).
func (s *Store) Delete(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, p)
	s.seq++
	s.log = append(s.log, change{seq: s.seq, path: p, deleted: true})
}

// Get returns the data of a resource ("" if missing).
func (s *Store) Get(p string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.objects[p]; o != nil {
		return string(o.data)
	}
	return ""
}

// Paths returns the resources below prefix.
func (s *Store) Paths(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.objects {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) list(dir string) map[string]*object {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*object{}
	for p, o := range s.objects {
		if path.Dir(p)+"/" == dir {
			out[p] = o
		}
	}
	return out
}

func (s *Store) get(p string) *object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[p]
}

func (s *Store) remove(p string) {
	s.Delete(p)
}

// ==== CalDAV backend ===========================================================

// CalendarPath is the calendar of the CalDAV test server.
const CalendarPath = "/alice/calendars/work/"

type calBackend struct{ s *Store }

func (b calBackend) CurrentUserPrincipal(context.Context) (string, error) { return Principal, nil }
func (b calBackend) CalendarHomeSetPath(context.Context) (string, error) {
	return "/alice/calendars/", nil
}
func (b calBackend) CreateCalendar(context.Context, *caldav.Calendar) error { return nil }
func (b calBackend) ListCalendars(context.Context) ([]caldav.Calendar, error) {
	return []caldav.Calendar{{Path: CalendarPath, Name: "Work", SupportedComponentSet: []string{"VEVENT", "VTODO"}}}, nil
}
func (b calBackend) GetCalendar(_ context.Context, p string) (*caldav.Calendar, error) {
	if strings.TrimSuffix(p, "/")+"/" != CalendarPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no calendar"))
	}
	return &caldav.Calendar{Path: CalendarPath, Name: "Work", SupportedComponentSet: []string{"VEVENT", "VTODO"}}, nil
}

func calObj(p string, o *object) (*caldav.CalendarObject, error) {
	cal, err := goical.NewDecoder(bytes.NewReader(o.data)).Decode()
	if err != nil {
		return nil, err
	}
	return &caldav.CalendarObject{Path: p, ModTime: o.modTime, ETag: o.etag, ContentLength: int64(len(o.data)), Data: cal}, nil
}

func (b calBackend) GetCalendarObject(_ context.Context, p string, _ *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	o := b.s.get(p)
	if o == nil {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("not found"))
	}
	return calObj(p, o)
}

func (b calBackend) ListCalendarObjects(_ context.Context, p string, _ *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	var out []caldav.CalendarObject
	for k, o := range b.s.list(strings.TrimSuffix(p, "/") + "/") {
		co, err := calObj(k, o)
		if err != nil {
			return nil, err
		}
		out = append(out, *co)
	}
	return out, nil
}

func (b calBackend) QueryCalendarObjects(ctx context.Context, p string, _ *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	return b.ListCalendarObjects(ctx, p, nil)
}

func (b calBackend) PutCalendarObject(_ context.Context, p string, cal *goical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	var buf bytes.Buffer
	if err := goical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}
	o, err := b.s.put(p, buf.Bytes(), opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, err
	}
	return calObj(p, o)
}

func (b calBackend) DeleteCalendarObject(_ context.Context, p string) error {
	b.s.remove(p)
	return nil
}

// ==== CardDAV backend ==========================================================

// AddressBookPath is the address book of the CardDAV test server.
const AddressBookPath = "/alice/contacts/default/"

type cardBackend struct{ s *Store }

func (b cardBackend) CurrentUserPrincipal(context.Context) (string, error) { return Principal, nil }
func (b cardBackend) AddressBookHomeSetPath(context.Context) (string, error) {
	return "/alice/contacts/", nil
}
func (b cardBackend) ListAddressBooks(context.Context) ([]carddav.AddressBook, error) {
	return []carddav.AddressBook{{Path: AddressBookPath, Name: "Contacts"}}, nil
}
func (b cardBackend) GetAddressBook(_ context.Context, p string) (*carddav.AddressBook, error) {
	if strings.TrimSuffix(p, "/")+"/" != AddressBookPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no address book"))
	}
	return &carddav.AddressBook{Path: AddressBookPath, Name: "Contacts"}, nil
}
func (b cardBackend) CreateAddressBook(context.Context, *carddav.AddressBook) error { return nil }
func (b cardBackend) DeleteAddressBook(context.Context, string) error               { return nil }

func cardObj(p string, o *object) (*carddav.AddressObject, error) {
	card, err := govcard.NewDecoder(bytes.NewReader(o.data)).Decode()
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{Path: p, ModTime: o.modTime, ETag: o.etag, ContentLength: int64(len(o.data)), Card: card}, nil
}

func (b cardBackend) GetAddressObject(_ context.Context, p string, _ *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	o := b.s.get(p)
	if o == nil {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("not found"))
	}
	return cardObj(p, o)
}

func (b cardBackend) ListAddressObjects(_ context.Context, p string, _ *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	var out []carddav.AddressObject
	for k, o := range b.s.list(strings.TrimSuffix(p, "/") + "/") {
		ao, err := cardObj(k, o)
		if err != nil {
			return nil, err
		}
		out = append(out, *ao)
	}
	return out, nil
}

func (b cardBackend) QueryAddressObjects(ctx context.Context, p string, _ *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	return b.ListAddressObjects(ctx, p, nil)
}

func (b cardBackend) PutAddressObject(_ context.Context, p string, card govcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	var buf bytes.Buffer
	if err := govcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, err
	}
	o, err := b.s.put(p, buf.Bytes(), opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, err
	}
	return cardObj(p, o)
}

func (b cardBackend) DeleteAddressObject(_ context.Context, p string) error {
	b.s.remove(p)
	return nil
}

// ==== Server ===================================================================

// Options configure a test server.
type Options struct {
	// SyncCollection enables the RFC 6578 REPORT.
	SyncCollection bool
	// Busy maps attendee addresses to busy periods for the outbox.
	Busy map[string][][2]time.Time
	// Username/Password require basic auth when set.
	Username, Password string
}

// Server is a running test server.
type Server struct {
	*httptest.Server
	Store *Store
	// Outbox is the URL of the scheduling outbox.
	Outbox string
	opts   Options
	tokens int
}

// NewCalDAV starts a CalDAV server.
func NewCalDAV(opts Options) *Server {
	s := &Server{Store: newStore(), opts: opts}
	h := &caldav.Handler{Backend: calBackend{s.Store}}
	s.Server = httptest.NewServer(s.wrap(h))
	s.Outbox = s.URL + "/alice/outbox/"
	return s
}

// NewCardDAV starts a CardDAV server.
func NewCardDAV(opts Options) *Server {
	s := &Server{Store: newStore(), opts: opts}
	h := &carddav.Handler{Backend: cardBackend{s.Store}}
	s.Server = httptest.NewServer(s.wrap(h))
	return s
}

var tokenRe = regexp.MustCompile(`<[^>]*sync-token>([^<]*)<`)

func (s *Server) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Store.mu.Lock()
		s.Store.Requests[r.Method]++
		s.Store.mu.Unlock()
		if s.opts.Username != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != s.opts.Username || p != s.opts.Password {
				w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("DAV", "1, 3, calendar-access, addressbook")
		}
		if r.Method == "POST" && r.URL.Path == "/alice/outbox/" {
			s.outbox(w, r)
			return
		}
		if r.Method == "REPORT" {
			body, _ := io.ReadAll(r.Body)
			if bytes.Contains(body, []byte("sync-collection")) {
				if !s.opts.SyncCollection {
					http.Error(w, "sync-collection not supported", http.StatusNotImplemented)
					return
				}
				s.syncCollection(w, r, body)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) syncCollection(w http.ResponseWriter, r *http.Request, body []byte) {
	since := 0
	if m := tokenRe.FindSubmatch(body); m != nil && len(m[1]) > 0 {
		tok := strings.TrimPrefix(string(m[1]), "http://tuxmail.test/sync/")
		n, err := strconv.Atoi(tok)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><d:error xmlns:d="DAV:"><d:valid-sync-token/></d:error>`)
			return
		}
		since = n
	}
	dir := strings.TrimSuffix(r.URL.Path, "/") + "/"
	st := s.Store
	st.mu.Lock()
	latest := map[string]change{}
	for _, c := range st.log {
		if c.seq > since && path.Dir(c.path)+"/" == dir {
			latest[c.path] = c
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:">`)
	for p, c := range latest {
		o := st.objects[p]
		if c.deleted || o == nil {
			if since == 0 {
				continue
			}
			fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>`, p)
			continue
		}
		fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>"%s"</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, p, o.etag)
	}
	fmt.Fprintf(&b, `<d:sync-token>http://tuxmail.test/sync/%d</d:sync-token></d:multistatus>`, st.seq)
	st.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, b.String())
}

func (s *Server) outbox(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><C:schedule-response xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`)
	for _, rc := range r.Header.Values("Recipient") {
		addr := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(rc), "mailto:"))
		periods, ok := s.opts.Busy[addr]
		if !ok {
			fmt.Fprintf(&b, `<C:response><C:recipient><D:href>mailto:%s</D:href></C:recipient><C:request-status>3.7;Invalid calendar user</C:request-status></C:response>`, addr)
			continue
		}
		var fb strings.Builder
		fb.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nMETHOD:REPLY\r\nBEGIN:VFREEBUSY\r\nUID:x\r\n")
		for _, p := range periods {
			fmt.Fprintf(&fb, "FREEBUSY;FBTYPE=BUSY:%s/%s\r\n", p[0].UTC().Format("20060102T150405Z"), p[1].UTC().Format("20060102T150405Z"))
		}
		fb.WriteString("END:VFREEBUSY\r\nEND:VCALENDAR\r\n")
		fmt.Fprintf(&b, `<C:response><C:recipient><D:href>mailto:%s</D:href></C:recipient><C:request-status>2.0;Success</C:request-status><C:calendar-data>%s</C:calendar-data></C:response>`,
			addr, xmlText(fb.String()))
	}
	b.WriteString(`</C:schedule-response>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
