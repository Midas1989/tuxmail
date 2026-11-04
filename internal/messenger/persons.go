// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"errors"
	"strings"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
)

// E164 normalizes a phone number or a phone-based user id (WhatsApp JID
// "4917012345@s.whatsapp.net", "+49 170 12345", "0049...") to E.164
// ("+4917012345"). Numbers without country code and anything that is not a
// phone number yield "".
func E164(v string) string {
	v = strings.TrimSpace(v)
	if at := strings.IndexByte(v, '@'); at > 0 {
		user := v[:at]
		if i := strings.IndexAny(user, ":."); i > 0 { // device suffix 49170:12@s.whatsapp.net
			user = user[:i]
		}
		v = "+" + user
	}
	var digits strings.Builder
	plus := strings.HasPrefix(v, "+")
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case strings.ContainsRune("+-/ ().", r):
		default:
			return ""
		}
	}
	d := digits.String()
	if !plus {
		if !strings.HasPrefix(d, "00") {
			return "" // national number: country unknown
		}
		d = d[2:]
	}
	if len(d) < 6 || len(d) > 15 || d[0] == '0' {
		return ""
	}
	return "+" + d
}

// handleValue returns the channel handle of a contact: phone-based
// networks use the E.164 number, the others the network user id.
func handleValue(channel string, c wire.Contact) string {
	switch channel {
	case store.HandleWhatsApp, store.HandleSignal:
		if p := E164(c.Phone); p != "" {
			return p
		}
		if p := E164(c.ID); p != "" && channel == store.HandleWhatsApp {
			return p
		}
	}
	return strings.TrimSpace(c.ID)
}

func contactName(c wire.Contact) string {
	switch {
	case strings.TrimSpace(c.Name) != "":
		return strings.TrimSpace(c.Name)
	case c.Username != "":
		return c.Username
	case E164(c.Phone) != "":
		return E164(c.Phone)
	}
	return c.ID
}

// resolvePerson finds or creates the person behind a contact (KONZEPT
// 10.2): first by the channel handle, then by phone number across all
// channels; persons that turn out to be the same (same phone number) are
// merged. The channel handle and the phone number are attached.
func (s *Service) resolvePerson(ctx context.Context, channel string, c wire.Contact) (int64, error) {
	hv := handleValue(channel, c)
	if hv == "" {
		return 0, nil
	}
	phone := E164(c.Phone)
	if phone == "" && (channel == store.HandleWhatsApp || channel == store.HandleSignal) {
		phone = E164(hv)
	}
	var byHandle, byPhone int64
	if p, err := s.st.FindPersonByHandle(ctx, channel, hv); err == nil {
		byHandle = p.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if phone != "" {
		for _, kind := range []string{store.HandlePhone, store.HandleWhatsApp, store.HandleSignal, store.HandleTelegram} {
			p, err := s.st.FindPersonByHandle(ctx, kind, phone)
			if err == nil {
				byPhone = p.ID
				break
			}
			if !errors.Is(err, store.ErrNotFound) {
				return 0, err
			}
		}
	}
	id := byHandle
	switch {
	case byHandle != 0 && byPhone != 0 && byHandle != byPhone:
		// Same phone number: one human with two person rows.
		if err := s.st.MergePersons(ctx, byPhone, byHandle); err != nil {
			return 0, err
		}
		id = byPhone
		s.mergedPersons()
	case id == 0 && byPhone != 0:
		id = byPhone
	case id == 0:
		nid, err := s.st.CreatePerson(ctx, store.Person{DisplayName: contactName(c)})
		if err != nil {
			return 0, err
		}
		id = nid
	}
	add := func(kind, v string) error {
		err := s.st.AddHandle(ctx, store.Handle{PersonID: id, Kind: kind, Value: v, Verified: true})
		if errors.Is(err, store.ErrHandleInUse) {
			return nil // owned by someone else (e.g. a shared number): keep it
		}
		return err
	}
	if err := add(channel, hv); err != nil {
		return 0, err
	}
	if phone != "" {
		if err := add(store.HandlePhone, phone); err != nil {
			return 0, err
		}
	}
	// A better name than the number we stored before.
	if p, err := s.st.GetPerson(ctx, id); err == nil && strings.TrimSpace(c.Name) != "" &&
		(p.DisplayName == "" || p.DisplayName == hv || p.DisplayName == phone || p.DisplayName == c.ID) {
		p.DisplayName = strings.TrimSpace(c.Name)
		if err := s.st.UpdatePerson(ctx, p); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// mergedPersons drops cached counterparts so that the person grouping is
// recomputed after a merge.
func (s *Service) mergedPersons() {
	if err := s.st.ResetCounterparts(context.Background()); err != nil {
		s.log.Warn("messenger: reset counterparts", "err", err)
	}
	s.notify.ViewsChanged()
}

// personByAddress returns the person owning an e-mail address (0 if unknown).
func (s *Service) personByAddress(ctx context.Context, email string) (int64, error) {
	p, err := s.st.FindPersonByHandle(ctx, store.HandleEmail, email)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	return p.ID, err
}
