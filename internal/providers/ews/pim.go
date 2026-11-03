// SPDX-License-Identifier: AGPL-3.0-or-later

package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// ==== out of office ===========================================================

// Vacation mirrors the vacation settings with SetUserOofSettings.
type Vacation struct {
	C *Client
	// Address is the mailbox address (required by the OOF operations).
	Address string
}

var _ vacation.ServerVacationSync = Vacation{}

// Supported implements vacation.ServerVacationSync.
func (Vacation) Supported() bool { return true }

// SetOofRequest renders the SetUserOofSettings body (golden tests).
func (v Vacation) SetOofRequest(s vacation.Settings) string {
	state := "Disabled"
	dur := ""
	if s.Enabled {
		state = "Enabled"
		if !s.Start.IsZero() && !s.End.IsZero() {
			state = "Scheduled"
			dur = `<t:Duration><t:StartTime>` + s.Start.UTC().Format(time.RFC3339) + `</t:StartTime><t:EndTime>` +
				s.End.UTC().Format(time.RFC3339) + `</t:EndTime></t:Duration>`
		}
	}
	in, ext := s.InternalText, s.ExternalText
	if in == "" {
		in = ext
	}
	if ext == "" {
		ext = in
	}
	return `<m:SetUserOofSettingsRequest><t:Mailbox><t:Address>` + esc(v.Address) + `</t:Address></t:Mailbox>` +
		`<t:UserOofSettings><t:OofState>` + state + `</t:OofState><t:ExternalAudience>All</t:ExternalAudience>` + dur +
		`<t:InternalReply><t:Message>` + esc(in) + `</t:Message></t:InternalReply>` +
		`<t:ExternalReply><t:Message>` + esc(ext) + `</t:Message></t:ExternalReply></t:UserOofSettings></m:SetUserOofSettingsRequest>`
}

// Push implements vacation.ServerVacationSync.
func (v Vacation) Push(ctx context.Context, s vacation.Settings) error {
	var resp struct {
		RM responseMessage `xml:"ResponseMessage"`
	}
	if err := v.C.call(ctx, v.SetOofRequest(s), &resp); err != nil {
		return err
	}
	return resp.RM.err()
}

// Oof is the current out-of-office state.
type Oof struct {
	State    string // Disabled | Enabled | Scheduled
	Start    time.Time
	End      time.Time
	Internal string
	External string
}

// Get reads the OOF settings.
func (v Vacation) Get(ctx context.Context) (Oof, error) {
	var resp struct {
		RM       responseMessage `xml:"ResponseMessage"`
		Settings struct {
			State    string    `xml:"OofState"`
			Start    time.Time `xml:"Duration>StartTime"`
			End      time.Time `xml:"Duration>EndTime"`
			Internal string    `xml:"InternalReply>Message"`
			External string    `xml:"ExternalReply>Message"`
		} `xml:"OofSettings"`
	}
	if err := v.C.call(ctx, `<m:GetUserOofSettingsRequest><t:Mailbox><t:Address>`+esc(v.Address)+
		`</t:Address></t:Mailbox></m:GetUserOofSettingsRequest>`, &resp); err != nil {
		return Oof{}, err
	}
	if err := resp.RM.err(); err != nil {
		return Oof{}, err
	}
	st := resp.Settings
	return Oof{State: st.State, Start: st.Start, End: st.End, Internal: st.Internal, External: st.External}, nil
}

// ==== calendar & contacts =====================================================

var (
	_ pimapi.Calendars = (*Client)(nil)
	_ pimapi.Contacts  = (*Client)(nil)
)

