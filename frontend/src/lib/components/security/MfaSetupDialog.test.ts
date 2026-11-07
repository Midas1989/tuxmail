// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MockTransport } from '../../api/mock';
import { MOCK_TOTP_CODE } from '../../api/mockSecurity';
import MfaSetupDialog from './MfaSetupDialog.svelte';
import { button, cleanup, click, hasButton, input, render, stepUp, submit, text, type, until } from './mountUtil';

afterEach(cleanup);

const mock = () => new MockTransport({ latencyMs: 0, security: { admin: true } });

describe('MfaSetupDialog (forced set-up for administrators)', () => {
  it('is a blocking dialog: no close button, Escape and backdrop clicks do nothing, only "Sign out" as alternative', async () => {
    const root = render(MfaSetupDialog, { api: mock(), onfinished: () => {}, onsignout: async () => {} });
    const dialog = root.querySelector('[role="dialog"]');
    expect(dialog).toBeTruthy();
    expect(dialog?.getAttribute('aria-modal')).toBe('true');
    expect(root.querySelector('h2')?.textContent).toBe('Set up two-factor authentication');
    expect(text(root)).toContain('Your account is an administrator account');
    expect(root.querySelector('button[aria-label="Close"]')).toBeNull();

    (dialog as HTMLElement).dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    root.querySelector('.backdrop')?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    expect(root.querySelector('[role="dialog"]')).toBeTruthy();
    expect(hasButton(root, 'Sign out')).toBe(true);
    // every other button belongs to the set-up itself
    await stepUp(root);
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
  });

  it('sign out ends the session (the injected sign-out)', async () => {
    const onsignout = vi.fn(async () => {});
    const root = render(MfaSetupDialog, { api: mock(), onfinished: () => {}, onsignout });
    await click(button(root, 'Sign out'));
    expect(onsignout).toHaveBeenCalledTimes(1);
  });

  it('a failing sign-out is reported', async () => {
    const root = render(MfaSetupDialog, { api: mock(), onfinished: () => {}, onsignout: async () => Promise.reject(new Error('offline')) });
    await click(button(root, 'Sign out'));
    await until(() => expect(root.querySelector('[role="alert"]')?.textContent).toContain('Sign-out failed: offline'));
  });

  it('the set-up runs inside the dialog and calls onfinished only after the recovery codes were confirmed', async () => {
    const onfinished = vi.fn();
    const api = mock();
    const root = render(MfaSetupDialog, { api, onfinished, onsignout: async () => {} });
    await stepUp(root);
    await until(() => expect(root.querySelector('[data-step="scan"]')).toBeTruthy());
    await click(button(root, 'Next'));
    type(input(root, 'input[inputmode="numeric"]'), MOCK_TOTP_CODE);
    await submit(root.querySelector('form') as HTMLFormElement);
    await until(() => expect(root.querySelector('[data-step="codes"]')).toBeTruthy());
    expect((await api.getAuthMe()).mfaSetupRequired).toBe(false);
    expect(onfinished).not.toHaveBeenCalled();
    input(root, 'input[type="checkbox"]').click();
    await until(() => expect(button(root, 'Finish').disabled).toBe(false));
    await click(button(root, 'Finish'));
    expect(onfinished).toHaveBeenCalledTimes(1);
  });
});
