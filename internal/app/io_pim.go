// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
	iecsv "github.com/Midas1989/Mailclient/internal/importexport/csv"
	"github.com/Midas1989/Mailclient/internal/importexport/ics"
	"github.com/Midas1989/Mailclient/internal/importexport/ldif"
	"github.com/Midas1989/Mailclient/internal/importexport/thunderbird"
	"github.com/Midas1989/Mailclient/internal/importexport/vcf"
	pimical "github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/store"
)

// storeCalendar implements ics.CalendarImporter and ics.CalendarExporter on
// the store (a minimal model; the PIM module may provide a richer one).
type storeCalendar struct{ a *App }

var (
	_ ics.CalendarImporter = (*storeCalendar)(nil)
	_ ics.CalendarExporter = (*storeCalendar)(nil)
)

// instanceWindow bounds the expansion of recurring events.
const instanceLimit = 1000

func (c *storeCalendar) instances(o ics.Object) []store.Instance {
	if o.Kind != ics.KindEvent || o.Start.IsZero() {
		return nil
	}
	dur := o.End.Sub(o.Start)
	if o.RRule == "" {
		return []store.Instance{{Start: o.Start, End: o.End}}
	}
	cal, err := ical.NewDecoder(strings.NewReader(o.ICal)).Decode()
	if err != nil {
		return []store.Instance{{Start: o.Start, End: o.End}}
	}
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent || comp.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		set, err := comp.RecurrenceSet(time.UTC)
		if err != nil || set == nil {
			break
		}
		now := c.a.now()
		var out []store.Instance
		for _, t := range set.Between(now.AddDate(-2, 0, 0), now.AddDate(3, 0, 0), true) {
			out = append(out, store.Instance{Start: t, End: t.Add(dur)})
			if len(out) >= instanceLimit {
				break
			}
		}
		return out
	}
	return []store.Instance{{Start: o.Start, End: o.End}}
}

// ImportObjects implements ics.CalendarImporter.
func (c *storeCalendar) ImportObjects(ctx context.Context, calendarID int64, objs []ics.Object, mode ics.DedupeMode) (ics.Result, error) {
	st := c.a.st
	res := ics.Result{TotalParsed: len(objs)}
	for _, o := range objs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if _, err := st.CalObjectByUID(ctx, calendarID, o.UID); err == nil {
			res.Duplicates++
			switch mode {
			case ics.Overwrite:
			case ics.Copy:
				cp, err := ics.WithNewUID(o)
				if err != nil {
					res.Failed++
					continue
				}
				o = cp
			default:
				res.Skipped++
				continue
			}
		}
		// Through the PIM storage when the data parses: keyed instances and,
		// for CalDAV calendars, a queued upload (pim.go).
		if cal, perr := pimical.Parse(o.ICal); perr == nil {
			obj := store.CalObject{CalendarID: calendarID, UID: o.UID}
			prev, gerr := st.CalObjectByUID(ctx, calendarID, o.UID)
			if gerr == nil {
				obj = prev
			}
			if _, err := c.a.storeObject(ctx, obj, cal, false); err != nil {
				res.Failed++
				continue
			}
			if gerr != nil {
				res.Created++
			} else {
				res.Updated++
			}
			continue
		}
		id, created, err := st.UpsertCalObject(ctx, store.CalObject{CalendarID: calendarID, UID: o.UID, Kind: o.Kind, ICal: o.ICal,
			DTStart: o.Start, DTEnd: o.End, RRule: o.RRule, Summary: o.Summary, Due: o.Due, Status: o.Status})
		if err != nil {
			res.Failed++
			continue
		}
		if err := st.ReplaceInstances(ctx, id, c.instances(o)); err != nil {
			return res, err
		}
		if created {
			res.Created++
		} else {
			res.Updated++
		}
	}
	return res, nil
}

