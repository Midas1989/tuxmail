// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render } from '../components/security/mountUtil';
import { flushSync } from 'svelte';
import ServerConnect from './ServerConnect.svelte';
import { normalizeServerUrl } from '../platform';

afterEach(cleanup);

const props = { store: {} as never, profiles: [], deviceName: 'Test', openUrl: async () => {}, ondone: vi.fn() };
const field = (root: HTMLElement) => root.querySelector<HTMLInputElement>('#server-url')!;

function typeInto(el: HTMLInputElement, value: string) {
  el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  flushSync();
}

describe('ServerConnect: address field next to the fixed https:// prefix', () => {
  it('takes a bare host name: plain text field (no browser URL check), URL keyboard', () => {
    const el = field(render(ServerConnect, props));
    // type="url" + required made the browser reject "mail.example.de" without a scheme.
    expect(el.type).toBe('text');
    expect(el.inputMode).toBe('url');
    typeInto(el, 'mail.example.de');
    expect(el.checkValidity()).toBe(true);
    expect(normalizeServerUrl(el.value)).toBe('https://mail.example.de');
  });

  it('removes a pasted https:// because the prefix is already shown', () => {
    const el = field(render(ServerConnect, props));
    typeInto(el, 'https://mail.example.de/');
    expect(el.value).toBe('mail.example.de/');
    expect(normalizeServerUrl(el.value)).toBe('https://mail.example.de');
  });

  it('keeps http:// so that the existing check rejects it (never upgraded silently)', () => {
    const el = field(render(ServerConnect, props));
    typeInto(el, 'http://mail.example.de');
    expect(el.value).toBe('http://mail.example.de');
    expect(normalizeServerUrl(el.value)).toBeNull();
  });

  it('prefill from a tuxmail://connect link or a re-login still works', () => {
    expect(field(render(ServerConnect, { ...props, prefill: 'https://mail.example.de' })).value).toBe('mail.example.de');
    cleanup();
    expect(field(render(ServerConnect, { ...props, prefill: 'mail.example.de' })).value).toBe('mail.example.de');
  });
});
