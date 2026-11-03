// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Login throttling policy (per username, persistent in login_throttle).
const (
	throttleWindow      = 15 * time.Minute
	throttleFreeFails   = 5
	throttleBaseLock    = time.Minute
	throttleMaxLock     = time.Hour
	mfaChallengeTTL     = 5 * time.Minute
	mfaChallengeMaxTry  = 5
	recoveryCodeCount   = 10
	totpSkewSteps       = 1
	totpIssuer          = "TuxMail"
	recoveryCodeLetters = "abcdefghjkmnpqrstuvwxyz23456789"
)

// ThrottleError is returned (wrapping ErrThrottled) while a login is locked.
type ThrottleError struct{ Until time.Time }

func (e *ThrottleError) Error() string { return ErrThrottled.Error() }
func (e *ThrottleError) Unwrap() error { return ErrThrottled }

// LoginRequest is a password login attempt.
type LoginRequest struct {
	Login        string // username or email
	Password     string
	TOTPCode     string // optional: second factor in the same request
	RecoveryCode string // optional alternative to TOTPCode
	Kind         string // KindWeb (default) or KindDevice
	Client       ClientInfo
}

// LoginResult is the outcome of a successful first or second step.
type LoginResult struct {
	// MFARequired is set when the password was correct but a second factor
	// is needed; Challenge must then be passed to CompleteMFA.
	MFARequired bool
	Challenge   string

	Token   string
	Session *Session
	User    *User
}

var throttleKeyMu sync.Mutex

func throttleKey(login string) string { return "u:" + strings.ToLower(strings.TrimSpace(login)) }

