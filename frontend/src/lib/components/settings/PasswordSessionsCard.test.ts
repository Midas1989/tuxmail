// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MockTransport } from '../../api/mock';
import { MOCK_MIN_PASSWORD, MOCK_PASSWORD, MOCK_TOTP_CODE } from '../../api/mockSecurity';
import type { MockSecurityOptions } from '../../api/mockSecurity';
import PasswordSessionsCard from './PasswordSessionsCard.svelte';
import { button, cleanup, click, render, submit, text, type, until } from '../security/mountUtil';

afterEach(cleanup);

const mock = (security: MockSecurityOptions = {}) => new MockTransport({ latencyMs: 0, security });
const rows = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[data-testid="sessions"] li')];
const form = (root: HTMLElement) => root.querySelector('form.pw') as HTMLFormElement;
const fields = (root: HTMLElement) => [...form(root).querySelectorAll<HTMLInputElement>('input')];
const alert = (root: HTMLElement) => form(root).querySelector('[role="alert"]')?.textContent;

async function ready(root: HTMLElement) {
  await until(() => expect(rows(root).length).toBeGreaterThan(0));
}

describe('PasswordSessionsCard: change password', () => {
  it('shows the policy hint from /api/auth/config', async () => {
    const root = render(PasswordSessionsCard, { api: mock() });
    await ready(root);
    await until(() => expect(text(form(root))).toContain(`At least ${MOCK_MIN_PASSWORD} characters.`));
    expect(fields(root).map((f) => f.autocomplete)).toEqual(['current-password', 'new-password', 'new-password']);
  });

  it('checks the form before sending anything', async () => {
    const api = mock();
    const change = vi.spyOn(api, 'changePassword');
    const root = render(PasswordSessionsCard, { api });
    await ready(root);
    await until(() => expect(text(form(root))).toContain('At least 12 characters.'));
    const [cur, next, repeat] = fields(root);

    await submit(form(root));
    expect(alert(root)).toBe('Enter your current password.');
    type(cur, MOCK_PASSWORD);
    type(next, 'short');
    type(repeat, 'short');
    await submit(form(root));
    expect(alert(root)).toBe('The new password needs at least 12 characters.');
    type(next, 'a-long-new-password');
    type(repeat, 'a-long-new-password-typo');
    await submit(form(root));
    expect(alert(root)).toBe('The two new passwords do not match.');
    expect(change).not.toHaveBeenCalled();
  });

  it('wrong current password: the server answer is shown in plain words and the form keeps its input', async () => {
    const root = render(PasswordSessionsCard, { api: mock() });
    await ready(root);
    const [cur, next, repeat] = fields(root);
    type(cur, 'not-my-password');
    type(next, 'a-long-new-password');
    type(repeat, 'a-long-new-password');
    await submit(form(root));
    await until(() => expect(alert(root)).toBe('The password is not correct.'));
    expect(next.value).toBe('a-long-new-password');
  });

  it('success: toast text, fields cleared, all other sessions gone', async () => {
    const api = mock();
    const root = render(PasswordSessionsCard, { api });
    await ready(root);
    expect(rows(root)).toHaveLength(3);
    const [cur, next, repeat] = fields(root);
    type(cur, MOCK_PASSWORD);
    type(next, 'a-long-new-password');
    type(repeat, 'a-long-new-password');
    await submit(form(root));
    await until(() => expect(rows(root)).toHaveLength(1));
    expect(fields(root).map((f) => f.value)).toEqual(['', '', '']);
    expect(alert(root)).toBeUndefined();
    expect((await api.listAuthSessions()).length).toBe(1);
  });

  it('account without local password and with TOTP: first password with a code instead of the current password', async () => {
    const api = mock({ hasPassword: false, totpEnabled: true });
    const spy = vi.spyOn(api, 'changePassword');
    const root = render(PasswordSessionsCard, { api });
    await ready(root);
    expect(root.querySelector('h2')?.textContent).toBe('Set a password');
    const [code, next, repeat] = fields(root);
    type(next, 'a-long-new-password');
    type(repeat, 'a-long-new-password');
    await submit(form(root));
    expect(alert(root)).toBe('Enter a code from your authenticator app or a recovery code.');
    type(code, '000000');
    await submit(form(root));
    await until(() => expect(alert(root)).toContain('This code is not correct'));
    type(code, MOCK_TOTP_CODE);
    await submit(form(root));
    await until(() => expect(root.querySelector('h2')?.textContent).toBe('Password'));
    expect(spy).toHaveBeenLastCalledWith({ newPassword: 'a-long-new-password', totpCode: MOCK_TOTP_CODE, recoveryCode: undefined });
  });

  it('first password without TOTP: the server asks for a fresh identity-provider login, shown localized', async () => {
    const root = render(PasswordSessionsCard, { api: mock({ hasPassword: false }) });
    await ready(root);
    const [next, repeat] = fields(root);
    type(next, 'a-long-new-password');
    type(repeat, 'a-long-new-password');
    await submit(form(root));
    await until(() => expect(alert(root)).toContain('sign in again with your identity provider'));
  });
});