// ExportObjects implements ics.CalendarExporter.
func (c *storeCalendar) ExportObjects(ctx context.Context, calendarIDs []int64, from, to time.Time) ([]ics.Object, error) {
	if len(calendarIDs) == 0 {
		cals, err := c.a.st.ListCalendars(ctx)
		if err != nil {
			return nil, err
		}
		for _, cal := range cals {
			calendarIDs = append(calendarIDs, cal.ID)
		}
	}
	var out []ics.Object
	for _, id := range calendarIDs {
		objs, err := c.a.st.ListCalObjects(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			obj := ics.Object{UID: o.UID, Kind: o.Kind, Summary: o.Summary, Start: o.DTStart, End: o.DTEnd, Due: o.Due, RRule: o.RRule,
				Status: o.Status, ICal: o.ICal}
			if ics.InRange(obj, from, to) {
				out = append(out, obj)
			}
		}
	}
	return out, nil
}

// storeContacts stores vCards in an address book.
type storeContacts struct{ a *App }

func (c *storeContacts) importCards(ctx context.Context, addressbookID int64, cards []vcard.Card) (ics.Result, error) {
	res := ics.Result{TotalParsed: len(cards)}
	for _, card := range cards {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		vcf.Complete(card)
		text, err := vcf.Encode(card, vcf.V4)
		if err != nil {
			res.Failed++
			continue
		}
		emails := vcf.Emails(card)
		// Duplicate detection by e-mail address (KONZEPT 13.3): an existing
		// contact with the same address in this book is updated.
		uid := card.Value(vcard.FieldUID)
		if _, err := c.a.st.ContactByUID(ctx, addressbookID, uid); err != nil && len(emails) > 0 {
			if found, _ := c.a.st.FindContactsByEmail(ctx, emails[0]); len(found) > 0 {
				for _, f := range found {
					if f.AddressbookID == addressbookID {
						uid = f.UID
						res.Duplicates++
						break
					}
				}
			}
		}
		_, created, err := c.a.st.UpsertContact(ctx, store.Contact{AddressbookID: addressbookID, UID: uid, VCard: text,
			DisplayName: card.Value(vcard.FieldFormattedName), Emails: emails})
		if err != nil {
			res.Failed++
			continue
		}
		if created {
			res.Created++
		} else {
			res.Updated++
		}
	}
	return res, nil
}

