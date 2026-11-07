// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys (WebAuthn / FIDO2) are passwordless logins.
//
// Design (also see the config comment in internal/config/passkeys.go):
//
//   - Discoverable credentials ("resident keys") with user verification
//     REQUIRED, attestation "none". The login page needs no user name; the
//     authenticator offers the passkeys of this site and returns the user
//     handle, which identifies the account. A verified assertion proves
//     possession of the device AND a PIN/biometric, i.e. two factors, so the
//     resulting session (login_method 'passkey', mfa) satisfies
//     auth.totp_required_for_admins - a password session of the same account
//     does not (MFASetupRequired).
//   - The ceremonies' state (library SessionData with the challenge) lives in
//     passkey_challenges: 5 minutes, single use (taken with DELETE ... RETURNING
//     before anything is verified, so a failed attempt burns the challenge),
//     identified by 32 random bytes of which only the SHA-256 is stored. Expiry
//     is enforced here with the service clock, not by the library.
//   - Failures of the unauthenticated login count in login_throttle under the
//     key "p:<network>" with the same policy as the per-network password lock
//     (5 free failures, then 1 min doubling to 1 h). The key carries no user
//     name and the answer is always ErrInvalidCredentials, so the endpoint
//     reveals nothing about accounts: unknown credential, wrong signature, wrong
//     origin or RP ID, expired or replayed challenge, regressed signature
//     counter and disabled account are indistinguishable for the caller (the
//     audit log has the reason). Passkey logins are not subject to the
//     "account under distributed attack" gate of password logins (login.go):
//     there is nothing to guess, a signature cannot be forged.
//   - The signature counter is compared and swapped in one UPDATE
//     (... WHERE sign_count = <value read>), so concurrent assertions cannot both
//     advance it. A counter that does not increase (when either value is
//     non-zero; synced passkeys always report 0) is a possible cloned
//     authenticator: the login is refused and audited.
//   - Managing passkeys (add, delete) needs a step-up like the other sensitive
//     self-service changes, see VerifyPasskeyStepUp, and is refused for sessions
//     that still owe the mandatory TOTP enrolment: otherwise a stolen admin
//     password could add a passkey and walk around auth.totp_required_for_admins.
const (
	// MaxPasskeysPerUser bounds the credentials of one account.
	MaxPasskeysPerUser = 10

	passkeyChallengeTTL       = 5 * time.Minute
	maxPasskeyNameRunes       = 64
	maxPendingLoginChallenges = 20000 // unauthenticated begin requests; oldest are evicted beyond this
	passkeyHandleLen          = 32    // Basis spec/AUTH.md; credentials of older versions keep their 64-byte handle
	defaultPasskeyName        = "Passkey"
)

// Passkey errors. The HTTP layer maps them (internal/server/util.go).
var (
	ErrPasskeysDisabled = errors.New("auth: passkeys are not enabled")
	ErrPasskeyLimit     = errors.New("auth: passkey limit reached")
	ErrPasskeyChallenge = errors.New("auth: the passkey request expired or was already used")
	ErrPasskeyInvalid   = errors.New("auth: the passkey could not be verified")
	ErrMFASetupRequired = errors.New("auth: set up two-factor authentication first")
)

