// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
// Automatic set-up of a mail account: only the result and a short, collapsed
// check report; no DNS records, no raw errors, nothing of other domains.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { cleanup, input, render, submit, type, until } from '../security/mountUtil';
import type { DiscoveryResult } from '../../types';
import en from '$locales/en.json';

const result: DiscoveryResult = {
  source: 'autoconfig',
  kind: 'imap',
  displayName: 'Beispiel Hosting',
  verified: true,
  incoming: { host: 'imap.example.org', port: 993, tls: 'tls', auth: 'password', username: 'anna@example.org' },
  outgoing: { host: 'smtp.example.org', port: 465, tls: 'tls', auth: 'password', username: 'anna@example.org' },
  candidates: [
    { source: 'autoconfig', kind: 'imap', incoming: { host: 'imap.example.org', port: 993, tls: 'tls', auth: 'password', username: 'anna@example.org' }, outgoing: { host: 'smtp.example.org', port: 465, tls: 'tls', auth: 'password', username: 'anna@example.org' }, in: { reachable: true, login: 'ok' }, out: { reachable: true, login: 'ok' }, verified: true, score: 300 },
    { source: 'guess', kind: 'imap', incoming: { host: 'mail.example.org', port: 143, tls: 'starttls', auth: 'password', username: 'anna@example.org' }, in: { reachable: false, error: 'dns' }, out: { reachable: false }, verified: false, score: 10 },
  ],
};

vi.mock('../../state', () => ({
  app: { api: { kind: 'mock', discoverAccount: async () => structuredClone(result) }, openSettings: () => {}, toast: () => {} },
}));

import AccountWizard from './AccountWizard.svelte';

afterEach(cleanup);

describe('AccountWizard discovery result', () => {
  it('shows provider, servers and security; the report is collapsed and has no raw records', async () => {
    const root = render(AccountWizard, { onclose: () => {} });
    type(input(root, 'input[type="email"]'), 'anna@example.org');
    type(input(root, 'input[type="password"]'), 'geheim');
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.textContent).toContain(en.onboarding.account.found.replace('{provider}', 'Beispiel Hosting')));
    const text = root.textContent!;
    expect(text).toContain('imap.example.org:993');
    expect(text).toContain('smtp.example.org:465');
    expect(root.querySelector('[data-testid="wizard-report-body"]')).toBeNull();
    (root.querySelector('[data-testid="wizard-report"]') as HTMLButtonElement).click();
    flushSync();
    const report = root.querySelector('[data-testid="wizard-report-body"]')!.textContent!;
    expect(report).toContain(en.onboarding.account.check.loginOk);
    expect(report).toContain(en.onboarding.account.check.reason.dns);
    expect(report).not.toMatch(/lookup|no such host|MX|SRV|TXT|CNAME/);
  });
});
