// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxPasswordRunes is the upper limit of a password (Basis spec/PARAMETER.md).
const maxPasswordRunes = 128

// User is a server account.
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Role        Role      `json:"role"`
	Disabled    bool      `json:"disabled"`
	HasPassword bool      `json:"hasPassword"`
	TOTPEnabled bool      `json:"totpEnabled"`
	Passkeys    int       `json:"passkeys"` // number of registered passkeys
	OIDC        bool      `json:"oidc"`
	CreatedAt   time.Time `json:"createdAt"`
	LastLoginAt time.Time `json:"lastLoginAt,omitzero"`

	passwordHash string
	totpLastStep int64
}

// IsAdmin reports whether the user has the admin role.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// NewUser holds the fields for CreateUser.
type NewUser struct {
	Username    string
	Email       string
	DisplayName string
	Role        Role
	Password    string // empty = no local password (OIDC-only or set later)

	oidcIssuer, oidcSubject string
	// emailUnverified marks an address chosen by the user (invite without
	// fixed address, unverified OIDC claim): it is not used to link OIDC
	// logins to this account.
	emailUnverified bool
}

// nextUserID allocates a user id that was never used before: SQLite would
// reuse the largest id after a delete, and a new user must never inherit
// the data directory or the cached App of a deleted user. Ids whose data
// directory still exists (deleted with keepData) are skipped as well.
func (s *Service) nextUserID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var hw, maxID int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM id_highwater WHERE name = 'users'), 0),
		COALESCE((SELECT MAX(id) FROM users), 0)`).Scan(&hw, &maxID); err != nil {
		return 0, err
	}
	id := max(hw, maxID) + 1
	for {
		if _, err := os.Lstat(s.UserDir(id)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		id++
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO id_highwater(name, value) VALUES ('users', ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value`, id)
	return id, err
}

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// ValidateUsername checks the allowed username syntax.
func ValidateUsername(name string) error {
	if !usernameRE.MatchString(name) {
		return fmt.Errorf("%w: username must be 1-64 characters of letters, digits, '.', '_', '-', '@'", ErrInvalidInput)
	}
	return nil
}

func normalizeEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", nil
	}
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email {
		return "", fmt.Errorf("%w: invalid email address %q", ErrInvalidInput, email)
	}
	return email, nil
}

// CheckPasswordPolicy validates a new password against the configured policy.
func (s *Service) CheckPasswordPolicy(password string) error {
	n := utf8.RuneCountInString(password)
	if n < s.cfg.Auth.PasswordMinLength {
		return fmt.Errorf("%w: at least %d characters required", ErrWeakPassword, s.cfg.Auth.PasswordMinLength)
	}
	if utf8.RuneCountInString(password) > maxPasswordRunes {
		return fmt.Errorf("%w: at most %d characters allowed", ErrWeakPassword, maxPasswordRunes)
	}
	if strings.TrimSpace(password) == "" || allSame(password) {
		return fmt.Errorf("%w: password is too simple", ErrWeakPassword)
	}
	if blocked, err := passwordBlocklisted(password); err != nil {
		return err
	} else if blocked {
		return fmt.Errorf("%w: this password is on a list of common passwords, choose another one", ErrWeakPassword)
	}
	return nil
}

func allSame(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return strings.Trim(s, string(r)) == ""
}

const userCols = `id, username, email, display_name, role, disabled, password_hash, totp_enabled, totp_last_step,
	oidc_subject IS NOT NULL, created_at, last_login_at, (SELECT COUNT(*) FROM passkeys WHERE passkeys.user_id = users.id)`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	var last sql.NullInt64
	if err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.Disabled, &u.passwordHash,
		&u.TOTPEnabled, &u.totpLastStep, &u.OIDC, &created, &last, &u.Passkeys); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.HasPassword = u.passwordHash != ""
	u.CreatedAt = time.Unix(created, 0).UTC()
	u.LastLoginAt = nullTime(last)
	return &u, nil
}

