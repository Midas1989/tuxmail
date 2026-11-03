// SPDX-License-Identifier: AGPL-3.0-or-later

// Package eastest provides an in-process fake Exchange ActiveSync server
// for tests. It speaks WBXML over HTTP and implements the commands used by
// the eas package with simplified but protocol-conformant semantics:
// OPTIONS, Provision (two-phase, remote wipe), FolderSync/Create/Update/
// Delete, Sync (SyncKey progression, WindowSize, MoreAvailable, client
// Add/Change/Delete/Fetch), GetItemEstimate, ItemOperations, MoveItems,
// SendMail/SmartReply/SmartForward, MeetingResponse, Search (Mailbox, GAL),
// Ping, Settings (OOF, DeviceInformation, UserInformation) and
// ResolveRecipients. Tests drive "other client" changes through methods
// such as AddMail, SetRead and DeleteItem.
package eastest

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// Well-known folder ids of the fake mailbox.
const (
	Inbox    = "1"
	Drafts   = "2"
	Deleted  = "3"
	Sent     = "4"
	Outbox   = "5"
	Calendar = "6"
	Contacts = "7"
	Tasks    = "8"
	Junk     = "9"
)

// Item is a stored item.
type Item struct {
	ServerID   string
	Class      string
	MIME       []byte // mail
	Read       bool
	Flagged    bool
	Categories []string
	Draft      bool
	// Data is the ApplicationData of PIM items (and drafts).
	Data    *wbxml.Node
	version int
}

type folder struct {
	id, parent, name string
	typ              int
	items            map[string]*Item
}

type collSync struct {
	key      int
	snapshot map[string]int // serverId -> version seen by the client
	last     []byte         // last response (replay of the previous key)
}

// Sent records a submitted message.
type SentMail struct {
	Cmd        string
	MIME       []byte
	SaveInSent bool
	Source     string // SmartReply/Forward ItemId
}

// Server is the fake EAS server.
type Server struct {
	*httptest.Server

	// Configuration (set before use; guarded by mu afterwards).
	Users            map[string]string // user -> password
	Tokens           map[string]bool   // accepted OAuth2 bearer tokens
	Versions         string            // MS-ASProtocolVersions
	RequireProvision bool
	// PolicySettings are sent in EASProvisionDoc.
	PolicySettings map[string]string
	// MaxPingWait caps the Ping wait regardless of the heartbeat.
	MaxPingWait time.Duration
	// MinHeartbeat makes Ping answer status 5 for shorter heartbeats.
	MinHeartbeat int

	mu         sync.Mutex
	folders    map[string]*folder
	nextID     int
	hierKey    int
	hierSnap   map[string]string // folder id -> name|parent seen by the client
	colls      map[string]*collSync
	policyKey  string
	tempKey    string
	wipe       int // 0 none, 1 RemoteWipe, 2 AccountOnlyRemoteWipe
	wiped      bool
	redirectTo string
	unavail    int
	oof        oof
	sent       []SentMail
	commands   []string
	changed    chan struct{}
	gal        []GALEntry
	responses  []string
	deviceInfo map[string]string
}

type oof struct {
	state                int
	start, end           string
	internal, external   string
	extKnown, extUnknown bool
}

// GALEntry is an entry of the fake global address list.
type GALEntry struct{ Name, Email string }

// New starts a fake server with user alice/secret and the default folders.
func New() *Server {
	s := &Server{
		Users: map[string]string{"alice": "secret"}, Tokens: map[string]bool{},
		Versions:    "2.5,12.0,12.1,14.0,14.1,16.0,16.1",
		MaxPingWait: 2 * time.Second, PolicySettings: map[string]string{"DevicePasswordEnabled": "0"},
		folders: map[string]*folder{}, colls: map[string]*collSync{}, nextID: 100,
		changed: make(chan struct{}), deviceInfo: map[string]string{},
	}
	for _, f := range []struct {
		id, name string
		typ      int
	}{{Inbox, "Inbox", 2}, {Drafts, "Drafts", 3}, {Deleted, "Deleted Items", 4}, {Sent, "Sent Items", 5},
		{Outbox, "Outbox", 6}, {Calendar, "Calendar", 8}, {Contacts, "Contacts", 9}, {Tasks, "Tasks", 7},
		{Junk, "Junk E-Mail", 12}} {
		s.folders[f.id] = &folder{id: f.id, parent: "0", name: f.name, typ: f.typ, items: map[string]*Item{}}
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL of the ActiveSync endpoint.
func (s *Server) EndpointURL() string { return s.Server.URL + "/Microsoft-Server-ActiveSync" }

// ==== test controls ================================================================

func (s *Server) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Server) newIDLocked() string {
	s.nextID++
	return strconv.Itoa(s.nextID)
}

// AddMail stores a message in a folder as if it was delivered and returns
// its ServerId.
func (s *Server) AddMail(folderID, mime string, read bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newIDLocked()
	s.folders[folderID].items[id] = &Item{ServerID: id, Class: "Email", MIME: []byte(mime), Read: read, version: 1}
	s.notify()
	return id
}

// AddItem stores a PIM item (ApplicationData children) and returns its id.
func (s *Server) AddItem(folderID, class string, data ...*wbxml.Node) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newIDLocked()
	s.folders[folderID].items[id] = &Item{ServerID: id, Class: class, Data: wbxml.E("AirSync:ApplicationData", data...), version: 1}
	s.notify()
	return id
}

// UpdateItem replaces the data of a PIM item.
func (s *Server) UpdateItem(folderID, id string, data ...*wbxml.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.folders[folderID].items[id]
	it.Data = wbxml.E("AirSync:ApplicationData", data...)
	it.version++
	s.notify()
}