type xCalItem struct {
	ItemID    itemID    `xml:"ItemId"`
	UID       string    `xml:"UID"`
	Subject   string    `xml:"Subject"`
	Start     time.Time `xml:"Start"`
	End       time.Time `xml:"End"`
	IsAllDay  bool      `xml:"IsAllDayEvent"`
	Location  string    `xml:"Location"`
	Organizer mailbox   `xml:"Organizer>Mailbox"`
	Required  []struct {
		Mailbox  mailbox `xml:"Mailbox"`
		Response string  `xml:"ResponseType"`
	} `xml:"RequiredAttendees>Attendee"`
	Optional []struct {
		Mailbox  mailbox `xml:"Mailbox"`
		Response string  `xml:"ResponseType"`
	} `xml:"OptionalAttendees>Attendee"`
	MyResponse  string   `xml:"MyResponseType"`
	IsCancelled bool     `xml:"IsCancelled"`
	Categories  []string `xml:"Categories>String"`
	JoinURL     string   `xml:"JoinOnlineMeetingUrl"`
	ChangeKey   string
}

var ewsResponse = map[string]string{"Accept": "accepted", "Tentative": "tentative", "Decline": "declined", "NoResponseReceived": "needs-action",
	"Unknown": "needs-action", "Organizer": "accepted"}

// ListCalendars implements pimapi.Calendars (the default calendar of the mailbox).
func (c *Client) ListCalendars(context.Context) ([]pimapi.Calendar, error) {
	return []pimapi.Calendar{{ID: "calendar", Name: "Kalender", IsDefault: true, Owner: c.cfg.Mailbox}}, nil
}

// ListEvents implements pimapi.Calendars with a CalendarView (expanded occurrences).
func (c *Client) ListEvents(ctx context.Context, calendarID string, from, to time.Time) ([]pimapi.Event, error) {
	if calendarID == "" {
		calendarID = "calendar"
	}
	body := `<m:FindItem Traversal="Shallow"><m:ItemShape><t:BaseShape>AllProperties</t:BaseShape></m:ItemShape>` +
		`<m:CalendarView MaxEntriesReturned="1000" StartDate="` + from.UTC().Format(time.RFC3339) + `" EndDate="` +
		to.UTC().Format(time.RFC3339) + `"/><m:ParentFolderIds>` + c.folderRef(calendarID) + `</m:ParentFolderIds></m:FindItem>`
	var resp struct {
		RM struct {
			Messages []struct {
				responseMessage
				Items []xCalItem `xml:"RootFolder>Items>CalendarItem"`
			} `xml:",any"`
		} `xml:"ResponseMessages"`
	}
	if err := c.call(ctx, body, &resp); err != nil {
		return nil, err
	}
	var out []pimapi.Event
	for _, m := range resp.RM.Messages {
		if err := m.err(); err != nil {
			return nil, err
		}
		for _, it := range m.Items {
			ev := pimapi.Event{ID: it.ItemID.ID, CalendarID: calendarID, UID: it.UID, Summary: it.Subject, Start: it.Start.UTC(),
				End: it.End.UTC(), AllDay: it.IsAllDay, Location: it.Location, Categories: it.Categories, OnlineURL: it.JoinURL,
				IsOnline: it.JoinURL != "", Response: ewsResponse[it.MyResponse], Status: "confirmed", ETag: it.ItemID.ChangeKey}
			if it.IsCancelled {
				ev.Status = "cancelled"
			}
			if it.Organizer.EmailAddress != "" {
				ev.Organizer = &pimapi.Attendee{Name: it.Organizer.Name, Email: it.Organizer.EmailAddress}
			}
			for _, a := range it.Required {
				ev.Attendees = append(ev.Attendees, pimapi.Attendee{Name: a.Mailbox.Name, Email: a.Mailbox.EmailAddress, Role: "required", Status: ewsResponse[a.Response]})
			}
			for _, a := range it.Optional {
				ev.Attendees = append(ev.Attendees, pimapi.Attendee{Name: a.Mailbox.Name, Email: a.Mailbox.EmailAddress, Role: "optional", Status: ewsResponse[a.Response]})
			}
			out = append(out, ev)
		}
	}
	return out, nil
}

