// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Login throttling and the defence against distributed guessing (E-03, NA-06).
//
// Goals: a stranger must never be able to lock the owner out of their own
// account, and guessing spread over many source addresses must stay bounded.
// Persistent state lives in login_throttle (counters) and
// login_known_networks (hashed networks of past successful logins).
//
//  1. Per network (hard lock). Failures are counted per login name AND client
//     network (IPv4 address, IPv6 /64), key "u:<name>|<net>". After
//     throttleFreeFails failures that network is locked for 1 min, doubling up
//     to 1 h; the counter resets after throttleWindow without failures. The
//     lock only concerns the attacking network: the owner on another network
//     is never affected.
//
//  2. Per user (soft signal, never a lock). Failures of one login name from
//     ANY network are also counted, key "u:<name>", without ever setting
//     locked_until. Once throttlePressureFails (50) of them happened within
//     throttleWindow the account is "under pressure" until 15 minutes pass
//     without a further failure. Unknown login names are counted and treated
//     identically, so the signal reveals nothing about which names exist.
//     Under pressure, password logins are
//     a. accepted only from a KNOWN network of that user: one of the user's
//     successful logins within knownNetworkTTL (90 days; IPv4 /24, IPv6
//     /64, stored as HMAC(server key) only) or the network of one of the
//     user's live sessions. Every other attempt is refused with
//     ErrThrottled (Retry-After = end of the pressure window) WITHOUT the
//     password being verified, and counts as a failure (the attacker's
//     network gets locked and the signal stays up while the attack lasts).
//     The owner therefore keeps working from home/office/phone networks
//     during an attack; only logins from brand-new networks wait until the
//     attack stops (or an administrator unlocks the account).
//     b. serialised and slowed: one verification per account at a time, each
//     after PressureDelay (default 1 s), at most pressureQueueMax waiting;
//     further attempts are refused at once. A guesser sitting on a known
//     /24 (shared NAT) is thus limited to about one guess per second per
//     account, on top of the per-network locks.
//     A successful login clears only the network counter, not the user-wide
//     one: otherwise the owner's login would reset the attacker's budget.
//     Failed second factors are bounded separately (challenge attempts, A-08).
//
//  3. Step-up checks of a logged-in user have their own per-user counter, see
//     stepup.go (NA-05); it never interacts with the counters above.
//
// Disabled accounts, accounts without a local password and unknown names
// answer exactly like a wrong password (NA-04) and are charged the same way.
const (
	throttleWindow      = 15 * time.Minute
	throttleFreeFails   = 5
	maxLoginLen         = 320 // longest e-mail address (254) plus slack; usernames are <= 64
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

// clientNet groups an address the way the HTTP rate limiter does: IPv4 per
// address, IPv6 per /64.
func clientNet(ip string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "?"
	}
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

// loginKeys are the two throttle counters of one login attempt.
type loginKeys struct{ user, net, ip string }

func keysFor(login, ip string) loginKeys {
	u := throttleKey(login)
	return loginKeys{user: u, net: u + "|" + clientNet(ip), ip: ip}
}

// checkKeys fails while the attempting network is locked for this login name.
// The user-wide counter is a soft signal only (see the top of this file) and
// is deliberately not consulted here: a lock row left behind by earlier
// versions is ignored.
func (s *Service) checkKeys(ctx context.Context, k loginKeys) error {
	return s.checkThrottle(ctx, k.net)
}

func (s *Service) failKeys(ctx context.Context, k loginKeys) {
	s.recordFailure(ctx, k.net, networkPolicy)
	s.recordFailure(ctx, k.user, userPolicy)
	s.hitRate(ctx, ipFailBucket(k.ip), ipFailLimit)
}

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

// throttlePolicy parameterises recordFailure: the first free-1 failures are
// free, every further one doubles the lock (base ... max); the counter resets
// after a quiet period of window.
type throttlePolicy struct {
	free      int
	window    time.Duration
	baseLock  time.Duration
	maxLock   time.Duration
	countOnly bool // soft signal: count failures, never set locked_until
}

var (
	networkPolicy = throttlePolicy{free: throttleFreeFails, window: throttleWindow, baseLock: throttleBaseLock, maxLock: throttleMaxLock}
	userPolicy    = throttlePolicy{window: throttleWindow, countOnly: true}
)

func (s *Service) recordFailure(ctx context.Context, key string, p throttlePolicy) {
	throttleKeyMu.Lock()
	defer throttleKeyMu.Unlock()
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	// first_failure holds the time of the most recent failure: the counter
	// resets only after a quiet period, so persistent attacks keep escalating.
	var fails int
	var last int64
	err := s.db.QueryRowContext(ctx, `SELECT failures, first_failure FROM login_throttle WHERE key = ?`, key).Scan(&fails, &last)
	if err != nil || now.Sub(time.Unix(last, 0)) > p.window {
		fails = 0
	}
	fails++
	first := now.Unix()
	var until int64
	if !p.countOnly && fails >= p.free {
		lock := p.baseLock << min(fails-p.free, 10)
		until = now.Add(min(lock, p.maxLock)).Unix()
	}
	s.db.ExecContext(ctx, `INSERT INTO login_throttle(key, failures, first_failure, locked_until) VALUES (?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET failures = excluded.failures, first_failure = excluded.first_failure,
		locked_until = excluded.locked_until`, key, fails, first, until)
}

func (s *Service) clearThrottle(ctx context.Context, key string) {
	s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM login_throttle WHERE key = ?`, key)
}

// UnlockLogin clears the failed-login counters of a username (admin action).
func (s *Service) UnlockLogin(ctx context.Context, username string) {
	k := throttleKey(username)
	// key = k, or k + "|" + network: "}" sorts right after "|" (binary collation).
	s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM login_throttle WHERE key = ? OR (key >= ? AND key < ?)`, k, k+"|", k+"}")
	if u, err := s.GetUserByName(ctx, username); err == nil {
		s.ClearStepUp(ctx, u.ID)
	}
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
	if len(req.Login) > maxLoginLen {
		// No valid username or e-mail address is this long. Never persist it
		// (throttle keys, audit log): an unauthenticated client could
		// otherwise fill the database with 1 MiB login names.
		s.dummyVerify(req.Password)
		s.Audit(ctx, "login", truncate(req.Login, 100), false, "invalid login name")
		return nil, ErrInvalidCredentials
	}
	key := keysFor(req.Login, req.Client.IP)
	if err := s.checkKeys(ctx, key); err != nil {
		s.Audit(ctx, "login", req.Login, false, "throttled")
		return nil, err
	}
	if err := s.checkRate(ctx, ipFailBucket(req.Client.IP), ipFailLimit); err != nil {
		s.Audit(ctx, "login", req.Login, false, "throttled: too many failures from this address")
		return nil, err
	}
	u, err := s.GetUserByName(ctx, req.Login)
	if errors.Is(err, ErrNotFound) {
		// Same answer as for an existing user from an unknown network while
		// the name is under distributed attack: no enumeration (NA-06).
		if active, until := s.underPressure(ctx, key.user); active {
			s.failKeys(ctx, key)
			s.Audit(ctx, "login", req.Login, false, "refused: login name under distributed attack")
			return nil, &ThrottleError{Until: until}
		}
		s.dummyVerify(req.Password)
		s.failKeys(ctx, key)
		s.Audit(ctx, "login", req.Login, false, "unknown user")
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	// Logins by e-mail address share the counter of the username, otherwise
	// every alias would add another set of free attempts (A-07).
	if canon := keysFor(u.Username, req.Client.IP); canon != key {
		key = canon
		if err := s.checkKeys(ctx, key); err != nil {
			s.Audit(ctx, "login", u.Username, false, "throttled")
			return nil, err
		}
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: req.Client.IP})
	if active, until := s.underPressure(ctx, key.user); active {
		// Distributed guessing (NA-06): only known networks may try, one at a
		// time and slowly. The owner on a known network is never locked out.
		if !s.networkKnown(ctx, u.ID, req.Client.IP) {
			s.failKeys(ctx, key)
			s.Audit(ctx, "login", u.Username, false, "refused: account under distributed attack, unknown network")
			return nil, &ThrottleError{Until: until}
		}
		release, err := s.enterPressureGate(ctx, key.user)
		if err != nil {
			s.Audit(ctx, "login", u.Username, false, "refused: too many parallel attempts under attack")
			return nil, err
		}
		defer release()
	}
	if u.passwordHash == "" {
		s.dummyVerify(req.Password)
		s.failKeys(ctx, key)
		s.Audit(ctx, "login", u.Username, false, "no local password")
		return nil, ErrInvalidCredentials
	}
	ok, rehash, err := VerifyPassword(req.Password, u.passwordHash, s.PasswordParams)
	if err != nil || !ok {
		s.failKeys(ctx, key)
		s.Audit(ctx, "login", u.Username, false, "wrong password")
		return nil, ErrInvalidCredentials
	}
	if u.Disabled {
		// NA-04: a disabled account must not confirm a correct password. The
		// caller sees (and the throttle counts) exactly what a wrong password
		// looks like; only the audit log records the reason.
		s.failKeys(ctx, key)
		s.Audit(ctx, "login", u.Username, false, "disabled (correct password)")
		return nil, ErrInvalidCredentials
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
		if err := s.checkRate(ctx, totpBucket(u.ID), totpFailLimit); err != nil {
			s.Audit(ctx, "login", u.Username, false, "throttled: too many wrong second factors")
			return nil, err
		}
		if err := s.verifySecondFactor(ctx, u.ID, req.TOTPCode, req.RecoveryCode); err != nil {
			s.failKeys(ctx, key)
			s.hitRate(ctx, totpBucket(u.ID), totpFailLimit)
			s.Audit(ctx, "login", u.Username, false, "wrong second factor")
			return nil, err
		}
	}
	return s.finishLogin(ctx, u, req.Kind, MethodPassword, u.TOTPEnabled, req.Client)
}