// CreateUser creates a user, its wrapped data key and its data directory.
func (s *Service) CreateUser(ctx context.Context, nu NewUser) (*User, error) {
	nu.Username = strings.TrimSpace(nu.Username)
	if err := ValidateUsername(nu.Username); err != nil {
		return nil, err
	}
	email, err := normalizeEmail(nu.Email)
	if err != nil {
		return nil, err
	}
	if nu.Role == "" {
		nu.Role = RoleUser
	}
	if !nu.Role.Valid() {
		return nil, fmt.Errorf("%w: unknown role %q", ErrInvalidInput, nu.Role)
	}
	var hash string
	if nu.Password != "" {
		if err := s.CheckPasswordPolicy(nu.Password); err != nil {
			return nil, err
		}
		if hash, err = HashPassword(nu.Password, s.PasswordParams); err != nil {
			return nil, err
		}
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	defer clear(dek)

	now := s.now().Unix()
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var iss, sub any
		if nu.oidcSubject != "" {
			iss, sub = nu.oidcIssuer, nu.oidcSubject
		}
		if id, err = s.nextUserID(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO users(id, username, email, display_name, role, password_hash,
			dek_wrapped, oidc_issuer, oidc_subject, created_at, updated_at, password_changed_at, email_verified)
			VALUES (?,?,?,?,?,?,X'',?,?,?,?,?,?)`,
			id, nu.Username, email, strings.TrimSpace(nu.DisplayName), nu.Role, hash, iss, sub, now, now, now, !nu.emailUnverified)
		if err != nil {
			if isUniqueErr(err) {
				return fmt.Errorf("%w: username or email already in use", ErrExists)
			}
			return err
		}
		// The wrapped key is bound to the user id, which is known only now.
		wrapped, err := s.seal(dek, secretDEK.context(id))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE users SET dek_wrapped = ? WHERE id = ?`, wrapped, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.UserDir(id), 0o700); err != nil {
		return nil, fmt.Errorf("auth: create user dir: %w", err)
	}
	s.Audit(ctx, "user.create", nu.Username, true, "role="+string(nu.Role))
	return s.GetUser(ctx, id)
}

// GetUser returns a user by id.
func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetUserByName returns a user by username or, if login contains '@' and no
// username matches, by email (both case-insensitive).
func (s *Service) GetUserByName(ctx context.Context, login string) (*User, error) {
	login = strings.TrimSpace(login)
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, login))
	if errors.Is(err, ErrNotFound) && strings.Contains(login, "@") {
		u, err = scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, login))
	}
	return u, err
}

// ListUsers returns all users ordered by username.
func (s *Service) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of users.
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// UserUpdate holds optional profile changes; nil fields are left unchanged.
type UserUpdate struct {
	Email       *string `json:"email"`
	DisplayName *string `json:"displayName"`
}

// UpdateUser changes profile fields.
func (s *Service) UpdateUser(ctx context.Context, id int64, up UserUpdate) (*User, error) {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	email, name := u.Email, u.DisplayName
	if up.Email != nil {
		if email, err = normalizeEmail(*up.Email); err != nil {
			return nil, err
		}
	}
	if up.DisplayName != nil {
		name = strings.TrimSpace(*up.DisplayName)
	}
	// Addresses set through UpdateUser come from an administrator or a
	// verified OIDC claim.
	keepFlag := up.Email == nil
	_, err = s.db.ExecContext(ctx, `UPDATE users SET email = ?, display_name = ?, updated_at = ?,
		email_verified = CASE WHEN ? THEN email_verified ELSE 1 END WHERE id = ?`,
		email, name, s.now().Unix(), keepFlag, id)
	if isUniqueErr(err) {
		return nil, fmt.Errorf("%w: email already in use", ErrExists)
	}
	if err != nil {
		return nil, err
	}
	s.Audit(ctx, "user.update", u.Username, true, "")
	return s.GetUser(ctx, id)
}

// MaxDisplayNameLength is the longest display name a user can give themselves, in characters.
const MaxDisplayNameLength = 100

// NormalizeDisplayName trims a self-chosen display name and rejects what must
// not end up in lists, mail templates or the audit view: an overlong name,
// control characters, line separators, invisible format characters (direction
// overrides, U+200B, U+00AD, U+2060, ...) and a name nobody can see or tell
// from an empty one. Only what a script or emoji needs stays allowed inside a
// name, without counting as visible: joiners (U+200C, U+200D) and variation
// selectors.
func NormalizeDisplayName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: display name is not valid text", ErrInvalidInput)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: display name must not be empty", ErrInvalidInput)
	}
	if utf8.RuneCountInString(name) > MaxDisplayNameLength {
		return "", fmt.Errorf("%w: display name must be at most %d characters", ErrInvalidInput, MaxDisplayNameLength)
	}
	visible := false
	for _, r := range name {
		if !allowedInDisplayName(r) {
			return "", fmt.Errorf("%w: display name contains characters that are not allowed", ErrInvalidInput)
		}
		// U+2800 is a symbol by category, but renders as a blank.
		visible = visible || (r != '\u2800' && unicode.In(r, unicode.L, unicode.N, unicode.P, unicode.S))
	}
	if !visible {
		return "", fmt.Errorf("%w: display name must contain a visible character", ErrInvalidInput)
	}
	return name, nil
}

