// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/api';
import { MockTransport } from '../../api/mock';
import { MOCK_TOTP_CODE } from '../../api/mockSecurity';
import TotpEnrollment from './TotpEnrollment.svelte';
import { button, cleanup, click, hasButton, input, render, stepUp, submit, text, type, until } from './mountUtil';

beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn(async () => undefined) } });
});
afterEach(cleanup);

const mock = () => new MockTransport({ latencyMs: 0 });

async function toVerifyStep(root: HTMLElement) {
  await stepUp(root);
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
  await click(button(root, 'Next'));
  expect(root.querySelector('[data-step="verify"]')).toBeTruthy();
}

describe('TotpEnrollment: QR code -> first code -> recovery codes', () => {
  it('asks for the password first and sends it; a wrong one keeps the question', async () => {
    const api = mock();
    const setup = vi.spyOn(api, 'totpSetup').mockRejectedValueOnce(new ApiError('wrong', 401, 'invalid_credentials'));
    const root = render(TotpEnrollment, { api, onfinished: () => {} });
    await stepUp(root, 'falsch');
    await until(() => expect(root.querySelector('[role="alert"]')).toBeTruthy());
    expect(root.querySelector('[data-step="reauth"]')).toBeTruthy();
    expect(setup).toHaveBeenLastCalledWith(expect.objectContaining({ password: 'falsch' }));
    await stepUp(root, 'richtig');
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
    expect(setup).toHaveBeenLastCalledWith(expect.objectContaining({ password: 'richtig' }));
  });

  it('the password field never disappears: lock, network error and "nothing entered" all stay at the field (t-101)', async () => {
    const api = mock();
    const setup = vi
      .spyOn(api, 'totpSetup')
      .mockRejectedValueOnce(new ApiError('x', 429, 'throttled', 240))
      .mockRejectedValueOnce(new ApiError('x', 0, 'network'))
      .mockRejectedValueOnce(new ApiError('x', 403, 'reauth_required'));
    const root = render(TotpEnrollment, { api, onfinished: () => {} });
    await until(() => expect(root.querySelector('[data-step="reauth"]')).toBeTruthy());
    expect(text(root)).toContain('Confirm with your TuxMail sign-in password.');
    for (const [i, wanted] of ['locked', 'cannot be reached', ''].entries()) {
      await stepUp(root, 'irgendwas');
      await until(() => expect(setup).toHaveBeenCalledTimes(i + 1));
      await until(() => expect(root.querySelector('[data-step="reauth"]')).toBeTruthy());
      expect(root.querySelector('input[type="password"]')).toBeTruthy();
      expect(hasButton(root, 'Try again')).toBe(false);
      expect(root.querySelector('[role="alert"]')?.textContent ?? '').toContain(wanted);
    }
  });

  it('no request leaves before a password was typed: opening the form is not an attempt', async () => {
    const api = mock();
    const setup = vi.spyOn(api, 'totpSetup');
    const root = render(TotpEnrollment, { api, onfinished: () => {} });
    await until(() => expect(root.querySelector('[data-step="reauth"]')).toBeTruthy());
    expect(setup).not.toHaveBeenCalled();
    expect(button(root, 'Continue').disabled).toBe(true);
  });

  it('an account without a password goes straight on: the server decides by the recent sign-in', async () => {
    const api = new MockTransport({ latencyMs: 0, security: { hasPassword: false } });
    const root = render(TotpEnrollment, { api, onfinished: () => {} });
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
  });

  it('step 1 shows the QR code and the secret for manual entry, in groups of four', async () => {
    const root = render(TotpEnrollment, { api: mock(), onfinished: () => {} });
    expect(text(root)).toContain('Step 1 of 3');
    await stepUp(root);
    await until(() => expect(root.querySelector('svg[role="img"]')).toBeTruthy());
    expect(root.querySelector('svg')?.getAttribute('aria-label')).toBe('QR code for your authenticator app');
    expect(root.querySelector('svg path')?.getAttribute('d')?.length).toBeGreaterThan(100);
    expect(root.querySelector('[data-testid="totp-secret"]')?.textContent).toBe('JBSW Y3DP EHPK 3PXP JBSW Y3DP EHPK 3PXP');
    await click(button(root, 'Copy key'));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP');
  });

  it('step 2: only digits, needs 6 of them; a wrong code is reported in plain words and cleared', async () => {
    const root = render(TotpEnrollment, { api: mock(), onfinished: () => {} });
    await toVerifyStep(root);
    expect(text(root)).toContain('Step 2 of 3');
    const field = input(root, 'input[inputmode="numeric"]');
    const verify = button(root, 'Verify and enable');
    expect(verify.disabled).toBe(true);
    type(field, '12ab345');
    expect(field.value).toBe('12345');
    expect(verify.disabled).toBe(true);
    type(field, '000000');
    expect(verify.disabled).toBe(false);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toContain('This code is not correct or was already used'));
    expect(field.value).toBe('');
    expect(root.querySelector('[data-step="codes"]')).toBeNull();
  });

  it('step 3: the codes are shown once; finish needs the confirmation "I have stored them"', async () => {
    const onfinished = vi.fn();
    const api = mock();
    const confirm = vi.spyOn(api, 'totpConfirm');
    const root = render(TotpEnrollment, { api, onfinished });
    await toVerifyStep(root);
    type(input(root, 'input[inputmode="numeric"]'), MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[data-step="codes"]')).toBeTruthy());
    expect(confirm).toHaveBeenCalledWith(MOCK_TOTP_CODE);
    expect(text(root)).toContain('Step 3 of 3');
    const codes = [...root.querySelectorAll('[data-testid="recovery-codes"] code')].map((c) => c.textContent);
    expect(codes).toHaveLength(10);
    expect(codes.every((c) => /^[a-z2-9]{5}-[a-z2-9]{5}$/.test(c ?? ''))).toBe(true);

    const finish = button(root, 'Finish');
    expect(finish.disabled).toBe(true);
    await click(finish);
    expect(onfinished).not.toHaveBeenCalled();

    const checkbox = input(root, 'input[type="checkbox"]');
    checkbox.click();
    await until(() => expect(button(root, 'Finish').disabled).toBe(false));
    await click(button(root, 'Finish'));
    expect(onfinished).toHaveBeenCalledTimes(1);
  });

  it('copy and download of the recovery codes', async () => {
    const api = mock();
    const root = render(TotpEnrollment, { api, onfinished: () => {} });
    await toVerifyStep(root);
    type(input(root, 'input[inputmode="numeric"]'), MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[data-step="codes"]')).toBeTruthy());
    const codes = [...root.querySelectorAll('[data-testid="recovery-codes"] code')].map((c) => c.textContent);

    await click(button(root, 'Copy codes'));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(codes.join('\n'));
    await until(() => expect(hasButton(root, 'Copied')).toBe(true));

    let blob: Blob | undefined;
    URL.createObjectURL = vi.fn((b: Blob | MediaSource) => ((blob = b as Blob), 'blob:x'));
    URL.revokeObjectURL = vi.fn();
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    await click(button(root, 'Download as file'));
    expect(clicked).toHaveBeenCalledTimes(1);
    const body = await blob!.text();
    expect(body).toContain('TuxMail recovery codes');
    expect(body).toContain(`01. ${codes[0]}`);
    clicked.mockRestore();
  });

  it('a clipboard failure is reported without losing the codes', async () => {
    Object.assign(navigator, { clipboard: { writeText: vi.fn(async () => Promise.reject(new Error('denied'))) } });
    const root = render(TotpEnrollment, { api: mock(), onfinished: () => {} });
    await toVerifyStep(root);
    type(input(root, 'input[inputmode="numeric"]'), MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[data-step="codes"]')).toBeTruthy());
    await click(button(root, 'Copy codes'));
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toContain('Copying failed'));
    expect(root.querySelectorAll('[data-testid="recovery-codes"] code')).toHaveLength(10);
  });

  it('back from step 2 keeps the same secret; cancel is offered before the codes only', async () => {
    const oncancel = vi.fn();
    const api = mock();
    const setup = vi.spyOn(api, 'totpSetup');
    const root = render(TotpEnrollment, { api, onfinished: () => {}, oncancel });
    await toVerifyStep(root);
    await click(button(root, 'Back'));
    expect(root.querySelector('[data-step="scan"]')).toBeTruthy();
    expect(setup).toHaveBeenCalledTimes(1);
    await click(button(root, 'Cancel'));
    expect(oncancel).toHaveBeenCalledTimes(1);

    await click(button(root, 'Next'));
    type(input(root, 'input[inputmode="numeric"]'), MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[data-step="codes"]')).toBeTruthy());
    expect(hasButton(root, 'Cancel')).toBe(false);
  });

  it('a failing set-up request shows the localized error at the password field and can be sent again', async () => {
    const api = mock();
    const setup = vi.spyOn(api, 'totpSetup').mockRejectedValueOnce(new ApiError('two-factor authentication is already active', 409, 'exists'));
    const root = render(TotpEnrollment, { api, onfinished: () => {} });
    await stepUp(root);
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toBe('Two-factor authentication is already active.'));
    expect(root.querySelector('[data-step="reauth"]')).toBeTruthy();
    setup.mockRestore();
    await stepUp(root);
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
  });
});