func (s *Service) finishLogin(ctx context.Context, u *User, kind, method string, mfa bool, ci ClientInfo) (*LoginResult, error) {
	if kind == "" {
		kind = KindWeb
	}
	k := keysFor(u.Username, ci.IP)
	// Only the counter of this network: the user-wide soft signal must not be
	// resettable by the owner's login, it would reset an attacker's budget.
	s.clearThrottle(ctx, k.net)
	s.recordKnownNetwork(ctx, u.ID, ci.IP)
	tok, sess, err := s.createSession(ctx, u.ID, kind, method, mfa, ci)
	if err != nil {
		return nil, err
	}
	// Basis spec/BETRIEB.md: no sign-in without its audit entry.
	if err := s.Audit(ctx, "login", u.Username, true, fmt.Sprintf("kind=%s method=%s mfa=%v", kind, method, mfa)); err != nil {
		s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM sessions WHERE id = ?`, sess.ID)
		return nil, fmt.Errorf("auth: audit: %w", err)
	}
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
	// Take an attempt atomically before checking the code: concurrent
	// requests must not get more than mfaChallengeMaxTry guesses (A-08).
	err := s.db.QueryRowContext(ctx, `UPDATE mfa_challenges SET attempts = attempts + 1
		WHERE token_hash = ? AND attempts < ? AND expires_at > ? RETURNING user_id, attempts, expires_at`,
		h, mfaChallengeMaxTry, s.now().Unix()).Scan(&userID, &attempts, &exp)
	if errors.Is(err, sql.ErrNoRows) {
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
		// NA-04: the challenge proves a correct password; do not reveal that
		// the account was disabled in the meantime.
		s.Audit(ctx, "login", u.Username, false, "disabled (second factor step)")
		return nil, ErrInvalidCode
	}
	if err := s.checkRate(ctx, totpBucket(u.ID), totpFailLimit); err != nil {
		s.Audit(ctx, "login", u.Username, false, "throttled: too many wrong second factors")
		return nil, err
	}
	if err := s.verifySecondFactor(ctx, u.ID, totpCode, recoveryCode); err != nil {
		s.failKeys(ctx, keysFor(u.Username, ci.IP))
		s.hitRate(ctx, totpBucket(u.ID), totpFailLimit)
		s.Audit(ctx, "login", u.Username, false, "wrong second factor")
		return nil, err
	}
	s.db.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE token_hash = ?`, h)
	return s.finishLogin(ctx, u, kind, MethodPassword, true, ci)
}