func allowedInDisplayName(r rune) bool {
	if r == '\u200C' || r == '\u200D' {
		return true
	}
	// Cf covers the direction controls; the second table is the rest of
	// Default_Ignorable_Code_Point except variation selectors (Hangul fillers, U+034F).
	return !unicode.IsControl(r) && !unicode.In(r, unicode.Zl, unicode.Zp, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point)
}

// UpdateOwnProfile changes what a signed-in user may change about themselves:
// only the display name. Deliberately not UpdateUser: that marks an address as
// verified, which links accounts on OIDC login, so the e-mail address (also a
// login name) stays with administrators and the identity provider. Accounts
// linked to an identity provider keep its name, because every OIDC login
// overwrites display_name anyway (provisionOIDCUser).
func (s *Service) UpdateOwnProfile(ctx context.Context, id int64, displayName string) (*User, error) {
	name, err := NormalizeDisplayName(displayName)
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.OIDC {
		return nil, ErrManagedByIdP
	}
	if name == u.DisplayName {
		return u, nil // nothing changed: nothing to write or to audit
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`, name, s.now().Unix(), id); err != nil {
		return nil, err
	}
	// Names the field, not the value: the audit log records that, not what.
	s.Audit(ctx, "user.update", u.Username, true, "displayName")
	return s.GetUser(ctx, id)
}

// ensureNotLastAdmin fails if removing admin rights from user id would leave
// no enabled administrator.
func (s *Service) ensureNotLastAdmin(ctx context.Context, tx *sql.Tx, id int64) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0 AND id <> ?`, id).Scan(&n)
	if err != nil {
		return err
	}
	var isAdmin bool
	if err := tx.QueryRowContext(ctx, `SELECT role = 'admin' AND disabled = 0 FROM users WHERE id = ?`, id).Scan(&isAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if isAdmin && n == 0 {
		return ErrLastAdmin
	}
	return nil
}

// SetRole changes a user's role.
func (s *Service) SetRole(ctx context.Context, id int64, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidInput, role)
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if role != RoleAdmin {
			if err := s.ensureNotLastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		return execOne(ctx, tx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, s.now().Unix(), id)
	})
	s.auditUser(ctx, "user.set_role", id, err, "role="+string(role))
	return err
}

// SetDisabled disables or enables a user. Disabling revokes all sessions.
func (s *Service) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if disabled {
			if err := s.ensureNotLastAdmin(ctx, tx, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
				return err
			}
		}
		return execOne(ctx, tx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, disabled, s.now().Unix(), id)
	})
	action := "user.enable"
	if disabled {
		action = "user.disable"
	}
	s.auditUser(ctx, action, id, err, "")
	return err
}

// DeleteUser removes a user, all of its server-side records and, if
// removeData is set, its data directory data_dir/users/<id>.
func (s *Service) DeleteUser(ctx context.Context, id int64, removeData bool) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.ensureNotLastAdmin(ctx, tx, id); err != nil {
			return err
		}
		return execOne(ctx, tx, `DELETE FROM users WHERE id = ?`, id)
	})
	if err == nil {
		s.dropVault(id)
		if removeData {
			err = os.RemoveAll(s.UserDir(id))
		}
	}
	s.Audit(ctx, "user.delete", u.Username, err == nil, fmt.Sprintf("remove_data=%v", removeData))
	return err
}

// SetPassword sets a new password after checking the policy and revokes all
// sessions of the user except keepSessionID (0 = revoke all).
func (s *Service) SetPassword(ctx context.Context, id int64, password string, keepSessionID int64) error {
	if err := s.CheckPasswordPolicy(password); err != nil {
		return err
	}
	hash, err := HashPassword(password, s.PasswordParams)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := execOne(ctx, tx, `UPDATE users SET password_hash = ?, password_changed_at = ?, updated_at = ? WHERE id = ?`,
			hash, now, now, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, id, keepSessionID)
		return err
	})
	s.auditUser(ctx, "user.set_password", id, err, "")
	return err
}

