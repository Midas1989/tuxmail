// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { isIos, isWails, normalizeServerUrl, shouldShowIosInstallHint, urlBase64ToUint8Array } from './platform';
import { MockTransport } from './api/mock';

const IPHONE_SAFARI = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1';
const IPHONE_CHROME = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0 Mobile/15E148 Safari/604.1';
const ANDROID = 'Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36';
const IPAD_DESKTOP_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15';

describe('platform detection', () => {
  it('detects Wails by window.go', () => {
    expect(isWails({ go: { app: {} } })).toBe(true);
    expect(isWails({})).toBe(false);
    expect(isWails(undefined)).toBe(false);
  });

  it('detects iOS including iPadOS desktop mode', () => {
    expect(isIos({ userAgent: IPHONE_SAFARI })).toBe(true);
    expect(isIos({ userAgent: IPAD_DESKTOP_UA, platform: 'MacIntel', maxTouchPoints: 5 })).toBe(true);
    expect(isIos({ userAgent: IPAD_DESKTOP_UA, platform: 'MacIntel', maxTouchPoints: 0 })).toBe(false);
    expect(isIos({ userAgent: ANDROID })).toBe(false);
  });

  it('shows the iOS install hint only in Safari, not installed, not dismissed', () => {
    expect(shouldShowIosInstallHint({ userAgent: IPHONE_SAFARI })).toBe(true);
    expect(shouldShowIosInstallHint({ userAgent: IPHONE_SAFARI, standalone: true })).toBe(false);
    expect(shouldShowIosInstallHint({ userAgent: IPHONE_SAFARI, displayStandalone: true })).toBe(false);
    expect(shouldShowIosInstallHint({ userAgent: IPHONE_SAFARI, dismissed: true })).toBe(false);
    expect(shouldShowIosInstallHint({ userAgent: IPHONE_CHROME })).toBe(false);
    expect(shouldShowIosInstallHint({ userAgent: ANDROID })).toBe(false);
  });

  it('decodes base64url VAPID keys', () => {
    const bytes = urlBase64ToUint8Array('AQID_-8');
    expect([...bytes]).toEqual([1, 2, 3, 255, 239]);
  });

  it('normalises server URLs and rejects plain http', () => {
    expect(normalizeServerUrl('mail.example.de')).toBe('https://mail.example.de');
    expect(normalizeServerUrl(' https://mail.example.de/tuxmail/ ')).toBe('https://mail.example.de/tuxmail');
    expect(normalizeServerUrl('http://mail.example.de')).toBeNull();
    expect(normalizeServerUrl('http://localhost:8080')).toBe('http://localhost:8080');
    expect(normalizeServerUrl('')).toBeNull();
  });
});

describe('MockTransport server provisioning', () => {
  it('runs the device flow until approval and returns a profile', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const auth = await api.deviceAuthorize('https://mail.example.de/');
    expect(auth.verificationUriComplete).toBe('https://mail.example.de/device?user_code=WDJB-MJHT');
    expect((await api.deviceToken('https://mail.example.de', auth.deviceCode)).status).toBe('pending');
    expect((await api.deviceToken('https://mail.example.de', auth.deviceCode)).status).toBe('pending');
    const done = await api.deviceToken('https://mail.example.de', auth.deviceCode);
    expect(done.status).toBe('approved');
    expect(done.profile?.accounts.length).toBeGreaterThan(0);
    expect((await api.deviceToken('https://mail.example.de', 'unknown')).status).toBe('expired');
  });

  it('requires TOTP for direct login', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    expect((await api.serverLogin('https://x.de', { username: 'jonas', password: 'pw' })).status).toBe('denied');
    expect((await api.serverLogin('https://x.de', { username: 'jonas', password: 'pw', totp: '123456' })).status).toBe('approved');
  });

  it('persists notification settings', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const s = await api.getNotificationSettings();
    s.privacy = 'generic';
    s.accounts['3'] = false;
    await api.saveNotificationSettings(s);
    expect(await api.getNotificationSettings()).toMatchObject({ privacy: 'generic', accounts: { '3': false } });
  });
});
