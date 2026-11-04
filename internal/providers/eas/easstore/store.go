// SPDX-License-Identifier: AGPL-3.0-or-later

// Package easstore implements eas.PIMStore on the calendar, task and
// contact tables of internal/store (iCalendar in cal_objects, vCard in
// contacts), so EAS calendars, contacts and tasks appear next to CalDAV/
// CardDAV data.
//
// Conventions (shared with other writers of these tables):
//   - Collections have RemoteID "eas:<ServerId>"; tasks folders are
//     calendars with RemoteID "eas:tasks/<ServerId>" holding VTODO objects.
//   - An object's Href is its EAS ServerId; objects without Href were
//     created locally and are sent with Sync Add.
//   - ETag is "eas-" + a hash of the iCalendar/vCard text as last
//     synchronized. A local editor that changes ICal/VCard without updating
//     the ETag marks the object as modified (sent with Sync Change).
//   - The SyncToken column holds JSON {"k": SyncKey, "ids": [ServerIds]};
//     ServerIds missing locally are reported as local deletions.
package easstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Store adapts *store.Store to eas.PIMStore. It is safe for use by one
// synchronization at a time.
type Store struct {
	st *store.Store

	mu    sync.Mutex
	known map[string]map[string]bool // collection key -> known server ids
}

var _ eas.PIMStore = (*Store)(nil)

// New returns an adapter on st.
func New(st *store.Store) *Store { return &Store{st: st, known: map[string]map[string]bool{}} }

const (
	remotePrefix = "eas:"
	tasksPrefix  = "tasks/"
)

// remoteID is the RemoteID of a local collection: "eas:<id>" or, for task
// folders, "eas:tasks/<id>".
func remoteID(class, serverID string) string {
	if class == "Tasks" {
		return remotePrefix + tasksPrefix + serverID
	}
	return remotePrefix + serverID
}

type token struct {
	Key string   `json:"k"`
	IDs []string `json:"ids,omitempty"`
}

func collKey(c eas.PIMCollection) string { return c.Class + ":" + strconv.FormatInt(c.LocalID, 10) }

func etag(data string) string {
	h := sha256.Sum256([]byte(data))
	return "eas-" + hex.EncodeToString(h[:12])
}

func decodeToken(s string) token {
	var t token
	if s == "" {
		return t
	}
	if err := json.Unmarshal([]byte(s), &t); err != nil {
		return token{}
	}
	return t
}

// Collection implements eas.PIMStore.
func (s *Store) Collection(ctx context.Context, accountID int64, f eas.Folder) (eas.PIMCollection, error) {
	c := eas.PIMCollection{ServerID: f.ServerID, Class: f.Class()}
	var raw string
	var err error
	switch c.Class {
	case "Calendar", "Tasks":
		c.LocalID, err = s.st.UpsertCalendar(ctx, store.Calendar{AccountID: accountID, RemoteID: remoteID(c.Class, f.ServerID), Name: f.Name})
		if err == nil {
			raw, err = s.st.CalendarSyncToken(ctx, c.LocalID)
		}
	case "Contacts":
		c.LocalID, err = s.st.UpsertAddressbook(ctx, store.Addressbook{AccountID: accountID, RemoteID: remotePrefix + f.ServerID, Name: f.Name})
		if err == nil {
			raw, err = s.st.AddressbookSyncToken(ctx, c.LocalID)
		}
	default:
		return c, fmt.Errorf("easstore: unsupported class %q", c.Class)
	}
	if err != nil {
		return c, err
	}
	t := decodeToken(raw)
	c.SyncKey = t.Key
	ids := map[string]bool{}
	for _, id := range t.IDs {
		ids[id] = true
	}
	s.mu.Lock()
	s.known[collKey(c)] = ids
	s.mu.Unlock()
	return c, nil
}

