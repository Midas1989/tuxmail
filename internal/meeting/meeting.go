// SPDX-License-Identifier: AGPL-3.0-or-later

// Package meeting creates video conference links for calendar events
// (KONZEPT 9.1): Nextcloud Talk rooms (OCS spreed API: create the
// conversation, set a password, add participants), Jitsi rooms (configurable
// instance, random room name) and Microsoft Teams meetings through a
// TeamsCreator implemented by the Graph provider. The link is written into
// LOCATION, CONFERENCE (RFC 7986) and the description of the event.
package meeting

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// Providers.
const (
	ProviderJitsi     = "jitsi"
	ProviderNextcloud = "nextcloud_talk"
	ProviderTeams     = "teams"
)

// Request describes the meeting to create.
type Request struct {
	Provider string `json:"provider"`
	Title    string `json:"title"`
	// Participants are e-mail addresses (Talk: invited as e-mail guests,
	// or as users when they match a Nextcloud user).
	Participants []string  `json:"participants,omitempty"`
	Start        time.Time `json:"start,omitzero"`
	End          time.Time `json:"end,omitzero"`
	// Public creates a public (link) room; Password protects it.
	Public   bool   `json:"public,omitempty"`
	Password string `json:"password,omitempty"`
	// AccountID selects the Nextcloud or Microsoft 365 account.
	AccountID int64 `json:"accountId,omitempty"`
}

// Link is a created meeting.
type Link struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Label    string `json:"label"`
	Password string `json:"password,omitempty"`
	// Token / ID are provider-specific identifiers (Talk token, Teams id).
	Token string `json:"token,omitempty"`
	// DialIn holds optional phone dial-in information.
	DialIn string `json:"dialIn,omitempty"`
}

// TeamsCreator creates Teams meetings (implemented by the Graph provider:
// onlineMeeting / event isOnlineMeeting).
type TeamsCreator interface {
	CreateTeamsMeeting(ctx context.Context, accountID int64, req Request) (Link, error)
}

// Service creates meeting links.
type Service struct {
	// JitsiBaseURL is the Jitsi instance (default https://meet.jit.si).
	JitsiBaseURL string
	Talk         *TalkClient // optional
	Teams        TeamsCreator
}

// Errors.
var (
	ErrUnknownProvider = errors.New("meeting: unknown provider")
	ErrNotConfigured   = errors.New("meeting: provider not configured")
)

// Create creates a meeting with the requested provider.
func (s *Service) Create(ctx context.Context, req Request) (Link, error) {
	switch req.Provider {
	case ProviderJitsi, "":
		return s.Jitsi(req)
	case ProviderNextcloud:
		if s.Talk == nil {
			return Link{}, fmt.Errorf("%w: nextcloud talk", ErrNotConfigured)
		}
		return s.Talk.CreateRoom(ctx, req)
	case ProviderTeams:
		if s.Teams == nil {
			return Link{}, fmt.Errorf("%w: teams", ErrNotConfigured)
		}
		return s.Teams.CreateTeamsMeeting(ctx, req.AccountID, req)
	}
	return Link{}, fmt.Errorf("%w: %q", ErrUnknownProvider, req.Provider)
}

// Jitsi returns a link with an unguessable random room name.
func (s *Service) Jitsi(req Request) (Link, error) {
	base := strings.TrimRight(s.JitsiBaseURL, "/")
	if base == "" {
		base = "https://meet.jit.si"
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Link{}, fmt.Errorf("meeting: invalid Jitsi URL %q", base)
	}
	room, err := RoomName(req.Title)
	if err != nil {
		return Link{}, err
	}
	return Link{Provider: ProviderJitsi, URL: base + "/" + room, Label: "Jitsi Meet", Token: room, Password: req.Password}, nil
}

const roomAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RoomName returns "<Slug>-<16 random characters>" (about 94 bits).
func RoomName(title string) (string, error) {
	var slug strings.Builder
	for _, r := range title {
		if slug.Len() >= 24 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			slug.WriteRune(r)
		case r == 'ä':
			slug.WriteString("ae")
		case r == 'ö':
			slug.WriteString("oe")
		case r == 'ü':
			slug.WriteString("ue")
		case r == 'ß':
			slug.WriteString("ss")
		case r == 'Ä':
			slug.WriteString("Ae")
		case r == 'Ö':
			slug.WriteString("Oe")
		case r == 'Ü':
			slug.WriteString("Ue")
		}
	}
	rnd, err := randomString(16)
	if err != nil {
		return "", err
	}
	if slug.Len() == 0 {
		return "TuxMail-" + rnd, nil
	}
	return slug.String() + "-" + rnd, nil
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(roomAlphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = roomAlphabet[v.Int64()]
	}
	return string(b), nil
}

// RandomPassword returns a meeting password.
func RandomPassword() string {
	s, _ := randomString(12)
	return s
}

// ==== iCalendar ==================================================================

// Description returns the text block added to the event description.
func Description(l Link, lang string) string {
	var b strings.Builder
	if strings.HasPrefix(lang, "de") {
		b.WriteString("Online-Meeting beitreten (" + l.Label + "): " + l.URL)
		if l.Password != "" {
			b.WriteString("\nPasswort: " + l.Password)
		}
	} else {
		b.WriteString("Join the online meeting (" + l.Label + "): " + l.URL)
		if l.Password != "" {
			b.WriteString("\nPassword: " + l.Password)
		}
	}
	if l.DialIn != "" {
		b.WriteString("\n" + l.DialIn)
	}
	return b.String()
}

// ApplyToICS writes the link into every VEVENT of an iCalendar object:
// LOCATION (if empty), CONFERENCE (RFC 7986, FEATURE=AUDIO,VIDEO) and the
// DESCRIPTION. An existing CONFERENCE with the same URL is kept.
func ApplyToICS(ics string, l Link, lang string) string {
	lines := unfold(ics)
	var out []string
	inEvent := false
	var hasLoc, hasConf bool
	descIdx := -1
	for _, ln := range lines {
		name := propName(ln)
		switch {
		case ln == "BEGIN:VEVENT":
			inEvent, hasLoc, hasConf, descIdx = true, false, false, -1
		case inEvent && ln == "END:VEVENT":
			if !hasLoc {
				out = append(out, "LOCATION:"+escapeText(l.URL))
			}
			if !hasConf {
				out = append(out, "CONFERENCE;VALUE=URI;FEATURE=AUDIO,VIDEO;LABEL="+paramValue(l.Label)+":"+l.URL)
			}
			desc := escapeText(Description(l, lang))
			if descIdx >= 0 {
				if !strings.Contains(out[descIdx], escapeText(l.URL)) {
					out[descIdx] += `\n\n` + desc
				}
			} else {
				out = append(out, "DESCRIPTION:"+desc)
			}
			inEvent = false
		case inEvent && name == "LOCATION":
			if v := ln[strings.IndexByte(ln, ':')+1:]; strings.TrimSpace(v) != "" {
				hasLoc = true
			} else {
				continue // replace empty LOCATION
			}
		case inEvent && name == "CONFERENCE":
			if strings.HasSuffix(ln, ":"+l.URL) {
				hasConf = true
			}
		case inEvent && name == "DESCRIPTION":
			descIdx = len(out)
		}
		out = append(out, ln)
	}
	var b strings.Builder
	for _, ln := range out {
		b.WriteString(fold(ln))
		b.WriteString("\r\n")
	}
	return b.String()
}

func propName(ln string) string {
	end := strings.IndexAny(ln, ";:")
	if end < 0 {
		return ""
	}
	return strings.ToUpper(ln[:end])
}

func unfold(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if (strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t")) && len(out) > 0 {
			out[len(out)-1] += ln[1:]
			continue
		}
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// fold folds a content line at 75 octets without splitting UTF-8 sequences.
func fold(ln string) string {
	if len(ln) <= 75 {
		return ln
	}
	var b strings.Builder
	n := 0
	for _, r := range ln {
		rl := len(string(r))
		if n+rl > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += rl
	}
	return b.String()
}

func escapeText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`)
	return r.Replace(s)
}

func paramValue(s string) string {
	s = strings.ReplaceAll(s, `"`, "'")
	if strings.ContainsAny(s, ";:,") {
		return `"` + s + `"`
	}
	return s
}