async function proof(root: HTMLElement) {
  type(root.querySelector<HTMLInputElement>('form.stepup input[type="password"]')!, 'pw');
  await submit(root.querySelector<HTMLFormElement>('form.stepup')!);
}

describe('PasswordSessionsCard: sessions', () => {
  it('lists this device first, marks it, describes browser/OS or the app and hides "Sign out" for this device', async () => {
    const root = render(PasswordSessionsCard, { api: mock() });
    await ready(root);
    const items = rows(root);
    expect(items).toHaveLength(3);
    expect(text(items[0])).toContain('Firefox on Linux');
    expect(text(items[0])).toContain('This device');
    expect(text(items[0])).toContain('192.0.2.10');
    expect([...items[0].querySelectorAll('button')]).toHaveLength(0);
    expect(items.some((i) => text(i).includes('Pixel 9') && text(i).includes('App'))).toBe(true);
    expect(items.some((i) => text(i).includes('Safari on macOS'))).toBe(true);
  });

  it('ends one session', async () => {
    const api = mock();
    const root = render(PasswordSessionsCard, { api });
    await ready(root);
    const revoke = vi.spyOn(api, 'revokeAuthSession');
    await click(root.querySelector<HTMLButtonElement>('button[aria-label="Sign out Safari on macOS"]')!);
    expect(revoke).not.toHaveBeenCalled(); // first the proof
    await proof(root);
    await until(() => expect(rows(root)).toHaveLength(2));
    expect(revoke).toHaveBeenCalledWith(2, expect.objectContaining({ password: 'pw' }));
    expect((await api.listAuthSessions()).map((s) => s.id)).toEqual([1, 3]);
  });

  it('"Sign out all other sessions" ends all but this one and is then disabled', async () => {
    const api = mock();
    const root = render(PasswordSessionsCard, { api });
    await ready(root);
    await click(button(root, 'Sign out all other sessions'));
    await proof(root);
    await until(() => expect(rows(root)).toHaveLength(1));
    expect(button(root, 'Sign out all other sessions').disabled).toBe(true);
    expect(text(root)).toContain('There are no other sessions.');
  });

  it('a failing revoke is reported without changing the list', async () => {
    const api = mock();
    vi.spyOn(api, 'revokeAuthSession').mockRejectedValueOnce(Object.assign(new Error('gone'), { status: 404, code: 'not_found' }));
    const root = render(PasswordSessionsCard, { api });
    await ready(root);
    await click(root.querySelector<HTMLButtonElement>('button[aria-label="Sign out Safari on macOS"]')!);
    await proof(root);
    await until(() => expect(root.textContent).toContain('This entry no longer exists'));
    expect(rows(root)).toHaveLength(3);
  });
});
