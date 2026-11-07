// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { button, cleanup, input, render, submit, text, type, until } from '../security/mountUtil';
import type { AuthUser } from '../../api/security';
import { ApiError } from '../../api/api';

const m = vi.hoisted(() => ({
  toast: vi.fn(),
  openSettings: vi.fn(),
  signOutWeb: vi.fn(async () => {}),
  updateProfile: vi.fn(async (_: { displayName: string }) => {}),
  user: null as unknown,
}));

vi.mock('../../session', () => ({ signOutWeb: m.signOutWeb }));
vi.mock('../../state', () => ({
  app: { api: { kind: 'http' }, toast: m.toast, openSettings: m.openSettings },
  security: {
    get me() {
      return { user: m.user, mfaSetupRequired: false, recoveryCodesLeft: 0 };
    },
    refresh: async () => null,
    updateProfile: m.updateProfile,
  },
}));

import ProfileSettings from './ProfileSettings.svelte';

const user = (over: Partial<AuthUser> = {}): AuthUser => ({ id: 1, username: 'jonas', email: 'jonas@example.org', displayName: 'Jonas Becker', role: 'user', hasPassword: true, totpEnabled: false, oidc: false, ...over });
const form = (root: ParentNode) => root.querySelector('form') as HTMLFormElement;

beforeEach(() => {
  vi.clearAllMocks();
  m.user = user();
});
afterEach(cleanup);

describe('ProfileSettings', () => {
  it('saves the trimmed display name', async () => {
    const root = render(ProfileSettings, { part: 'profile' });
    const field = input(root, '#profile-name');
    expect(field.value).toBe('Jonas Becker');
    type(field, '  Jonas B.  ');
    await submit(form(root));
    await until(() => expect(m.updateProfile).toHaveBeenCalledWith({ displayName: 'Jonas B.' }));
    await until(() => expect(m.toast).toHaveBeenCalledTimes(1));
  });

  it('has nothing to save while the name is unchanged, and refuses an empty name', async () => {
    const root = render(ProfileSettings, { part: 'profile' });
    expect((root.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true);
    type(input(root, '#profile-name'), '   ');
    await submit(form(root));
    flushSync();
    expect(m.updateProfile).not.toHaveBeenCalled();
    expect(root.querySelector('[role="alert"]')).toBeTruthy();
    expect(input(root, '#profile-name').getAttribute('aria-invalid')).toBe('true');
  });

  it('shows a rejected name next to the field and keeps the input', async () => {
    m.updateProfile.mockRejectedValueOnce(new ApiError('display name contains characters', 400, 'invalid_input'));
    const root = render(ProfileSettings, { part: 'profile' });
    type(input(root, '#profile-name'), 'bad');
    await submit(form(root));
    await until(() => expect(root.querySelector('[role="alert"]')).toBeTruthy());
    expect(input(root, '#profile-name').value).toBe('bad');
    expect(root.querySelector('[role="alert"]')?.textContent).toMatch(/100/);
    expect(m.toast).not.toHaveBeenCalled();
  });

  it('username and e-mail address are shown, not editable', () => {
    const root = render(ProfileSettings, { part: 'profile' });
    expect(text(root)).toContain('jonas@example.org');
    expect(root.querySelectorAll('section#set-profile input')).toHaveLength(1);
  });

  it('accounts of an identity provider: nothing editable, with the reason', () => {
    m.user = user({ oidc: true, hasPassword: false });
    const root = render(ProfileSettings, { part: 'profile' });
    expect(root.querySelector('#set-profile input')).toBeNull();
    expect(root.querySelector('form')).toBeNull();
    expect(text(root)).toContain('Jonas Becker');
  });

  it('points to the security cards instead of repeating them, and offers sign-out', async () => {
    const root = render(ProfileSettings, { part: 'signin' });
    expect(root.querySelector('#set-password, #set-twofactor, #set-sessions')).toBeNull();
    button(root.querySelector('#set-profile-security')!, /passwor/i).click();
    expect(m.openSettings).toHaveBeenCalledWith('security', 'privacy');
    const session = render(ProfileSettings, { part: 'session' });
    (session.querySelector('#set-profile-session button') as HTMLButtonElement).click();
    await until(() => expect(m.signOutWeb).toHaveBeenCalledTimes(1));
  });
});