// Passkey is a registered credential as shown to its owner. The credential id
// and the public key are never part of the API.
type Passkey struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"createdAt"`
	LastUsedAt     time.Time `json:"lastUsedAt,omitzero"`
	Transports     []string  `json:"transports"`
	BackupEligible bool      `json:"backupEligible"` // synced passkey (can be backed up)
	BackupState    bool      `json:"backupState"`    // currently backed up
	// Usable is false for a passkey registered for another relying party ID
	// (public_url's host name changed): it can only be deleted.
	Usable bool `json:"usable"`
}

// PasskeysEnabled reports whether passkeys are available (config).
func (s *Service) PasskeysEnabled() bool { return s.cfg.PasskeysEnabled() }

func (s *Service) webAuthn() (*webauthn.WebAuthn, string, error) {
	if !s.cfg.PasskeysEnabled() {
		return nil, "", ErrPasskeysDisabled
	}
	rpID, origin, _ := s.cfg.PasskeyRP()
	s.waOnce.Do(func() {
		s.wa, s.waErr = webauthn.New(s.webAuthnConfig(rpID, origin, nil))
	})
	return s.wa, rpID, s.waErr
}

// webAuthnConfig is the relying party of all ceremonies; opaque lists further
// accepted non-web origins (passkey_device.go: Android apps).
func (s *Service) webAuthnConfig(rpID, origin string, opaque []string) *webauthn.Config {
	return &webauthn.Config{
		RPID:                  rpID,
		RPDisplayName:         s.cfg.PasskeyRPName(),
		RPOrigins:             []string{origin},
		RPOpaqueOrigins:       opaque,
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: protocol.ResidentKeyRequired(),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
		// Sent to the client as the ceremony timeout; the server-side limit is
		// passkeyChallengeTTL in the database.
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: passkeyChallengeTTL, TimeoutUVD: passkeyChallengeTTL},
			Registration: webauthn.TimeoutConfig{Timeout: passkeyChallengeTTL, TimeoutUVD: passkeyChallengeTTL},
		},
	}
}

// allowedPasskeyAlgorithms is Basis spec/PARAMETER.md: EdDSA, ES256, RS256.
// The library's default offers many more.
var allowedPasskeyAlgorithms = []protocol.CredentialParameter{
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgEdDSA},
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgRS256},
}

const minPasskeyRSABits = 2048

// checkPasskeyKey enforces the allowed algorithms on what the authenticator
// actually returned: the list above is only a request to the client.
func checkPasskeyKey(cose []byte) error {
	parsed, err := webauthncose.ParsePublicKey(cose)
	if err != nil {
		return errors.New("unreadable public key")
	}
	switch k := parsed.(type) {
	case webauthncose.OKPPublicKeyData:
		if webauthncose.COSEAlgorithmIdentifier(k.Algorithm) == webauthncose.AlgEdDSA {
			return nil
		}
	case webauthncose.EC2PublicKeyData:
		if webauthncose.COSEAlgorithmIdentifier(k.Algorithm) == webauthncose.AlgES256 {
			return nil
		}
	case webauthncose.RSAPublicKeyData:
		if webauthncose.COSEAlgorithmIdentifier(k.Algorithm) == webauthncose.AlgRS256 {
			if new(big.Int).SetBytes(k.Modulus).BitLen() < minPasskeyRSABits {
				return fmt.Errorf("RSA key shorter than %d bits", minPasskeyRSABits)
			}
			return nil
		}
	}
	return errors.New("algorithm not allowed")
}

// webauthnUser implements webauthn.User.
type webauthnUser struct {
	handle      []byte
	name        string
	displayName string
	creds       []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte                         { return u.handle }
func (u *webauthnUser) WebAuthnName() string                       { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// passkeyRow is a stored credential.
type passkeyRow struct {
	id, userID                           int64
	credentialID, publicKey, aaguid      []byte
	rpID, attType, attFormat, transports string
	signCount                            uint32
	be, bs, uv                           bool
	name                                 string
	createdAt                            time.Time
	lastUsedAt                           time.Time
}

const passkeyCols = `id, user_id, credential_id, public_key, rp_id, attestation_type, attestation_format, aaguid, sign_count,
	backup_eligible, backup_state, user_verified, transports, name, created_at, last_used_at`

func scanPasskey(sc interface{ Scan(...any) error }) (*passkeyRow, error) {
	var r passkeyRow
	var sc32 int64
	var created int64
	var last sql.NullInt64
	if err := sc.Scan(&r.id, &r.userID, &r.credentialID, &r.publicKey, &r.rpID, &r.attType, &r.attFormat, &r.aaguid, &sc32,
		&r.be, &r.bs, &r.uv, &r.transports, &r.name, &created, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.signCount = uint32(max(0, min(sc32, 1<<32-1)))
	r.createdAt = time.Unix(created, 0).UTC()
	r.lastUsedAt = nullTime(last)
	return &r, nil
}

func (r *passkeyRow) credential() webauthn.Credential {
	flags := protocol.FlagUserPresent
	if r.uv {
		flags |= protocol.FlagUserVerified
	}
	if r.be {
		flags |= protocol.FlagBackupEligible
	}
	if r.bs {
		flags |= protocol.FlagBackupState
	}
	c := webauthn.Credential{
		ID:                r.credentialID,
		PublicKey:         r.publicKey,
		AttestationType:   r.attType,
		AttestationFormat: r.attFormat,
		Flags:             webauthn.NewCredentialFlags(flags),
		Authenticator:     webauthn.Authenticator{AAGUID: r.aaguid, SignCount: r.signCount},
	}
	for _, t := range splitTransports(r.transports) {
		c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
	}
	return c
}

func (r *passkeyRow) view(currentRP string) Passkey {
	return Passkey{
		ID: r.id, Name: r.name, CreatedAt: r.createdAt, LastUsedAt: r.lastUsedAt, Transports: splitTransports(r.transports),
		BackupEligible: r.be, BackupState: r.bs, Usable: r.rpID == currentRP,
	}
}

func splitTransports(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

var knownTransports = map[string]bool{"usb": true, "nfc": true, "ble": true, "smart-card": true, "hybrid": true, "internal": true}

// storableTransports keeps the well-known transport hints (they only steer the
// browser's UI), at most once each.
func storableTransports(in []protocol.AuthenticatorTransport) string {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		if k := string(t); knownTransports[k] && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return strings.Join(out, ",")
}

// normalizePasskeyName trims and validates a passkey label (1-64 characters, no
// control characters); an empty name becomes "Passkey".
func normalizePasskeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultPasskeyName, nil
	}
	if utf8.RuneCountInString(name) > maxPasskeyNameRunes || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: the name must have at most %d characters and no control characters", ErrInvalidInput, maxPasskeyNameRunes)
	}
	return name, nil
}

// passkeyHandle returns the WebAuthn user handle of a user, creating it on
// first use: random bytes without any personal data.
func (s *Service) passkeyHandle(ctx context.Context, userID int64) ([]byte, error) {
	var h []byte
	err := s.db.QueryRowContext(ctx, `SELECT handle FROM passkey_users WHERE user_id = ?`, userID).Scan(&h)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	h = make([]byte, passkeyHandleLen)
	if _, err := rand.Read(h); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO passkey_users(user_id, handle) VALUES (?, ?)`, userID, h); err != nil {
		return nil, err
	}
	// A concurrent first registration may have won: read what is stored.
	if err := s.db.QueryRowContext(ctx, `SELECT handle FROM passkey_users WHERE user_id = ?`, userID).Scan(&h); err != nil {
		return nil, err
	}
	return h, nil
}