func calItemXML(ev pimapi.Event) string {
	var b strings.Builder
	b.WriteString(`<t:CalendarItem><t:Subject>` + esc(ev.Summary) + `</t:Subject>`)
	if ev.Body != "" {
		b.WriteString(`<t:Body BodyType="Text">` + esc(ev.Body) + `</t:Body>`)
	}
	b.WriteString(`<t:Start>` + ev.Start.UTC().Format(time.RFC3339) + `</t:Start><t:End>` + ev.End.UTC().Format(time.RFC3339) + `</t:End>`)
	if ev.AllDay {
		b.WriteString(`<t:IsAllDayEvent>true</t:IsAllDayEvent>`)
	}
	if ev.Location != "" {
		b.WriteString(`<t:Location>` + esc(ev.Location) + `</t:Location>`)
	}
	var req, opt strings.Builder
	for _, a := range ev.Attendees {
		x := `<t:Attendee><t:Mailbox><t:EmailAddress>` + esc(a.Email) + `</t:EmailAddress></t:Mailbox></t:Attendee>`
		if a.Role == "optional" {
			opt.WriteString(x)
		} else {
			req.WriteString(x)
		}
	}
	if req.Len() > 0 {
		b.WriteString(`<t:RequiredAttendees>` + req.String() + `</t:RequiredAttendees>`)
	}
	if opt.Len() > 0 {
		b.WriteString(`<t:OptionalAttendees>` + opt.String() + `</t:OptionalAttendees>`)
	}
	b.WriteString(`</t:CalendarItem>`)
	return b.String()
}

type createResp struct {
	RM struct {
		Messages []struct {
			responseMessage
			Items struct {
				List []struct {
					ItemID itemID `xml:"ItemId"`
				} `xml:",any"`
			} `xml:"Items"`
		} `xml:",any"`
	} `xml:"ResponseMessages"`
}

func (r createResp) first() (string, error) {
	if len(r.RM.Messages) == 0 {
		return "", fmt.Errorf("ews: empty response")
	}
	m := r.RM.Messages[0]
	if err := m.err(); err != nil {
		return "", err
	}
	if len(m.Items.List) == 0 {
		return "", nil
	}
	return m.Items.List[0].ItemID.ID, nil
}

// CreateEvent implements pimapi.Calendars (invitations are sent to attendees).
func (c *Client) CreateEvent(ctx context.Context, calendarID string, ev pimapi.Event) (pimapi.Event, error) {
	if calendarID == "" {
		calendarID = "calendar"
	}
	mode := "SendToNone"
	if len(ev.Attendees) > 0 {
		mode = "SendToAllAndSaveCopy"
	}
	var resp createResp
	if err := c.call(ctx, `<m:CreateItem SendMeetingInvitations="`+mode+`"><m:SavedItemFolderId>`+c.folderRef(calendarID)+
		`</m:SavedItemFolderId><m:Items>`+calItemXML(ev)+`</m:Items></m:CreateItem>`, &resp); err != nil {
		return ev, err
	}
	id, err := resp.first()
	ev.ID, ev.CalendarID = id, calendarID
	return ev, err
}

// UpdateEvent implements pimapi.Calendars (subject, time and location).
func (c *Client) UpdateEvent(ctx context.Context, calendarID string, ev pimapi.Event) (pimapi.Event, error) {
	set := func(uri, val string) string {
		return `<t:SetItemField><t:FieldURI FieldURI="` + uri + `"/><t:CalendarItem>` + val + `</t:CalendarItem></t:SetItemField>`
	}
	updates := set("item:Subject", `<t:Subject>`+esc(ev.Summary)+`</t:Subject>`) +
		set("calendar:Start", `<t:Start>`+ev.Start.UTC().Format(time.RFC3339)+`</t:Start>`) +
		set("calendar:End", `<t:End>`+ev.End.UTC().Format(time.RFC3339)+`</t:End>`) +
		set("calendar:Location", `<t:Location>`+esc(ev.Location)+`</t:Location>`)
	var resp createResp
	if err := c.call(ctx, `<m:UpdateItem ConflictResolution="AlwaysOverwrite" SendMeetingInvitationsOrCancellations="SendToChangedAndSaveCopy">`+
		`<m:ItemChanges><t:ItemChange><t:ItemId Id="`+esc(ev.ID)+`"/><t:Updates>`+updates+`</t:Updates></t:ItemChange></m:ItemChanges></m:UpdateItem>`, &resp); err != nil {
		return ev, err
	}
	_, err := resp.first()
	return ev, err
}

