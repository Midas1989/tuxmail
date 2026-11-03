// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/vcard"
	"github.com/Midas1989/Mailclient/internal/store"
)

const localAddressbookName = "TuxMail"

// maxPhoto bounds contact photos.
const maxPhoto = 2 << 20

// ensureAddressbooks creates the local address book on first use.
func (a *App) ensureAddressbooks(ctx context.Context) ([]store.AddressbookWithMeta, error) {
	books, err := a.st.ListAddressbooksWithMeta(ctx)
	if err != nil {
		return nil, err
	}
	hasLocal, hasDefault := false, false
	for _, b := range books {
		if b.AccountID == 0 {
			hasLocal = true
		}
		hasDefault = hasDefault || b.Meta.IsDefault
	}
	if !hasLocal {
		id, err := a.st.CreateAddressbook(ctx, store.Addressbook{Name: localAddressbookName})
		if err != nil {
			return nil, err
		}
		if err := a.st.PutAddressbookMeta(ctx, store.AddressbookMeta{AddressbookID: id, Kind: store.BookKindLocal, IsDefault: !hasDefault}); err != nil {
			return nil, err
		}
		return a.st.ListAddressbooksWithMeta(ctx)
	}
	if !hasDefault && len(books) > 0 {
		m := books[0].Meta
		m.IsDefault = true
		if err := a.st.PutAddressbookMeta(ctx, m); err != nil {
			return nil, err
		}
		return a.st.ListAddressbooksWithMeta(ctx)
	}
	return books, nil
}

// ListAddressBooks returns all address books with their contact counts.
func (a *App) ListAddressBooks(ctx context.Context) ([]PimAddressBook, error) {
	books, err := a.ensureAddressbooks(ctx)
	if err != nil {
		return nil, err
	}
	names := a.accountNames(ctx)
	counts := map[int64]int{}
	if all, err := a.st.ListAllContacts(ctx, 0); err == nil {
		for _, c := range all {
			counts[c.AddressbookID]++
		}
	}
	out := make([]PimAddressBook, 0, len(books))
	for _, b := range books {
		out = append(out, PimAddressBook{ID: b.ID, AccountID: b.AccountID, AccountName: names[b.AccountID], Name: b.Name, Kind: b.Meta.Kind,
			IsDefault: b.Meta.IsDefault, Count: counts[b.ID], URL: b.Meta.URL, LastSync: rfc3339(b.Meta.LastSyncAt), Error: b.Meta.LastError})
	}
	return out, nil
}

// SaveAddressBook creates or renames a local address book.
func (a *App) SaveAddressBook(ctx context.Context, in AddressBookInput) (PimAddressBook, error) {
	name := strings.TrimSpace(in.Name)
	if in.ID == 0 {
		if name == "" {
			return PimAddressBook{}, invalid("name required")
		}
		id, err := a.st.CreateAddressbook(ctx, store.Addressbook{Name: name})
		if err != nil {
			return PimAddressBook{}, err
		}
		in.ID = id
		if err := a.st.PutAddressbookMeta(ctx, store.AddressbookMeta{AddressbookID: id, Kind: store.BookKindLocal,
			IsDefault: in.IsDefault != nil && *in.IsDefault}); err != nil {
			return PimAddressBook{}, err
		}
	} else {
		ab, err := a.st.GetAddressbook(ctx, in.ID)
		if err != nil {
			return PimAddressBook{}, err
		}
		if name != "" {
			ab.Name = name
			if err := a.st.UpdateAddressbook(ctx, ab); err != nil {
				return PimAddressBook{}, err
			}
		}
		if in.IsDefault != nil {
			books, _ := a.st.ListAddressbooksWithMeta(ctx)
			for _, b := range books {
				if b.ID == in.ID {
					m := b.Meta
					m.IsDefault = *in.IsDefault
					if err := a.st.PutAddressbookMeta(ctx, m); err != nil {
						return PimAddressBook{}, err
					}
				}
			}
		}
	}
	a.publishContactsChanged()
	books, err := a.ListAddressBooks(ctx)
	if err != nil {
		return PimAddressBook{}, err
	}
	for _, b := range books {
		if b.ID == in.ID {
			return b, nil
		}
	}
	return PimAddressBook{}, errNotFound
}

