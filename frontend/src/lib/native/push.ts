// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Native push registration (KONZEPT 15.9): the Kotlin side talks to FCM
// (play flavor) or a UnifiedPush distributor (foss flavor) and reports the
// result as a `pushRegistration` event; this module registers it at the
// TuxMail server.
//
// UnifiedPush endpoints are Web-Push endpoints (RFC 8030/8291 with VAPID),
// so they use the existing POST /api/push/subscribe body unchanged.
// FCM tokens are stored by servers with push.fcm configured (501 otherwise).

import type { PushRegistration } from './plugins';

export interface PushSession {
  serverUrl: string;
  token: string;
  deviceName: string;
}

export interface SubscribeBody {
  type: 'unifiedpush' | 'fcm';
  endpoint?: string;
  keys?: { p256dh: string; auth: string };
  token?: string;
  deviceName: string;
  /** Lets the server pick the FCM project / app (several builds may share one server). */
  appId?: string;
}

/** Request body for POST /api/push/subscribe, or null if the registration is incomplete. */
export function subscribeBody(reg: PushRegistration, deviceName: string, appId?: string): SubscribeBody | null {
  if (reg.provider === 'unifiedpush') {
    if (!reg.endpoint || !/^https:\/\//.test(reg.endpoint) || !reg.keys?.p256dh || !reg.keys.auth) return null;
    return { type: 'unifiedpush', endpoint: reg.endpoint, keys: { p256dh: reg.keys.p256dh, auth: reg.keys.auth }, deviceName };
  }
  if (reg.provider === 'fcm') {
    if (!reg.token) return null;
    return { type: 'fcm', token: reg.token, deviceName, appId };
  }
  return null;
}

/** Stable identity of a registration, to avoid re-posting the same one on every start. */
export function registrationKey(reg: PushRegistration): string {
  return reg.provider + ':' + (reg.endpoint ?? reg.token ?? '');
}

async function call(session: PushSession, method: string, path: string, body?: unknown, f: typeof fetch = fetch): Promise<Response> {
  return f(session.serverUrl + path, {
    method,
    headers: { Authorization: `Bearer ${session.token}`, Accept: 'application/json', ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'omit',
  });
}

/** VAPID public key of the server (UnifiedPush distributors require it). */
export async function fetchVapidKey(session: PushSession, f: typeof fetch = fetch): Promise<string | undefined> {
  try {
    const res = await call(session, 'GET', '/api/push/vapid-public-key', undefined, f);
    if (!res.ok) return undefined;
    const j = (await res.json()) as { publicKey?: string; key?: string };
    return j.publicKey ?? j.key;
  } catch {
    return undefined;
  }
}

export type SubscribeOutcome = 'subscribed' | 'unsupported-by-server' | 'invalid' | 'failed';

export async function subscribeAtServer(session: PushSession, reg: PushRegistration, appId?: string, f: typeof fetch = fetch): Promise<SubscribeOutcome> {
  const body = subscribeBody(reg, session.deviceName, appId);
  if (!body) return 'invalid';
  try {
    const res = await call(session, 'POST', '/api/push/subscribe', body, f);
    if (res.ok) return 'subscribed';
    // Servers without FCM support reject the token (no endpoint/keys).
    if ((res.status === 400 || res.status === 501) && reg.provider === 'fcm') return 'unsupported-by-server';
    return 'failed';
  } catch {
    return 'failed';
  }
}

export async function unsubscribeAtServer(session: PushSession, reg: PushRegistration, f: typeof fetch = fetch): Promise<void> {
  const endpoint = reg.endpoint ?? (reg.token ? `fcm:${reg.token}` : undefined);
  if (!endpoint) return;
  try {
    await call(session, 'DELETE', '/api/push/subscribe', { endpoint, type: reg.provider, token: reg.token }, f);
  } catch {
    /* best effort */
  }
}