// Collections implements eas.PIMStore.
func (s *Store) Collections(ctx context.Context, accountID int64) ([]eas.PIMCollection, error) {
	var out []eas.PIMCollection
	cals, err := s.st.ListAccountCalendars(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, c := range cals {
		if id, ok := strings.CutPrefix(c.RemoteID, remotePrefix); ok {
			class := "Calendar"
			if t, ok := strings.CutPrefix(id, tasksPrefix); ok {
				class, id = "Tasks", t
			}
			out = append(out, eas.PIMCollection{LocalID: c.ID, ServerID: id, Class: class, SyncKey: decodeToken(c.SyncToken).Key})
		}
	}
	books, err := s.st.ListAccountAddressbooks(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, b := range books {
		if id, ok := strings.CutPrefix(b.RemoteID, remotePrefix); ok {
			out = append(out, eas.PIMCollection{LocalID: b.ID, ServerID: id, Class: "Contacts", SyncKey: decodeToken(b.SyncToken).Key})
		}
	}
	return out, nil
}

// RemoveCollection implements eas.PIMStore.
func (s *Store) RemoveCollection(ctx context.Context, c eas.PIMCollection) error {
	s.mu.Lock()
	delete(s.known, collKey(c))
	s.mu.Unlock()
	var err error
	if c.Class == "Contacts" {
		err = s.st.DeleteAddressbook(ctx, c.LocalID)
	} else {
		err = s.st.DeleteCalendar(ctx, c.LocalID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Store) knownIDs(c eas.PIMCollection) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.known[collKey(c)]
	if m == nil {
		m = map[string]bool{}
		s.known[collKey(c)] = m
	}
	return m
}

// SetSyncKey implements eas.PIMStore; it also persists the known ids.
func (s *Store) SetSyncKey(ctx context.Context, c eas.PIMCollection, key string) error {
	ids := s.knownIDs(c)
	s.mu.Lock()
	t := token{Key: key}
	for id := range ids {
		t.IDs = append(t.IDs, id)
	}
	s.mu.Unlock()
	sort.Strings(t.IDs)
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if c.Class == "Contacts" {
		return s.st.SetAddressbookSyncToken(ctx, c.LocalID, string(data))
	}
	return s.st.SetCalendarSyncToken(ctx, c.LocalID, string(data))
}

// Reset implements eas.PIMStore.
func (s *Store) Reset(ctx context.Context, c eas.PIMCollection) error {
	s.mu.Lock()
	s.known[collKey(c)] = map[string]bool{}
	s.mu.Unlock()
	if c.Class == "Contacts" {
		cs, err := s.st.ListContacts(ctx, c.LocalID)
		if err != nil {
			return err
		}
		for _, x := range cs {
			if x.Href != "" {
				if err := s.st.DeleteContact(ctx, x.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
			}
		}
		return nil
	}
	objs, err := s.st.ListCalObjects(ctx, c.LocalID)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if o.Href != "" {
			if err := s.st.DeleteCalObject(ctx, o.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
	}
	return nil
}

func (s *Store) contactByHref(ctx context.Context, book int64, href string) (store.Contact, bool, error) {
	cs, err := s.st.ListContacts(ctx, book)
	if err != nil {
		return store.Contact{}, false, err
	}
	for _, c := range cs {
		if c.Href == href {
			return c, true, nil
		}
	}
	return store.Contact{}, false, nil
}

// Put implements eas.PIMStore.
func (s *Store) Put(ctx context.Context, c eas.PIMCollection, serverID string, item eas.PIMItem) error {
	s.knownIDs(c)[serverID] = true
	switch {
	case item.Contact != nil:
		old, exists, err := s.contactByHref(ctx, c.LocalID, serverID)
		if err != nil {
			return err
		}
		uid := "eas-" + serverID
		if exists {
			uid = old.UID
		}
		vc := contactVCard(uid, item.Contact)
		_, _, err = s.st.UpsertContact(ctx, store.Contact{AddressbookID: c.LocalID, UID: uid, Href: serverID, ETag: etag(vc),
			VCard: vc, DisplayName: contactDisplayName(item.Contact), Emails: item.Contact.Emails})
		return err
	case item.Event != nil || item.Task != nil:
		uid := "eas-" + serverID
		if old, err := s.st.CalObjectByHref(ctx, c.LocalID, serverID); err == nil {
			uid = old.UID
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		} else if item.Event != nil && item.Event.UID != "" {
			uid = item.Event.UID
		}
		o := store.CalObject{CalendarID: c.LocalID, UID: uid, Href: serverID}
		var inst []store.Instance
		if e := item.Event; e != nil {
			o.Kind, o.ICal = store.CalEvent, eventICal(uid, e)
			o.DTStart, o.DTEnd, o.Summary, o.Status = e.Start, e.End, e.Subject, "CONFIRMED"
			if e.Recurrence != nil {
				o.RRule = rrule(e.Recurrence)
			}
			if !e.Start.IsZero() {
				end := e.End
				if end.Before(e.Start) {
					end = e.Start
				}
				// Only the first occurrence; series are expanded by the
				// PIM module from RRule.
				inst = []store.Instance{{Start: e.Start, End: end}}
			}
		} else {
			t := item.Task
			o.Kind, o.ICal = store.CalTodo, taskICal(uid, t)
			o.DTStart, o.Due, o.Summary = t.Start, t.Due, t.Subject
			o.Status = "NEEDS-ACTION"
			if t.Complete {
				o.Status, o.CompletedAt = "COMPLETED", t.CompletedAt
				if o.CompletedAt.IsZero() {
					o.CompletedAt = time.Now().UTC()
				}
			}
		}
		o.ETag = etag(o.ICal)
		id, _, err := s.st.UpsertCalObject(ctx, o)
		if err != nil {
			return err
		}
		if o.Kind == store.CalEvent {
			return s.st.ReplaceInstances(ctx, id, inst)
		}
		return nil
	}
	return nil
}

// Remove implements eas.PIMStore.
func (s *Store) Remove(ctx context.Context, c eas.PIMCollection, serverID string) error {
	delete(s.knownIDs(c), serverID)
	if c.Class == "Contacts" {
		old, ok, err := s.contactByHref(ctx, c.LocalID, serverID)
		if err != nil || !ok {
			return err
		}
		if err := s.st.DeleteContact(ctx, old.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return nil
	}
	o, err := s.st.CalObjectByHref(ctx, c.LocalID, serverID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.st.DeleteCalObject(ctx, o.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// LocalChanges implements eas.PIMStore.
func (s *Store) LocalChanges(ctx context.Context, c eas.PIMCollection) ([]eas.LocalChange, error) {
	var out []eas.LocalChange
	present := map[string]bool{}
	if c.Class == "Contacts" {
		cs, err := s.st.ListContacts(ctx, c.LocalID)
		if err != nil {
			return nil, err
		}
		for _, x := range cs {
			if x.Href != "" {
				present[x.Href] = true
			}
			if x.Href != "" && x.ETag == etag(x.VCard) {
				continue
			}
			ct, _ := parseVCard(x.VCard)
			ch := eas.LocalChange{Kind: "Change", ServerID: x.Href, Item: eas.PIMItem{Class: "Contacts", Contact: ct}}
			if x.Href == "" {
				ch.Kind, ch.LocalRef = "Add", "c"+strconv.FormatInt(x.ID, 10)
			}
			out = append(out, ch)
		}
	} else {
		objs, err := s.st.ListCalObjects(ctx, c.LocalID)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			if o.Href != "" {
				present[o.Href] = true
			}
			if o.Href != "" && o.ETag == etag(o.ICal) {
				continue
			}
			var it eas.PIMItem
			switch {
			case o.Kind == store.CalEvent && c.Class == "Calendar":
				e, _ := parseEventICal(o.ICal)
				e.UID = o.UID
				it = eas.PIMItem{Class: "Calendar", Event: e}
			case o.Kind == store.CalTodo && c.Class == "Tasks":
				t, _ := parseTaskICal(o.ICal)
				it = eas.PIMItem{Class: "Tasks", Task: t}
			default:
				continue // not representable in this collection
			}
			ch := eas.LocalChange{Kind: "Change", ServerID: o.Href, Item: it}
			if o.Href == "" {
				ch.Kind, ch.LocalRef = "Add", "o"+strconv.FormatInt(o.ID, 10)
			}
			out = append(out, ch)
		}
	}
	s.mu.Lock()
	var deleted []string
	for id := range s.known[collKey(c)] {
		if !present[id] {
			deleted = append(deleted, id)
		}
	}
	s.mu.Unlock()
	sort.Strings(deleted)
	for _, id := range deleted {
		out = append(out, eas.LocalChange{Kind: "Delete", ServerID: id})
	}
	return out, nil
}

// Ack implements eas.PIMStore.
func (s *Store) Ack(ctx context.Context, c eas.PIMCollection, ch eas.LocalChange, serverID string) error {
	switch ch.Kind {
	case "Delete":
		delete(s.knownIDs(c), ch.ServerID)
		return nil
	case "Add":
		s.knownIDs(c)[serverID] = true
	}
	ref := ch.LocalRef
	if c.Class == "Contacts" {
		var x store.Contact
		var err error
		if ch.Kind == "Add" {
			id, _ := strconv.ParseInt(strings.TrimPrefix(ref, "c"), 10, 64)
			x, err = s.st.GetContact(ctx, id)
		} else {
			var ok bool
			x, ok, err = s.contactByHref(ctx, c.LocalID, ch.ServerID)
			if err == nil && !ok {
				return nil
			}
		}
		if err != nil {
			return err
		}
		x.Href, x.ETag = nonEmpty(serverID, x.Href), etag(x.VCard)
		_, _, err = s.st.UpsertContact(ctx, x)
		return err
	}
	var o store.CalObject
	var err error
	if ch.Kind == "Add" {
		id, _ := strconv.ParseInt(strings.TrimPrefix(ref, "o"), 10, 64)
		o, err = s.st.GetCalObject(ctx, id)
	} else {
		o, err = s.st.CalObjectByHref(ctx, c.LocalID, ch.ServerID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	o.Href, o.ETag = nonEmpty(serverID, o.Href), etag(o.ICal)
	_, _, err = s.st.UpsertCalObject(ctx, o)
	return err
}
