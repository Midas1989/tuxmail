// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import en from '$locales/en.json';
import de from '$locales/de.json';
import { createTranslator, flatten } from './i18n/core';
import { ApiError } from './api/api';
import {
  buildPasswordChange,
  buildReauth,
  classifySecondFactor,
  describeSession,
  formatSecret,
  parseUserAgent,
  reauthComplete,
  recoveryCodesText,
  retryMinutes,
  sanitizeTotpCode,
  secondFactorComplete,
  securityErrorMessage,
  sortSessions,
  validatePasswordForm,
  type PasswordForm,
} from './security';

const EN = flatten(en);
const DE = flatten(de);
const tEn = createTranslator(EN, EN, 'en');
const tDe = createTranslator(DE, EN, 'de');

describe('securityErrorMessage', () => {
  const cases: [string, string][] = [
    ['invalid_code', 'security.errors.invalidCode'],
    ['invalid_credentials', 'security.errors.invalidCredentials'],
    ['reauth_required', 'security.errors.reauthRequired'],
    ['mfa_setup_required', 'security.errors.mfaSetupRequired'],
    ['weak_password', 'security.errors.weakPassword'],
    ['totp_not_enabled', 'security.errors.notEnabled'],
    ['exists', 'security.errors.alreadyEnabled'],
    ['not_found', 'security.errors.notFound'],
    ['not_supported', 'security.errors.notSupported'],
    ['network', 'security.errors.network'],
    ['unauthenticated', 'security.errors.signedOut'],
  ];
  for (const [code, key] of cases) {
    it(`${code} -> own localized text instead of the English server message`, () => {
      const err = new ApiError('server says something English', 400, code);
      expect(securityErrorMessage(err, tEn)).toBe(EN[key]);
      expect(securityErrorMessage(err, tDe)).toBe(DE[key]);
      expect(securityErrorMessage(err, tDe)).not.toContain('English');
    });
  }

  it('429 throttled: with Retry-After the minutes are named (rounded up), without a generic text', () => {
    expect(securityErrorMessage(new ApiError('x', 429, 'throttled', 240), tEn)).toBe('Too many wrong attempts. Security checks are locked; try again in 4 min.');
    expect(securityErrorMessage(new ApiError('x', 429, 'throttled', 61), tEn)).toContain('2 min');
    expect(securityErrorMessage(new ApiError('x', 429, 'throttled', 5), tEn)).toContain('1 min');
    expect(securityErrorMessage(new ApiError('x', 429, 'throttled'), tEn)).toBe(EN['security.errors.throttled']);
    // by status alone, e.g. a proxy answering 429 without our error code
    expect(securityErrorMessage(new ApiError('Too Many Requests', 429, 'http_429'), tDe)).toBe(DE['security.errors.throttled']);
  });

  it('unknown codes fall back to the server message, then to a generic text', () => {
    expect(securityErrorMessage(new ApiError('disk full', 500, 'internal'), tEn)).toBe('disk full');
    expect(securityErrorMessage(new ApiError('', 500, 'internal'), tEn)).toBe(EN['security.errors.generic']);
    expect(securityErrorMessage(undefined, tEn)).toBe(EN['security.errors.generic']);
    expect(securityErrorMessage(new Error('boom'), tEn)).toBe('boom');
  });

  it('retryMinutes', () => {
    expect(retryMinutes(new ApiError('x', 429, 'throttled', 300))).toBe(5);
    expect(retryMinutes(new ApiError('x', 429, 'throttled'))).toBeUndefined();
    expect(retryMinutes(null)).toBeUndefined();
  });
});

describe('second factor input', () => {
  it('six digits (spaces and dashes allowed) are an authenticator code, anything else a recovery code', () => {
    expect(classifySecondFactor('123456')).toEqual({ code: '123456' });
    expect(classifySecondFactor(' 123 456 ')).toEqual({ code: '123456' });
    expect(classifySecondFactor('abcde-fghjk')).toEqual({ recoveryCode: 'abcde-fghjk' });
    expect(classifySecondFactor('ABCDE FGHJK')).toEqual({ recoveryCode: 'ABCDE FGHJK' });
    expect(classifySecondFactor('   ')).toEqual({});
  });

  it('completeness', () => {
    expect(secondFactorComplete('12345')).toBe(false);
    expect(secondFactorComplete('123456')).toBe(true);
    expect(secondFactorComplete('abcde-fgh')).toBe(false);
    expect(secondFactorComplete('abcde-fghjk')).toBe(true);
    expect(secondFactorComplete('')).toBe(false);
  });

  it('sanitizeTotpCode keeps at most 6 digits', () => {
    expect(sanitizeTotpCode('12a3 456789')).toBe('123456');
    expect(sanitizeTotpCode('abc')).toBe('');
  });

  it('formatSecret groups by four', () => {
    expect(formatSecret('JBSWY3DPEHPK3PXP')).toBe('JBSW Y3DP EHPK 3PXP');
    expect(formatSecret('ABCDE')).toBe('ABCD E');
  });
});

