// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Pure helpers of the account security pages (two-factor authentication,
// password, sessions) and of the forced two-factor set-up: mapping of server
// errors to localized messages, building the re-authentication payload,
// validation of the password change, recovery code export and the readable
// description of a session. No framework imports so everything is unit-testable.

import type { MessageKey } from './i18n/index.svelte';
import type { Params } from './i18n/core';
import type { AuthSession, PasswordChange, Reauth } from './api/security';

export type Translate = (key: MessageKey, params?: Params) => string;

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

interface ErrorLike {
  code?: unknown;
  status?: unknown;
  message?: unknown;
  retryAfter?: unknown;
}

/** Minutes to wait after a 429 (Retry-After), at least 1; undefined when the server did not say. */
export function retryMinutes(err: unknown): number | undefined {
  const s = (err as ErrorLike | null)?.retryAfter;
  return typeof s === 'number' && s > 0 ? Math.max(1, Math.ceil(s / 60)) : undefined;
}

/** End of a server lock (ms since epoch) after a 429 with Retry-After; null for any other error. */
export function lockUntilOf(err: unknown, now = Date.now()): number | null {
  const e = (err ?? {}) as ErrorLike;
  if (e.code !== 'throttled' && e.status !== 429) return null;
  const s = e.retryAfter;
  return now + (typeof s === 'number' && s > 0 ? s : 60) * 1000;
}

/**
 * Localized text for an error of the account security routes. Known server
 * codes map to our own wording (the server texts are English); anything else
 * falls back to the server message.
 */
export function securityErrorMessage(err: unknown, tr: Translate): string {
  const e = (err ?? {}) as ErrorLike;
  const code = typeof e.code === 'string' ? e.code : '';
  const status = typeof e.status === 'number' ? e.status : 0;
  if (code === 'throttled' || status === 429) {
    const min = retryMinutes(err);
    return min ? tr('security.errors.throttledMinutes', { minutes: min }) : tr('security.errors.throttled');
  }
  switch (code) {
    case 'invalid_code':
      return tr('security.errors.invalidCode');
    case 'invalid_credentials':
      return tr('security.errors.invalidCredentials');
    case 'reauth_required':
      return tr('security.errors.reauthRequired');
    case 'mfa_setup_required':
      return tr('security.errors.mfaSetupRequired');
    case 'weak_password':
      return tr('security.errors.weakPassword');
    case 'totp_not_enabled':
      return tr('security.errors.notEnabled');
    case 'exists':
      return tr('security.errors.alreadyEnabled');
    case 'not_found':
      return tr('security.errors.notFound');
    case 'not_supported':
      return tr('security.errors.notSupported');
    case 'network':
      return tr('security.errors.network');
    case 'unauthenticated':
      return tr('security.errors.signedOut');
  }
  return typeof e.message === 'string' && e.message ? e.message : tr('security.errors.generic');
}

// ---------------------------------------------------------------------------
// Second factor input
// ---------------------------------------------------------------------------

/** Authenticator codes are 6 digits (spaces allowed while typing); everything else counts as a recovery code. */
export function classifySecondFactor(input: string): { code?: string; recoveryCode?: string } {
  const v = input.trim();
  if (!v) return {};
  const compact = v.replace(/[\s-]/g, '');
  if (/^\d{6}$/.test(compact)) return { code: compact };
  return { recoveryCode: v };
}

/** Is the input complete enough to send (6 digits or a recovery code of 10 characters)? */
export function secondFactorComplete(input: string): boolean {
  const c = classifySecondFactor(input);
  if (c.code) return true;
  return !!c.recoveryCode && c.recoveryCode.replace(/[\s-]/g, '').length >= 10;
}

/** Only digits, at most 6 (the enrolment field); spaces from copy and paste are removed. */
export function sanitizeTotpCode(input: string): string {
  return input.replace(/\D/g, '').slice(0, 6);
}

/**
 * The re-authentication payload of totp/disable and recovery-codes: the
 * password only for accounts that have one (the server rejects nothing
 * without it, but verifies it when present), plus the second factor.
 */
export function buildReauth(opts: { hasPassword: boolean; password: string; secondFactor: string }): Reauth {
  const out: Reauth = { ...classifySecondFactor(opts.secondFactor) };
  if (opts.hasPassword) out.password = opts.password;
  return out;
}

/** Is everything the server needs for the re-authentication filled in? */
export function reauthComplete(opts: { hasPassword: boolean; password: string; secondFactor: string }): boolean {
  return (!opts.hasPassword || opts.password.length > 0) && secondFactorComplete(opts.secondFactor);
}