func (s *Service) checkThrottle(ctx context.Context, key string) error {
	var until int64
	err := s.db.QueryRowContext(ctx, `SELECT locked_until FROM login_throttle WHERE key = ?`, key).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if t := time.Unix(until, 0); s.now().Before(t) {
		return &ThrottleError{Until: t.UTC()}
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, key string) {
	throttleKeyMu.Lock()
	defer throttleKeyMu.Unlock()
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	// first_failure holds the time of the most recent failure: the counter
	// resets only after a quiet period, so persistent attacks keep escalating.
	var fails int
	var last int64
	err := s.db.QueryRowContext(ctx, `SELECT failures, first_failure FROM login_throttle WHERE key = ?`, key).Scan(&fails, &last)
	if err != nil || now.Sub(time.Unix(last, 0)) > throttleWindow {
		fails = 0
	}
	fails++
	first := now.Unix()
	var until int64
	if fails >= throttleFreeFails {
		lock := throttleBaseLock << min(fails-throttleFreeFails, 10)
		until = now.Add(min(lock, throttleMaxLock)).Unix()
	}
	s.db.ExecContext(ctx, `INSERT INTO login_throttle(key, failures, first_failure, locked_until) VALUES (?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET failures = excluded.failures, first_failure = excluded.first_failure,
		locked_until = excluded.locked_until`, key, fails, first, until)
}

func (s *Service) clearThrottle(ctx context.Context, key string) {
	s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM login_throttle WHERE key = ?`, key)
}

// UnlockLogin clears the failed-login counter of a username (admin action).
func (s *Service) UnlockLogin(ctx context.Context, username string) {
	s.clearThrottle(ctx, throttleKey(username))
}

var (
	dummyOnce sync.Once
	dummyPHC  string
)

func (s *Service) dummyVerify(password string) {
	dummyOnce.Do(func() { dummyPHC, _ = HashPassword("tuxmail-dummy-password", s.PasswordParams) })
	VerifyPassword(password, dummyPHC, s.PasswordParams)
}

// Login checks username and password (and optionally the second factor).
func (s *Service) Login(ctx context.Context, req LoginRequest) (*LoginResult, error) {
	ctx = WithActor(ctx, Actor{Name: truncate(req.Login, 100), IP: req.Client.IP})
	key := throttleKey(req.Login)
	if err := s.checkThrottle(ctx, key); err != nil {
		s.Audit(ctx, "login", req.Login, false, "throttled")
		return nil, err
	}
	u, err := s.GetUserByName(ctx, req.Login)
	if errors.Is(err, ErrNotFound) {
		s.dummyVerify(req.Password)
		s.recordFailure(ctx, key)
		s.Audit(ctx, "login", req.Login, false, "unknown user")
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: req.Client.IP})
	if u.passwordHash == "" {
		s.dummyVerify(req.Password)
		s.recordFailure(ctx, key)
		s.Audit(ctx, "login", u.Username, false, "no local password")
		return nil, ErrInvalidCredentials
	}
	ok, rehash, err := VerifyPassword(req.Password, u.passwordHash, s.PasswordParams)
	if err != nil || !ok {
		s.recordFailure(ctx, key)
		s.Audit(ctx, "login", u.Username, false, "wrong password")
		return nil, ErrInvalidCredentials
	}
	if u.Disabled {
		s.Audit(ctx, "login", u.Username, false, "disabled")
		return nil, ErrUserDisabled
	}
	if rehash {
		if h, err := HashPassword(req.Password, s.PasswordParams); err == nil {
			s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, h, u.ID)
		}
	}
	if u.TOTPEnabled {
		if req.TOTPCode == "" && req.RecoveryCode == "" {
			ch := randomToken(32)
			_, err := s.db.ExecContext(ctx, `INSERT INTO mfa_challenges(token_hash, user_id, expires_at) VALUES (?,?,?)`,
				hashToken(ch), u.ID, s.now().Add(mfaChallengeTTL).Unix())
			if err != nil {
				return nil, err
			}
			return &LoginResult{MFARequired: true, Challenge: ch, User: u}, nil
		}
		if err := s.VerifySecondFactor(ctx, u.ID, req.TOTPCode, req.RecoveryCode); err != nil {
			s.recordFailure(ctx, key)
			s.Audit(ctx, "login", u.Username, false, "wrong second factor")
			return nil, err
		}
	}
	return s.finishLogin(ctx, u, req.Kind, u.TOTPEnabled, req.Client)
}

func (s *Service) finishLogin(ctx context.Context, u *User, kind string, mfa bool, ci ClientInfo) (*LoginResult, error) {
	if kind == "" {
		kind = KindWeb
	}
	s.clearThrottle(ctx, throttleKey(u.Username))
	tok, sess, err := s.createSession(ctx, u.ID, kind, mfa, ci)
	if err != nil {
		return nil, err
	}
	s.Audit(ctx, "login", u.Username, true, fmt.Sprintf("kind=%s mfa=%v", kind, mfa))
	u, err = s.GetUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: tok, Session: sess, User: u}, nil
}

// CompleteMFA finishes a login started with Login that returned MFARequired.
func (s *Service) CompleteMFA(ctx context.Context, challenge, totpCode, recoveryCode, kind string, ci ClientInfo) (*LoginResult, error) {
	h := hashToken(challenge)
	var userID int64
	var attempts int
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, attempts, expires_at FROM mfa_challenges WHERE token_hash = ?`, h).
		Scan(&userID, &attempts, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (s.now().Unix() >= exp || attempts >= mfaChallengeMaxTry)) {
		return nil, ErrInvalidCode
	}
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: ci.IP})
	if u.Disabled {
		return nil, ErrUserDisabled
	}
	if err := s.VerifySecondFactor(ctx, u.ID, totpCode, recoveryCode); err != nil {
		s.db.ExecContext(ctx, `UPDATE mfa_challenges SET attempts = attempts + 1 WHERE token_hash = ?`, h)
		s.recordFailure(ctx, throttleKey(u.Username))
		s.Audit(ctx, "login", u.Username, false, "wrong second factor")
		return nil, err
	}
	s.db.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE token_hash = ?`, h)
	return s.finishLogin(ctx, u, kind, true, ci)
}

// MFASetupRequired reports whether the user must enrol TOTP before using
// privileged functions (auth.totp_required_for_admins).
func (s *Service) MFASetupRequired(u *User) bool {
	return s.cfg.Auth.TOTPRequiredForAdmins && u.IsAdmin() && !u.TOTPEnabled && !u.OIDC
}

// VerifySecondFactor checks a TOTP code (with replay protection) or consumes
// a recovery code.
func (s *Service) VerifySecondFactor(ctx context.Context, userID int64, totpCode, recoveryCode string) error {
	if recoveryCode != "" {
		return s.useRecoveryCode(ctx, userID, recoveryCode)
	}
	var enc []byte
	var enabled bool
	var last int64
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret_enc, totp_enabled, totp_last_step FROM users WHERE id = ?`, userID).
		Scan(&enc, &enabled, &last)
	if err != nil {
		return err
	}
	if !enabled || enc == nil {
		return ErrInvalidCode
	}
	secret, err := s.open(enc, fmt.Sprintf("totp:%d", userID))
	if err != nil {
		return err
	}
	step, ok := TOTP{Secret: secret}.Verify(totpCode, s.now(), totpSkewSteps, last)
	if !ok {
		return ErrInvalidCode
	}
	// Conditional update: a concurrent request with the same code loses.
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvalidCode
	}
	return nil
}

