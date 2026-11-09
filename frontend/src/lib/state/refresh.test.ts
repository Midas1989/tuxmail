// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
// "Aktualisieren" and the automatic refresh: the automatic one only reads the
// list (and the sync status), never message contents or the folders of every
// account; the manual one asks the server to sync every account.
import { describe, expect, it, vi } from 'vitest';
import { AppState } from './app.svelte';
import { MockTransport } from '../api/mock';

async function setup() {
  const api = new MockTransport({ latencyMs: 0 });
  const app = new AppState(api);
  await app.init();
  const spies = {
    list: vi.spyOn(api, 'listMessages'),
    conv: vi.spyOn(api, 'listConversations'),
    get: vi.spyOn(api, 'getMessage'),
    folders: vi.spyOn(api, 'listFolders'),
    accounts: vi.spyOn(api, 'listAccounts'),
    sync: vi.spyOn(api, 'syncNow'),
  };
  return { app, api, spies };
}

describe('refreshing the mail list', () => {
  it('the light refresh reads only the list: no contents, folders or accounts', async () => {
    const { app, spies } = await setup();
    await app.refreshQuietly();
    expect(spies.list.mock.calls.length + spies.conv.mock.calls.length).toBe(1);
    expect(spies.get).not.toHaveBeenCalled();
    expect(spies.folders).not.toHaveBeenCalled();
    expect(spies.accounts).not.toHaveBeenCalled();
    expect(spies.sync).not.toHaveBeenCalled();
    app.destroy();
  });

  it('the light refresh rejects when the list cannot be read (counts for the backoff)', async () => {
    const { app, spies } = await setup();
    spies.list.mockRejectedValue(new Error('offline'));
    spies.conv.mockRejectedValue(new Error('offline'));
    await expect(app.refreshQuietly()).rejects.toThrow('offline');
    app.destroy();
  });

  it('"Aktualisieren" syncs every mail account once, even when tapped twice', async () => {
    const { app, spies } = await setup();
    const a = app.refreshNow();
    const b = app.refreshNow();
    expect(app.refreshing).toBe(true);
    await Promise.all([a, b]);
    expect(app.refreshing).toBe(false);
    expect(spies.sync).toHaveBeenCalledTimes(app.mailAccounts.length);
    app.destroy();
  });
});
