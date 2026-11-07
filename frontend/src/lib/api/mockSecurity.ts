// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Mock of the account security routes (security.ts) for the demo/development
// transport: one in-memory account whose TOTP, password and sessions can be
// changed. Behaves like the server: re-authentication needs the password (if
// the account has one) and a second factor, a wrong answer is
// invalid_credentials / invalid_code, five wrong answers lock the step-up for
// five minutes (429 throttled).
//
// Demo values: the password is "demo-password"; the authenticator code that
// is accepted (enrolment and re-authentication) is "123456"; the recovery
// codes are random.

import { ApiError } from './api';
import type { AuthConfigInfo, AuthMe, AuthProfileUpdate, AuthSession, AuthUser, PasswordChange, Reauth, TotpEnrollment } from './security';

export const MOCK_PASSWORD = 'demo-password';
export const MOCK_TOTP_CODE = '123456';
export const MOCK_MIN_PASSWORD = 12;
const LOCK_AFTER = 5;
const LOCK_SECONDS = 300;

export interface MockSecurityOptions {
  /** Administrator without TOTP (shows the forced set-up). */
  admin?: boolean;
  hasPassword?: boolean;
  totpEnabled?: boolean;
  now?: () => Date;
}

export class MockSecurity {
  private user: AuthMe['user'];
  private sessions: AuthSession[];
  private recovery: string[] = [];
  private pending = false;
  private fails = 0;
  private lockedUntil = 0;
  private readonly now: () => Date;
  password: string | null;
  /** Whether privileged functions of this admin demand TOTP first (auth.totp_required_for_admins). */
  readonly requireForAdmins: boolean;

  constructor(opts: MockSecurityOptions = {}) {
    this.now = opts.now ?? (() => new Date());
    const hasPassword = opts.hasPassword ?? true;
    this.password = hasPassword ? MOCK_PASSWORD : null;
    this.requireForAdmins = true;
    this.user = {
      id: 1,
      username: 'jonas',
      email: 'jonas.becker@example.org',
      displayName: 'Jonas Becker',
      role: opts.admin ? 'admin' : 'user',
      hasPassword,
      totpEnabled: !!opts.totpEnabled,
      oidc: !hasPassword,
    };
    if (this.user.totpEnabled) this.recovery = newCodes();
    const iso = (minAgo: number) => new Date(this.now().getTime() - minAgo * 60_000).toISOString();
    const later = (days: number) => new Date(this.now().getTime() + days * 86_400_000).toISOString();
    this.sessions = [
      { id: 1, kind: 'web', deviceName: '', ip: '192.0.2.10', userAgent: 'Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0', mfa: false, loginMethod: 'password', createdAt: iso(90), expiresAt: later(7), lastSeenAt: iso(0) },
      { id: 2, kind: 'web', deviceName: '', ip: '198.51.100.7', userAgent: 'Mozilla/5.0 (Macintosh) Safari/605.1.15', mfa: false, loginMethod: 'password', createdAt: iso(60 * 30), expiresAt: later(5), lastSeenAt: iso(60 * 5) },
      { id: 3, kind: 'device', deviceName: 'Pixel 9', ip: '203.0.113.21', userAgent: 'TuxMail-Android/1.0', mfa: false, loginMethod: 'password', createdAt: iso(60 * 24 * 12), expiresAt: later(40), lastSeenAt: iso(45) },
    ];
  }

  /** Same rules as the server: an identity provider keeps its name, a name is 1-100 characters. */
  updateProfile(update: AuthProfileUpdate): AuthUser {
    if (this.user.oidc) throw new ApiError('this value is managed by the identity provider', 409, 'managed_by_idp');
    const name = update.displayName.trim();
    if (!name || [...name].length > 100) throw new ApiError('invalid display name', 400, 'invalid_input');
    this.user = { ...this.user, displayName: name };
    return { ...this.user };
  }

  me(): AuthMe {
    const session = this.sessions.find((s) => s.id === 1);
    return {
      user: { ...this.user },
      session: session && { ...session, current: true },
      mfaSetupRequired: this.user.role === 'admin' && this.requireForAdmins && !this.user.totpEnabled,
      recoveryCodesLeft: this.user.totpEnabled ? this.recovery.length : 0,
    };
  }

  config(): AuthConfigInfo {
    return { passwordMinLength: MOCK_MIN_PASSWORD, registration: 'invite', oidcEnabled: false };
  }

