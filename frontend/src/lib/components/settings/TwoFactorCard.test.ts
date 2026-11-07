// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MockTransport } from '../../api/mock';
import { MOCK_PASSWORD, MOCK_TOTP_CODE } from '../../api/mockSecurity';
import type { MockSecurityOptions } from '../../api/mockSecurity';
import TwoFactorCard from './TwoFactorCard.svelte';
import { button, cleanup, click, hasButton, input, render, stepUp, submit, text, type, until } from '../security/mountUtil';

beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn(async () => undefined) } });
});
afterEach(cleanup);

const mock = (security: MockSecurityOptions = {}) => new MockTransport({ latencyMs: 0, security });
const state = (root: HTMLElement) => root.querySelector('[data-testid="twofactor-state"]')?.textContent?.trim();

async function ready(root: HTMLElement) {
  await until(() => expect(state(root)).toBeTruthy());
}

describe('TwoFactorCard: enable', () => {
  it('shows "not set up" and walks through QR -> code -> recovery codes, then shows the status with 10 codes left', async () => {
    const api = mock();
    const root = render(TwoFactorCard, { api });
    await ready(root);
    expect(state(root)).toBe('Not set up');
    expect(hasButton(root, 'Turn off')).toBe(false);

    await click(button(root, 'Set up two-factor authentication'));
    await stepUp(root);
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
    await click(button(root, 'Next'));
    type(input(root, 'input[inputmode="numeric"]'), MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[data-step="codes"]')).toBeTruthy());
    expect(root.querySelectorAll('[data-testid="recovery-codes"] code')).toHaveLength(10);
    // still the enrolment while the codes are not confirmed
    expect(state(root)).toBe('Not set up');
    input(root, 'input[type="checkbox"]').click();
    await until(() => expect(button(root, 'Finish').disabled).toBe(false));
    await click(button(root, 'Finish'));

    await until(() => expect(state(root)).toBe('Active'));
    expect(root.querySelector('[data-testid="codes-left"]')?.textContent).toContain('10 recovery codes left');
    expect(hasButton(root, 'New recovery codes')).toBe(true);
    expect((await api.getAuthMe()).user.totpEnabled).toBe(true);
  });

  it('an administrator is told why (policy), a normal user is not', async () => {
    const admin = render(TwoFactorCard, { api: mock({ admin: true }) });
    await ready(admin);
    expect(text(admin)).toContain('Administrators on this server need two-factor authentication');
    cleanup();
    const user = render(TwoFactorCard, { api: mock() });
    await ready(user);
    expect(text(user)).not.toContain('Administrators on this server');
  });

  it('cancel leaves the enrolment', async () => {
    const root = render(TwoFactorCard, { api: mock() });
    await ready(root);
    await click(button(root, 'Set up two-factor authentication'));
    await stepUp(root);
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
    await click(button(root, 'Cancel'));
    expect(hasButton(root, 'Set up two-factor authentication')).toBe(true);
  });
});

describe('TwoFactorCard: disable (re-authentication as the server demands it)', () => {
  it('needs password AND code; wrong answers show localized messages; correct ones switch it off', async () => {
    const api = mock({ totpEnabled: true });
    const root = render(TwoFactorCard, { api });
    await until(() => expect(state(root)).toBe('Active'));
    await click(button(root, 'Turn off'));
    const submitBtn = button(root, 'Turn off');
    const [pw, code] = [...root.querySelectorAll<HTMLInputElement>('form input')];
    expect(pw.type).toBe('password');
    expect(submitBtn.disabled).toBe(true);
    type(pw, 'wrong');
    expect(submitBtn.disabled).toBe(true); // code still missing
    type(code, MOCK_TOTP_CODE);
    expect(submitBtn.disabled).toBe(false);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toBe('The password is not correct.'));

    type(pw, MOCK_PASSWORD);
    type(code, '111111');
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toContain('This code is not correct or was already used'));
    expect((await api.getAuthMe()).user.totpEnabled).toBe(true);

    type(code, MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(state(root)).toBe('Not set up'));
    expect((await api.getAuthMe()).user.totpEnabled).toBe(false);
  });

  it('too many wrong answers: the 429 is explained with the waiting time', async () => {
    const root = render(TwoFactorCard, { api: mock({ totpEnabled: true }) });
    await until(() => expect(state(root)).toBe('Active'));
    await click(button(root, 'Turn off'));
    const [pw, code] = [...root.querySelectorAll<HTMLInputElement>('form input')];
    type(pw, MOCK_PASSWORD);
    for (let i = 0; i < 6; i++) {
      type(code, '000000');
      await submit(root.querySelector('form') as HTMLFormElement);
      await until(() => expect(root.querySelector('[role="alert"]')).toBeTruthy());
    }
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toBe('Too many wrong attempts. Security checks (changing password, codes and sessions) are locked; try again in 5 min. Signing in is not affected.'));
  });

  it('an account without local password is asked for the code only', async () => {
    const api = mock({ totpEnabled: true, hasPassword: false });
    const root = render(TwoFactorCard, { api });
    await until(() => expect(state(root)).toBe('Active'));
    await click(button(root, 'Turn off'));
    expect(root.querySelectorAll('form input')).toHaveLength(1);
    expect(root.querySelector('input[type="password"]')).toBeNull();
    type(root.querySelector('form input') as HTMLInputElement, MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(state(root)).toBe('Not set up'));
  });

  it('cancel keeps two-factor authentication on', async () => {
    const root = render(TwoFactorCard, { api: mock({ totpEnabled: true }) });
    await until(() => expect(state(root)).toBe('Active'));
    await click(button(root, 'Turn off'));
    await click(button(root, 'Cancel'));
    expect(state(root)).toBe('Active');
    expect(root.querySelector('form')).toBeNull();
  });
});