func (s *Service) userPasskeys(ctx context.Context, userID int64) ([]*passkeyRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+passkeyCols+` FROM passkeys WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*passkeyRow
	for rows.Next() {
		r, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListPasskeys returns the passkeys of a user, oldest first.
func (s *Service) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rpID, _, _ := s.cfg.PasskeyRP()
	rows, err := s.userPasskeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Passkey, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view(rpID))
	}
	return out, nil
}

// --- ceremony state ---------------------------------------------------------

// ceremonyState is what passkey_challenges.data holds: the library's
// SessionData and the purpose the ceremony was started for. The purpose binds a
// challenge to its endpoint beyond the kind column (whose CHECK constraint only
// knows 'register' and 'login'): "" is the web sign-in and registration,
// passkey_device.go adds the device login and the device approval.
type ceremonyState struct {
	webauthn.SessionData
	Purpose string `json:"tuxmailPurpose,omitempty"`
	// Bind ties the ceremony to one object (the device approval: the user code).
	Bind string `json:"tuxmailBind,omitempty"`
}

// putPasskeyChallenge stores the ceremony state and returns the opaque id the
// client must present to finish it.
func (s *Service) putPasskeyChallenge(ctx context.Context, kind, purpose string, userID int64, sd *webauthn.SessionData) (string, error) {
	return s.putBoundChallenge(ctx, kind, purpose, "", userID, sd)
}

func (s *Service) putBoundChallenge(ctx context.Context, kind, purpose, bind string, userID int64, sd *webauthn.SessionData) (string, error) {
	data, err := json.Marshal(ceremonyState{SessionData: *sd, Purpose: purpose, Bind: bind})
	if err != nil {
		return "", err
	}
	id := randomToken(32)
	var uid any
	if userID != 0 {
		uid = userID
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO passkey_challenges(id_hash, kind, user_id, data, expires_at) VALUES (?,?,?,?,?)`,
		hashToken(id), kind, uid, data, s.now().Add(passkeyChallengeTTL).Unix()); err != nil {
		return "", err
	}
	return id, nil
}