func (c *storeContacts) exportCards(ctx context.Context, ids []int64) ([]vcard.Card, error) {
	if len(ids) == 0 {
		books, err := c.a.st.ListAddressbooks(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range books {
			ids = append(ids, b.ID)
		}
	}
	var out []vcard.Card
	for _, id := range ids {
		cs, err := c.a.st.ListContacts(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, ct := range cs {
			cards, err := vcf.Parse(strings.NewReader(ct.VCard))
			if err != nil || len(cards) == 0 {
				continue
			}
			out = append(out, cards[0])
		}
	}
	return out, nil
}

func (a *App) importPIM(ctx context.Context, j *ioJob, path string, req ImportRequest) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var res ics.Result
	switch req.Source.Format {
	case "ics":
		objs, err := ics.Parse(f)
		if err != nil {
			return invalid("%v", err)
		}
		cal := req.Target.CalendarID
		if cal == 0 {
			if cal, err = a.st.CreateCalendar(ctx, store.Calendar{Name: base}); err != nil {
				return err
			}
		}
		j.update(func(x *IOJob) { x.Total = len(objs) })
		mode := ics.DedupeMode(req.Options.DuplicateMode)
		if mode == "" {
			mode = ics.Skip
		}
		if res, err = (&storeCalendar{a: a}).ImportObjects(ctx, cal, objs, mode); err != nil {
			return err
		}
		j.count("events", res.Created+res.Updated)
	default:
		var cards []vcard.Card
		switch req.Source.Format {
		case "vcf":
			cards, err = vcf.Parse(f)
		case "ldif":
			cards, err = ldif.Parse(f)
		case "csv":
			var file *iecsv.File
			if file, err = iecsv.Read(f); err == nil {
				cards = file.Cards(csvMapping(file.Header, req.Options))
			}
		}
		if err != nil {
			return invalid("%v", err)
		}
		book := req.Target.AddressbookID
		if book == 0 {
			if book, err = a.st.CreateAddressbook(ctx, store.Addressbook{Name: base}); err != nil {
				return err
			}
		}
		j.update(func(x *IOJob) { x.Total = len(cards) })
		if res, err = (&storeContacts{a: a}).importCards(ctx, book, cards); err != nil {
			return err
		}
		j.count("contacts", res.Created+res.Updated)
	}
	j.update(func(x *IOJob) {
		x.Done, x.Skipped, x.Failed = res.TotalParsed, res.Skipped, res.Failed
		if x.Summary == nil {
			x.Summary = map[string]int{}
		}
		x.Summary["duplicates"] = res.Duplicates
	})
	return nil
}

// csvMapping returns the mapping of the request, or the suggested one.
func csvMapping(header []string, o ImportOptions) []iecsv.Field {
	if len(o.CSVMapping) > 0 {
		out := make([]iecsv.Field, len(header))
		for i := range header {
			if i < len(o.CSVMapping) {
				out[i] = iecsv.Field(o.CSVMapping[i])
			}
		}
		return out
	}
	if p, ok := iecsv.PresetByID(o.CSVPreset); ok {
		out := make([]iecsv.Field, len(header))
		for i, h := range header {
			for _, c := range p.Columns {
				if strings.EqualFold(strings.TrimSpace(h), c.Name) {
					out[i] = c.Field
				}
			}
		}
		return out
	}
	_, m := iecsv.Suggest(header)
	return m
}

func (a *App) exportPIM(ctx context.Context, j *ioJob, req ExportRequest, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	if req.Format == "ics" {
		from, err := parseTime(req.From)
		if err != nil {
			return err
		}
		to, err := parseTime(req.To)
		if err != nil {
			return err
		}
		objs, err := (&storeCalendar{a: a}).ExportObjects(ctx, req.CalendarIDs, from, to)
		if err != nil {
			return err
		}
		name := ""
		if len(req.CalendarIDs) == 1 {
			if c, err := a.st.GetCalendar(ctx, req.CalendarIDs[0]); err == nil {
				name = c.Name
			}
		}
		j.update(func(x *IOJob) { x.Total, x.Done = len(objs), len(objs) })
		j.count("events", len(objs))
		return ics.Write(f, objs, name)
	}
	cards, err := (&storeContacts{a: a}).exportCards(ctx, req.AddressbookIDs)
	if err != nil {
		return err
	}
	j.update(func(x *IOJob) { x.Total, x.Done = len(cards), len(cards) })
	j.count("contacts", len(cards))
	switch req.Format {
	case "vcf":
		v := vcf.V4
		if req.VCardVersion == "3.0" {
			v = vcf.V3
		}
		return vcf.Write(f, cards, v)
	case "csv":
		p := iecsv.Outlook
		if req.CSVPreset == "google" {
			p = iecsv.Google
		}
		return iecsv.Write(f, cards, p)
	default:
		return ldif.Write(f, cards)
	}
}

// ==== inspection (wizard step "mapping/options") ===============================

// ImportPreview describes a selected import file.
type ImportPreview struct {
	Format string `json:"format"`
	// Items is the number of contacts/events (mail: not counted up front).
	Items int `json:"items"`
	// CSV: header, suggested preset and mapping, the first rows and the
	// list of target fields.
	Header   []string   `json:"header,omitempty"`
	Preset   string     `json:"preset,omitempty"`
	Mapping  []string   `json:"mapping,omitempty"`
	Sample   [][]string `json:"sample,omitempty"`
	Fields   []string   `json:"fields,omitempty"`
	Problems []string   `json:"problems,omitempty"`
	// Thunderbird profile content.
	Profile *thunderbird.Profile `json:"profile,omitempty"`
}

// InspectImport reads a file of the import wizard.
func (a *App) InspectImport(ctx context.Context, src IOSource) (ImportPreview, error) {
	path, cleanup, err := a.resolveSource(src)
	if err != nil {
		return ImportPreview{}, err
	}
	defer cleanup()
	p := ImportPreview{Format: src.Format}
	open := func() (*os.File, error) { return os.Open(path) }
	switch src.Format {
	case "csv":
		f, err := open()
		if err != nil {
			return p, err
		}
		defer f.Close()
		file, err := iecsv.Read(f)
		if err != nil {
			return p, invalid("%v", err)
		}
		preset, mapping := iecsv.Suggest(file.Header)
		p.Header, p.Preset, p.Items = file.Header, preset, len(file.Rows)
		for _, m := range mapping {
			p.Mapping = append(p.Mapping, string(m))
		}
		p.Sample = file.Rows[:min(3, len(file.Rows))]
		for _, f := range iecsv.AllFields {
			p.Fields = append(p.Fields, string(f))
		}
	case "vcf", "ldif":
		f, err := open()
		if err != nil {
			return p, err
		}
		defer f.Close()
		var cards []vcard.Card
		if src.Format == "vcf" {
			cards, err = vcf.Parse(f)
		} else {
			cards, err = ldif.Parse(f)
		}
		if err != nil {
			return p, invalid("%v", err)
		}
		p.Items = len(cards)
	case "ics":
		f, err := open()
		if err != nil {
			return p, err
		}
		defer f.Close()
		objs, err := ics.Parse(f)
		if err != nil {
			return p, invalid("%v", err)
		}
		p.Items = len(objs)
	case "thunderbird":
		pr, err := thunderbird.Read(ctx, path, thunderbird.Options{Root: a.importRoot(src, path)})
		if err != nil {
			return p, invalid("%v", err)
		}
		for i := range pr.AddressBooks {
			pr.AddressBooks[i].Cards = nil
		}
		p.Profile = pr
	case "eml", "mbox", "maildir", "pst":
		srcObj, err := openMailSource(src.Format, path, ImportOptions{})
		if err != nil {
			if errors.Is(err, ie.ErrCorrupt) {
				return p, invalid("%v", err)
			}
			return p, err
		}
		// Count quickly (headers only are not available for every format;
		// the walk is bounded by the size of typical exports).
		n := 0
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		err = srcObj.Walk(cctx, func(ie.Message) error { n++; return nil })
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return p, invalid("%v", err)
		}
		p.Items = n
		if rep, ok := srcObj.(ie.Reporter); ok {
			for _, pr := range rep.Problems() {
				p.Problems = append(p.Problems, pr.Item+": "+pr.Error)
			}
		}
	default:
		return p, invalid("unknown format %q", src.Format)
	}
	return p, nil
}

