// SPDX-License-Identifier: AGPL-3.0-or-later

package easvacation

import (
	"context"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/providers/eas/eastest"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

func TestPushAndFetch(t *testing.T) {
	srv := eastest.New()
	defer srv.Close()
	c, err := eas.NewClient(eas.ClientConfig{URL: srv.EndpointURL(), Username: "alice", Password: "secret", DeviceID: "OOF1",
		State: &eas.MemoryState{}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	s := &Sync{Client: c, Now: func() time.Time { return now }}
	if !s.Supported() {
		t.Fatal("not supported")
	}
	ctx := context.Background()
	st := vacation.Settings{Enabled: true, Start: now.Add(24 * time.Hour), End: now.Add(10 * 24 * time.Hour),
		InternalText: "Bin im Urlaub ({{betreff}}).", ExternalText: "On vacation."}
	if err := s.Push(ctx, st); err != nil {
		t.Fatal(err)
	}
	state, internal, external := srv.Oof()
	if state != eas.OofTimed || internal != "Bin im Urlaub ()." || external != "On vacation." {
		t.Fatalf("server oof %d %q %q", state, internal, external)
	}
	got, err := s.Fetch(ctx)
	if err != nil || !got.Enabled || !got.Start.Equal(st.Start) || !got.End.Equal(st.End) || got.ExternalText != "On vacation." {
		t.Fatalf("fetch %+v %v", got, err)
	}
	// Disabling switches the server off.
	if err := s.Push(ctx, vacation.Settings{}); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := srv.Oof(); state != eas.OofDisabled {
		t.Fatalf("state %d", state)
	}
	// Open-ended absence -> global.
	if o := ToOof(vacation.Settings{Enabled: true, InternalText: "x"}, now); o.State != eas.OofGlobal || o.ExternalText != "x" {
		t.Fatalf("%+v", o)
	}
	// Ended absence -> disabled.
	if o := ToOof(vacation.Settings{Enabled: true, InternalText: "x", End: now.Add(-time.Hour)}, now); o.State != eas.OofDisabled {
		t.Fatalf("%+v", o)
	}
}
