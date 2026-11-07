// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest';
import { ACTIVITY_HEADER, ACTIVITY_WINDOW_MS, activityHeaders } from './activity';
import { HttpTransport } from './api/http';

describe('activityHeaders', () => {
  it('marks requests soon after input, and only those', () => {
    window.dispatchEvent(new KeyboardEvent('keydown'));
    const t = Date.now();
    expect(activityHeaders(t + 1000)).toEqual({ [ACTIVITY_HEADER]: '1' });
    expect(activityHeaders(t + ACTIVITY_WINDOW_MS + 1000)).toEqual({});
  });

  let future = Date.now();
  for (const make of [() => new MouseEvent('pointerdown'), () => new WheelEvent('wheel'), () => new Event('touchstart')]) {
    it(`counts ${make().type}`, () => {
      vi.useFakeTimers();
      try {
        future += 10 * ACTIVITY_WINDOW_MS; // later than any input of the tests before
        vi.setSystemTime(future);
        expect(activityHeaders()).toEqual({});
        window.dispatchEvent(make());
        expect(activityHeaders()).toEqual({ [ACTIVITY_HEADER]: '1' });
      } finally {
        vi.useRealTimers();
      }
    });
  }
});

describe('transport', () => {
  function sent(authToken?: () => string | undefined) {
    const fetchImpl = vi.fn(async () => new Response('{}', { status: 200 }));
    const api = new HttpTransport({ fetchImpl: fetchImpl as unknown as typeof fetch, webSocketImpl: undefined, authToken });
    return { api, fetchImpl };
  }
  const headersOf = (f: ReturnType<typeof vi.fn>) => (f.mock.calls[0] as unknown as [string, RequestInit])[1].headers as Record<string, string>;

  it('sends the header with a session cookie after input', async () => {
    window.dispatchEvent(new KeyboardEvent('keydown'));
    const { api, fetchImpl } = sent();
    await api.subscribePush({} as never).catch(() => {});
    expect(headersOf(fetchImpl)[ACTIVITY_HEADER]).toBe('1');
  });

  it('never sends it with a device token', async () => {
    window.dispatchEvent(new KeyboardEvent('keydown'));
    const { api, fetchImpl } = sent(() => 'dev');
    await api.subscribePush({} as never).catch(() => {});
    expect(headersOf(fetchImpl)[ACTIVITY_HEADER]).toBeUndefined();
  });
});
