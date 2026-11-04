// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { MockTransport } from './mock';
import { ApiError } from './api';

function mock() {
  return new MockTransport({ latencyMs: 0, now: new Date('2026-11-04T10:00:00Z') });
}

describe('account edit (mock transport)', () => {
  it('reads and updates the settings of an account', async () => {
    const api = mock();
    const s = await api.getAccountSettings(1);
    expect(s.email).toBe('jonas.becker@posteo.de');
    expect(s.tests).toEqual(['imap', 'smtp', 'sieve']);
    expect(s.bodyPolicy).toBe('recent');
    const events: string[] = [];
    api.subscribe((e) => events.push(e.type));
    const u = await api.updateAccount(1, { displayName: 'Posteo', color: '#112233', bodyPolicy: 'all', pollMinutes: 10, idleMode: 'custom', idleFolders: [3, 1, 3] });
    expect(u.account.displayName).toBe('Posteo');
    expect(u.idleFolders).toEqual([1, 3]);
    expect((await api.listAccounts()).find((a) => a.id === 1)?.color).toBe('#112233');
    expect(events).toContain('account.updated');
    await expect(api.updateAccount(1, { color: 'red' })).rejects.toMatchObject({ code: 'invalid_input' });
    await expect(api.updateAccount(1, { incoming: { host: '', port: 993, tls: 'tls', auth: 'password', username: '' } })).rejects.toBeInstanceOf(ApiError);
  });

  it('tests connections and refuses broken settings when verifying', async () => {
    const api = mock();
    const ok = await api.testAccountConnection(1, {});
    expect(ok.ok).toBe(true);
    const bad = await api.testAccountConnection(1, { password: 'wrong', kinds: ['imap'] });
    expect(bad.steps[0]).toMatchObject({ kind: 'imap', ok: false, code: 'auth_failed' });
    const acc = (await api.listAccounts()).find((a) => a.id === 1)!;
    await expect(api.updateAccount(1, { incoming: { ...acc.incoming!, host: 'invalid.example' }, verify: true })).rejects.toMatchObject({ code: 'connection_failed' });
    expect((await api.getAccountSettings(1)).account.incoming?.host).toBe('posteo.de');
    // The demo Exchange account starts with a login error; a new password fixes it.
    expect((await api.getAccountSettings(2)).status?.state).toBe('error');
    await expect(api.changeAccountPassword(2, 'wrong', true)).rejects.toMatchObject({ code: 'connection_failed' });
    expect((await api.changeAccountPassword(2, 'richtig', true)).status?.state).toBe('online');
  });

  it('manages identities and deletes accounts', async () => {
    const api = mock();
    const before = (await api.listIdentities()).filter((i) => i.accountId === 1);
    const alias = await api.createIdentity({ id: 0, accountId: 1, name: 'Jonas', email: 'info@becker.example', bccSelf: false, isDefault: false });
    await api.setDefaultIdentity(alias.id);
    let ids = (await api.listIdentities()).filter((i) => i.accountId === 1);
    expect(ids.find((i) => i.isDefault)?.id).toBe(alias.id);
    await api.deleteIdentity(alias.id);
    ids = (await api.listIdentities()).filter((i) => i.accountId === 1);
    expect(ids).toHaveLength(before.length);
    expect(ids.some((i) => i.isDefault)).toBe(true);

    const diag = await api.accountDiagnostics(1);
    expect(diag.account.secretRef).toBeUndefined();
    expect(diag.folders.length).toBeGreaterThan(0);

    await api.deleteAccount(1, { keepData: true });
    expect((await api.listAccounts()).find((a) => a.id === 1)?.kind).toBe('local');
    await api.deleteAccount(1, { keepData: false });
    expect((await api.listAccounts()).some((a) => a.id === 1)).toBe(false);
  });
});