// takePasskeyChallenge consumes a ceremony (single use). It fails with
// ErrPasskeyChallenge for unknown, expired, used or foreign ids and for a
// ceremony started for another purpose (which is consumed all the same).
func (s *Service) takePasskeyChallenge(ctx context.Context, id, kind, purpose string, userID int64) (*webauthn.SessionData, error) {
	return s.takeBoundChallenge(ctx, id, kind, purpose, "", userID)
}

func (s *Service) takeBoundChallenge(ctx context.Context, id, kind, purpose, bind string, userID int64) (*webauthn.SessionData, error) {
	if id == "" || len(id) > 100 {
		return nil, ErrPasskeyChallenge
	}
	q := `DELETE FROM passkey_challenges WHERE id_hash = ? AND kind = ? AND expires_at > ? AND `
	args := []any{hashToken(id), kind, s.now().Unix()}
	if userID != 0 {
		q += `user_id = ?`
		args = append(args, userID)
	} else {
		q += `user_id IS NULL`
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, q+` RETURNING data`, args...).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPasskeyChallenge
		}
		return nil, err
	}
	var st ceremonyState
	if err := json.Unmarshal(data, &st); err != nil || st.Purpose != purpose || subtle.ConstantTimeCompare([]byte(st.Bind), []byte(bind)) != 1 {
		return nil, ErrPasskeyChallenge
	}
	return &st.SessionData, nil
}

// --- registration -----------------------------------------------------------

// VerifyPasskeyStepUp is the re-authentication before passkeys are added or
// removed. The session must not owe the mandatory TOTP enrolment
// (ErrMFASetupRequired). An account with a local password must present it (and
// the second factor if TOTP is on), under the persistent step-up lockout of
// stepup.go. An account without a password (OIDC, passkey-only) needs its
// second factor if it has one, otherwise the calling session itself must come
// from an identity-provider or passkey login at most FirstPasswordReauthWindow
// ago - a passkey is a login path that bypasses the identity provider, so a
// stolen long-lived session must not be enough to create one.
func (s *Service) VerifyPasskeyStepUp(ctx context.Context, userID, sessionID int64, password, totpCode, recoveryCode string) error {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	sess, err := s.getSession(ctx, `id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return err
	}
	if s.MFASetupRequired(u, sess) {
		s.Audit(ctx, "passkey.stepup", u.Username, false, "refused: two-factor enrolment pending")
		return ErrMFASetupRequired
	}
	return s.verifyStepUp(ctx, u, sess, password, totpCode, recoveryCode)
}

// VerifyStepUp is the re-authentication before a sensitive change (Basis
// spec/AUTH.md §3): the same proofs as VerifyPasskeyStepUp, and also for a session
// that owes the mandatory TOTP enrolment, which it is about to do.
func (s *Service) VerifyStepUp(ctx context.Context, userID, sessionID int64, password, totpCode, recoveryCode string) error {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	sess, err := s.getSession(ctx, `id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return err
	}
	return s.verifyStepUp(ctx, u, sess, password, totpCode, recoveryCode)
}

func (s *Service) verifyStepUp(ctx context.Context, u *User, sess *Session, password, totpCode, recoveryCode string) error {
	userID := u.ID
	if StepUpInputMissing(u, password, totpCode, recoveryCode) {
		// Nothing was entered: not a wrong answer, so it is not counted (and
		// does not extend a lock either).
		return fmt.Errorf("%w: enter your password", ErrReauthRequired)
	}
	if err := s.StepUpCheck(ctx, userID); err != nil {
		return err
	}
	if u.passwordHash != "" {
		if err := s.VerifyStepUpPassword(ctx, userID, password); err != nil {
			return err
		}
	}
	if u.TOTPEnabled {
		if totpCode == "" && recoveryCode == "" && u.passwordHash == "" {
			return fmt.Errorf("%w: enter your two-factor code", ErrReauthRequired)
		}
		if err := s.VerifyStepUpSecondFactor(ctx, userID, totpCode, recoveryCode); err != nil {
			return err
		}
	} else if u.passwordHash == "" {
		if (sess.LoginMethod != MethodOIDC && sess.LoginMethod != MethodPasskey) || s.now().Sub(sess.CreatedAt) > FirstPasswordReauthWindow {
			s.Audit(ctx, "passkey.stepup", u.Username, false, "no recent identity-provider or passkey login")
			return fmt.Errorf("%w: sign in again and retry within %d minutes", ErrReauthRequired, int(FirstPasswordReauthWindow/time.Minute))
		}
	}
	s.ClearStepUp(ctx, userID)
	return nil
}

