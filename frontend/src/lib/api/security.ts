// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Account security of the web client (server mode): two-factor
// authentication (TOTP) with recovery codes, password change and the list of
// active sessions. Types only; the methods are part of the Api interface
// (api.ts) and implemented by HttpTransport (server routes below), MockTransport
// (in memory) and WailsTransport (not supported: the desktop shell has no
// server account).
//
// REST routes (internal/server/api_auth.go):
//   GET    /api/auth/me                    -> AuthMe  (user, session, mfaSetupRequired, recoveryCodesLeft)
//   GET    /api/auth/config                -> AuthConfigInfo (public; password policy)
//   POST   /api/auth/totp/setup            Reauth -> TotpEnrollment (secret + otpauth:// URL; replaces a pending secret)
//   POST   /api/auth/totp/confirm          {code} -> {recoveryCodes}
//   POST   /api/auth/totp/disable          Reauth (password if the account has one, plus a second factor)
//   POST   /api/auth/recovery-codes        Reauth -> {recoveryCodes} (409 totp_not_enabled without TOTP)
//   POST   /api/auth/password              PasswordChange (204; ends all other sessions)
//   GET    /api/auth/sessions              -> {sessions: AuthSession[]}
//   DELETE /api/auth/sessions/{id}         Reauth (not for the own session)
//   POST   /api/auth/sessions/revoke-others Reauth
// Server errors (ApiError.code): invalid_code (400), invalid_credentials (401),
// reauth_required (403), mfa_setup_required (403), weak_password (400),
// throttled (429, Retry-After), totp_not_enabled (409), exists (409), not_found (404).

/** The signed-in user as returned by /api/auth/me. */
export interface AuthUser {
  id: number;
  username: string;
  email: string;
  displayName: string;
  role: 'admin' | 'user' | string;
  /** False for accounts that only sign in through an identity provider (OIDC). */
  hasPassword: boolean;
  totpEnabled: boolean;
  oidc: boolean;
}

/** A server session (browser or device token) of the signed-in user. */
export interface AuthSession {
  id: number;
  kind: 'web' | 'device' | string;
  deviceName: string;
  ip: string;
  userAgent: string;
  mfa: boolean;
  loginMethod: 'password' | 'oidc' | string;
  createdAt: string;
  expiresAt: string;
  lastSeenAt: string;
  /** This browser's own session. */
  current?: boolean;
}

export interface AuthMe {
  user: AuthUser;
  session?: AuthSession;
  /** An administrator without TOTP while `auth.totp_required_for_admins` is on: privileged functions answer 403 mfa_setup_required. */
  mfaSetupRequired: boolean;
  recoveryCodesLeft: number;
}

/** Public server settings relevant to the account pages. */
export interface AuthConfigInfo {
  passwordMinLength: number;
  registration?: string;
  oidcEnabled?: boolean;
}

/** First step of enrolment: the new (not yet active) secret. */
export interface TotpEnrollment {
  /** Base32 secret for manual entry. */
  secret: string;
  /** otpauth://totp/... URL for the QR code. */
  otpauthUrl: string;
}

/** Re-authentication of a sensitive change: the password (accounts that have one) plus a TOTP or recovery code. */
export interface Reauth {
  password?: string;
  code?: string;
  recoveryCode?: string;
}

export interface PasswordChange {
  /** Required for accounts that have a local password. */
  currentPassword?: string;
  newPassword: string;
  /** Accounts without local password (first password): second factor instead. */
  totpCode?: string;
  recoveryCode?: string;
}