// SetRead changes the read state of a mail (another client).
func (s *Server) SetRead(folderID, id string, read bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.folders[folderID].items[id]
	it.Read = read
	it.version++
	s.notify()
}

// SetCategories changes the categories of a mail (another client).
func (s *Server) SetCategories(folderID, id string, cats ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.folders[folderID].items[id]
	it.Categories = cats
	it.version++
	s.notify()
}

// DeleteItem removes an item (another client).
func (s *Server) DeleteItem(folderID, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.folders[folderID].items, id)
	s.notify()
}

// Item returns a copy of an item (nil if missing).
func (s *Server) Item(folderID, id string) *Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[folderID]
	if f == nil || f.items[id] == nil {
		return nil
	}
	c := *f.items[id]
	return &c
}

// Items returns copies of all items of a folder sorted by id.
func (s *Server) Items(folderID string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.folders[folderID].items {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerID < out[j].ServerID })
	return out
}

// FolderByName returns the id of a folder ("" if missing).
func (s *Server) FolderByName(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.folders {
		if f.name == name {
			return f.id
		}
	}
	return ""
}

// AddFolder creates a folder (another client).
func (s *Server) AddFolder(parent, name string, typ int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newIDLocked()
	s.folders[id] = &folder{id: id, parent: parent, name: name, typ: typ, items: map[string]*Item{}}
	s.notify()
	return id
}

// SentMails returns the submitted messages.
func (s *Server) SentMails() []SentMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SentMail(nil), s.sent...)
}

// Commands returns the received commands in order.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// ResetCommands clears the command log.
func (s *Server) ResetCommands() {
	s.mu.Lock()
	s.commands = nil
	s.mu.Unlock()
}

// RequestWipe makes the next Provision answer with a remote wipe
// (accountOnly selects AccountOnlyRemoteWipe) and invalidates the policy.
func (s *Server) RequestWipe(accountOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wipe = 1
	if accountOnly {
		s.wipe = 2
	}
	s.policyKey = "invalidated"
	s.RequireProvision = true
}

// Wiped reports whether a remote wipe was acknowledged.
func (s *Server) Wiped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wiped
}

// InvalidatePolicy forces re-provisioning.
func (s *Server) InvalidatePolicy() {
	s.mu.Lock()
	s.policyKey = "invalidated"
	s.mu.Unlock()
}

// PolicyKey returns the current final policy key.
func (s *Server) PolicyKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policyKey
}

// RedirectOnce answers the next command with HTTP 451 pointing to url.
func (s *Server) RedirectOnce(url string) {
	s.mu.Lock()
	s.redirectTo = url
	s.mu.Unlock()
}

// UnavailableOnce answers the next n commands with 503 Retry-After: 0... 1.
func (s *Server) Unavailable(n int) {
	s.mu.Lock()
	s.unavail = n
	s.mu.Unlock()
}

// SetGAL sets the global address list.
func (s *Server) SetGAL(entries ...GALEntry) {
	s.mu.Lock()
	s.gal = entries
	s.mu.Unlock()
}

// Oof returns state and internal text of the OOF settings.
func (s *Server) Oof() (state int, internal, external string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oof.state, s.oof.internal, s.oof.external
}

// MeetingResponses returns the received meeting responses ("requestId:response").
func (s *Server) MeetingResponses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.responses...)
}

// DeviceInformation returns the last device information received.
func (s *Server) DeviceInformation() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]string{}
	for k, v := range s.deviceInfo {
		m[k] = v
	}
	return m
}

// ==== HTTP ==========================================================================

func (s *Server) authorized(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
		return s.Tokens[tok]
	}
	u, p, ok := r.BasicAuth()
	return ok && s.Users[u] == p && p != ""
}

type reqInfo struct {
	cmd, version, policyKey string
	params                  map[string]string
}

func parseQuery(r *http.Request) reqInfo {
	q := r.URL.Query()
	ri := reqInfo{cmd: q.Get("Cmd"), version: r.Header.Get("MS-ASProtocolVersion"),
		policyKey: r.Header.Get("X-MS-PolicyKey"), params: map[string]string{}}
	for k, v := range q {
		ri.params[k] = v[0]
	}
	if ri.cmd == "" && r.URL.RawQuery != "" {
		if raw, err := base64.StdEncoding.DecodeString(mustUnescape(r.URL.RawQuery)); err == nil {
			ri = decodeBase64Query(raw)
		}
	}
	return ri
}

func mustUnescape(s string) string {
	s = strings.ReplaceAll(s, "%2B", "+")
	s = strings.ReplaceAll(s, "%2F", "/")
	return strings.ReplaceAll(s, "%3D", "=")
}

var cmdNames = map[byte]string{0: "Sync", 1: "SendMail", 2: "SmartForward", 3: "SmartReply", 9: "FolderSync",
	10: "FolderCreate", 11: "FolderDelete", 12: "FolderUpdate", 13: "MoveItems", 14: "GetItemEstimate",
	15: "MeetingResponse", 16: "Search", 17: "Settings", 18: "Ping", 19: "ItemOperations", 20: "Provision",
	21: "ResolveRecipients"}

