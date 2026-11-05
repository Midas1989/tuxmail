// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// Step-up checks (NA-05) are the re-authentications of an already logged-in
// user: approving a device, switching off TOTP, regenerating recovery codes,
// setting a first local password. The attacker model is a stolen session
// cookie: the 6-digit TOTP space must not be guessable from many source
// addresses, so wrong answers are counted persistently per USER (login_throttle
// key "s:<id>", independent of the client network) and lock the step-up
// functions for that user with escalating durations. The key is separate from
// the login counters ("u:..."): a locked step-up never blocks the normal login
// path, and failed logins never block step-up for the legitimate owner.
//
// The counter resets only after a day without failures (a successful complete
// step-up clears it at once), so after the free attempts at most one guess per
// stepUpMaxLock remains.
const (
	stepUpFreeFails = 5
	stepUpWindow    = 24 * time.Hour
	stepUpBaseLock  = 5 * time.Minute
	stepUpMaxLock   = time.Hour
)

var stepUpPolicy = throttlePolicy{free: stepUpFreeFails, window: stepUpWindow, baseLock: stepUpBaseLock, maxLock: stepUpMaxLock}

func stepUpKey(userID int64) string { return "s:" + strconv.FormatInt(userID, 10) }

// StepUpCheck fails with a *ThrottleError while step-up checks of the user
// are locked.
func (s *Service) StepUpCheck(ctx context.Context, userID int64) error {
	return s.checkThrottle(ctx, stepUpKey(userID))
}

// ClearStepUp resets the step-up counter after a completely successful
// re-authentication.
func (s *Service) ClearStepUp(ctx context.Context, userID int64) {
	s.clearThrottle(ctx, stepUpKey(userID))
}

func (s *Service) stepUpFailed(ctx context.Context, userID int64, what string) {
	s.recordFailure(ctx, stepUpKey(userID), stepUpPolicy)
	s.auditUser(ctx, "stepup.failed", userID, errors.New(what), "")
}

// VerifyStepUpSecondFactor checks a TOTP or recovery code of a logged-in user
// under the persistent step-up lockout. It does not clear the counter on
// success; call ClearStepUp when the whole re-authentication succeeded.
func (s *Service) VerifyStepUpSecondFactor(ctx context.Context, userID int64, totpCode, recoveryCode string) error {
	if err := s.StepUpCheck(ctx, userID); err != nil {
		return err
	}
	err := s.verifySecondFactor(ctx, userID, totpCode, recoveryCode)
	if errors.Is(err, ErrInvalidCode) {
		s.stepUpFailed(ctx, userID, "wrong second factor")
	}
	return err
}

// VerifyStepUpPassword checks the password of a logged-in user under the
// persistent step-up lockout (see VerifyUserPassword).
func (s *Service) VerifyStepUpPassword(ctx context.Context, userID int64, password string) error {
	if err := s.StepUpCheck(ctx, userID); err != nil {
		return err
	}
	err := s.VerifyUserPassword(ctx, userID, password)
	if errors.Is(err, ErrInvalidCredentials) {
		s.stepUpFailed(ctx, userID, "wrong password")
	}
	return err
}
