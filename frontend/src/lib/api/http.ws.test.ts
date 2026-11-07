// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { HttpTransport } from './http';

// The WebSocket handshake carries its credential as subprotocol (server: internal/server/middleware.go).
function connect(opts: { authToken?: () => string | undefined }) {
  const protocols: Array<string[] | undefined> = [];
  class FakeSocket {
    onopen = null;
    onmessage = null;
    onclose = null;
    onerror = null;
    constructor(_url: string, p?: string[]) {
      protocols.push(p);
    }
    close() {}
  }
  const api = new HttpTransport({ fetchImpl: vi.fn() as unknown as typeof fetch, webSocketImpl: FakeSocket as unknown as typeof WebSocket, baseUrl: 'https://mail.example.org', ...opts });
  const off = api.subscribe(() => {});
  return { protocols, off };
}

describe('event socket credentials', () => {
  afterEach(() => {
    document.cookie = 'tuxmail_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT';
  });

  it('browser: CSRF token of the session', () => {
    document.cookie = 'tuxmail_csrf=abc123';
    const { protocols, off } = connect({});
    expect(protocols[0]).toEqual(['tuxmail.v1', 'tuxmail.csrf.abc123']);
    off();
  });

  it('native app: device token, no CSRF token', () => {
    document.cookie = 'tuxmail_csrf=abc123';
    const { protocols, off } = connect({ authToken: () => 'dev' });
    expect(protocols[0]).toEqual(['tuxmail.v1', 'tuxmail.bearer.dev']);
    off();
  });
});