func decodeBase64Query(b []byte) reqInfo {
	ri := reqInfo{params: map[string]string{}}
	if len(b) < 5 {
		return ri
	}
	v := int(b[0])
	ri.version = fmt.Sprintf("%d.%d", v/10, v%10)
	ri.cmd = cmdNames[b[1]]
	p := 4
	next := func() string {
		if p >= len(b) {
			return ""
		}
		n := int(b[p])
		p++
		if p+n > len(b) {
			return ""
		}
		s := string(b[p : p+n])
		p += n
		return s
	}
	ri.params["DeviceId"] = next()
	if p < len(b) {
		n := int(b[p])
		p++
		if n == 4 && p+4 <= len(b) {
			k := uint32(b[p]) | uint32(b[p+1])<<8 | uint32(b[p+2])<<16 | uint32(b[p+3])<<24
			ri.policyKey = strconv.FormatUint(uint64(k), 10)
		}
		p += n
	}
	ri.params["DeviceType"] = next()
	for p < len(b) {
		tag := b[p]
		p++
		val := next()
		switch tag {
		case 1:
			ri.params["CollectionId"] = val
		case 3:
			ri.params["ItemId"] = val
		case 8:
			ri.params["User"] = val
		case 7:
			if len(val) > 0 && val[0]&1 != 0 {
				ri.params["SaveInSent"] = "T"
			}
		}
	}
	return ri
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/Microsoft-Server-ActiveSync") {
		http.NotFound(w, r)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="eas"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("MS-ASProtocolVersions", s.Versions)
		w.Header().Set("MS-ASProtocolCommands", "Sync,SendMail,SmartForward,SmartReply,GetAttachment,FolderSync,"+
			"FolderCreate,FolderDelete,FolderUpdate,MoveItems,GetItemEstimate,MeetingResponse,Search,Settings,Ping,"+
			"ItemOperations,Provision,ResolveRecipients,ValidateCert")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	ri := parseQuery(r)
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.commands = append(s.commands, ri.cmd)
	if s.redirectTo != "" {
		w.Header().Set("X-MS-Location", s.redirectTo)
		s.redirectTo = ""
		s.mu.Unlock()
		w.WriteHeader(451)
		return
	}
	if s.unavail > 0 {
		s.unavail--
		s.mu.Unlock()
		w.Header().Set("Retry-After", "0")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	s.mu.Unlock()

	if ri.cmd == "SendMail" || ri.cmd == "SmartReply" || ri.cmd == "SmartForward" {
		if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "message/rfc822") {
			if !s.provisioned(ri) {
				w.WriteHeader(449)
				return
			}
			s.recordSent(ri.cmd, body, ri.params["SaveInSent"] == "T", ri.params["ItemId"])
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	var req *wbxml.Node
	if len(body) > 0 {
		var err error
		if req, err = wbxml.Unmarshal(body); err != nil {
			http.Error(w, "bad wbxml", http.StatusBadRequest)
			return
		}
	}
	if ri.cmd != "Provision" && !s.provisioned(ri) {
		if compareVersion(ri.version, "14.0") < 0 {
			w.WriteHeader(449)
			return
		}
		root := map[string]string{"Sync": "AirSync:Sync", "FolderSync": "FolderHierarchy:FolderSync",
			"Ping": "Ping:Ping", "Settings": "Settings:Settings", "ItemOperations": "ItemOperations:ItemOperations",
			"MoveItems": "Move:MoveItems", "Search": "Search:Search", "SendMail": "ComposeMail:SendMail",
			"FolderCreate": "FolderHierarchy:FolderCreate", "GetItemEstimate": "ItemEstimate:GetItemEstimate"}[ri.cmd]
		if root == "" {
			w.WriteHeader(449)
			return
		}
		page := root[:strings.IndexByte(root, ':')]
		writeWBXML(w, wbxml.E(root, wbxml.S(page+":Status", "142")))
		return
	}
	var resp *wbxml.Node
	var raw []byte
	switch ri.cmd {
	case "Provision":
		resp = s.provision(req)
	case "FolderSync":
		resp = s.folderSync(req)
	case "FolderCreate", "FolderUpdate", "FolderDelete":
		resp = s.folderCmd(ri.cmd, req)
	case "Sync":
		raw = s.sync(req)
	case "GetItemEstimate":
		resp = s.estimate(req)
	case "ItemOperations":
		resp = s.itemOps(req)
	case "MoveItems":
		resp = s.moveItems(req)
	case "SendMail", "SmartReply", "SmartForward":
		resp = s.sendMail(ri.cmd, req)
	case "MeetingResponse":
		resp = s.meetingResponse(req)
	case "Search":
		resp = s.search(req)
	case "Ping":
		resp = s.ping(req)
	case "Settings":
		resp = s.settings(req)
	case "ResolveRecipients":
		resp = s.resolve(req)
	default:
		http.Error(w, "unknown command", http.StatusBadRequest)
		return
	}
	if raw != nil {
		w.Header().Set("Content-Type", wbxml.ContentType)
		w.Write(raw)
		return
	}
	if resp == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeWBXML(w, resp)
}

func writeWBXML(w http.ResponseWriter, n *wbxml.Node) {
	data, err := wbxml.Marshal(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", wbxml.ContentType)
	w.Write(data)
}

func compareVersion(a, b string) int {
	pa := func(v string) int {
		x, y, _ := strings.Cut(v, ".")
		i, _ := strconv.Atoi(x)
		j, _ := strconv.Atoi(y)
		return i*100 + j
	}
	return pa(a) - pa(b)
}

func (s *Server) provisioned(ri reqInfo) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.RequireProvision || (s.policyKey != "" && ri.policyKey == s.policyKey)
}

// ==== Provision ======================================================================

func (s *Server) provision(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if di := req.Path("Settings:DeviceInformation/Settings:Set"); di != nil {
		for _, c := range di.Children {
			s.deviceInfo[c.Tag.Name()] = c.Value()
		}
	}
	if req.Has("Provision:RemoteWipe") || req.Has("Provision:AccountOnlyRemoteWipe") {
		s.wiped, s.wipe = true, 0
		elem := "Provision:RemoteWipe"
		if req.Has("Provision:AccountOnlyRemoteWipe") {
			elem = "Provision:AccountOnlyRemoteWipe"
		}
		return wbxml.E("Provision:Provision", wbxml.S("Provision:Status", "1"), wbxml.E(elem, wbxml.S("Provision:Status", "1")))
	}
	pol := req.Path("Provision:Policies/Provision:Policy")
	resp := wbxml.E("Provision:Provision", wbxml.S("Provision:Status", "1"))
	if s.wipe != 0 {
		if s.wipe == 2 {
			resp.Add(wbxml.E("Provision:AccountOnlyRemoteWipe"))
		} else {
			resp.Add(wbxml.E("Provision:RemoteWipe"))
		}
		return resp
	}
	ptype := pol.TextOf("Provision:PolicyType")
	if key := pol.TextOf("Provision:PolicyKey"); key != "" {
		// Phase 2: acknowledgment.
		if key != s.tempKey {
			return resp.Add(wbxml.E("Provision:Policies", wbxml.E("Provision:Policy",
				wbxml.S("Provision:PolicyType", ptype), wbxml.S("Provision:Status", "5"))))
		}
		s.policyKey = strconv.Itoa(1000000 + s.nextID*7)
		s.nextID++
		return resp.Add(wbxml.E("Provision:Policies", wbxml.E("Provision:Policy",
			wbxml.S("Provision:PolicyType", ptype), wbxml.S("Provision:Status", "1"),
			wbxml.S("Provision:PolicyKey", s.policyKey))))
	}
	s.tempKey = strconv.Itoa(500000 + s.nextID)
	s.nextID++
	doc := wbxml.E("Provision:EASProvisionDoc")
	keys := make([]string, 0, len(s.PolicySettings))
	for k := range s.PolicySettings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if t, ok := wbxml.LookupTag("Provision:" + k); ok {
			doc.Add(&wbxml.Node{Tag: t, Text: s.PolicySettings[k]})
		}
	}
	return resp.Add(wbxml.E("Provision:Policies", wbxml.E("Provision:Policy",
		wbxml.S("Provision:PolicyType", ptype), wbxml.S("Provision:Status", "1"),
		wbxml.S("Provision:PolicyKey", s.tempKey), wbxml.E("Provision:Data", doc))))
}

// ==== folders ========================================================================

func (s *Server) folderSync(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.TextOf("FolderHierarchy:SyncKey")
	if key == "0" {
		s.hierSnap = map[string]string{}
	} else if key != strconv.Itoa(s.hierKey) || s.hierSnap == nil {
		return wbxml.E("FolderHierarchy:FolderSync", wbxml.S("FolderHierarchy:Status", "9"))
	}
	changes := wbxml.E("FolderHierarchy:Changes")
	var ids []string
	for id := range s.folders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	count := 0
	for _, id := range ids {
		f := s.folders[id]
		sig := f.name + "|" + f.parent
		old, known := s.hierSnap[id]
		if known && old == sig {
			continue
		}
		kind := "FolderHierarchy:Add"
		if known {
			kind = "FolderHierarchy:Update"
		}
		changes.Add(wbxml.E(kind, wbxml.S("FolderHierarchy:ServerId", id), wbxml.S("FolderHierarchy:ParentId", f.parent),
			wbxml.S("FolderHierarchy:DisplayName", f.name), wbxml.S("FolderHierarchy:Type", strconv.Itoa(f.typ))))
		s.hierSnap[id] = sig
		count++
	}
	for id := range s.hierSnap {
		if s.folders[id] == nil {
			changes.Add(wbxml.E("FolderHierarchy:Delete", wbxml.S("FolderHierarchy:ServerId", id)))
			delete(s.hierSnap, id)
			count++
		}
	}
	changes.Children = append([]*wbxml.Node{wbxml.S("FolderHierarchy:Count", strconv.Itoa(count))}, changes.Children...)
	s.hierKey++
	return wbxml.E("FolderHierarchy:FolderSync", wbxml.S("FolderHierarchy:Status", "1"),
		wbxml.S("FolderHierarchy:SyncKey", strconv.Itoa(s.hierKey)), changes)
}

func (s *Server) folderCmd(cmd string, req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	root := "FolderHierarchy:" + cmd
	fail := func(st int) *wbxml.Node {
		return wbxml.E(root, wbxml.S("FolderHierarchy:Status", strconv.Itoa(st)))
	}
	if req.TextOf("FolderHierarchy:SyncKey") != strconv.Itoa(s.hierKey) {
		return fail(9)
	}
	ok := func(extra ...*wbxml.Node) *wbxml.Node {
		s.hierKey++
		n := wbxml.E(root, wbxml.S("FolderHierarchy:Status", "1"), wbxml.S("FolderHierarchy:SyncKey", strconv.Itoa(s.hierKey)))
		return n.Add(extra...)
	}
	switch cmd {
	case "FolderCreate":
		parent, name := req.TextOf("FolderHierarchy:ParentId"), req.TextOf("FolderHierarchy:DisplayName")
		if parent != "0" && s.folders[parent] == nil {
			return fail(5)
		}
		for _, f := range s.folders {
			if f.parent == parent && f.name == name {
				return fail(2)
			}
		}
		id := s.newIDLocked()
		typ, _ := strconv.Atoi(req.TextOf("FolderHierarchy:Type"))
		s.folders[id] = &folder{id: id, parent: parent, name: name, typ: typ, items: map[string]*Item{}}
		s.hierSnap[id] = name + "|" + parent
		return ok(wbxml.S("FolderHierarchy:ServerId", id))
	case "FolderUpdate":
		f := s.folders[req.TextOf("FolderHierarchy:ServerId")]
		if f == nil {
			return fail(4)
		}
		if f.typ != 12 && f.typ != 1 {
			return fail(3)
		}
		f.name, f.parent = req.TextOf("FolderHierarchy:DisplayName"), req.TextOf("FolderHierarchy:ParentId")
		s.hierSnap[f.id] = f.name + "|" + f.parent
		return ok()
	default:
		id := req.TextOf("FolderHierarchy:ServerId")
		f := s.folders[id]
		if f == nil {
			return fail(4)
		}
		if f.typ != 12 && f.typ != 1 {
			return fail(3)
		}
		delete(s.folders, id)
		delete(s.hierSnap, id)
		return ok()
	}
}

// ==== Sync ===========================================================================

// mailData renders the ApplicationData of a mail item.
func mailData(it *Item, opts *wbxml.Node) *wbxml.Node {
	d := wbxml.E("AirSync:ApplicationData")
	if it.Data != nil && it.MIME == nil {
		d.Children = append(d.Children, it.Data.Children...)
	} else {
		msg, err := mail.ReadMessage(bytes.NewReader(it.MIME))
		if err == nil {
			h := msg.Header
			d.Add(optS("Email:To", h.Get("To")), optS("Email:Cc", h.Get("Cc")), optS("Email:From", h.Get("From")),
				optS("Email:Subject", h.Get("Subject")))
			if t, err := h.Date(); err == nil {
				d.Add(wbxml.S("Email:DateReceived", t.UTC().Format("2006-01-02T15:04:05.000Z")))
			}
		}
		d.Add(wbxml.S("Email:MessageClass", "IPM.Note"))
	}
	d.Add(wbxml.S("Email:Read", b2s(it.Read)))
	if it.Flagged {
		d.Add(wbxml.E("Email:Flag", wbxml.S("Email:Status", "2"), wbxml.S("Email:FlagType", "Flag for follow up")))
	} else {
		d.Add(wbxml.E("Email:Flag"))
	}
	if it.MIME != nil {
		trunc := 0
		if bp := opts.Child("AirSyncBase:BodyPreference"); bp != nil {
			trunc = bp.IntOf("AirSyncBase:TruncationSize", 0)
		}
		data := it.MIME
		truncated := "0"
		if trunc > 0 && len(data) > trunc {
			data, truncated = data[:trunc], "1"
		}
		d.Add(wbxml.E("AirSyncBase:Body", wbxml.S("AirSyncBase:Type", "4"),
			wbxml.S("AirSyncBase:EstimatedDataSize", strconv.Itoa(len(it.MIME))), wbxml.S("AirSyncBase:Truncated", truncated),
			wbxml.S("AirSyncBase:Data", string(data))))
	}
	cats := wbxml.E("Email:Categories")
	for _, c := range it.Categories {
		cats.Add(wbxml.S("Email:Category", c))
	}
	d.Add(cats)
	if it.MIME != nil {
		if msg, err := mail.ReadMessage(bytes.NewReader(it.MIME)); err == nil {
			subj := strings.TrimPrefix(strings.TrimPrefix(msg.Header.Get("Subject"), "Re: "), "RE: ")
			d.Add(wbxml.Op("Email2:ConversationId", []byte("conv:"+subj)))
		}
	}
	return d
}

func optS(name, v string) *wbxml.Node {
	if v == "" {
		return nil
	}
	return wbxml.S(name, v)
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (s *Server) itemData(it *Item, opts *wbxml.Node) *wbxml.Node {
	if it.Class == "Email" {
		return mailData(it, opts)
	}
	return wbxml.E("AirSync:ApplicationData", it.Data.Children...)
}

func (s *Server) sync(req *wbxml.Node) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	col := req.Path("AirSync:Collections/AirSync:Collection")
	id := col.TextOf("AirSync:CollectionId")
	f := s.folders[id]
	out := wbxml.E("AirSync:Collection", wbxml.S("AirSync:SyncKey", "0"), wbxml.S("AirSync:CollectionId", id))
	resp := wbxml.E("AirSync:Sync", wbxml.E("AirSync:Collections", out))
	status := func(st int) []byte {
		out.Add(wbxml.S("AirSync:Status", strconv.Itoa(st)))
		data, _ := wbxml.Marshal(resp)
		return data
	}
	if f == nil {
		return status(8)
	}
	key := col.TextOf("AirSync:SyncKey")
	cs := s.colls[id]
	if key == "0" {
		cs = &collSync{key: 1, snapshot: map[string]int{}}
		s.colls[id] = cs
		out.Children[0].Text = "1"
		return status(1)
	}
	if cs == nil {
		return status(3)
	}
	if key == strconv.Itoa(cs.key-1) && cs.last != nil {
		return cs.last
	}
	if key != strconv.Itoa(cs.key) {
		return status(3)
	}
	cs.key++
	out.Children[0].Text = strconv.Itoa(cs.key)
	out.Add(wbxml.S("AirSync:Status", "1"))
	opts := col.Child("AirSync:Options")
	// Client commands.
	responses := wbxml.E("AirSync:Responses")
	for _, c := range col.Path("AirSync:Commands").Elems() {
		sid := c.TextOf("AirSync:ServerId")
		switch {
		case c.Is("AirSync:Add"):
			nid := s.newIDLocked()
			class := map[int]string{8: "Calendar", 13: "Calendar", 9: "Contacts", 14: "Contacts", 7: "Tasks", 15: "Tasks"}[f.typ]
			it := &Item{ServerID: nid, Class: nonEmpty(class, "Email"), Data: c.Child("AirSync:ApplicationData"), version: 1}
			if it.Class == "Email" {
				it.Draft = true
			}
			f.items[nid] = it
			cs.snapshot[nid] = it.version
			responses.Add(wbxml.E("AirSync:Add", wbxml.S("AirSync:ClientId", c.TextOf("AirSync:ClientId")),
				wbxml.S("AirSync:ServerId", nid), wbxml.S("AirSync:Status", "1")))
		case c.Is("AirSync:Change"):
			it := f.items[sid]
			if it == nil {
				responses.Add(wbxml.E("AirSync:Change", wbxml.S("AirSync:ServerId", sid), wbxml.S("AirSync:Status", "8")))
				continue
			}
			d := c.Child("AirSync:ApplicationData")
			if it.Class == "Email" {
				if r := d.Child("Email:Read"); r != nil {
					it.Read = r.Value() == "1"
				}
				if fl := d.Child("Email:Flag"); fl != nil {
					it.Flagged = fl.TextOf("Email:Status") == "2"
				}
				if cats := d.Child("Email:Categories"); cats != nil {
					it.Categories = nil
					for _, x := range cats.All("Email:Category") {
						it.Categories = append(it.Categories, x.Value())
					}
				}
			} else {
				it.Data = d
			}
			it.version++
			cs.snapshot[sid] = it.version
		case c.Is("AirSync:Delete"):
			it := f.items[sid]
			if it == nil {
				responses.Add(wbxml.E("AirSync:Delete", wbxml.S("AirSync:ServerId", sid), wbxml.S("AirSync:Status", "8")))
				continue
			}
			delete(f.items, sid)
			delete(cs.snapshot, sid)
			if col.TextOf("AirSync:DeletesAsMoves") != "0" && f.typ != 4 {
				s.folders[Deleted].items[sid] = it
				it.version++
			}
		case c.Is("AirSync:Fetch"):
			it := f.items[sid]
			if it == nil {
				responses.Add(wbxml.E("AirSync:Fetch", wbxml.S("AirSync:ServerId", sid), wbxml.S("AirSync:Status", "8")))
				continue
			}
			d := wbxml.E("AirSync:ApplicationData", wbxml.S("Email:MIMEData", string(it.MIME)))
			responses.Add(wbxml.E("AirSync:Fetch", wbxml.S("AirSync:ServerId", sid), wbxml.S("AirSync:Status", "1"), d))
		}
	}
	// Server changes.
	if col.TextOf("AirSync:GetChanges") != "0" {
		ws := col.IntOf("AirSync:WindowSize", 100)
		cmds := wbxml.E("AirSync:Commands")
		var ids []string
		for sid := range f.items {
			ids = append(ids, sid)
		}
		for sid := range cs.snapshot {
			if f.items[sid] == nil {
				ids = append(ids, sid)
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			a, _ := strconv.Atoi(ids[i])
			b, _ := strconv.Atoi(ids[j])
			return a < b
		})
		n, more := 0, false
		for _, sid := range ids {
			it := f.items[sid]
			v, known := cs.snapshot[sid]
			var cmd *wbxml.Node
			switch {
			case it == nil:
				cmd = wbxml.E("AirSync:Delete", wbxml.S("AirSync:ServerId", sid))
			case !known:
				cmd = wbxml.E("AirSync:Add", wbxml.S("AirSync:ServerId", sid), s.itemData(it, opts))
			case v != it.version:
				cmd = wbxml.E("AirSync:Change", wbxml.S("AirSync:ServerId", sid), s.itemData(it, opts))
			default:
				continue
			}
			if n == ws {
				more = true
				break
			}
			n++
			cmds.Add(cmd)
			if it == nil {
				delete(cs.snapshot, sid)
			} else {
				cs.snapshot[sid] = it.version
			}
		}
		if more {
			out.Add(wbxml.E("AirSync:MoreAvailable"))
		}
		if len(cmds.Children) > 0 {
			out.Add(cmds)
		}
	}
	if len(responses.Children) > 0 {
		out.Add(responses)
	}
	data, _ := wbxml.Marshal(resp)
	cs.last = data
	return data
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) estimate(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	col := req.Path("ItemEstimate:Collections/ItemEstimate:Collection")
	id := col.TextOf("ItemEstimate:CollectionId")
	cs, f := s.colls[id], s.folders[id]
	if f == nil || cs == nil || col.TextOf("AirSync:SyncKey") != strconv.Itoa(cs.key) {
		return wbxml.E("ItemEstimate:GetItemEstimate", wbxml.E("ItemEstimate:Response", wbxml.S("ItemEstimate:Status", "4")))
	}
	n := 0
	for sid, it := range f.items {
		if v, ok := cs.snapshot[sid]; !ok || v != it.version {
			n++
		}
	}
	for sid := range cs.snapshot {
		if f.items[sid] == nil {
			n++
		}
	}
	return wbxml.E("ItemEstimate:GetItemEstimate", wbxml.E("ItemEstimate:Response", wbxml.S("ItemEstimate:Status", "1"),
		wbxml.E("ItemEstimate:Collection", wbxml.S("ItemEstimate:CollectionId", id), wbxml.S("ItemEstimate:Estimate", strconv.Itoa(n)))))
}

func (s *Server) itemOps(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := req.Child("ItemOperations:Fetch")
	fid, sid := f.TextOf("AirSync:CollectionId"), f.TextOf("AirSync:ServerId")
	resp := wbxml.E("ItemOperations:ItemOperations", wbxml.S("ItemOperations:Status", "1"))
	fo := s.folders[fid]
	if fo == nil || fo.items[sid] == nil {
		return resp.Add(wbxml.E("ItemOperations:Response", wbxml.E("ItemOperations:Fetch", wbxml.S("ItemOperations:Status", "6"))))
	}
	it := fo.items[sid]
	return resp.Add(wbxml.E("ItemOperations:Response", wbxml.E("ItemOperations:Fetch",
		wbxml.S("ItemOperations:Status", "1"), wbxml.S("AirSync:CollectionId", fid), wbxml.S("AirSync:ServerId", sid),
		wbxml.S("AirSync:Class", it.Class), wbxml.E("ItemOperations:Properties", wbxml.E("AirSyncBase:Body",
			wbxml.S("AirSyncBase:Type", "4"), wbxml.S("AirSyncBase:EstimatedDataSize", strconv.Itoa(len(it.MIME))),
			wbxml.S("AirSyncBase:Data", string(it.MIME)))))))
}

func (s *Server) moveItems(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := wbxml.E("Move:MoveItems")
	for _, m := range req.All("Move:Move") {
		sid, src, dst := m.TextOf("Move:SrcMsgId"), m.TextOf("Move:SrcFldId"), m.TextOf("Move:DstFldId")
		r := wbxml.E("Move:Response", wbxml.S("Move:SrcMsgId", sid))
		sf, df := s.folders[src], s.folders[dst]
		switch {
		case sf == nil || sf.items[sid] == nil:
			r.Add(wbxml.S("Move:Status", "1"))
		case df == nil:
			r.Add(wbxml.S("Move:Status", "2"))
		case src == dst:
			r.Add(wbxml.S("Move:Status", "4"))
		default:
			it := sf.items[sid]
			delete(sf.items, sid)
			if cs := s.colls[src]; cs != nil {
				delete(cs.snapshot, sid)
			}
			nid := s.newIDLocked()
			it.ServerID = nid
			it.version++
			df.items[nid] = it
			r.Add(wbxml.S("Move:Status", "3"), wbxml.S("Move:DstMsgId", nid))
		}
		resp.Add(r)
	}
	return resp
}

func (s *Server) recordSent(cmd string, mime []byte, save bool, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, SentMail{Cmd: cmd, MIME: mime, SaveInSent: save, Source: source})
	if save {
		id := s.newIDLocked()
		s.folders[Sent].items[id] = &Item{ServerID: id, Class: "Email", MIME: mime, Read: true, version: 1}
		s.notify()
	}
}