// TOTPEnrollment is returned when a user starts TOTP enrolment.
type TOTPEnrollment struct {
	Secret string `json:"secret"` // base32
	URL    string `json:"otpauthUrl"`
}

// BeginTOTP generates and stores a new (not yet active) TOTP secret.
// Calling it again replaces a pending secret. It fails if TOTP is active.
func (s *Service) BeginTOTP(ctx context.Context, userID int64) (*TOTPEnrollment, error) {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.TOTPEnabled {
		return nil, fmt.Errorf("%w: two-factor authentication is already active", ErrExists)
	}
	secret := NewTOTPSecret()
	enc, err := s.seal(secret, fmt.Sprintf("totp:%d", userID))
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret_enc = ?, totp_last_step = 0 WHERE id = ?`, enc, userID); err != nil {
		return nil, err
	}
	account := u.Username
	if u.Email != "" {
		account = u.Email
	}
	return &TOTPEnrollment{Secret: EncodeTOTPSecret(secret), URL: TOTPURL(totpIssuer, account, secret)}, nil
}

// ConfirmTOTP activates the pending secret if code is valid and returns
// fresh recovery codes.
func (s *Service) ConfirmTOTP(ctx context.Context, userID int64, code string) ([]string, error) {
	var enc []byte
	var enabled bool
	if err := s.db.QueryRowContext(ctx, `SELECT totp_secret_enc, totp_enabled FROM users WHERE id = ?`, userID).Scan(&enc, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if enabled || enc == nil {
		return nil, ErrInvalidCode
	}
	secret, err := s.open(enc, fmt.Sprintf("totp:%d", userID))
	if err != nil {
		return nil, err
	}
	step, ok := TOTP{Secret: secret}.Verify(code, s.now(), totpSkewSteps, 0)
	if !ok {
		return nil, ErrInvalidCode
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET totp_enabled = 1, totp_last_step = ?, updated_at = ? WHERE id = ?`,
		step, s.now().Unix(), userID); err != nil {
		return nil, err
	}
	s.auditUser(ctx, "totp.enable", userID, nil, "")
	return s.RegenerateRecoveryCodes(ctx, userID)
}

// ResetTOTP disables TOTP and deletes recovery codes (user or admin action).
func (s *Service) ResetTOTP(ctx context.Context, userID int64) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := execOne(ctx, tx, `UPDATE users SET totp_secret_enc = NULL, totp_enabled = 0, totp_last_step = 0,
			updated_at = ? WHERE id = ?`, s.now().Unix(), userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
		return err
	})
	s.auditUser(ctx, "totp.reset", userID, err, "")
	return err
}

func normalizeRecoveryCode(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	return strings.NewReplacer("-", "", " ", "").Replace(c)
}

// RegenerateRecoveryCodes replaces all recovery codes and returns the new ones
// (shown once; only hashes are stored).
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return err
		}
		for i := range codes {
			raw := randomString(recoveryCodeLetters, 10)
			codes[i] = raw[:5] + "-" + raw[5:]
			if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id, code_hash) VALUES (?,?)`,
				userID, hashToken(raw)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.auditUser(ctx, "recovery_codes.generate", userID, nil, "")
	return codes, nil
}

// RecoveryCodesLeft returns the number of unused recovery codes.
func (s *Service) RecoveryCodesLeft(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

func (s *Service) useRecoveryCode(ctx context.Context, userID int64, code string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		s.now().Unix(), userID, hashToken(normalizeRecoveryCode(code)))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvalidCode
	}
	s.auditUser(ctx, "recovery_code.use", userID, nil, "")
	return nil
}

// VerifyUserPassword checks the password of a logged-in user, e.g. before
// sensitive changes. Users without a local password (OIDC) always fail.
func (s *Service) VerifyUserPassword(ctx context.Context, userID int64, password string) error {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if u.passwordHash == "" {
		return ErrInvalidCredentials
	}
	ok, _, err := VerifyPassword(password, u.passwordHash, s.PasswordParams)
	if err != nil || !ok {
		return ErrInvalidCredentials
	}
	return nil
}