// DeleteEvent implements pimapi.Calendars (cancellations are sent).
func (c *Client) DeleteEvent(ctx context.Context, _ string, eventID string) error {
	var resp createResp
	if err := c.call(ctx, `<m:DeleteItem DeleteType="MoveToDeletedItems" SendMeetingCancellations="SendToAllAndSaveCopy"><m:ItemIds>`+
		itemIDs([]string{eventID})+`</m:ItemIds></m:DeleteItem>`, &resp); err != nil {
		return err
	}
	if _, err := resp.first(); err != nil && !IsCode(err, "ErrorItemNotFound") {
		return err
	}
	return nil
}

// Respond implements pimapi.Calendars (AcceptItem, TentativelyAcceptItem, DeclineItem).
func (c *Client) Respond(ctx context.Context, _ string, eventID, response, comment string, notify bool) error {
	el := map[string]string{pimapi.Accept: "AcceptItem", pimapi.Tentative: "TentativelyAcceptItem", pimapi.Decline: "DeclineItem"}[response]
	if el == "" {
		return fmt.Errorf("ews: unknown response %q", response)
	}
	disp := "SendAndSaveCopy"
	if !notify {
		disp = "SaveOnly"
	}
	body := ""
	if comment != "" {
		body = `<t:Body BodyType="Text">` + esc(comment) + `</t:Body>`
	}
	var resp createResp
	if err := c.call(ctx, `<m:CreateItem MessageDisposition="`+disp+`"><m:Items><t:`+el+`><t:ReferenceItemId Id="`+esc(eventID)+`"/>`+
		body+`</t:`+el+`></m:Items></m:CreateItem>`, &resp); err != nil {
		return err
	}
	_, err := resp.first()
	return err
}

type xContact struct {
	ItemID      itemID `xml:"ItemId"`
	DisplayName string `xml:"DisplayName"`
	GivenName   string `xml:"GivenName"`
	Surname     string `xml:"Surname"`
	Company     string `xml:"CompanyName"`
	JobTitle    string `xml:"JobTitle"`
	Emails      []struct {
		Value string `xml:",chardata"`
	} `xml:"EmailAddresses>Entry"`
	Phones []struct {
		Value string `xml:",chardata"`
	} `xml:"PhoneNumbers>Entry"`
	XMLName xml.Name
}

// ListAddressBooks implements pimapi.Contacts (the default contacts folder).
func (c *Client) ListAddressBooks(context.Context) ([]pimapi.AddressBook, error) {
	return []pimapi.AddressBook{{ID: "contacts", Name: "Kontakte", Owner: c.cfg.Mailbox}}, nil
}