// BeginPasskeyRegistration starts the registration of a new passkey for userID
// and returns the id of the ceremony and the options for
// navigator.credentials.create. The caller has verified the step-up. A new
// begin replaces a pending registration of the same user.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, userID int64) (string, *protocol.CredentialCreation, error) {
	wa, rpID, err := s.webAuthn()
	if err != nil {
		return "", nil, err
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	rows, err := s.userPasskeys(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	if len(rows) >= MaxPasskeysPerUser {
		return "", nil, fmt.Errorf("%w: at most %d passkeys per account", ErrPasskeyLimit, MaxPasskeysPerUser)
	}
	handle, err := s.passkeyHandle(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	var exclude []protocol.CredentialDescriptor
	for _, r := range rows {
		if r.rpID == rpID {
			c := r.credential()
			exclude = append(exclude, c.Descriptor())
		}
	}
	display := u.DisplayName
	if display == "" {
		display = u.Username
	}
	wu := &webauthnUser{handle: handle, name: u.Username, displayName: display}
	creation, sd, err := wa.BeginRegistration(wu, webauthn.WithExclusions(exclude), webauthn.WithCredentialParameters(allowedPasskeyAlgorithms))
	if err != nil {
		return "", nil, fmt.Errorf("auth: begin passkey registration: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM passkey_challenges WHERE kind = 'register' AND user_id = ?`, userID); err != nil {
		return "", nil, err
	}
	id, err := s.putPasskeyChallenge(ctx, "register", "", userID, sd)
	if err != nil {
		return "", nil, err
	}
	return id, creation, nil
}

// FinishPasskeyRegistration verifies the authenticator's answer (the JSON of
// the credential as produced by PublicKeyCredential.toJSON) and stores the new
// passkey under name.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, userID int64, challengeID, name string, credentialJSON []byte) (*Passkey, error) {
	wa, rpID, err := s.webAuthn()
	if err != nil {
		return nil, err
	}
	name, err = normalizePasskeyName(name)
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	fail := func(reason string, err error) (*Passkey, error) {
		s.Audit(ctx, "passkey.register", u.Username, false, reason)
		return nil, err
	}
	sd, err := s.takePasskeyChallenge(ctx, challengeID, "register", "", userID)
	if err != nil {
		return fail("unknown, expired or used ceremony", err)
	}
	handle, err := s.passkeyHandle(ctx, userID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credentialJSON)
	if err != nil {
		return fail("malformed response: "+protocolReason(err), ErrPasskeyInvalid)
	}
	wu := &webauthnUser{handle: handle, name: u.Username, displayName: u.DisplayName}
	cred, err := wa.CreateCredential(wu, *sd, parsed)
	if err != nil {
		return fail("verification failed: "+protocolReason(err), ErrPasskeyInvalid)
	}
	if !cred.Flags.UserVerified {
		return fail("user verification missing", ErrPasskeyInvalid)
	}
	if err := checkPasskeyKey(cred.PublicKey); err != nil {
		return fail("key refused: "+err.Error(), ErrPasskeyInvalid)
	}
	now := s.now()
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxPasskeysPerUser {
			return fmt.Errorf("%w: at most %d passkeys per account", ErrPasskeyLimit, MaxPasskeysPerUser)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO passkeys(user_id, credential_id, public_key, rp_id, attestation_type, attestation_format,
			aaguid, sign_count, backup_eligible, backup_state, user_verified, transports, name, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			userID, cred.ID, cred.PublicKey, rpID, cred.AttestationType, cred.AttestationFormat, cred.Authenticator.AAGUID,
			int64(cred.Authenticator.SignCount), cred.Flags.BackupEligible, cred.Flags.BackupState, cred.Flags.UserVerified,
			storableTransports(cred.Transport), name, now.Unix())
		if err != nil {
			if isUniqueErr(err) {
				return fmt.Errorf("%w: this passkey is already registered", ErrExists)
			}
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return fail("storing failed: "+err.Error(), err)
	}
	s.Audit(ctx, "passkey.register", u.Username, true, fmt.Sprintf("name=%q synced=%v", name, cred.Flags.BackupEligible))
	row, err := s.passkeyByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	v := row.view(rpID)
	return &v, nil
}

func (s *Service) passkeyByID(ctx context.Context, userID, id int64) (*passkeyRow, error) {
	return scanPasskey(s.db.QueryRowContext(ctx, `SELECT `+passkeyCols+` FROM passkeys WHERE id = ? AND user_id = ?`, id, userID))
}

// RenamePasskey changes the label of a passkey of userID.
func (s *Service) RenamePasskey(ctx context.Context, userID, id int64, name string) error {
	name, err := normalizePasskeyName(name)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.auditUser(ctx, "passkey.rename", userID, nil, fmt.Sprintf("name=%q", name))
	return nil
}

// DeletePasskey removes one passkey of userID (the caller verified the
// step-up). Other users' ids and unknown ids are ErrNotFound.
func (s *Service) DeletePasskey(ctx context.Context, userID, id int64) error {
	row, err := s.passkeyByID(ctx, userID, id)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.auditUser(ctx, "passkey.delete", userID, nil, fmt.Sprintf("name=%q", row.name))
	return nil
}

// ResetSecondFactors is the administrator's "reset 2FA" of a user: it removes
// the TOTP enrolment with its recovery codes AND all passkeys, atomically, and
// ends nothing else (sessions are managed separately).
func (s *Service) ResetSecondFactors(ctx context.Context, userID int64) error {
	var removed int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.resetTOTPTx(ctx, tx, userID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM passkeys WHERE user_id = ?`, userID)
		if err != nil {
			return err
		}
		removed, _ = res.RowsAffected()
		return nil
	})
	s.auditUser(ctx, "totp.reset", userID, err, fmt.Sprintf("passkeys_removed=%d", removed))
	return err
}

// --- login ------------------------------------------------------------------

func passkeyThrottleKey(ip string) string { return "p:" + clientNet(ip) }

// BeginPasskeyLogin starts a discoverable-credential login for an unknown
// user: it returns the id of the ceremony and the options for
// navigator.credentials.get. Wrongly behaving networks are locked like for
// failed password logins.
func (s *Service) BeginPasskeyLogin(ctx context.Context, ci ClientInfo) (string, *protocol.CredentialAssertion, error) {
	wa, _, err := s.webAuthn()
	if err != nil {
		return "", nil, err
	}
	return s.beginDiscoverableLogin(ctx, wa, "", ci)
}

func (s *Service) beginDiscoverableLogin(ctx context.Context, wa *webauthn.WebAuthn, purpose string, ci ClientInfo) (string, *protocol.CredentialAssertion, error) {
	if err := s.checkThrottle(ctx, passkeyThrottleKey(ci.IP)); err != nil {
		return "", nil, err
	}
	assertion, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, fmt.Errorf("auth: begin passkey login: %w", err)
	}
	// Unauthenticated callers create rows: bound the table by evicting the
	// oldest ceremonies (the HTTP layer rate-limits per network as well).
	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkey_challenges WHERE kind = 'login'`).Scan(&pending); err != nil {
		return "", nil, err
	}
	if pending >= maxPendingLoginChallenges {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM passkey_challenges WHERE id_hash IN
			(SELECT id_hash FROM passkey_challenges WHERE kind = 'login' ORDER BY expires_at LIMIT ?)`, maxPendingLoginChallenges/10); err != nil {
			return "", nil, err
		}
	}
	id, err := s.putPasskeyChallenge(ctx, "login", purpose, 0, sd)
	if err != nil {
		return "", nil, err
	}
	return id, assertion, nil
}

// protocolReason extracts a short, secret-free reason from a library error for
// the audit log.
func protocolReason(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		if pe.Details != "" {
			return pe.Type + ": " + pe.Details
		}
		return pe.Type
	}
	var ue *protocol.ErrorUnknownCredential
	if errors.As(err, &ue) {
		return "unknown credential"
	}
	return "invalid"
}

// FinishPasskeyLogin verifies the authenticator's assertion (the JSON of
// PublicKeyCredential.toJSON) and creates a session with login_method
// 'passkey'. Every failure is ErrInvalidCredentials (or *ThrottleError while the
// network is locked); the reason is only in the audit log.
func (s *Service) FinishPasskeyLogin(ctx context.Context, challengeID string, credentialJSON []byte, kind string, ci ClientInfo) (*LoginResult, error) {
	wa, rpID, err := s.webAuthn()
	if err != nil {
		return nil, err
	}
	u, err := s.finishDiscoverableLogin(ctx, wa, rpID, "", challengeID, credentialJSON, ci, nil)
	if err != nil {
		return nil, err
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: ci.IP})
	// A verified passkey is possession plus user verification: two factors.
	return s.finishLogin(ctx, u, kind, MethodPasskey, true, ci)
}

// finishDiscoverableLogin verifies a discoverable-credential assertion of the
// ceremony challengeID (started for purpose) and returns the account. It
// enforces everything but the session: network throttle, single use, origin
// and RP ID (by wa), user verification, the signature counter and disabled
// accounts. Every failure is ErrInvalidCredentials.
//
// allowOrigin, if set, narrows the origins wa accepts (the app sign-in takes
// app origins only, although the library needs the web origin in its list).
func (s *Service) finishDiscoverableLogin(ctx context.Context, wa *webauthn.WebAuthn, rpID, purpose, challengeID string, credentialJSON []byte, ci ClientInfo, allowOrigin func(string) bool) (*User, error) {
	ctx = WithActor(ctx, Actor{Name: "(passkey)", IP: ci.IP})
	key := passkeyThrottleKey(ci.IP)
	if err := s.checkThrottle(ctx, key); err != nil {
		s.Audit(ctx, "login", "(passkey)", false, "throttled")
		return nil, err
	}
	fail := func(target, reason string) (*User, error) {
		s.recordFailure(ctx, key, networkPolicy)
		s.Audit(ctx, "login", target, false, "passkey: "+reason)
		return nil, ErrInvalidCredentials
	}
	sd, err := s.takePasskeyChallenge(ctx, challengeID, "login", purpose, 0)
	if err != nil {
		if errors.Is(err, ErrPasskeyChallenge) {
			return fail("(passkey)", "unknown, expired or used ceremony")
		}
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credentialJSON)
	if err != nil {
		return fail("(passkey)", "malformed response: "+protocolReason(err))
	}
	if allowOrigin != nil && !allowOrigin(parsed.Response.CollectedClientData.Origin) {
		return fail("(passkey)", "origin not allowed here")
	}

	var row *passkeyRow
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		r, err := scanPasskey(s.db.QueryRowContext(ctx, `SELECT `+passkeyCols+` FROM passkeys WHERE credential_id = ?`, rawID))
		if err != nil {
			return nil, errors.New("unknown credential")
		}
		var want []byte
		if err := s.db.QueryRowContext(ctx, `SELECT handle FROM passkey_users WHERE user_id = ?`, r.userID).Scan(&want); err != nil {
			return nil, errors.New("no user handle")
		}
		if subtle.ConstantTimeCompare(userHandle, want) != 1 {
			return nil, errors.New("user handle mismatch")
		}
		if r.rpID != rpID {
			return nil, errors.New("credential registered for another relying party")
		}
		row = r
		return &webauthnUser{handle: want, creds: []webauthn.Credential{r.credential()}}, nil
	}
	_, cred, err := wa.ValidatePasskeyLogin(handler, *sd, parsed)
	if err != nil {
		target := "(passkey)"
		if row != nil {
			if u, e := s.GetUser(ctx, row.userID); e == nil {
				target = u.Username
			}
		}
		return fail(target, protocolReason(err))
	}
	u, err := s.GetUser(ctx, row.userID)
	if err != nil {
		return fail("(passkey)", "user vanished")
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: ci.IP})
	if err := s.acceptAssertion(ctx, u, row, cred); err != nil {
		return fail(u.Username, err.Error())
	}
	s.clearThrottle(ctx, key)
	return u, nil
}

// acceptAssertion is the end of every verified assertion: a counter that did not
// increase or a disabled account refuses it, otherwise the counter is stored
// with a compare-and-swap (a concurrent assertion with the same stored value
// loses). The error text is the audit reason.
func (s *Service) acceptAssertion(ctx context.Context, u *User, row *passkeyRow, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		return errors.New("signature counter did not increase (possible cloned authenticator)")
	}
	if u.Disabled {
		// NA-04: same answer as for any failure.
		return errors.New("disabled")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE passkeys SET sign_count = ?, backup_state = ?, last_used_at = ? WHERE id = ? AND sign_count = ?`,
		int64(cred.Authenticator.SignCount), cred.Flags.BackupState, s.now().Unix(), row.id, int64(row.signCount))
	if err != nil {
		return errors.New("storing the counter failed")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("signature counter changed concurrently")
	}
	return nil
}
