// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, until } from '../security/mountUtil';

const flags = vi.hoisted(() => ({ web: true }));

vi.mock('../../state', async () => {
  const { MockTransport } = await import('../../api/mock');
  const api = new MockTransport({ latencyMs: 0 });
  Object.defineProperty(api, 'kind', { value: 'http' });
  return {
    app: { api, vault: null, toast: () => {}, openSettings: () => {} },
    security: {
      get available() {
        return flags.web;
      },
      refresh: async () => null,
    },
  };
});

import SecuritySettings from './SecuritySettings.svelte';

afterEach(cleanup);

describe('SecuritySettings: account security cards (web client only)', () => {
  it('web client: two-factor card, password/sessions card and the sign-out card', async () => {
    flags.web = true;
    const root = render(SecuritySettings, { part: 'privacy' });
    await until(() => expect(root.querySelector('#set-sessions [data-testid="sessions"] li')).toBeTruthy());
    expect([...root.querySelectorAll('section.card')].map((s) => s.id)).toEqual(['set-privacy', 'set-twofactor', 'set-password', 'set-sessions', 'set-session']);
  });

  it('desktop shell, demo and native app: neither card is rendered, no account request is made', () => {
    flags.web = false;
    const root = render(SecuritySettings, { part: 'privacy' });
    expect(root.querySelector('#set-twofactor')).toBeNull();
    expect(root.querySelector('#set-password')).toBeNull();
    expect(root.querySelector('#set-sessions')).toBeNull();
  });
});