// DeleteAddressBook deletes a local address book with its contacts.
func (a *App) DeleteAddressBook(ctx context.Context, id int64) error {
	books, err := a.st.ListAddressbooksWithMeta(ctx)
	if err != nil {
		return err
	}
	for _, b := range books {
		if b.ID == id {
			if b.Meta.Kind != store.BookKindLocal {
				return invalid("address books of accounts are removed with their account")
			}
			if err := a.st.DeleteAddressbook(ctx, id); err != nil {
				return err
			}
			a.publishContactsChanged()
			return nil
		}
	}
	return errNotFound
}

func (a *App) bookKind(ctx context.Context, id int64) string {
	books, err := a.st.ListAddressbooksWithMeta(ctx)
	if err != nil {
		return store.BookKindLocal
	}
	for _, b := range books {
		if b.ID == id {
			return b.Meta.Kind
		}
	}
	return store.BookKindLocal
}

func (a *App) defaultAddressbook(ctx context.Context) (int64, error) {
	books, err := a.ensureAddressbooks(ctx)
	if err != nil {
		return 0, err
	}
	for _, b := range books {
		if b.Meta.IsDefault {
			return b.ID, nil
		}
	}
	return books[0].ID, nil
}

// toPimContact converts a stored contact (without photo data).
func (a *App) toPimContact(sc store.Contact, handles map[int64][]store.Handle) (PimContact, *ical.Component, error) {
	cards, err := vcard.Parse(sc.VCard)
	if err != nil {
		return PimContact{}, nil, err
	}
	ct := vcard.Read(cards[0])
	pc := PimContact{ID: sc.ID, AddressBookID: sc.AddressbookID, PersonID: sc.PersonID, DisplayName: vcard.DisplayName(ct),
		Contact: ct, Channels: []PersonHandle{}}
	if pc.UID == "" {
		pc.UID = sc.UID
	}
	pc.HasPhoto = len(ct.Photo) > 0 || ct.PhotoURI != ""
	if len(ct.Photo) > 0 {
		pc.PhotoURL = fmt.Sprintf("/api/contacts/%d/photo?v=%s", sc.ID, shortHash(sc.ETag+sc.VCard[len(sc.VCard)/2:]))
	} else if ct.PhotoURI != "" {
		pc.PhotoURL = ct.PhotoURI
	}
	pc.Photo = ""
	pc.Contact.Photo = nil
	for _, h := range handles[sc.PersonID] {
		if frontendHandleKinds[h.Kind] {
			pc.Channels = append(pc.Channels, PersonHandle{Kind: h.Kind, Value: h.Value})
		}
	}
	return pc, cards[0], nil
}

func shortHash(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

func (a *App) personHandles(ctx context.Context) map[int64][]store.Handle {
	out := map[int64][]store.Handle{}
	if ps, err := a.st.ListPersonsWithHandles(ctx); err == nil {
		for _, p := range ps {
			out[p.ID] = p.Handles
		}
	}
	return out
}

func contactMatches(c PimContact, q string) bool {
	if q == "" {
		return true
	}
	parts := []string{c.DisplayName, c.Nickname, c.Org, c.Title, c.Note}
	for _, e := range c.Emails {
		parts = append(parts, e.Value)
	}
	for _, p := range c.Phones {
		parts = append(parts, p.Value, strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' || r == '+' {
				return r
			}
			return -1
		}, p.Value))
	}
	hay := strings.ToLower(strings.Join(parts, " "))
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// ListContacts returns contacts matching q, ordered by name.
func (a *App) ListContacts(ctx context.Context, q ContactQuery) ([]PimContact, error) {
	if _, err := a.ensureAddressbooks(ctx); err != nil {
		return nil, err
	}
	all, err := a.st.ListAllContacts(ctx, q.AddressBookID)
	if err != nil {
		return nil, err
	}
	handles := a.personHandles(ctx)
	out := []PimContact{}
	for _, sc := range all {
		pc, _, err := a.toPimContact(sc, handles)
		if err != nil {
			continue
		}
		if q.Group != "" && !containsFold(pc.Categories, q.Group) {
			continue
		}
		if !contactMatches(pc, q.Search) {
			continue
		}
		out = append(out, pc)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(sortName(out[i])) < strings.ToLower(sortName(out[j]))
	})
	return out, nil
}