func (s *Server) sendMail(cmd string, req *wbxml.Node) *wbxml.Node {
	mime := req.Child("ComposeMail:Mime").Bytes()
	if len(mime) == 0 {
		return wbxml.E("ComposeMail:"+cmd, wbxml.S("ComposeMail:Status", "107"))
	}
	source := req.Path("ComposeMail:Source").TextOf("ComposeMail:ItemId")
	s.recordSent(cmd, mime, req.Has("ComposeMail:SaveInSentItems"), source)
	return nil // empty body = success
}

func (s *Server) meetingResponse(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := wbxml.E("MeetingResponse:MeetingResponse")
	for _, r := range req.All("MeetingResponse:Request") {
		rid := r.TextOf("MeetingResponse:RequestId")
		s.responses = append(s.responses, rid+":"+r.TextOf("MeetingResponse:UserResponse"))
		resp.Add(wbxml.E("MeetingResponse:Result", wbxml.S("MeetingResponse:RequestId", rid),
			wbxml.S("MeetingResponse:Status", "1"), wbxml.S("MeetingResponse:CalendarId", "cal-"+rid)))
	}
	return resp
}

func (s *Server) search(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := req.Child("Search:Store")
	store := wbxml.E("Search:Store", wbxml.S("Search:Status", "1"))
	switch st.TextOf("Search:Name") {
	case "GAL":
		q := strings.ToLower(st.TextOf("Search:Query"))
		for _, e := range s.gal {
			if strings.Contains(strings.ToLower(e.Name+" "+e.Email), q) {
				store.Add(wbxml.E("Search:Result", wbxml.E("Search:Properties",
					wbxml.S("GAL:DisplayName", e.Name), wbxml.S("GAL:EmailAddress", e.Email))))
			}
		}
	default:
		and := st.Path("Search:Query/Search:And")
		q := strings.ToLower(and.TextOf("Search:FreeText"))
		var ids []string
		for fid := range s.folders {
			ids = append(ids, fid)
		}
		sort.Strings(ids)
		for _, fid := range ids {
			if cid := and.TextOf("AirSync:CollectionId"); cid != "" && cid != fid {
				continue
			}
			for sid, it := range s.folders[fid].items {
				if it.MIME != nil && strings.Contains(strings.ToLower(string(it.MIME)), q) {
					store.Add(wbxml.E("Search:Result", wbxml.S("AirSync:Class", "Email"), wbxml.S("Search:LongId", fid+":"+sid),
						wbxml.S("AirSync:CollectionId", fid), wbxml.E("Search:Properties", mailData(it, nil).Children...)))
				}
			}
		}
	}
	return wbxml.E("Search:Search", wbxml.S("Search:Status", "1"), wbxml.E("Search:Response", store))
}