/** Groups of four for manual entry: "JBSW Y3DP ...". */
export function formatSecret(secret: string): string {
  return secret.replace(/\s+/g, '').replace(/(.{4})(?=.)/g, '$1 ');
}

// ---------------------------------------------------------------------------
// Recovery codes
// ---------------------------------------------------------------------------

export const RECOVERY_FILENAME = 'tuxmail-recovery-codes.txt';

/** Text file with the recovery codes (one per line, numbered) for printing or a password manager. */
export function recoveryCodesText(codes: readonly string[], header: string, note: string, date = new Date()): string {
  const lines = [header, date.toISOString().slice(0, 10), '', ...codes.map((c, i) => `${String(i + 1).padStart(2, '0')}. ${c}`), '', note, ''];
  return lines.join('\n');
}

// ---------------------------------------------------------------------------
// Password change
// ---------------------------------------------------------------------------

export interface PasswordForm {
  hasPassword: boolean;
  current: string;
  next: string;
  repeat: string;
  /** Second factor (accounts without local password and with TOTP set a first password with it). */
  secondFactor: string;
  totpEnabled: boolean;
  minLength: number;
}

/** First problem of the form as a message key, or null when it can be sent. */
export function validatePasswordForm(f: PasswordForm): { key: MessageKey; params?: Params } | null {
  if (f.hasPassword && !f.current) return { key: 'security.password.errCurrent' };
  if (!f.hasPassword && f.totpEnabled && !secondFactorComplete(f.secondFactor)) return { key: 'security.password.errSecondFactor' };
  if ([...f.next].length < f.minLength) return { key: 'security.password.errShort', params: { count: f.minLength } };
  if (f.next.trim() === '' || new Set(f.next).size < 2) return { key: 'security.errors.weakPassword' };
  if (f.hasPassword && f.next === f.current) return { key: 'security.password.errSame' };
  if (f.next !== f.repeat) return { key: 'security.password.errMismatch' };
  return null;
}

export function buildPasswordChange(f: PasswordForm): PasswordChange {
  if (f.hasPassword) return { currentPassword: f.current, newPassword: f.next };
  return { newPassword: f.next, ...(f.totpEnabled ? classifyForPassword(f.secondFactor) : {}) };
}

function classifyForPassword(input: string): Pick<PasswordChange, 'totpCode' | 'recoveryCode'> {
  const c = classifySecondFactor(input);
  return { totpCode: c.code, recoveryCode: c.recoveryCode };
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

export interface SessionDescription {
  /** Device name, or the browser; "" when unknown. */
  device: string;
  /** Operating system; "" when unknown. */
  os: string;
}

/** Browser and operating system from a user agent string (best effort; no external library). */
export function parseUserAgent(ua: string): { browser: string; os: string } {
  const has = (re: RegExp) => re.test(ua);
  let browser = '';
  if (has(/Edg(e|A|iOS)?\//)) browser = 'Edge';
  else if (has(/OPR\/|Opera/)) browser = 'Opera';
  else if (has(/Firefox\/|FxiOS\//)) browser = 'Firefox';
  else if (has(/Chrome\/|CriOS\//)) browser = 'Chrome';
  else if (has(/Safari\//)) browser = 'Safari';
  else if (has(/TuxMail/i)) browser = 'TuxMail';
  let os = '';
  if (has(/Android/)) os = 'Android';
  else if (has(/iPhone|iPad|iPod/)) os = 'iOS';
  else if (has(/Windows/)) os = 'Windows';
  else if (has(/Mac OS X|Macintosh/)) os = 'macOS';
  else if (has(/CrOS/)) os = 'ChromeOS';
  else if (has(/Linux|X11/)) os = 'Linux';
  return { browser, os };
}

export function describeSession(s: Pick<AuthSession, 'deviceName' | 'userAgent'>): SessionDescription {
  const ua = parseUserAgent(s.userAgent || '');
  return { device: s.deviceName || ua.browser, os: s.deviceName ? '' : ua.os };
}

/** Current session first, then by last activity (newest first). */
export function sortSessions<T extends Pick<AuthSession, 'current' | 'lastSeenAt'>>(list: readonly T[]): T[] {
  return [...list].sort((a, b) => Number(!!b.current) - Number(!!a.current) || Date.parse(b.lastSeenAt) - Date.parse(a.lastSeenAt));
}
