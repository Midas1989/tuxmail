// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import { MockTransport } from './mock';
import { ApiError } from './api';

describe('mock OAuth / cloud accounts', () => {
  it('signs in with Microsoft and creates the account', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    api.cloud.flowDelayMs = 5;
    const d = await api.discoverAccount('anna@outlook.com', '');
    expect(d.oauthProvider).toBe('microsoft');
    const before = (await api.listAccounts()).length;
    const flow = await api.startOAuth({ provider: 'microsoft', email: 'anna@outlook.com', kind: 'graph' });
    expect(flow.status).toBe('pending');
    await new Promise((r) => setTimeout(r, 30));
    const done = await api.getOAuthFlow(flow.id);
    expect(done.status).toBe('done');
    const accs = await api.listAccounts();
    expect(accs.length).toBe(before + 1);
    expect(accs.find((a) => a.id === done.accountId)?.kind).toBe('graph');
  });

  it('reports a missing Google client ID until an own one is saved', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    await expect(api.startOAuth({ provider: 'google', email: 'x@gmail.com', kind: 'gmail' })).rejects.toMatchObject({ code: 'oauth_not_configured' });
    await api.saveOAuthClient({ provider: 'google', clientId: 'own.apps.googleusercontent.com', clientSecret: 's' });
    const ps = await api.listOAuthProviders();
    const g = ps.find((p) => p.id === 'google')!;
    expect(g.configured).toBe(true);
    expect(g.source).toBe('user');
    expect(g.userClient?.hasSecret).toBe(true);
    expect(g.userClient?.clientSecret).toBeUndefined();
    const f = await api.startOAuth({ provider: 'google', email: 'x@gmail.com', kind: 'gmail' });
    expect(f.id).toBeTruthy();
  });

  it('adds a shared mailbox with a Send-on-Behalf identity', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const owner = (await api.listAccounts())[0];
    const acc = await api.addSharedMailbox({ accountId: owner.id, address: 'info@firma.example', sendMode: 'send_on_behalf' });
    expect(acc.extra?.mailbox).toBe('info@firma.example');
    const dels = await api.listIdentityDelegations();
    expect(dels).toHaveLength(1);
    expect(dels[0].mode).toBe('send_on_behalf');
    await api.setIdentityDelegation({ identityId: dels[0].identityId, mode: 'send_as' });
    expect((await api.listIdentityDelegations())[0].mode).toBe('send_as');
    await expect(api.addSharedMailbox({ accountId: owner.id, address: 'nope' })).rejects.toBeInstanceOf(ApiError);
  });
});
