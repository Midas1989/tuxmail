// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas"
)

// The race with the mail worker's provisioning shows as an error or as errors
// per folder: both are tried again, and it ends.
func TestEASPIMRetriesBothKindsOfFailure(t *testing.T) {
	calls := 0
	res, err := easPIMWithRetry(context.Background(), time.Millisecond, func() (eas.PIMResult, error) {
		calls++
		switch calls {
		case 1:
			return eas.PIMResult{}, errors.New("provisioning")
		case 2:
			return eas.PIMResult{Errors: []error{errors.New("calendar: provisioning")}}, nil
		}
		return eas.PIMResult{}, nil
	})
	if err != nil || len(res.Errors) != 0 || calls != 3 {
		t.Fatalf("%d calls, %+v, %v", calls, res, err)
	}
	calls = 0
	if _, err := easPIMWithRetry(context.Background(), time.Millisecond, func() (eas.PIMResult, error) {
		calls++
		return eas.PIMResult{}, errors.New("down")
	}); err == nil || calls != 4 {
		t.Fatalf("%d calls, %v", calls, err)
	}
}