// pendingFor reports which of the folders have changes not yet synced.
func (s *Server) pendingFor(ids []string) []string {
	var out []string
	for _, id := range ids {
		f, cs := s.folders[id], s.colls[id]
		if f == nil || cs == nil {
			continue
		}
		changed := false
		for sid, it := range f.items {
			if v, ok := cs.snapshot[sid]; !ok || v != it.version {
				changed = true
			}
		}
		for sid := range cs.snapshot {
			if f.items[sid] == nil {
				changed = true
			}
		}
		if changed {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) ping(req *wbxml.Node) *wbxml.Node {
	hb := req.IntOf("Ping:HeartbeatInterval", 0)
	var ids []string
	for _, f := range req.Path("Ping:Folders").All("Ping:Folder") {
		ids = append(ids, f.TextOf("Ping:Id"))
	}
	s.mu.Lock()
	if s.MinHeartbeat > 0 && hb < s.MinHeartbeat {
		s.mu.Unlock()
		return wbxml.E("Ping:Ping", wbxml.S("Ping:Status", "5"), wbxml.S("Ping:HeartbeatInterval", strconv.Itoa(s.MinHeartbeat)))
	}
	if len(ids) == 0 {
		s.mu.Unlock()
		return wbxml.E("Ping:Ping", wbxml.S("Ping:Status", "3"))
	}
	wait := min(time.Duration(hb)*time.Second, s.MaxPingWait)
	s.mu.Unlock()
	deadline := time.After(wait)
	for {
		s.mu.Lock()
		changed := s.pendingFor(ids)
		ch := s.changed
		s.mu.Unlock()
		if len(changed) > 0 {
			fs := wbxml.E("Ping:Folders")
			for _, id := range changed {
				fs.Add(wbxml.S("Ping:Folder", id))
			}
			return wbxml.E("Ping:Ping", wbxml.S("Ping:Status", "2"), fs)
		}
		select {
		case <-ch:
		case <-deadline:
			return wbxml.E("Ping:Ping", wbxml.S("Ping:Status", "1"))
		}
	}
}

func (s *Server) settings(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := wbxml.E("Settings:Settings", wbxml.S("Settings:Status", "1"))
	if o := req.Child("Settings:Oof"); o != nil {
		if set := o.Child("Settings:Set"); set != nil {
			s.oof.state = set.IntOf("Settings:OofState", 0)
			s.oof.start, s.oof.end = set.TextOf("Settings:StartTime"), set.TextOf("Settings:EndTime")
			for _, m := range set.All("Settings:OofMessage") {
				en := m.TextOf("Settings:Enabled") == "1"
				switch {
				case m.Has("Settings:AppliesToInternal"):
					s.oof.internal = m.TextOf("Settings:ReplyMessage")
				case m.Has("Settings:AppliesToExternalKnown"):
					s.oof.external, s.oof.extKnown = m.TextOf("Settings:ReplyMessage"), en
				case m.Has("Settings:AppliesToExternalUnknown"):
					s.oof.extUnknown = en
				}
			}
			resp.Add(wbxml.E("Settings:Oof", wbxml.S("Settings:Status", "1")))
		} else {
			g := wbxml.E("Settings:Get", wbxml.S("Settings:OofState", strconv.Itoa(s.oof.state)))
			if s.oof.start != "" {
				g.Add(wbxml.S("Settings:StartTime", s.oof.start), wbxml.S("Settings:EndTime", s.oof.end))
			}
			g.Add(wbxml.E("Settings:OofMessage", wbxml.E("Settings:AppliesToInternal"), wbxml.S("Settings:Enabled", b2s(s.oof.state != 0)),
				optS("Settings:ReplyMessage", s.oof.internal), wbxml.S("Settings:BodyType", "Text")))
			g.Add(wbxml.E("Settings:OofMessage", wbxml.E("Settings:AppliesToExternalKnown"), wbxml.S("Settings:Enabled", b2s(s.oof.extKnown)),
				optS("Settings:ReplyMessage", s.oof.external), wbxml.S("Settings:BodyType", "Text")))
			g.Add(wbxml.E("Settings:OofMessage", wbxml.E("Settings:AppliesToExternalUnknown"), wbxml.S("Settings:Enabled", b2s(s.oof.extUnknown)),
				optS("Settings:ReplyMessage", s.oof.external), wbxml.S("Settings:BodyType", "Text")))
			resp.Add(wbxml.E("Settings:Oof", wbxml.S("Settings:Status", "1"), g))
		}
	}
	if di := req.Path("Settings:DeviceInformation/Settings:Set"); di != nil {
		for _, c := range di.Children {
			s.deviceInfo[c.Tag.Name()] = c.Value()
		}
		resp.Add(wbxml.E("Settings:DeviceInformation", wbxml.S("Settings:Status", "1")))
	}
	if req.Has("Settings:UserInformation") {
		resp.Add(wbxml.E("Settings:UserInformation", wbxml.S("Settings:Status", "1"), wbxml.E("Settings:Get",
			wbxml.E("Settings:EmailAddresses", wbxml.S("Settings:SmtpAddress", "alice@example.com"),
				wbxml.S("Settings:PrimarySmtpAddress", "alice@example.com")))))
	}
	return resp
}

func (s *Server) resolve(req *wbxml.Node) *wbxml.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := wbxml.E("ResolveRecipients:ResolveRecipients", wbxml.S("ResolveRecipients:Status", "1"))
	for _, to := range req.All("ResolveRecipients:To") {
		q := strings.ToLower(to.Value())
		r := wbxml.E("ResolveRecipients:Response", wbxml.S("ResolveRecipients:To", to.Value()), wbxml.S("ResolveRecipients:Status", "1"))
		for _, e := range s.gal {
			if strings.Contains(strings.ToLower(e.Name+" "+e.Email), q) {
				r.Add(wbxml.E("ResolveRecipients:Recipient", wbxml.S("ResolveRecipients:Type", "1"),
					wbxml.S("ResolveRecipients:DisplayName", e.Name), wbxml.S("ResolveRecipients:EmailAddress", e.Email)))
			}
		}
		resp.Add(r)
	}
	return resp
}
