// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, click, render, until } from '../security/mountUtil';
import { decryptSecret } from '../../secretlink';

const m = vi.hoisted(() => ({ create: vi.fn(), toast: vi.fn() }));

vi.mock('../../state', () => ({
  app: { api: { kind: 'http', createSecretLink: m.create }, toast: m.toast },
}));

import SecretLinkDialog from './SecretLinkDialog.svelte';

beforeEach(() => {
  vi.clearAllMocks();
  m.create.mockResolvedValue({ id: 'AAAAAAAAAAAAAAAAAAAAAA', createdAt: '2026-11-09T12:00:00Z', expiresAt: '2026-11-10T12:00:00Z', views: 0, maxViews: 1, url: 'https://mail.example.org/secret/AAAAAAAAAAAAAAAAAAAAAA' });
});
afterEach(cleanup);

const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!;

describe('SecretLinkDialog', () => {
  it('names Bitwarden Send for really secret passwords before anything is typed', () => {
    render(SecretLinkDialog, { onclose: () => {} });
    const note = dlg().querySelector('[role="note"]');
    expect(note?.textContent).toMatch(/Bitwarden Send/);
  });

  it('encrypts in the browser: the server gets the ciphertext, the key only ends up in the link fragment', async () => {
    const oninsert = vi.fn();
    render(SecretLinkDialog, { oninsert, onclose: () => {} });
    const ta = dlg().querySelector('textarea')!;
    ta.value = 'Mein Passwort 123';
    ta.dispatchEvent(new Event('input', { bubbles: true }));
    const create = [...dlg().querySelectorAll('button')].find((b) => b.classList.contains('btn-primary'))!;
    await until(() => expect(create.disabled).toBe(false));
    await click(create);
    await until(() => expect(m.create).toHaveBeenCalledTimes(1));

    const sent = m.create.mock.calls[0][0] as { ciphertext: string; ttlSeconds: number; maxViews: number };
    expect(sent.maxViews).toBe(1);
    expect(sent.ttlSeconds).toBe(86400);
    expect(JSON.stringify(sent)).not.toContain('Passwort');

    let link = '';
    await until(() => {
      link = dlg().querySelector<HTMLInputElement>('input[readonly]')?.value ?? '';
      expect(link).not.toBe('');
    });
    const url = new URL(link);
    expect(url.origin + url.pathname).toBe('https://mail.example.org/secret/AAAAAAAAAAAAAAAAAAAAAA');
    expect(await decryptSecret(sent.ciphertext, url.hash.slice(1))).toBe('Mein Passwort 123');
    expect(dlg().querySelector('textarea')).toBeNull(); // the clear text is gone from the dialog
  });
});