  setup(): TotpEnrollment {
    if (this.user.totpEnabled) throw new ApiError('two-factor authentication is already active', 409, 'exists');
    this.pending = true;
    const secret = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP';
    return { secret, otpauthUrl: `otpauth://totp/TuxMail:${encodeURIComponent(this.user.email)}?secret=${secret}&issuer=TuxMail&algorithm=SHA1&digits=6&period=30` };
  }

  confirm(code: string): string[] {
    if (this.user.totpEnabled || !this.pending || code.replace(/\s/g, '') !== MOCK_TOTP_CODE) throw new ApiError('invalid or expired code', 400, 'invalid_code');
    this.user.totpEnabled = true;
    this.pending = false;
    this.recovery = newCodes();
    return [...this.recovery];
  }

  disable(r: Reauth): void {
    this.reauth(r);
    this.user.totpEnabled = false;
    this.recovery = [];
  }

  regenerate(r: Reauth): string[] {
    if (!this.user.totpEnabled) throw new ApiError('two-factor authentication is not enabled', 409, 'totp_not_enabled');
    this.reauth(r);
    this.recovery = newCodes();
    return [...this.recovery];
  }

  changePassword(input: PasswordChange): void {
    if (this.user.hasPassword) {
      this.checkLock();
      if (input.currentPassword !== this.password) this.fail('wrong password', 'invalid_credentials', 401);
    } else {
      if (!this.user.totpEnabled) throw new ApiError('sign in again with your identity provider and retry', 403, 'reauth_required');
      this.checkSecondFactor(input.totpCode ?? '', input.recoveryCode ?? '');
    }
    if ([...input.newPassword].length < MOCK_MIN_PASSWORD || new Set(input.newPassword).size < 2) throw new ApiError('password does not meet the policy', 400, 'weak_password');
    this.password = input.newPassword;
    this.user.hasPassword = true;
    this.fails = 0;
    this.sessions = this.sessions.filter((s) => s.id === 1);
  }

  list(): AuthSession[] {
    return this.sessions.map((s) => ({ ...s, current: s.id === 1 }));
  }

  revoke(id: number): void {
    const i = this.sessions.findIndex((s) => s.id === id);
    if (i < 0) throw new ApiError('not found', 404, 'not_found');
    this.sessions.splice(i, 1);
  }

  revokeOthers(): void {
    this.sessions = this.sessions.filter((s) => s.id === 1);
  }

  private reauth(r: Reauth): void {
    this.checkLock();
    if (this.user.hasPassword && r.password !== this.password) this.fail('wrong password', 'invalid_credentials', 401);
    if (this.user.totpEnabled) this.checkSecondFactor(r.code ?? '', r.recoveryCode ?? '');
    this.fails = 0;
  }

  private checkSecondFactor(code: string, recoveryCode: string): void {
    this.checkLock();
    if (recoveryCode) {
      const norm = recoveryCode.toLowerCase().replace(/[-\s]/g, '');
      const hit = this.recovery.findIndex((c) => c.replace('-', '') === norm);
      if (hit < 0) this.fail('invalid or expired code', 'invalid_code', 400);
      this.recovery.splice(hit, 1);
      return;
    }
    if (code.replace(/\s/g, '') !== MOCK_TOTP_CODE) this.fail('invalid or expired code', 'invalid_code', 400);
  }

  private checkLock(): void {
    const left = Math.ceil((this.lockedUntil - this.now().getTime()) / 1000);
    if (left > 0) throw new ApiError('too many failed attempts', 429, 'throttled', left);
  }

  private fail(message: string, code: string, status: number): never {
    this.fails++;
    if (this.fails > LOCK_AFTER) {
      this.lockedUntil = this.now().getTime() + LOCK_SECONDS * 1000;
      throw new ApiError('too many failed attempts', 429, 'throttled', LOCK_SECONDS);
    }
    throw new ApiError(message, status, code);
  }
}

function newCodes(): string[] {
  const letters = 'abcdefghjkmnpqrstuvwxyz23456789';
  const bytes = new Uint8Array(100);
  (globalThis.crypto ?? { getRandomValues: (a: Uint8Array) => a.map(() => Math.floor(Math.random() * 256)) }).getRandomValues(bytes);
  const out: string[] = [];
  for (let i = 0; i < 10; i++) {
    const raw = Array.from(bytes.slice(i * 10, i * 10 + 10), (b) => letters[b % letters.length]).join('');
    out.push(raw.slice(0, 5) + '-' + raw.slice(5));
  }
  return out;
}
