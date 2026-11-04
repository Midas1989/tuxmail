// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/autoconfig"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/rrule"
	"github.com/Midas1989/Mailclient/internal/pim/vcard"
	"github.com/Midas1989/Mailclient/internal/providers/caldav"
	"github.com/Midas1989/Mailclient/internal/providers/carddav"
	"github.com/Midas1989/Mailclient/internal/providers/dav"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Account Extra keys of DAV settings (CalDAV/CardDAV accounts and mail
// accounts with DAV URLs found by autoconfig).
const (
	extraCalDAV      = "caldavUrl"
	extraCardDAV     = "carddavUrl"
	extraCalOutbox   = "caldavOutbox"
	extraDavChecked  = "davChecked"
	extraDavUsername = "davUsername"
)

// SyncPIM synchronizes all CalDAV/CardDAV accounts and due ICS
// subscriptions now. Errors of single collections are recorded on the
// collection (PimCalendar.Error) and do not stop the others.
func (a *App) SyncPIM(ctx context.Context) error {
	p := a.pimSvc()
	p.syncMu.Lock()
	defer p.syncMu.Unlock()
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, acc := range accs {
		if !acc.Enabled {
			continue
		}
		if p.opts.DiscoverDAV && acc.Kind != domain.KindCalDAV && acc.Kind != domain.KindCardDAV && acc.Extra[extraDavChecked] == "" {
			a.discoverDAV(ctx, &acc)
		}
		if acc.Extra[extraCalDAV] != "" {
			if err := a.syncCalDAVAccount(ctx, acc); err != nil {
				errs = append(errs, fmt.Errorf("account %d (CalDAV): %w", acc.ID, err))
			}
		}
		if acc.Extra[extraCardDAV] != "" {
			if err := a.syncCardDAVAccount(ctx, acc); err != nil {
				errs = append(errs, fmt.Errorf("account %d (CardDAV): %w", acc.ID, err))
			}
		}
	}
	cals, err := a.st.ListCalendarsWithMeta(ctx)
	if err == nil {
		for _, c := range cals {
			if c.Meta.Kind == store.CalKindSubscription {
				if err := a.refreshSubscription(ctx, c.ID, false); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	a.publishCalendarChanged()
	a.publishContactsChanged()
	return errors.Join(errs...)
}

// discoverDAV looks up CalDAV/CardDAV URLs of a mail account once.
func (a *App) discoverDAV(ctx context.Context, acc *Account) {
	addr := acc.Extra["address"]
	if acc.Extra == nil {
		acc.Extra = map[string]string{}
	}
	acc.Extra[extraDavChecked] = strconv.FormatInt(a.now().Unix(), 10)
	if addr != "" {
		find := a.pimSvc().opts.DAVDiscover
		if find == nil {
			find = func(ctx context.Context, email string) (string, string, error) {
				cands, err := autoconfig.New().Discover(ctx, email)
				if err != nil {
					return "", "", err
				}
				for _, c := range cands {
					if c.CalDAVURL != "" || c.CardDAVURL != "" {
						return c.CalDAVURL, c.CardDAVURL, nil
					}
				}
				return "", "", nil
			}
		}
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cal, card, err := find(dctx, addr)
		cancel()
		if err == nil {
			if cal != "" {
				acc.Extra[extraCalDAV] = cal
			}
			if card != "" {
				acc.Extra[extraCardDAV] = card
			}
		}
	}
	if err := a.st.UpdateAccount(ctx, *acc); err != nil {
		a.log.Warn("app: saving DAV discovery failed", "account", acc.ID, "err", err)
	}
}

// AddDavAccount connects a CalDAV/CardDAV server (e.g. Nextcloud, Radicale,
// Baïkal) and runs the first sync.
func (a *App) AddDavAccount(ctx context.Context, in DavAccountInput) (Account, error) {
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Account{}, invalid("a http(s) server URL is required")
	}
	if in.DisplayName == "" {
		in.DisplayName = u.Host
	}
	card := strings.TrimSpace(in.CardDAVURL)
	if card == "" {
		card = u.String()
	}
	port := 443
	tls := domain.TLSImplicit
	if u.Scheme == "http" {
		port, tls = 80, domain.TLSNone
	}
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	acc := domain.Account{Kind: domain.KindCalDAV, DisplayName: in.DisplayName, Enabled: true,
		Incoming: &domain.ServerConfig{Host: u.Hostname(), Port: port, TLS: tls, Auth: domain.AuthPassword, Username: in.Username},
		Extra:    map[string]string{extraCalDAV: u.String(), extraCardDAV: card, extraDavUsername: in.Username}}
	if in.Email != "" {
		acc.Extra["address"] = strings.ToLower(in.Email)
	}
	// Check the credentials before saving anything.
	c := &dav.Client{HTTP: a.pimHTTP(), Username: in.Username, Password: in.Password}
	if _, err := caldav.Discover(ctx, c, u.String()); err != nil {
		if _, err2 := carddav.Discover(ctx, c, card); err2 != nil {
			return Account{}, Errorf(400, CodeInvalidInput, "server not reachable or login failed: %v", err)
		}
		delete(acc.Extra, extraCalDAV)
	}
	id, err := a.st.CreateAccount(ctx, acc)
	if err != nil {
		return Account{}, err
	}
	acc.ID = id
	if in.Password != "" {
		ref := accountSecretRef(id)
		if err := a.opts.Secrets.Set(ref, []byte(in.Password)); err != nil {
			_ = a.st.DeleteAccount(ctx, id)
			return Account{}, err
		}
		acc.SecretRef = ref
		if err := a.st.UpdateAccount(ctx, acc); err != nil {
			return Account{}, err
		}
	}
	if err := a.SyncPIM(ctx); err != nil {
		a.log.Warn("app: first DAV sync failed", "account", id, "err", err)
	}
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	return a.st.GetAccount(ctx, id)
}

// ==== CalDAV ===================================================================

func (a *App) syncCalDAVAccount(ctx context.Context, acc Account) error {
	c, err := a.davClient(ctx, acc)
	if err != nil {
		return err
	}
	da, err := caldav.Discover(ctx, c, acc.Extra[extraCalDAV])
	if err != nil {
		return err
	}
	if da.Outbox != acc.Extra[extraCalOutbox] {
		acc.Extra[extraCalOutbox] = da.Outbox
		_ = a.st.UpdateAccount(ctx, acc)
	}
	existing, err := a.st.ListAccountCalendars(ctx, acc.ID)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	var errs []error
	for _, col := range da.Calendars {
		id, err := a.st.UpsertCalendar(ctx, store.Calendar{AccountID: acc.ID, RemoteID: col.URL, Name: col.Name, Color: col.Color, ReadOnly: col.ReadOnly})
		if err != nil {
			return err
		}
		seen[id] = true
		cm, err := a.st.GetCalendarWithMeta(ctx, id)
		if err != nil {
			return err
		}
		fresh := cm.Meta.Kind != store.CalKindCalDAV
		meta := cm.Meta
		meta.Kind, meta.URL, meta.Components, meta.ServerScheduling, meta.Description = store.CalKindCalDAV, col.URL, col.Components,
			da.AutoSchedule, col.Description
		if fresh {
			meta.Visible = true
		}
		if err := a.st.PutCalendarMeta(ctx, meta); err != nil {
			return err
		}
		if err := a.syncCalendar(ctx, c, id, col, cm.Meta.CTag); err != nil {
			errs = append(errs, err)
			_ = a.st.SetCalendarSyncState(ctx, id, "", a.now(), err.Error())
		}
	}
	for _, cal := range existing {
		if !seen[cal.ID] {
			_ = a.st.DeleteCalendar(ctx, cal.ID) // removed on the server
		}
	}
	return errors.Join(errs...)
}

func (a *App) syncCalendar(ctx context.Context, c *dav.Client, calID int64, col dav.Collection, oldCTag string) error {
	pending, err := a.st.PendingPIMChanges(ctx, store.ChangeCal, calID)
	if err != nil {
		return err
	}
	if len(pending) == 0 && col.CTag != "" && col.CTag == oldCTag {
		return a.st.SetCalendarSyncState(ctx, calID, col.CTag, a.now(), "")
	}
	for _, ch := range pending {
		if err := a.pushCalChange(ctx, c, calID, col.URL, ch); err != nil {
			_ = a.st.FailPIMChange(ctx, ch.ID, err.Error())
			a.log.Info("app: pushing calendar change failed", "calendar", calID, "uid", ch.UID, "err", err)
		}
	}
	objs, err := a.st.ListCalObjects(ctx, calID)
	if err != nil {
		return err
	}
	local := map[string]string{}
	byHref := map[string]store.CalObject{}
	for _, o := range objs {
		if o.Href != "" {
			local[o.Href] = o.ETag
			byHref[o.Href] = o
		}
	}
	token, _ := a.st.CalendarSyncToken(ctx, calID)
	d, err := caldav.Pull(ctx, c, col.URL, token, col.SyncToken != "", local)
	if err != nil {
		return err
	}
	loc := a.pimLoc(ctx)
	for _, it := range d.Changed {
		cal, err := ical.Parse(string(it.Data))
		if err != nil {
			a.log.Info("app: skipping invalid calendar object", "href", it.Href, "err", err)
			continue
		}
		uid := ""
		for _, ch := range cal.Children {
			if ch.Name == "VEVENT" || ch.Name == "VTODO" || ch.Name == "VJOURNAL" {
				uid = ch.Value("UID")
				break
			}
		}
		if uid == "" {
			continue
		}
		if pend, _ := a.st.HasPendingPIMChange(ctx, store.ChangeCal, calID, uid); pend {
			continue // local change not pushed yet (failed): keep it
		}
		o := store.CalObject{CalendarID: calID, UID: uid, Href: it.Href, ETag: it.ETag}
		if old, ok := byHref[it.Href]; ok && old.UID != uid {
			_ = a.st.DeleteCalObject(ctx, old.ID)
		}
		_ = loc
		if _, err := a.storeObject(ctx, o, cal, true); err != nil {
			return err
		}
	}
	for _, h := range d.Deleted {
		if o, ok := byHref[h]; ok {
			if pend, _ := a.st.HasPendingPIMChange(ctx, store.ChangeCal, calID, o.UID); pend {
				continue
			}
			_ = a.st.DeleteCalObject(ctx, o.ID)
		}
	}
	if d.SyncToken != "" {
		if err := a.st.SetCalendarSyncToken(ctx, calID, d.SyncToken); err != nil {
			return err
		}
	}
	return a.st.SetCalendarSyncState(ctx, calID, col.CTag, a.now(), "")
}

// pushCalChange sends one local change. On a conflict (412) the server
// version wins and the local version is kept as a conflict copy.
func (a *App) pushCalChange(ctx context.Context, c *dav.Client, calID int64, colURL string, ch store.PIMChange) error {
	if ch.Op == "delete" {
		err := c.Delete(ctx, ch.Href, ch.ETag)
		if errors.Is(err, dav.ErrPrecondition) {
			err = nil // changed on the server: the next pull brings it back
		}
		if err != nil {
			return err
		}
		return a.st.DropPIMChange(ctx, store.ChangeCal, calID, ch.UID)
	}
	o, err := a.st.CalObjectByUID(ctx, calID, ch.UID)
	if errors.Is(err, store.ErrNotFound) {
		return a.st.DropPIMChange(ctx, store.ChangeCal, calID, ch.UID)
	}
	if err != nil {
		return err
	}
	href := o.Href
	if href == "" {
		href = caldav.ObjectURL(colURL, o.UID)
	}
	etag, err := caldav.Put(ctx, c, href, []byte(o.ICal), o.ETag)
	if errors.Is(err, dav.ErrPrecondition) {
		return a.calConflict(ctx, c, calID, o, href)
	}
	if err != nil {
		return err
	}
	o.Href, o.ETag = href, etag
	if _, _, err := a.st.UpsertCalObject(ctx, o); err != nil {
		return err
	}
	return a.st.DropPIMChange(ctx, store.ChangeCal, calID, ch.UID)
}

func (a *App) calConflict(ctx context.Context, c *dav.Client, calID int64, local store.CalObject, href string) error {
	data, etag, err := c.Get(ctx, href)
	if err != nil && !errors.Is(err, dav.ErrNotFound) {
		return err
	}
	if err := a.st.DropPIMChange(ctx, store.ChangeCal, calID, local.UID); err != nil {
		return err
	}
	// Keep the local version as a copy (not pushed; the user decides).
	if lc, err := ical.Parse(local.ICal); err == nil {
		copyUID := local.UID + "-conflict-" + strconv.FormatInt(a.now().Unix(), 10)
		for _, it := range lc.Children {
			if it.Value("UID") == local.UID {
				it.Set("UID", copyUID)
				it.Set("X-TUXMAIL-CONFLICT", local.UID)
			}
		}
		if _, err := a.storeObject(ctx, store.CalObject{CalendarID: calID, UID: copyUID}, lc, true); err != nil {
			return err
		}
	}
	if data == nil {
		return a.st.DeleteCalObject(ctx, local.ID)
	}
	sc, err := ical.Parse(string(data))
	if err != nil {
		return err
	}
	_, err = a.storeObject(ctx, store.CalObject{CalendarID: calID, UID: local.UID, Href: href, ETag: etag}, sc, true)
	return err
}

// ==== CardDAV ==================================================================

func (a *App) syncCardDAVAccount(ctx context.Context, acc Account) error {
	c, err := a.davClient(ctx, acc)
	if err != nil {
		return err
	}
	da, err := carddav.Discover(ctx, c, acc.Extra[extraCardDAV])
	if err != nil {
		return err
	}
	existing, err := a.st.ListAccountAddressbooks(ctx, acc.ID)
	if err != nil {
		return err
	}
	books, _ := a.st.ListAddressbooksWithMeta(ctx)
	metaOf := func(id int64) store.AddressbookMeta {
		for _, b := range books {
			if b.ID == id {
				return b.Meta
			}
		}
		return store.AddressbookMeta{AddressbookID: id}
	}
	seen := map[int64]bool{}
	var errs []error
	for _, col := range da.AddressBooks {
		id, err := a.st.UpsertAddressbook(ctx, store.Addressbook{AccountID: acc.ID, RemoteID: col.URL, Name: col.Name})
		if err != nil {
			return err
		}
		seen[id] = true
		meta := metaOf(id)
		oldCTag := meta.CTag
		meta.AddressbookID, meta.Kind, meta.URL = id, store.BookKindCardDAV, col.URL
		if err := a.st.PutAddressbookMeta(ctx, meta); err != nil {
			return err
		}
		if err := a.syncAddressbook(ctx, c, id, col, oldCTag); err != nil {
			errs = append(errs, err)
			_ = a.st.SetAddressbookSyncState(ctx, id, "", a.now(), err.Error())
		}
	}
	for _, b := range existing {
		if !seen[b.ID] {
			_ = a.st.DeleteAddressbook(ctx, b.ID)
		}
	}
	return errors.Join(errs...)
}

func (a *App) syncAddressbook(ctx context.Context, c *dav.Client, bookID int64, col dav.Collection, oldCTag string) error {
	pending, err := a.st.PendingPIMChanges(ctx, store.ChangeCard, bookID)
	if err != nil {
		return err
	}
	if len(pending) == 0 && col.CTag != "" && col.CTag == oldCTag {
		return a.st.SetAddressbookSyncState(ctx, bookID, col.CTag, a.now(), "")
	}
	for _, ch := range pending {
		if err := a.pushCardChange(ctx, c, bookID, col.URL, ch); err != nil {
			_ = a.st.FailPIMChange(ctx, ch.ID, err.Error())
		}
	}
	contacts, err := a.st.ListContacts(ctx, bookID)
	if err != nil {
		return err
	}
	local := map[string]string{}
	byHref := map[string]store.Contact{}
	for _, sc := range contacts {
		if sc.Href != "" {
			local[sc.Href] = sc.ETag
			byHref[sc.Href] = sc
		}
	}
	token, _ := a.st.AddressbookSyncToken(ctx, bookID)
	d, err := carddav.Pull(ctx, c, col.URL, token, col.SyncToken != "", local)
	if err != nil {
		return err
	}
	for _, it := range d.Changed {
		cards, err := vcard.Parse(string(it.Data))
		if err != nil {
			continue
		}
		uid := strings.TrimSpace(cards[0].Value("UID"))
		if uid == "" {
			uid = it.Href
		}
		if pend, _ := a.st.HasPendingPIMChange(ctx, store.ChangeCard, bookID, uid); pend {
			continue
		}
		sc := store.Contact{AddressbookID: bookID, UID: uid, Href: it.Href, ETag: it.ETag}
		if old, ok := byHref[it.Href]; ok {
			sc.PersonID = old.PersonID
			if old.UID != uid {
				_ = a.st.DeleteContact(ctx, old.ID)
			}
		}
		if _, err := a.storeContact(ctx, sc, cards[0], true); err != nil {
			return err
		}
	}
	for _, h := range d.Deleted {
		if sc, ok := byHref[h]; ok {
			if pend, _ := a.st.HasPendingPIMChange(ctx, store.ChangeCard, bookID, sc.UID); !pend {
				_ = a.st.DeleteContact(ctx, sc.ID)
			}
		}
	}
	if d.SyncToken != "" {
		if err := a.st.SetAddressbookSyncToken(ctx, bookID, d.SyncToken); err != nil {
			return err
		}
	}
	return a.st.SetAddressbookSyncState(ctx, bookID, col.CTag, a.now(), "")
}

func (a *App) pushCardChange(ctx context.Context, c *dav.Client, bookID int64, colURL string, ch store.PIMChange) error {
	if ch.Op == "delete" {
		err := c.Delete(ctx, ch.Href, ch.ETag)
		if errors.Is(err, dav.ErrPrecondition) {
			err = nil
		}
		if err != nil {
			return err
		}
		return a.st.DropPIMChange(ctx, store.ChangeCard, bookID, ch.UID)
	}
	sc, err := a.st.ContactByUID(ctx, bookID, ch.UID)
	if errors.Is(err, store.ErrNotFound) {
		return a.st.DropPIMChange(ctx, store.ChangeCard, bookID, ch.UID)
	}
	if err != nil {
		return err
	}
	href := sc.Href
	if href == "" {
		href = carddav.ObjectURL(colURL, sc.UID)
	}
	etag, err := carddav.Put(ctx, c, href, []byte(sc.VCard), sc.ETag)
	if errors.Is(err, dav.ErrPrecondition) {
		// Server wins; keep the local version as a separate local contact.
		data, setag, gerr := c.Get(ctx, href)
		_ = a.st.DropPIMChange(ctx, store.ChangeCard, bookID, sc.UID)
		if cards, perr := vcard.Parse(sc.VCard); perr == nil {
			if lb, lerr := a.localAddressbookID(ctx); lerr == nil {
				copyUID := sc.UID + "-conflict-" + strconv.FormatInt(a.now().Unix(), 10)
				cards[0].Set("UID", copyUID)
				cards[0].Set("X-TUXMAIL-CONFLICT", sc.UID)
				_, _ = a.storeContact(ctx, store.Contact{AddressbookID: lb, UID: copyUID}, cards[0], true)
			}
		}
		if gerr != nil {
			return gerr
		}
		cards, err := vcard.Parse(string(data))
		if err != nil {
			return err
		}
		sc.Href, sc.ETag = href, setag
		_, err = a.storeContact(ctx, sc, cards[0], true)
		return err
	}
	if err != nil {
		return err
	}
	sc.Href, sc.ETag = href, etag
	if _, _, err := a.st.UpsertContact(ctx, sc); err != nil {
		return err
	}
	return a.st.DropPIMChange(ctx, store.ChangeCard, bookID, ch.UID)
}

func (a *App) localAddressbookID(ctx context.Context) (int64, error) {
	books, err := a.ensureAddressbooks(ctx)
	if err != nil {
		return 0, err
	}
	for _, b := range books {
		if b.AccountID == 0 {
			return b.ID, nil
		}
	}
	return 0, errNotFound
}

// ==== ICS subscriptions ========================================================

// refreshSubscription downloads a subscribed calendar when its refresh
// interval has passed (or force) and replaces its events.
func (a *App) refreshSubscription(ctx context.Context, calID int64, force bool) error {
	cm, err := a.st.GetCalendarWithMeta(ctx, calID)
	if err != nil {
		return err
	}
	interval := time.Duration(cm.Meta.RefreshMinutes) * time.Minute
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	if !force && !cm.Meta.LastSyncAt.IsZero() && a.now().Sub(cm.Meta.LastSyncAt) < interval && cm.Meta.LastError == "" {
		return nil
	}
	data, etag, notModified, err := caldav.FetchICS(ctx, a.pimHTTP(), cm.Meta.URL, cm.Meta.CTag)
	if err != nil {
		_ = a.st.SetCalendarSyncState(ctx, calID, "", a.now(), err.Error())
		return fmt.Errorf("subscription %d: %w", calID, err)
	}
	if notModified {
		return a.st.SetCalendarSyncState(ctx, calID, "", a.now(), "")
	}
	if err := a.importICS(ctx, calID, data, true); err != nil {
		_ = a.st.SetCalendarSyncState(ctx, calID, "", a.now(), err.Error())
		return err
	}
	if cm.Name == cm.Meta.URL {
		if root, err := ical.Parse(string(data)); err == nil {
			if n := root.Text("X-WR-CALNAME"); n != "" {
				cm.Name = n
				_ = a.st.UpdateCalendar(ctx, cm.Calendar)
			}
		}
	}
	return a.st.SetCalendarSyncState(ctx, calID, etag, a.now(), "")
}

// importICS stores the events of an iCalendar file in a calendar, one
// object per UID. With replace, objects missing from the file are removed.
func (a *App) importICS(ctx context.Context, calID int64, data []byte, replace bool) error {
	roots, err := ical.ParseAll(string(data))
	if err != nil {
		return err
	}
	type group struct {
		tzs   []*ical.Component
		items []*ical.Component
	}
	groups := map[string]*group{}
	var order []string
	for _, root := range roots {
		tzs := root.ChildrenNamed("VTIMEZONE")
		for _, c := range root.Children {
			if c.Name != "VEVENT" && c.Name != "VTODO" {
				continue
			}
			uid := c.Value("UID")
			if uid == "" {
				uid = fmt.Sprintf("noid-%d-%s", len(order), c.Value("DTSTART"))
				c.Set("UID", uid)
			}
			g := groups[uid]
			if g == nil {
				g = &group{tzs: tzs}
				groups[uid] = g
				order = append(order, uid)
			}
			g.items = append(g.items, c)
		}
	}
	keep := map[string]bool{}
	for _, uid := range order {
		g := groups[uid]
		cal := ical.NewCalendar()
		used := map[string]bool{}
		for _, it := range g.items {
			for _, p := range it.Props {
				if tzid := p.Param("TZID"); tzid != "" {
					used[tzid] = true
				}
			}
		}
		for _, z := range g.tzs {
			if used[z.Value("TZID")] {
				cal.Children = append(cal.Children, z)
			}
		}
		cal.Children = append(cal.Children, g.items...)
		keep[uid] = true
		o := store.CalObject{CalendarID: calID, UID: uid}
		if old, err := a.st.CalObjectByUID(ctx, calID, uid); err == nil {
			if old.ICal == cal.Encode() {
				continue
			}
			o = old
		}
		if _, err := a.storeObject(ctx, o, cal, true); err != nil {
			return err
		}
	}
	if replace {
		objs, err := a.st.ListCalObjects(ctx, calID)
		if err != nil {
			return err
		}
		for _, o := range objs {
			if !keep[o.UID] {
				_ = a.st.DeleteCalObject(ctx, o.ID)
			}
		}
	}
	return nil
}

// ImportICS imports an .ics file into a calendar (events with known UIDs
// are updated).
func (a *App) ImportICS(ctx context.Context, calendarID int64, data []byte) (int, error) {
	cm, err := a.writableCalendar(ctx, calendarID, "VEVENT")
	if err != nil {
		return 0, err
	}
	before, _ := a.st.ListCalObjects(ctx, cm.ID)
	roots, err := ical.ParseAll(string(data))
	if err != nil {
		return 0, invalid("not an iCalendar file: %v", err)
	}
	// Local writes: store each object normally (queued for CalDAV).
	n := 0
	for _, root := range roots {
		byUID := map[string][]*ical.Component{}
		var order []string
		for _, c := range root.Children {
			if c.Name == "VEVENT" || c.Name == "VTODO" {
				uid := c.Value("UID")
				if uid == "" {
					uid = newUID()
					c.Set("UID", uid)
				}
				if byUID[uid] == nil {
					order = append(order, uid)
				}
				byUID[uid] = append(byUID[uid], c)
			}
		}
		for _, uid := range order {
			cal := ical.NewCalendar()
			cal.Children = append(cal.Children, root.ChildrenNamed("VTIMEZONE")...)
			cal.Children = append(cal.Children, byUID[uid]...)
			o := store.CalObject{CalendarID: cm.ID, UID: uid}
			if old, err := a.st.CalObjectByUID(ctx, cm.ID, uid); err == nil {
				o = old
			}
			if _, err := a.storeObject(ctx, o, cal, false); err != nil {
				return n, err
			}
			n++
		}
	}
	_ = before
	a.publishCalendarChanged()
	return n, nil
}

// ==== Reminders (VALARM) =======================================================

// checkReminders publishes calendar.reminder events for alarms that became
// due since the last check (and for snoozed reminders whose time came).
func (a *App) checkReminders(ctx context.Context) {
	p := a.pimSvc()
	now := a.now()
	p.mu.Lock()
	since := p.lastAlarm
	if since.IsZero() || now.Sub(since) > 2*time.Hour {
		since = now.Add(-10 * time.Minute)
	}
	p.lastAlarm = now
	p.mu.Unlock()
	for _, d := range a.dueAlarms(ctx, since, now) {
		state, _, _ := a.st.ReminderState(ctx, d.objectID, d.instance, d.trigger)
		if state != "" {
			continue
		}
		_ = a.st.SetReminderState(ctx, d.objectID, d.instance, d.trigger, "fired", time.Time{})
		a.fireReminder(ctx, d)
	}
	if snoozed, err := a.st.DueSnoozedReminders(ctx, now); err == nil {
		for _, s := range snoozed {
			_ = a.st.SetReminderState(ctx, s.ObjectID, s.Instance, s.Trigger, "fired", time.Time{})
			a.fireReminder(ctx, dueAlarm{objectID: s.ObjectID, instance: s.Instance, trigger: s.Trigger})
		}
	}
	// Move the precomputation window once a day.
	if now.Hour() == 3 && now.Minute() == 0 {
		_ = a.refreshInstances(ctx)
	}
}

type dueAlarm struct {
	objectID int64
	instance string
	trigger  time.Time
	title    string
	start    time.Time
}

func (a *App) fireReminder(ctx context.Context, d dueAlarm) {
	id := strconv.FormatInt(d.objectID, 10)
	if d.instance != "" {
		id += "/" + d.instance
	}
	a.hub.Publish(Event{Type: EventCalendarReminder, ItemID: id, State: rfc3339(d.trigger)})
	a.bus.Publish(events.Event{ID: events.NewID(), Type: events.CalendarEventStarting, Time: a.now(), Source: "calendar", Ref: id,
		Calendar: nil, Data: map[string]string{"title": d.title, "start": rfc3339(d.start)}})
}

// dueAlarms returns alarms with trigger in (from, to].
func (a *App) dueAlarms(ctx context.Context, from, to time.Time) []dueAlarm {
	loc := a.pimLoc(ctx)
	insts, err := a.st.ListKeyedInstances(ctx, store.CalEvent, from.Add(-time.Hour), to.AddDate(0, 0, 8), nil)
	if err != nil {
		return nil
	}
	todos, _ := a.st.ListKeyedInstances(ctx, store.CalTodo, from.Add(-time.Hour), to.AddDate(0, 0, 8), nil)
	insts = append(insts, todos...)
	var out []dueAlarm
	cache := map[int64]*parsedObject{}
	for _, in := range insts {
		if !strings.Contains(in.Object.ICal, "VALARM") {
			continue
		}
		p := cache[in.Object.ID]
		if p == nil {
			if p, err = a.parseObject(in.Object, loc); err != nil {
				continue
			}
			cache[in.Object.ID] = p
		}
		comp := p.master()
		for _, it := range p.items {
			if r := it.Prop("RECURRENCE-ID"); r != nil && in.RID != "" {
				if v, err := p.tz.PropTime(r); err == nil && rrule.RIDKey(v.Time, v.AllDay) == in.RID {
					comp = it
				}
			}
		}
		ev := ical.ReadEvent(comp, p.tz)
		start := in.Start
		if in.AllDay {
			start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
		}
		for _, al := range ev.Alarms {
			trig := al.Absolute
			if trig.IsZero() {
				base := start
				if al.Related == "END" {
					base = in.End
				}
				trig = base.Add(al.Trigger)
			}
			if trig.After(from) && !trig.After(to) {
				out = append(out, dueAlarm{objectID: in.Object.ID, instance: in.RID, trigger: trig, title: ev.Summary, start: start})
			}
		}
	}
	return out
}

// SnoozeReminder postpones a fired reminder (event id from the reminder
// event, trigger from its State field).
func (a *App) SnoozeReminder(ctx context.Context, eventID, trigger string, minutes int) error {
	oid, inst, err := splitEventID(eventID)
	if err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, trigger)
	if err != nil {
		return invalid("invalid trigger")
	}
	if minutes <= 0 {
		return a.st.SetReminderState(ctx, oid, inst, t, "dismissed", time.Time{})
	}
	return a.st.SetReminderState(ctx, oid, inst, t, "snoozed", a.now().Add(time.Duration(minutes)*time.Minute))
}
