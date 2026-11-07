// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Rotation of the device token (server: internal/auth/devicetokens.go).
//
//   POST /api/device/rotate (Bearer old) -> {access_token, expires_in, ...}
//
// The server keeps the old token valid until the new one is used for the first
// time; from then on the old one is only tolerated for a short while, and using
// it later revokes the whole device. So the order below matters: the new token
// is stored everywhere an old one could still be read (secure storage, the
// native side for push and notification actions) BEFORE anything uses it. If
// storing fails, the app stays on the old token and nothing is lost.

export type RotationOutcome = 'rotated' | 'not_due' | 'unsupported' | 'failed';

/** How often a client asks for a new token. The lifetime on the server is far longer. */
export const ROTATE_EVERY_MS = 24 * 60 * 60 * 1000;

export interface RotationDeps {
  serverUrl: string;
  /** The token in use. */
  token: () => string;
  /** Persist in secure storage. */
  persist: (token: string) => Promise<void>;
  /** Hand to the native side (push registration, notification actions). */
  mirror: (token: string) => Promise<void>;
  /** Switch the running app (transport, push). */
  use: (token: string) => void;
  lastRotatedAt: () => Promise<number>;
  setLastRotatedAt: (at: number) => Promise<void>;
  fetchImpl?: typeof fetch;
  now?: () => number;
}

let inFlight: Promise<RotationOutcome> | null = null;

/** Rotates if due. Never throws; concurrent calls share one rotation. */
export function rotateIfDue(d: RotationDeps): Promise<RotationOutcome> {
  inFlight ??= run(d)
    .catch((): RotationOutcome => 'failed')
    .finally(() => {
      inFlight = null;
    });
  return inFlight;
}

async function run(d: RotationDeps): Promise<RotationOutcome> {
  const f = d.fetchImpl ?? fetch;
  const now = d.now ?? Date.now;
  const old = d.token();
  if (!old) return 'not_due';
  // Tokens from before rotation have no timestamp: they are due at once.
  if (now() - (await d.lastRotatedAt()) < ROTATE_EVERY_MS) return 'not_due';

  const res = await f(d.serverUrl + '/api/device/rotate', {
    method: 'POST',
    headers: { Authorization: `Bearer ${old}`, Accept: 'application/json' },
    credentials: 'omit',
  });
  if (res.status === 404 || res.status === 405) {
    // A server that does not rotate yet: ask again tomorrow.
    await d.setLastRotatedAt(now());
    return 'unsupported';
  }
  if (res.status !== 200) return 'failed';
  const body = (await res.json().catch(() => ({}))) as { access_token?: unknown };
  const next = body.access_token;
  if (typeof next !== 'string' || !next) return 'failed';

  await d.persist(next);
  try {
    await d.mirror(next);
  } catch {
    // The native side keeps the old token: the new one must not come into use.
    await d.persist(old);
    return 'failed';
  }
  d.use(next);

  // First use: the server now makes the new token the current one.
  const me = await f(d.serverUrl + '/api/auth/me', {
    headers: { Authorization: `Bearer ${next}`, Accept: 'application/json' },
    credentials: 'omit',
  }).catch(() => null);
  if (me?.status === 401) {
    // Refused (a newer successor replaced it): the old token is still the valid one.
    await d.persist(old);
    await d.mirror(old).catch(() => {});
    d.use(old);
    return 'failed';
  }
  await d.setLastRotatedAt(now());
  return 'rotated';
}
