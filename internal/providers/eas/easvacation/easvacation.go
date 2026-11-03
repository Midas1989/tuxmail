// SPDX-License-Identifier: AGPL-3.0-or-later

// Package easvacation mirrors TuxMail's out-of-office settings to an
// Exchange ActiveSync server (Settings/Oof, [MS-ASCMD] 2.2.1.18), so the
// server answers while the client is offline.
package easvacation

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// OofClient is the part of *eas.Client used here.
type OofClient interface {
	GetOof(ctx context.Context) (eas.Oof, error)
	SetOof(ctx context.Context, o eas.Oof) error
}

// Sync implements vacation.ServerVacationSync for one EAS account.
type Sync struct {
	Client OofClient
	// Now is used to decide whether an already ended absence is pushed
	// as disabled (default time.Now).
	Now func() time.Time
}

var _ vacation.ServerVacationSync = (*Sync)(nil)

// Supported implements vacation.ServerVacationSync.
func (s *Sync) Supported() bool { return s.Client != nil }

var placeholder = regexp.MustCompile(`\{\{[^}]*\}\}`)

// plain removes template placeholders that the server cannot expand.
func plain(text string) string {
	return strings.TrimSpace(placeholder.ReplaceAllString(text, ""))
}

// ToOof converts the settings to EAS OOF settings.
func ToOof(st vacation.Settings, now time.Time) eas.Oof {
	o := eas.Oof{State: eas.OofDisabled}
	internal, external := plain(st.InternalText), plain(st.ExternalText)
	if internal == "" {
		internal = external
	}
	if external == "" {
		external = internal
	}
	o.InternalText, o.ExternalText = internal, external
	o.ExternalEnabled = external != ""
	if !st.Enabled || (!st.End.IsZero() && !st.End.After(now)) {
		return o
	}
	switch {
	case st.Start.IsZero() && st.End.IsZero():
		o.State = eas.OofGlobal
	default:
		o.State = eas.OofTimed
		o.Start, o.End = st.Start, st.End
		if o.Start.IsZero() {
			o.Start = now
		}
		if o.End.IsZero() {
			// EAS needs an end for timed OOF; open-ended absences are global.
			o.State, o.Start = eas.OofGlobal, time.Time{}
			if st.Start.After(now) {
				o.State, o.Start, o.End = eas.OofTimed, st.Start, st.Start.AddDate(10, 0, 0)
			}
		}
	}
	return o
}

// Push implements vacation.ServerVacationSync.
func (s *Sync) Push(ctx context.Context, st vacation.Settings) error {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return s.Client.SetOof(ctx, ToOof(st, now()))
}

// Fetch reads the server settings (e.g. set by Outlook) as vacation
// settings; templates and filters that EAS does not know are left empty.
func (s *Sync) Fetch(ctx context.Context) (vacation.Settings, error) {
	o, err := s.Client.GetOof(ctx)
	if err != nil {
		return vacation.Settings{}, err
	}
	st := vacation.Settings{Enabled: o.State != eas.OofDisabled, InternalText: o.InternalText}
	if o.ExternalEnabled {
		st.ExternalText = o.ExternalText
	}
	if o.State == eas.OofTimed {
		st.Start, st.End = o.Start, o.End
	}
	return st, nil
}
