// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ROTATE_EVERY_MS, rotateIfDue, type RotationDeps } from './rotation';

function setup(route: (url: string, init?: RequestInit) => { status: number; body?: unknown } | Error) {
  const order: string[] = [];
  const state = { token: 'old', stored: 'old', native: 'old', last: 0 };
  const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    order.push('fetch ' + new URL(String(input)).pathname + ' ' + String((init?.headers as Record<string, string>).Authorization));
    const r = route(String(input), init);
    if (r instanceof Error) throw r;
    return new Response(JSON.stringify(r.body ?? {}), { status: r.status });
  });
  const deps: RotationDeps = {
    serverUrl: 'https://mail.example.org',
    token: () => state.token,
    persist: async (t) => {
      order.push('persist ' + t);
      state.stored = t;
    },
    mirror: async (t) => {
      order.push('mirror ' + t);
      state.native = t;
    },
    use: (t) => {
      order.push('use ' + t);
      state.token = t;
    },
    lastRotatedAt: async () => state.last,
    setLastRotatedAt: async (at) => {
      state.last = at;
    },
    fetchImpl: fetchImpl as unknown as typeof fetch,
    now: () => 1_000_000_000_000,
  };
  return { deps, state, order, fetchImpl };
}

const okRoute = (url: string) => (url.endsWith('/api/device/rotate') ? { status: 200, body: { access_token: 'new', expires_in: 7776000 } } : { status: 200, body: {} });

describe('rotateIfDue', () => {
  beforeEach(() => vi.restoreAllMocks());

  it('stores the new token everywhere before using it, then uses it once', async () => {
    const { deps, state, order } = setup(okRoute);
    expect(await rotateIfDue(deps)).toBe('rotated');
    expect(order).toEqual(['fetch /api/device/rotate Bearer old', 'persist new', 'mirror new', 'use new', 'fetch /api/auth/me Bearer new']);
    expect(state).toEqual({ token: 'new', stored: 'new', native: 'new', last: 1_000_000_000_000 });
  });

  it('does not rotate again before it is due', async () => {
    const { deps, state, fetchImpl } = setup(okRoute);
    state.last = 1_000_000_000_000 - ROTATE_EVERY_MS + 1;
    expect(await rotateIfDue(deps)).toBe('not_due');
    expect(fetchImpl).not.toHaveBeenCalled();
    state.last = 1_000_000_000_000 - ROTATE_EVERY_MS;
    expect(await rotateIfDue(deps)).toBe('rotated');
  });

  it('keeps the old token when the server cannot be reached', async () => {
    const { deps, state } = setup(() => new TypeError('Failed to fetch'));
    expect(await rotateIfDue(deps)).toBe('failed');
    expect(state).toEqual({ token: 'old', stored: 'old', native: 'old', last: 0 });
  });

  it('keeps the old token on a server error or a broken answer', async () => {
    for (const r of [{ status: 500 }, { status: 409, body: { error: 'token_superseded' } }, { status: 200, body: {} }, { status: 200, body: { access_token: 5 } }]) {
      const { deps, state, order } = setup(() => r);
      expect(await rotateIfDue(deps)).toBe('failed');
      expect(state.token).toBe('old');
      expect(order.filter((o) => o.startsWith('persist'))).toEqual([]);
    }
  });

  it('accepts servers without rotation', async () => {
    const { deps, state } = setup(() => ({ status: 404 }));
    expect(await rotateIfDue(deps)).toBe('unsupported');
    expect(state.token).toBe('old');
    expect(state.last).toBe(1_000_000_000_000);
  });

  it('never uses the new token when the native side could not take it', async () => {
    const { deps, state, order } = setup(okRoute);
    deps.mirror = async () => {
      throw new Error('plugin');
    };
    expect(await rotateIfDue(deps)).toBe('failed');
    expect(state.token).toBe('old');
    expect(state.stored).toBe('old');
    expect(order.some((o) => o.includes('Bearer new'))).toBe(false);
  });

  it('goes back when the new token is refused', async () => {
    const { deps, state } = setup((url) => (url.endsWith('/rotate') ? { status: 200, body: { access_token: 'new' } } : { status: 401 }));
    expect(await rotateIfDue(deps)).toBe('failed');
    expect(state).toEqual({ token: 'old', stored: 'old', native: 'old', last: 0 });
  });

  it('stays on the new token when only the confirmation got lost', async () => {
    const { deps, state } = setup((url) => (url.endsWith('/rotate') ? { status: 200, body: { access_token: 'new' } } : new TypeError('offline')));
    expect(await rotateIfDue(deps)).toBe('rotated');
    expect(state.token).toBe('new');
  });

  it('runs one rotation at a time', async () => {
    const { deps, fetchImpl } = setup(okRoute);
    const [a, b] = await Promise.all([rotateIfDue(deps), rotateIfDue(deps)]);
    expect([a, b]).toEqual(['rotated', 'rotated']);
    expect(fetchImpl.mock.calls.filter((c) => String(c[0]).endsWith('/rotate'))).toHaveLength(1);
  });
});