// IOFormats lists the available import and export formats.
type IOFormats struct {
	Import []string `json:"import"`
	Export []string `json:"export"`
	PST    bool     `json:"pst"`
}

// ListIOFormats reports the formats of this build.
func (a *App) ListIOFormats(ctx context.Context) (IOFormats, error) {
	f := IOFormats{Import: []string{"eml", "mbox", "maildir", "thunderbird", "ics", "vcf", "csv", "ldif"},
		Export: []string{"eml", "mbox", "maildir", "ics", "vcf", "csv", "ldif"}, PST: true}
	f.Import = append(f.Import, "pst")
	exportHooks.Range(func(k, _ any) bool {
		f.Export = append(f.Export, k.(string))
		return true
	})
	slices.Sort(f.Export)
	return f, nil
}

// ==== zip helper ===============================================================

type zipWriter struct{ zw *zip.Writer }

func newZipWriter(w io.Writer) *zipWriter { return &zipWriter{zw: zip.NewWriter(w)} }

// AddDir adds all files below dir (relative names, forward slashes).
func (z *zipWriter) AddDir(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			_, err := z.zw.Create(name + "/")
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Method = name, zip.Deflate
		w, err := z.zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		// #nosec G122 -- walks a temporary export directory written by TuxMail itself
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, bytes.NewReader(b))
		return err
	})
}

func (z *zipWriter) Close() error { return z.zw.Close() }
