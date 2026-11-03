// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it, vi } from 'vitest';
import { fetchVapidKey, registrationKey, subscribeAtServer, subscribeBody, unsubscribeAtServer } from './push';

const session = { serverUrl: 'https://s.example', token: 'tok', deviceName: 'TuxMail Android' };
const up = { provider: 'unifiedpush' as const, endpoint: 'https://ntfy.sh/upAbc?up=1', keys: { p256dh: 'BPk', auth: 'au' } };

function okFetch(status = 201, body: unknown = { id: 1 }) {
  return vi.fn(async () => new Response(JSON.stringify(body), { status })) as unknown as typeof fetch & ReturnType<typeof vi.fn>;
}

describe('subscribeBody', () => {
  it('UnifiedPush uses the Web-Push subscription shape', () => {
    expect(subscribeBody(up, 'Phone')).toEqual({ type: 'unifiedpush', endpoint: up.endpoint, keys: up.keys, deviceName: 'Phone' });
  });
  it('FCM sends the token', () => {
    expect(subscribeBody({ provider: 'fcm', token: 'fcm-tok' }, 'Phone', 'org.tuxmail.app')).toEqual({ type: 'fcm', token: 'fcm-tok', deviceName: 'Phone', appId: 'org.tuxmail.app' });
  });
  it('rejects incomplete or insecure registrations', () => {
    expect(subscribeBody({ provider: 'unifiedpush', endpoint: 'http://ntfy.local/x', keys: up.keys }, 'P')).toBeNull();
    expect(subscribeBody({ provider: 'unifiedpush', endpoint: up.endpoint }, 'P')).toBeNull();
    expect(subscribeBody({ provider: 'fcm' }, 'P')).toBeNull();
  });
  it('keys registrations by provider + endpoint/token', () => {
    expect(registrationKey(up)).toBe('unifiedpush:' + up.endpoint);
    expect(registrationKey({ provider: 'fcm', token: 't' })).toBe('fcm:t');
  });
});

describe('server calls', () => {
  it('POSTs /api/push/subscribe with the Bearer token', async () => {
    const f = okFetch();
    expect(await subscribeAtServer(session, up, undefined, f)).toBe('subscribed');
    const [url, init] = f.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('https://s.example/api/push/subscribe');
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer tok');
    expect(JSON.parse(String(init.body)).endpoint).toBe(up.endpoint);
  });
  it('reports servers without FCM support', async () => {
    expect(await subscribeAtServer(session, { provider: 'fcm', token: 't' }, undefined, okFetch(400, { error: 'invalid_subscription' }))).toBe('unsupported-by-server');
    expect(await subscribeAtServer(session, up, undefined, okFetch(500))).toBe('failed');
    expect(await subscribeAtServer(session, { provider: 'fcm' }, undefined, okFetch())).toBe('invalid');
  });
  it('reads the VAPID key (publicKey as served by internal/server)', async () => {
    expect(await fetchVapidKey(session, okFetch(200, { publicKey: 'BKey' }))).toBe('BKey');
    expect(await fetchVapidKey(session, okFetch(404, {}))).toBeUndefined();
  });
  it('unsubscribes with DELETE', async () => {
    const f = okFetch(204, {});
    await unsubscribeAtServer(session, up, f);
    expect((f.mock.calls[0] as [string, RequestInit])[1].method).toBe('DELETE');
  });
});
