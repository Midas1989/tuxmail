// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import OfflineSettings from './OfflineSettings.svelte';
import { connection } from '../state/connection.svelte';
import { DEFAULT_SETTINGS } from './offline/rules';
import { button, cleanup, click, render, until } from '../components/security/mountUtil';
import en from '$locales/en.json';
import type { OfflineController } from './status';

afterEach(() => {
  cleanup();
  connection.status = { connected: false, state: 'none', cacheBytes: 0, outbox: [], settingsLog: [] };
});

function ctl(over: Partial<OfflineController> = {}): OfflineController {
  return {
    settings: () => DEFAULT_SETTINGS,
    save: vi.fn(async () => {}),
    syncNow: vi.fn(async () => {}),
    clear: vi.fn(async () => {}),
    foreign: () => [{ name: 'tuxmail-offline-p0', profileId: 'p0', account: 'anna@mail.example.org', mails: 3, actions: 1 }],
    discardForeign: vi.fn(async () => {}),
    lockEnabled: async () => false,
    ...over,
  };
}

describe('Offline settings', () => {
  it("another account's waiting items: only how many and whose; deleting needs a second tap", async () => {
    const c = ctl();
    // The waiting mail of the other account must never show up (it is not in this user's status anyway).
    connection.status = { connected: true, state: 'online', cacheBytes: 0, settingsLog: [], outbox: [{ id: 'x', kind: 'send', title: 'Geheim', recipients: 'ben@x', state: 'pending', createdAt: '' }] };
    const root = render(OfflineSettings, { ctl: c });
    const box = root.querySelector('[data-testid="foreign-waiting"]') as HTMLElement;
    expect(box.textContent).toContain('3 unsent mails of anna@mail.example.org');
    expect(box.textContent).not.toContain('Geheim');
    expect(box.textContent).not.toContain('ben@x');
    await click(button(box, en.native.offline.foreignDiscard));
    expect(c.discardForeign).not.toHaveBeenCalled();
    expect(box.textContent).toContain(en.native.offline.foreignConfirm);
    await click(button(box, en.native.offline.foreignDiscard));
    await until(() => expect(c.discardForeign).toHaveBeenCalledWith('tuxmail-offline-p0'));
  });

  it('recommends the app lock while it is off', async () => {
    const root = render(OfflineSettings, { ctl: ctl({ foreign: () => [] }) });
    await until(() => expect(root.querySelector('[data-testid="lock-recommended"]')).not.toBeNull());
  });
});
