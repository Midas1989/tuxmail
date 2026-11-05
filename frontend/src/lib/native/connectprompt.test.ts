// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it } from 'vitest';
import { IDLE, cancelConnect, confirmConnect, requestConnect } from './connectprompt';

const link = (server: string) => 'tuxmail://connect?server=' + encodeURIComponent(server);

describe('connect link confirmation (NG-01)', () => {
  it('asks first and names the full host and the current session', () => {
    const s = requestConnect(link('https://mail.example.org.evil.tld:8443'), 'https://mail.example.org');
    expect(s).toEqual({ phase: 'confirm', server: 'https://mail.example.org.evil.tld:8443', host: 'mail.example.org.evil.tld:8443', current: 'mail.example.org' });
  });
  it('has no current host when signed out', () => {
    expect(requestConnect(link('https://a.example.org'), null)).toMatchObject({ phase: 'confirm', current: null });
  });
  it('refuses cleartext and malformed links without ever prompting', () => {
    expect(requestConnect(link('http://evil.example'), 'https://mail.example.org')).toBe(IDLE);
    expect(requestConnect(link('<script>'), null)).toBe(IDLE);
    expect(requestConnect('tuxmail://open?conversationId=1', null)).toBe(IDLE);
  });
  it('only continues after an explicit confirmation', () => {
    const asked = requestConnect(link('https://a.example.org'), 'https://b.example.org');
    expect(asked.phase).toBe('confirm');
    expect(confirmConnect(asked)).toEqual({ phase: 'connect', server: 'https://a.example.org', host: 'a.example.org' });
    expect(confirmConnect(IDLE)).toBe(IDLE); // nothing to confirm
    expect(cancelConnect()).toBe(IDLE);
  });
  it('a second confirm in connect phase is a no-op', () => {
    const c = confirmConnect(requestConnect(link('https://a.example.org'), null));
    expect(confirmConnect(c)).toBe(c);
  });
});