// ResetPassword sets a random password and returns it.
func (s *Service) ResetPassword(ctx context.Context, id int64) (string, error) {
	n := max(s.cfg.Auth.PasswordMinLength, 20)
	pw := GeneratePassword(n)
	if err := s.SetPassword(ctx, id, pw, 0); err != nil {
		return "", err
	}
	return pw, nil
}

// FirstPasswordReauthWindow is how recent the OIDC login of the calling
// session must be to set the first local password without a second factor.
const FirstPasswordReauthWindow = 5 * time.Minute

// SetFirstPassword gives an account without a local password (OIDC-provisioned)
// one. This creates a login path that bypasses the identity provider (and its
// MFA, deprovisioning), so a stolen session must not be enough (NA-02): with
// TOTP enabled a valid second factor is required, otherwise the calling
// session itself must have been created by an OIDC login at most
// FirstPasswordReauthWindow ago. The change is always audited; other sessions
// are revoked like for any password change.
func (s *Service) SetFirstPassword(ctx context.Context, id int64, next string, sessionID int64, totpCode, recoveryCode string) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	deny := func(reason string, err error) error {
		s.Audit(ctx, "user.set_first_password", u.Username, false, reason)
		return err
	}
	if u.passwordHash != "" {
		return deny("a local password already exists", fmt.Errorf("%w: a local password is already set; use the password change", ErrInvalidInput))
	}
	method := ""
	if u.TOTPEnabled {
		if totpCode == "" && recoveryCode == "" {
			return deny("second factor missing", fmt.Errorf("%w: enter your two-factor code to set a password", ErrReauthRequired))
		}
		if err := s.VerifyStepUpSecondFactor(ctx, id, totpCode, recoveryCode); err != nil {
			return deny("second factor failed", err)
		}
		method = "totp"
	} else {
		sess, err := s.getSession(ctx, `id = ? AND user_id = ?`, sessionID, id)
		if err != nil || sess.LoginMethod != MethodOIDC || s.now().Sub(sess.CreatedAt) > FirstPasswordReauthWindow {
			return deny("no recent identity-provider login", fmt.Errorf("%w: sign in again with your identity provider and retry within %d minutes",
				ErrReauthRequired, int(FirstPasswordReauthWindow/time.Minute)))
		}
		method = "fresh_oidc_session"
	}
	if err := s.SetPassword(ctx, id, next, sessionID); err != nil {
		return deny("password rejected: "+err.Error(), err)
	}
	s.ClearStepUp(ctx, id)
	s.Audit(ctx, "user.set_first_password", u.Username, true, "reauth="+method)
	return nil
}

// ChangePassword verifies the current password and sets a new one; the
// calling session (keepSessionID) stays valid. Accounts without a local
// password must use SetFirstPassword.
func (s *Service) ChangePassword(ctx context.Context, id int64, current, next string, keepSessionID int64) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.passwordHash == "" {
		s.Audit(ctx, "user.change_password", u.Username, false, "no local password; re-authentication required")
		return fmt.Errorf("%w: set a first password with a second factor or a fresh identity-provider login", ErrReauthRequired)
	}
	{
		// Wrong current passwords count against the persistent step-up
		// lockout of the user (NA-05): a stolen session must not be able to
		// guess the password from many source addresses.
		if err := s.StepUpCheck(ctx, id); err != nil {
			return err
		}
		ok, _, err := VerifyPassword(current, u.passwordHash, s.PasswordParams)
		if err != nil || !ok {
			s.recordFailure(ctx, stepUpKey(id), stepUpPolicy)
			s.Audit(ctx, "user.change_password", u.Username, false, "wrong current password")
			return ErrInvalidCredentials
		}
	}
	if err := s.SetPassword(ctx, id, next, keepSessionID); err != nil {
		return err
	}
	s.ClearStepUp(ctx, id)
	return nil
}

func (s *Service) auditUser(ctx context.Context, action string, id int64, err error, details string) {
	target := fmt.Sprint(id)
	if u, e := s.GetUser(ctx, id); e == nil {
		target = u.Username
	}
	if err != nil {
		details = strings.TrimSpace(details + " error=" + err.Error())
	}
	s.Audit(ctx, action, target, err == nil, details)
}

func execOne(ctx context.Context, tx *sql.Tx, q string, args ...any) error {
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
