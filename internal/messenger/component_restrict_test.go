// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"errors"
	"testing"
)

// Re-audit 2026-11-05 (NH-09): a server user cannot make the server download
// and install an external program; the UI is told it is not installable.
func TestComponentInstallRestrictedInServerMode(t *testing.T) {
	svc, _ := newHostSettingsService(t, true)
	if err := svc.InstallComponent(context.Background(), "signal"); !errors.Is(err, ErrInstallRestricted) {
		t.Fatalf("InstallComponent in server mode = %v, want ErrInstallRestricted", err)
	}
	if c := svc.Component("signal"); c == nil || c.Installable {
		t.Fatalf("component offered for installation in server mode: %+v", c)
	}
	if svc.installs["signal"] != nil {
		t.Fatal("an install was started")
	}
}