describe('re-authentication payload', () => {
  it('sends the password only for accounts that have one, plus the second factor', () => {
    expect(buildReauth({ hasPassword: true, password: 'pw', secondFactor: '123456' })).toEqual({ password: 'pw', code: '123456' });
    expect(buildReauth({ hasPassword: false, password: 'ignored', secondFactor: '123456' })).toEqual({ code: '123456' });
    expect(buildReauth({ hasPassword: true, password: 'pw', secondFactor: 'abcde-fghjk' })).toEqual({ password: 'pw', recoveryCode: 'abcde-fghjk' });
  });

  it('is complete only with everything the server demands', () => {
    expect(reauthComplete({ hasPassword: true, password: '', secondFactor: '123456' })).toBe(false);
    expect(reauthComplete({ hasPassword: true, password: 'pw', secondFactor: '' })).toBe(false);
    expect(reauthComplete({ hasPassword: true, password: 'pw', secondFactor: '123456' })).toBe(true);
    expect(reauthComplete({ hasPassword: false, password: '', secondFactor: '123456' })).toBe(true);
  });
});

describe('recovery code export', () => {
  it('numbered, one per line, with header, date and note', () => {
    const txt = recoveryCodesText(['aaaaa-bbbbb', 'ccccc-ddddd'], 'Header', 'Note', new Date('2026-11-06T10:00:00Z'));
    expect(txt).toBe('Header\n2026-11-06\n\n01. aaaaa-bbbbb\n02. ccccc-ddddd\n\nNote\n');
  });
});

describe('password form', () => {
  const base: PasswordForm = { hasPassword: true, current: 'old-password', next: 'a-new-long-password', repeat: 'a-new-long-password', secondFactor: '', totpEnabled: false, minLength: 12 };

  it('valid form passes and sends current + new', () => {
    expect(validatePasswordForm(base)).toBeNull();
    expect(buildPasswordChange(base)).toEqual({ currentPassword: 'old-password', newPassword: 'a-new-long-password' });
  });

  it('reports the first problem as a localizable key', () => {
    expect(validatePasswordForm({ ...base, current: '' })).toEqual({ key: 'security.password.errCurrent' });
    expect(validatePasswordForm({ ...base, next: 'short', repeat: 'short' })).toEqual({ key: 'security.password.errShort', params: { count: 12 } });
    expect(validatePasswordForm({ ...base, next: 'aaaaaaaaaaaaaaaa', repeat: 'aaaaaaaaaaaaaaaa' })).toEqual({ key: 'security.errors.weakPassword' });
    expect(validatePasswordForm({ ...base, next: 'old-password', repeat: 'old-password' })).toEqual({ key: 'security.password.errSame' });
    expect(validatePasswordForm({ ...base, repeat: 'something else entirely' })).toEqual({ key: 'security.password.errMismatch' });
  });

  it('counts characters, not bytes (umlauts)', () => {
    expect(validatePasswordForm({ ...base, minLength: 8, next: 'äöüäöüäö', repeat: 'äöüäöüäö' })).toBeNull();
  });

  it('first password of an account without local password: second factor instead of the current password', () => {
    const first: PasswordForm = { ...base, hasPassword: false, current: '', totpEnabled: true, secondFactor: '' };
    expect(validatePasswordForm(first)).toEqual({ key: 'security.password.errSecondFactor' });
    const ok = { ...first, secondFactor: '123456' };
    expect(validatePasswordForm(ok)).toBeNull();
    expect(buildPasswordChange(ok)).toEqual({ newPassword: 'a-new-long-password', totpCode: '123456', recoveryCode: undefined });
    expect(buildPasswordChange({ ...ok, secondFactor: 'abcde-fghjk' })).toMatchObject({ recoveryCode: 'abcde-fghjk', totpCode: undefined });
    // no TOTP: the server checks for a fresh identity-provider login itself
    expect(buildPasswordChange({ ...first, totpEnabled: false })).toEqual({ newPassword: 'a-new-long-password' });
  });
});

describe('sessions', () => {
  it('parses browser and operating system', () => {
    expect(parseUserAgent('Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0')).toEqual({ browser: 'Firefox', os: 'Linux' });
    expect(parseUserAgent('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36 Edg/130.0')).toEqual({ browser: 'Edge', os: 'Windows' });
    expect(parseUserAgent('Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/130.0 Safari/537.36')).toEqual({ browser: 'Chrome', os: 'Windows' });
    expect(parseUserAgent('Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1')).toEqual({ browser: 'Safari', os: 'iOS' });
    expect(parseUserAgent('Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 Chrome/130.0 Mobile Safari/537.36')).toEqual({ browser: 'Chrome', os: 'Android' });
    expect(parseUserAgent('')).toEqual({ browser: '', os: '' });
  });

  it('a device name wins over the user agent', () => {
    expect(describeSession({ deviceName: 'Pixel 9', userAgent: 'TuxMail-Android/1.0' })).toEqual({ device: 'Pixel 9', os: '' });
    expect(describeSession({ deviceName: '', userAgent: 'Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0' })).toEqual({ device: 'Firefox', os: 'Linux' });
    expect(describeSession({ deviceName: '', userAgent: '' })).toEqual({ device: '', os: '' });
  });

  it('current session first, then newest activity', () => {
    const list = [
      { id: 1, current: false, lastSeenAt: '2026-11-01T10:00:00Z' },
      { id: 2, current: false, lastSeenAt: '2026-11-05T10:00:00Z' },
      { id: 3, current: true, lastSeenAt: '2026-10-01T10:00:00Z' },
    ];
    expect(sortSessions(list).map((s) => s.id)).toEqual([3, 2, 1]);
  });
});

describe('texts', () => {
  it('every new text exists in German with the same placeholders (parity test covers keys) and is in du-form', () => {
    for (const k of Object.keys(DE).filter((k) => k.startsWith('security.'))) {
      expect(DE[k], k).not.toMatch(/\b(Sie|Ihre|Ihr|Ihnen|Ihren|Ihrem)\b/);
    }
  });
});
