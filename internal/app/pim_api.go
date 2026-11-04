// SPDX-License-Identifier: AGPL-3.0-or-later

package app

// Wails bindings of the calendar, task and contact API (the "// --- PIM ---"
// section of frontend/src/lib/api/wails.ts).

// Calendars & events

func (x *Api) ListCalendars() ([]PimCalendar, error) { return r1(x.app.ListCalendars(x.ctx)) }
func (x *Api) SaveCalendar(in CalendarInput) (PimCalendar, error) {
	return r1(x.app.SaveCalendar(x.ctx, in))
}
func (x *Api) DeleteCalendar(id int64) error { return werr(x.app.DeleteCalendar(x.ctx, id)) }
func (x *Api) SyncCalendars() error          { return werr(x.app.SyncPIM(x.ctx)) }
func (x *Api) ImportCalendarFile(calendarID int64, data string) (AddedResult, error) {
	n, err := x.app.ImportICS(x.ctx, calendarID, []byte(data))
	return AddedResult{Added: n}, werr(err)
}
func (x *Api) ListEvents(q EventQuery) ([]CalendarEvent, error) {
	return r1(x.app.ListEvents(x.ctx, q))
}
func (x *Api) GetEvent(id string) (CalendarEvent, error) { return r1(x.app.GetEvent(x.ctx, id)) }
func (x *Api) CreateEvent(in EventInput) (CalendarEvent, error) {
	return r1(x.app.CreateEvent(x.ctx, in))
}
func (x *Api) UpdateEvent(id string, in EventInput, scope string) (CalendarEvent, error) {
	return r1(x.app.UpdateEvent(x.ctx, id, in, scope))
}
func (x *Api) DeleteEvent(id, scope string) error { return werr(x.app.DeleteEvent(x.ctx, id, scope)) }
func (x *Api) ResolveEventConflict(objectID int64, keep bool) error {
	return werr(x.app.ResolveConflict(x.ctx, objectID, keep))
}
func (x *Api) GetFreeBusy(q FreeBusyQuery) ([]FreeBusy, error) {
	return r1(x.app.GetFreeBusy(x.ctx, q))
}
func (x *Api) SnoozeReminder(eventID, trigger string, minutes int) error {
	return werr(x.app.SnoozeReminder(x.ctx, eventID, trigger, minutes))
}
func (x *Api) AddDavAccount(in DavAccountInput) (Account, error) {
	return r1(x.app.AddDavAccount(x.ctx, in))
}

// Tasks

func (x *Api) ListTasks(q TaskQuery) ([]PimTask, error) { return r1(x.app.ListTasks(x.ctx, q)) }
func (x *Api) SaveTask(in TaskInput) (PimTask, error)   { return r1(x.app.SaveTask(x.ctx, in)) }
func (x *Api) DeleteTask(id int64) error                { return werr(x.app.DeleteTask(x.ctx, id)) }

// Invitations

func (x *Api) GetMessageInvitations(messageID int64) ([]Invitation, error) {
	return r1(x.app.GetMessageInvitations(x.ctx, messageID))
}
func (x *Api) ListInvitations() ([]Invitation, error) { return r1(x.app.ListInvitations(x.ctx)) }
func (x *Api) RespondInvitation(id int64, r InvitationResponse) (Invitation, error) {
	return r1(x.app.RespondInvitation(x.ctx, id, r))
}

// Contacts

func (x *Api) ListAddressBooks() ([]PimAddressBook, error) { return r1(x.app.ListAddressBooks(x.ctx)) }
func (x *Api) SaveAddressBook(in AddressBookInput) (PimAddressBook, error) {
	return r1(x.app.SaveAddressBook(x.ctx, in))
}
func (x *Api) DeleteAddressBook(id int64) error { return werr(x.app.DeleteAddressBook(x.ctx, id)) }
func (x *Api) ListContacts(q ContactQuery) ([]PimContact, error) {
	return r1(x.app.ListContacts(x.ctx, q))
}
func (x *Api) GetContact(id int64) (PimContact, error)      { return r1(x.app.GetContact(x.ctx, id)) }
func (x *Api) SaveContact(c PimContact) (PimContact, error) { return r1(x.app.SaveContact(x.ctx, c)) }
func (x *Api) DeleteContact(id int64) error                 { return werr(x.app.DeleteContact(x.ctx, id)) }
func (x *Api) ListContactGroups() ([]ContactGroup, error)   { return r1(x.app.ListContactGroups(x.ctx)) }
func (x *Api) FindDuplicateContacts() ([]DuplicateGroup, error) {
	return r1(x.app.FindDuplicateContacts(x.ctx))
}
func (x *Api) MergeContacts(ids []int64, merged *PimContact) (PimContact, error) {
	return r1(x.app.MergeContacts(x.ctx, ids, merged))
}
func (x *Api) AutocompleteAddresses(q string, limit int) ([]AddressSuggestion, error) {
	return r1(x.app.AutocompleteAddresses(x.ctx, q, limit))
}