describe('TwoFactorCard: new recovery codes', () => {
  it('re-authenticates, shows the new codes once and needs the confirmation', async () => {
    const api = mock({ totpEnabled: true });
    const root = render(TwoFactorCard, { api });
    await until(() => expect(state(root)).toBe('Active'));
    await click(button(root, 'New recovery codes'));
    const [pw, code] = [...root.querySelectorAll<HTMLInputElement>('form input')];
    type(pw, MOCK_PASSWORD);
    type(code, MOCK_TOTP_CODE);
    await click(button(root, 'Create new codes'));
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelectorAll('[data-testid="recovery-codes"] code')).toHaveLength(10));
    expect(button(root, 'Finish').disabled).toBe(true);
    input(root, 'input[type="checkbox"]').click();
    await until(() => expect(button(root, 'Finish').disabled).toBe(false));
    await click(button(root, 'Finish'));
    expect(root.querySelector('[data-testid="recovery-codes"]')).toBeNull();
    expect(state(root)).toBe('Active');
  });

  it('a recovery code is accepted as the second factor', async () => {
    const api = mock({ totpEnabled: true });
    const fresh = await api.regenerateRecoveryCodes({ password: MOCK_PASSWORD, code: MOCK_TOTP_CODE });
    const spy = vi.spyOn(api, 'regenerateRecoveryCodes');
    const root = render(TwoFactorCard, { api });
    await until(() => expect(state(root)).toBe('Active'));
    await click(button(root, 'New recovery codes'));
    const [pw, code] = [...root.querySelectorAll<HTMLInputElement>('form input')];
    type(pw, MOCK_PASSWORD);
    type(code, fresh[0]);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[data-testid="recovery-codes"]')).toBeTruthy());
    expect(spy).toHaveBeenCalledWith({ password: MOCK_PASSWORD, recoveryCode: fresh[0] });
  });

  it('warns when only a few codes are left (and when none are)', async () => {
    const api = mock({ totpEnabled: true });
    const real = await api.getAuthMe();
    const me = vi.spyOn(api, 'getAuthMe');
    me.mockResolvedValue({ ...real, recoveryCodesLeft: 2 });
    const root = render(TwoFactorCard, { api });
    await until(() => expect(state(root)).toBe('Active'));
    const line = root.querySelector('[data-testid="codes-left"]');
    expect(line?.classList.contains('low')).toBe(true);
    expect(text(line!)).toBe('2 recovery codes left Create new recovery codes soon.');
    cleanup();

    me.mockResolvedValue({ ...real, recoveryCodesLeft: 1 });
    const one = render(TwoFactorCard, { api });
    await until(() => expect(one.querySelector('[data-testid="codes-left"]')?.textContent).toContain('1 recovery code left'));
    cleanup();

    me.mockResolvedValue({ ...real, recoveryCodesLeft: 0 });
    const none = render(TwoFactorCard, { api });
    await until(() => expect(none.querySelector('[data-testid="codes-left"]')?.textContent).toContain('No recovery codes left'));
  });
});

describe('TwoFactorCard: loading problems', () => {
  it('shows the localized error and a retry button', async () => {
    const api = mock();
    vi.spyOn(api, 'getAuthMe').mockRejectedValueOnce(Object.assign(new Error('x'), { code: 'network', status: 0 }));
    const root = render(TwoFactorCard, { api });
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toContain('The server cannot be reached'));
    await click(button(root, 'Try again'));
    await ready(root);
  });
});