// MFASetupRequired reports whether the session must enrol TOTP before using
// privileged functions (auth.totp_required_for_admins). Exempt are only the
// sessions that carry a second factor of their own: those created by the OIDC
// callback of an OIDC-linked account (the identity provider's) and passkey
// sessions (a passkey login always verified the user, PIN or biometric, on top
// of possessing the device). A password login of the very same account is not
// exempt (NA-02), whatever passkeys or OIDC link the account has. A nil
// session is never exempt. An OIDC login counts whether or not the identity
// provider reports MFA (amr/acr, which many do not fill in): an administrator
// without it is not locked out, see docs/security/BASIS-ABWEICHUNGEN.md.
func (s *Service) MFASetupRequired(u *User, sess *Session) bool {
	if !s.cfg.Auth.TOTPRequiredForAdmins || !u.IsAdmin() || u.TOTPEnabled {
		return false
	}
	if sess == nil {
		return true
	}
	return !(sess.LoginMethod == MethodPasskey || (u.OIDC && sess.LoginMethod == MethodOIDC))
}

// verifySecondFactor checks a TOTP code (with replay protection) or consumes
// a recovery code. It has no lockout of its own: Login and CompleteMFA charge
// the login counters, every other caller must use VerifyStepUpSecondFactor.
func (s *Service) verifySecondFactor(ctx context.Context, userID int64, totpCode, recoveryCode string) error {
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
	secret, err := s.open(enc, secretTOTP.context(userID), secretTOTP.legacy(userID))
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
	enc, err := s.seal(secret, secretTOTP.context(userID))
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
	secret, err := s.open(enc, secretTOTP.context(userID), secretTOTP.legacy(userID))
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
// Passkeys are not touched; the administrator's reset removes them as well
// (ResetSecondFactors).
func (s *Service) ResetTOTP(ctx context.Context, userID int64) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error { return s.resetTOTPTx(ctx, tx, userID) })
	s.auditUser(ctx, "totp.reset", userID, err, "")
	return err
}

func (s *Service) resetTOTPTx(ctx context.Context, tx *sql.Tx, userID int64) error {
	if err := execOne(ctx, tx, `UPDATE users SET totp_secret_enc = NULL, totp_enabled = 0, totp_last_step = 0,
		updated_at = ? WHERE id = ?`, s.now().Unix(), userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
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
	// Outside the transaction: creating the key on first use writes to the database.
	pepper, err := s.purposeKey(ctx, recoveryPepperName)
	if err != nil {
		return nil, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return err
		}
		for i := range codes {
			raw := randomString(recoveryCodeLetters, 10)
			codes[i] = raw[:5] + "-" + raw[5:]
			if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id, code_hash, scheme) VALUES (?,?,'hmac')`,
				userID, recoveryHMAC(pepper, raw)); err != nil {
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
