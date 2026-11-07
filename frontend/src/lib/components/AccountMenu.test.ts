// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { button, cleanup, click, hasButton, render, text, until } from './security/mountUtil';

const m = vi.hoisted(() => ({ web: true, openSettings: vi.fn(), toast: vi.fn(), signOutWeb: vi.fn(async () => {}) }));

vi.mock('../session', () => ({ signOutWeb: m.signOutWeb }));
vi.mock('../state', () => {
  const api = { kind: 'http' };
  return {
    app: { api, openSettings: m.openSettings, toast: m.toast },
    security: {
      get available() {
        return m.web;
      },
      me: { user: { id: 1, username: 'jonas', email: 'jonas@example.org', displayName: 'Jonas Becker', role: 'user', hasPassword: true, totpEnabled: false, oidc: false }, mfaSetupRequired: false, recoveryCodesLeft: 0 },
    },
  };
});

import AccountMenu from './AccountMenu.svelte';
import { app } from '../state';

const trigger = (root: ParentNode) => root.querySelector<HTMLButtonElement>('[data-testid="account-menu"]');

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe('AccountMenu', () => {
  it('web client: shows who is signed in with "Edit profile" and "Sign out"', async () => {
    m.web = true;
    const root = render(AccountMenu, {});
    expect(trigger(root)?.getAttribute('aria-expanded')).toBe('false');
    await click(trigger(root)!);
    const menu = await vi.waitFor(() => {
      const el = document.querySelector<HTMLElement>('[role="menu"]');
      expect(el).toBeTruthy();
      return el!;
    });
    expect(text(menu)).toContain('Jonas Becker');
    expect(text(menu)).toContain('jonas@example.org');
    expect([...menu.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent?.trim()).length).toBe(2);
  });

  it('desktop shell, demo and native app: nothing is rendered', () => {
    m.web = false;
    const root = render(AccountMenu, {});
    expect(trigger(root)).toBeNull();
    expect(root.querySelector('button')).toBeNull();
  });

  it('sign-out calls signOutWeb with the application API', async () => {
    m.web = true;
    const root = render(AccountMenu, {});
    await click(trigger(root)!);
    await until(() => expect(hasButton(document.body, /abmelden|sign out/i)).toBe(true));
    await click(button(document.body, /abmelden|sign out/i));
    await until(() => expect(m.signOutWeb).toHaveBeenCalledTimes(1));
    expect(m.signOutWeb).toHaveBeenCalledWith({ api: app.api });
    expect(m.toast).not.toHaveBeenCalled();
  });

  it('tells the user when the server could not be reached on sign-out', async () => {
    m.web = true;
    m.signOutWeb.mockRejectedValueOnce(new Error('offline'));
    const root = render(AccountMenu, {});
    await click(trigger(root)!);
    await click(button(document.body, /abmelden|sign out/i));
    await until(() => expect(m.toast).toHaveBeenCalledTimes(1));
    expect(String(m.toast.mock.calls[0][0])).toContain('offline');
  });

  it('"Edit profile" opens the settings page', async () => {
    m.web = true;
    const root = render(AccountMenu, {});
    await click(trigger(root)!);
    await click(button(document.body, /profil bearbeiten|edit profile/i));
    await until(() => expect(m.openSettings).toHaveBeenCalledWith('profile'));
  });
});