func sortName(c PimContact) string {
	if c.N.Family != "" {
		return c.N.Family + " " + c.N.Given
	}
	return c.DisplayName
}

// ListContactGroups returns the categories used by contacts.
func (a *App) ListContactGroups(ctx context.Context) ([]ContactGroup, error) {
	all, err := a.ListContacts(ctx, ContactQuery{})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, c := range all {
		for _, g := range c.Categories {
			counts[g]++
		}
	}
	out := []ContactGroup{}
	for g, n := range counts {
		out = append(out, ContactGroup{Name: g, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// GetContact returns one contact.
func (a *App) GetContact(ctx context.Context, id int64) (PimContact, error) {
	sc, err := a.st.GetContact(ctx, id)
	if err != nil {
		return PimContact{}, err
	}
	pc, _, err := a.toPimContact(sc, a.personHandles(ctx))
	return pc, err
}

// ContactPhoto returns the embedded photo of a contact.
func (a *App) ContactPhoto(ctx context.Context, id int64) (AttachmentContent, error) {
	sc, err := a.st.GetContact(ctx, id)
	if err != nil {
		return AttachmentContent{}, err
	}
	cards, err := vcard.Parse(sc.VCard)
	if err != nil {
		return AttachmentContent{}, err
	}
	ct := vcard.Read(cards[0])
	if len(ct.Photo) == 0 {
		return AttachmentContent{}, errNotFound
	}
	mt := ct.PhotoType
	if !strings.HasPrefix(mt, "image/") || mt == "image/svg+xml" {
		mt = http.DetectContentType(ct.Photo)
	}
	return AttachmentContent{Filename: "photo", MIMEType: mt, Data: ct.Photo}, nil
}

func decodePhoto(s string) ([]byte, string, error) {
	meta, data, ok := strings.Cut(strings.TrimPrefix(s, "data:"), ",")
	mt, isB64 := strings.CutSuffix(meta, ";base64")
	if !ok || !isB64 || !strings.HasPrefix(mt, "image/") {
		return nil, "", invalid("photo must be a base64 image data URL")
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, "", invalid("invalid photo data")
	}
	if len(b) > maxPhoto {
		return nil, "", invalid("photo is too large")
	}
	return b, mt, nil
}

// SaveContact creates (ID 0) or updates a contact. Unknown vCard
// properties of existing cards are preserved.
func (a *App) SaveContact(ctx context.Context, in PimContact) (PimContact, error) {
	var (
		sc   store.Contact
		card *ical.Component
		old  vcard.Contact
	)
	if in.ID != 0 {
		var err error
		if sc, err = a.st.GetContact(ctx, in.ID); err != nil {
			return PimContact{}, err
		}
		cards, err := vcard.Parse(sc.VCard)
		if err != nil {
			return PimContact{}, err
		}
		card, old = cards[0], vcard.Read(cards[0])
		if in.AddressBookID != 0 && in.AddressBookID != sc.AddressbookID {
			// Move: delete from the old book, create in the new one.
			if err := a.deleteContact(ctx, sc); err != nil {
				return PimContact{}, err
			}
			sc = store.Contact{AddressbookID: in.AddressBookID, UID: sc.UID, PersonID: sc.PersonID}
		}
	} else {
		book := in.AddressBookID
		if book == 0 {
			var err error
			if book, err = a.defaultAddressbook(ctx); err != nil {
				return PimContact{}, err
			}
		}
		uid := in.UID
		if uid == "" {
			uid = newID("") + "@tuxmail"
		}
		sc = store.Contact{AddressbookID: book, UID: uid}
		card = vcard.New(uid, "")
	}
	ct := in.Contact
	ct.UID = sc.UID
	switch {
	case in.Photo == "-":
		ct.Photo, ct.PhotoType, ct.PhotoURI = nil, "", ""
	case strings.HasPrefix(in.Photo, "data:"):
		b, mt, err := decodePhoto(in.Photo)
		if err != nil {
			return PimContact{}, err
		}
		ct.Photo, ct.PhotoType, ct.PhotoURI = b, mt, ""
	default:
		ct.Photo, ct.PhotoType, ct.PhotoURI = old.Photo, old.PhotoType, old.PhotoURI
	}
	if strings.TrimSpace(vcard.DisplayName(ct)) == "" {
		return PimContact{}, invalid("name, organization, e-mail or phone required")
	}
	vcard.Write(card, ct)
	id, err := a.storeContact(ctx, sc, card, false)
	if err != nil {
		return PimContact{}, err
	}
	a.publishContactsChanged()
	return a.GetContact(ctx, id)
}

// storeContact saves a card, links it to a person and queues CardDAV changes.
func (a *App) storeContact(ctx context.Context, sc store.Contact, card *ical.Component, fromServer bool) (int64, error) {
	ct := vcard.Read(card)
	sc.VCard = card.Encode()
	sc.DisplayName = vcard.DisplayName(ct)
	sc.Emails = sc.Emails[:0]
	for _, e := range ct.Emails {
		sc.Emails = append(sc.Emails, e.Value)
	}
	if sc.UID == "" {
		sc.UID = ct.UID
	}
	if sc.UID == "" {
		return 0, invalid("contact without UID")
	}
	id, created, err := a.st.UpsertContact(ctx, sc)
	if err != nil {
		return 0, err
	}
	if err := a.resolvePerson(ctx, id, ct); err != nil {
		a.log.Warn("app: linking contact to person failed", "contact", id, "err", err)
	}
	if !fromServer && a.bookKind(ctx, sc.AddressbookID) == store.BookKindCardDAV {
		if err := a.st.QueuePIMChange(ctx, store.PIMChange{Kind: store.ChangeCard, CollectionID: sc.AddressbookID, UID: sc.UID, Op: "put",
			CreatedAt: a.now()}); err != nil {
			return 0, err
		}
		a.kickPIMSync()
	}
	if created {
		a.invalidateViews()
	}
	return id, nil
}

// DeleteContact deletes a contact.
func (a *App) DeleteContact(ctx context.Context, id int64) error {
	sc, err := a.st.GetContact(ctx, id)
	if err != nil {
		return err
	}
	if err := a.deleteContact(ctx, sc); err != nil {
		return err
	}
	a.publishContactsChanged()
	return nil
}

func (a *App) deleteContact(ctx context.Context, sc store.Contact) error {
	if err := a.st.DeleteContact(ctx, sc.ID); err != nil {
		return err
	}
	if a.bookKind(ctx, sc.AddressbookID) != store.BookKindCardDAV {
		return nil
	}
	if sc.Href == "" {
		return a.st.DropPIMChange(ctx, store.ChangeCard, sc.AddressbookID, sc.UID)
	}
	a.kickPIMSync()
	return a.st.QueuePIMChange(ctx, store.PIMChange{Kind: store.ChangeCard, CollectionID: sc.AddressbookID, UID: sc.UID, Op: "delete",
		Href: sc.Href, ETag: sc.ETag, CreatedAt: a.now()})
}

// ==== Person resolver (KONZEPT 10.2) ===========================================

// contactHandles returns the normalized handles of a contact.
func contactHandles(ct vcard.Contact, region string) []store.Handle {
	var out []store.Handle
	add := func(kind, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if kind == store.HandlePhone || kind == store.HandleWhatsApp || kind == store.HandleSignal {
			value = vcard.NormalizePhone(value, region)
		} else {
			value = store.NormalizeHandle(kind, value)
		}
		if value == "" {
			return
		}
		h := store.Handle{Kind: kind, Value: value}
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	for _, e := range ct.Emails {
		add(store.HandleEmail, e.Value)
	}
	for _, p := range ct.Phones {
		add(store.HandlePhone, p.Value)
	}
	for _, m := range ct.IMPP {
		scheme, v, ok := strings.Cut(m.Value, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(scheme) {
		case "whatsapp":
			add(store.HandleWhatsApp, v)
		case "signal", "sgnl":
			add(store.HandleSignal, v)
		case "telegram", "tg":
			add(store.HandleTelegram, strings.TrimPrefix(v, "@"))
		case "matrix", "im":
			add(store.HandleMatrix, v)
		}
	}
	return out
}

// resolvePerson links a contact to the person owning one of its handles
// (creating a person if none matches) and adds the missing handles.
func (a *App) resolvePerson(ctx context.Context, contactID int64, ct vcard.Contact) error {
	hs := contactHandles(ct, a.phoneRegion(ctx))
	if len(hs) == 0 {
		return nil
	}
	sc, err := a.st.GetContact(ctx, contactID)
	if err != nil {
		return err
	}
	personID := sc.PersonID
	if personID == 0 {
		for _, h := range hs {
			if p, err := a.st.FindPersonByHandle(ctx, h.Kind, h.Value); err == nil {
				personID = p.ID
				break
			}
		}
	}
	if personID == 0 {
		personID, err = a.st.CreatePerson(ctx, store.Person{DisplayName: vcard.DisplayName(ct)})
		if err != nil {
			return err
		}
	} else if p, err := a.st.GetPerson(ctx, personID); err == nil && strings.TrimSpace(p.DisplayName) == "" {
		p.DisplayName = vcard.DisplayName(ct)
		_ = a.st.UpdatePerson(ctx, p)
	}
	for _, h := range hs {
		h.PersonID = personID
		if err := a.st.AddHandle(ctx, h); err != nil && !errors.Is(err, store.ErrHandleInUse) {
			return err
		}
		if h.Kind == store.HandleEmail {
			_, _ = a.st.LinkAddressesToPerson(ctx, h.Value, personID)
		}
	}
	if sc.PersonID != personID {
		if err := a.st.LinkContactToPerson(ctx, contactID, personID); err != nil {
			return err
		}
		a.invalidateViews()
	}
	return nil
}

// ==== Duplicates & merge =======================================================

// FindDuplicateContacts returns groups of probable duplicates (same e-mail,
// phone number or name) with a merge preview.
func (a *App) FindDuplicateContacts(ctx context.Context) ([]DuplicateGroup, error) {
	all, err := a.ListContacts(ctx, ContactQuery{})
	if err != nil {
		return nil, err
	}
	region := a.phoneRegion(ctx)
	list := make([]vcard.Contact, len(all))
	for i, c := range all {
		list[i] = c.Contact
		list[i].FN = c.DisplayName
	}
	out := []DuplicateGroup{}
	for _, g := range vcard.Groups(list, region) {
		dg := DuplicateGroup{}
		var others []vcard.Contact
		for _, i := range g {
			dg.Contacts = append(dg.Contacts, all[i])
			if i != g[0] {
				others = append(others, list[i])
			}
		}
		prev := all[g[0]]
		prev.Contact = vcard.Merge(region, list[g[0]], others...)
		prev.DisplayName = vcard.DisplayName(prev.Contact)
		dg.Preview = prev
		out = append(out, dg)
	}
	return out, nil
}

// MergeContacts merges contacts into the first id (or into primaryID):
// fields are combined, the others are deleted and their persons merged.
// merged, if set, replaces the computed result (edited preview).
func (a *App) MergeContacts(ctx context.Context, ids []int64, merged *PimContact) (PimContact, error) {
	if len(ids) < 2 {
		return PimContact{}, invalid("at least two contacts are required")
	}
	region := a.phoneRegion(ctx)
	var (
		primary store.Contact
		cards   []vcard.Contact
		stored  []store.Contact
	)
	for i, id := range ids {
		sc, err := a.st.GetContact(ctx, id)
		if err != nil {
			return PimContact{}, err
		}
		cs, err := vcard.Parse(sc.VCard)
		if err != nil {
			return PimContact{}, err
		}
		if i == 0 {
			primary = sc
		}
		stored = append(stored, sc)
		cards = append(cards, vcard.Read(cs[0]))
	}
	result := vcard.Merge(region, cards[0], cards[1:]...)
	if merged != nil {
		photo, pt, pu := result.Photo, result.PhotoType, result.PhotoURI
		result = merged.Contact
		result.Photo, result.PhotoType, result.PhotoURI = photo, pt, pu
	}
	result.UID = primary.UID
	cs, _ := vcard.Parse(primary.VCard)
	vcard.Write(cs[0], result)
	var persons []int64
	for _, sc := range stored[1:] {
		if sc.PersonID != 0 && sc.PersonID != primary.PersonID && !slices.Contains(persons, sc.PersonID) {
			persons = append(persons, sc.PersonID)
		}
		if err := a.deleteContact(ctx, sc); err != nil {
			return PimContact{}, err
		}
	}
	if primary.PersonID != 0 && len(persons) > 0 {
		if err := a.st.MergePersons(ctx, primary.PersonID, persons...); err != nil {
			a.log.Warn("app: merging persons failed", "err", err)
		}
	} else if primary.PersonID == 0 && len(persons) > 0 {
		primary.PersonID = persons[0]
		if len(persons) > 1 {
			_ = a.st.MergePersons(ctx, persons[0], persons[1:]...)
		}
	}
	id, err := a.storeContact(ctx, primary, cs[0], false)
	if err != nil {
		return PimContact{}, err
	}
	a.invalidateViews()
	a.publishContactsChanged()
	return a.GetContact(ctx, id)
}

// ==== Autocomplete =============================================================

// AutocompleteAddresses suggests recipients for the composer from contacts
// and recently used addresses.
func (a *App) AutocompleteAddresses(ctx context.Context, q string, limit int) ([]AddressSuggestion, error) {
	q = strings.TrimSpace(q)
	out := []AddressSuggestion{}
	if q == "" {
		return out, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	seen := map[string]bool{}
	contacts, err := a.st.SearchContacts(ctx, q, limit*2)
	if err != nil {
		return nil, err
	}
	for _, c := range contacts {
		for _, e := range c.Emails {
			if seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, AddressSuggestion{Name: c.DisplayName, Email: e, ContactID: c.ID, PersonID: c.PersonID, Source: "contact"})
		}
	}
	recent, err := a.st.RecentAddresses(ctx, q, limit*2)
	if err != nil {
		return nil, err
	}
	for _, r := range recent {
		if seen[r.Email] {
			continue
		}
		seen[r.Email] = true
		out = append(out, AddressSuggestion{Name: r.Name, Email: r.Email, PersonID: r.PersonID, Source: "recent"})
	}
	ql := strings.ToLower(q)
	sort.SliceStable(out, func(i, j int) bool {
		pi := strings.HasPrefix(strings.ToLower(out[i].Name), ql) || strings.HasPrefix(out[i].Email, ql)
		pj := strings.HasPrefix(strings.ToLower(out[j].Name), ql) || strings.HasPrefix(out[j].Email, ql)
		if pi != pj {
			return pi
		}
		return out[i].Source == "contact" && out[j].Source != "contact"
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