// ListContacts implements pimapi.Contacts.
func (c *Client) ListContacts(ctx context.Context, book string) ([]pimapi.Contact, error) {
	if book == "" {
		book = "contacts"
	}
	var out []pimapi.Contact
	for offset := 0; ; {
		body := fmt.Sprintf(`<m:FindItem Traversal="Shallow"><m:ItemShape><t:BaseShape>AllProperties</t:BaseShape></m:ItemShape>`+
			`<m:IndexedPageItemView MaxEntriesReturned="500" Offset="%d" BasePoint="Beginning"/><m:ParentFolderIds>%s</m:ParentFolderIds></m:FindItem>`,
			offset, c.folderRef(book))
		var resp struct {
			RM struct {
				Messages []struct {
					responseMessage
					Root struct {
						Last     bool       `xml:"IncludesLastItemInRange,attr"`
						Next     int        `xml:"IndexedPagingOffset,attr"`
						Contacts []xContact `xml:"Items>Contact"`
					} `xml:"RootFolder"`
				} `xml:",any"`
			} `xml:"ResponseMessages"`
		}
		if err := c.call(ctx, body, &resp); err != nil {
			return nil, err
		}
		if len(resp.RM.Messages) == 0 {
			return out, nil
		}
		m := resp.RM.Messages[0]
		if err := m.err(); err != nil {
			return nil, err
		}
		for _, x := range m.Root.Contacts {
			ct := pimapi.Contact{ID: x.ItemID.ID, FolderID: book, DisplayName: x.DisplayName, GivenName: x.GivenName,
				FamilyName: x.Surname, Company: x.Company, JobTitle: x.JobTitle, ETag: x.ItemID.ChangeKey}
			for _, e := range x.Emails {
				if v := strings.TrimPrefix(strings.TrimSpace(e.Value), "SMTP:"); v != "" {
					ct.Emails = append(ct.Emails, v)
				}
			}
			for _, p := range x.Phones {
				if v := strings.TrimSpace(p.Value); v != "" {
					ct.Phones = append(ct.Phones, v)
				}
			}
			out = append(out, ct)
		}
		if m.Root.Last || m.Root.Next <= offset {
			return out, nil
		}
		offset = m.Root.Next
	}
}

func contactXML(ct pimapi.Contact) string {
	var b strings.Builder
	b.WriteString(`<t:Contact>`)
	if ct.GivenName != "" {
		b.WriteString(`<t:GivenName>` + esc(ct.GivenName) + `</t:GivenName>`)
	}
	if ct.FamilyName != "" {
		b.WriteString(`<t:Surname>` + esc(ct.FamilyName) + `</t:Surname>`)
	}
	if ct.Company != "" {
		b.WriteString(`<t:CompanyName>` + esc(ct.Company) + `</t:CompanyName>`)
	}
	if len(ct.Emails) > 0 {
		b.WriteString(`<t:EmailAddresses>`)
		for i, e := range ct.Emails {
			if i >= 3 {
				break
			}
			b.WriteString(fmt.Sprintf(`<t:Entry Key="EmailAddress%d">%s</t:Entry>`, i+1, esc(e)))
		}
		b.WriteString(`</t:EmailAddresses>`)
	}
	b.WriteString(`</t:Contact>`)
	return b.String()
}

// CreateContact implements pimapi.Contacts.
func (c *Client) CreateContact(ctx context.Context, book string, ct pimapi.Contact) (pimapi.Contact, error) {
	if book == "" {
		book = "contacts"
	}
	var resp createResp
	if err := c.call(ctx, `<m:CreateItem><m:SavedItemFolderId>`+c.folderRef(book)+`</m:SavedItemFolderId><m:Items>`+
		contactXML(ct)+`</m:Items></m:CreateItem>`, &resp); err != nil {
		return ct, err
	}
	id, err := resp.first()
	ct.ID = id
	return ct, err
}

// UpdateContact implements pimapi.Contacts (delete + create; EWS indexed
// fields make partial updates verbose).
func (c *Client) UpdateContact(ctx context.Context, book string, ct pimapi.Contact) (pimapi.Contact, error) {
	if err := c.DeleteContact(ctx, book, ct.ID); err != nil {
		return ct, err
	}
	return c.CreateContact(ctx, book, ct)
}

// DeleteContact implements pimapi.Contacts.
func (c *Client) DeleteContact(ctx context.Context, _ string, id string) error {
	var resp createResp
	if err := c.call(ctx, `<m:DeleteItem DeleteType="MoveToDeletedItems"><m:ItemIds>`+itemIDs([]string{id})+`</m:ItemIds></m:DeleteItem>`, &resp); err != nil {
		return err
	}
	if _, err := resp.first(); err != nil && !IsCode(err, "ErrorItemNotFound") {
		return err
	}
	return nil
}
