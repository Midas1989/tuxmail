// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { applyOverrides, badgeOf, folderOptions, menuRolesFor, missingRoles, roleEntries } from './folderconfig';
import { MockTransport } from './api/mock';
import type { Folder, FolderConfig } from './types';

const f = (id: number, name: string, extra: Partial<Folder> = {}): Folder => ({ id, accountId: 1, remoteId: name, name, subscribed: true, unread: 0, total: 0, ...extra });

const folders: Folder[] = [
  f(1, 'INBOX', { role: 'inbox', roleSource: 'server' }),
  f(2, 'Sent', { role: 'sent', roleSource: 'name' }),
  f(3, 'Ausgang'),
  f(4, 'Trash', { role: 'trash', roleSource: 'server' }),
  f(5, '2026', { parentId: 3 }),
];
const server = { inbox: { folderId: 1, source: 'server' as const }, sent: { folderId: 2, source: 'name' as const }, trash: { folderId: 4, source: 'server' as const } };

describe('folder config logic', () => {
  it('applies overrides: one folder per role, server role removed', () => {
    const out = applyOverrides(folders, server, { sent: 3 });
    expect(out.find((x) => x.id === 3)).toMatchObject({ role: 'sent', roleSource: 'user' });
    expect(out.find((x) => x.id === 2)?.role).toBeUndefined();
    expect(out.filter((x) => x.role === 'sent')).toHaveLength(1);
    expect(out.find((x) => x.id === 4)).toMatchObject({ role: 'trash', roleSource: 'server' });
  });

  it('ignores unresolved overrides (folder missing) and reports them', () => {
    const out = applyOverrides(folders, server, { sent: 99 });
    expect(out.find((x) => x.id === 2)).toMatchObject({ role: 'sent', roleSource: 'name' });
    const sent = roleEntries(out, server, { sent: 99 }).find((e) => e.role === 'sent')!;
    expect(sent).toMatchObject({ folderId: 2, overridden: true, unresolved: true });
    expect(badgeOf(sent)).toBe('unresolved');
  });

  it('badges and missing roles', () => {
    const entries = roleEntries(applyOverrides(folders, server, { archive: 3 }), server, { archive: 3 });
    const by = Object.fromEntries(entries.map((e) => [e.role, e]));
    expect(badgeOf(by.sent)).toBe('name');
    expect(badgeOf(by.trash)).toBe('server');
    expect(badgeOf(by.archive)).toBe('user');
    expect(badgeOf(by.drafts)).toBe('none');
    const cfg = { roles: entries } as FolderConfig;
    expect(missingRoles(cfg)).toEqual(['drafts', 'junk', 'templates']);
  });

  it('lists folders in tree order with paths', () => {
    expect(folderOptions(folders).map((o) => [o.label, o.depth])).toEqual([
      ['INBOX', 0],
      ['Ausgang', 0],
      ['Ausgang/2026', 1],
      ['Sent', 0],
      ['Trash', 0],
    ]);
  });

  it('context menu never reassigns the inbox and skips the current role', () => {
    expect(menuRolesFor(folders[0])).toEqual([]);
    expect(menuRolesFor(folders[1])).not.toContain('sent');
    expect(menuRolesFor(folders[2])).toContain('sent');
  });
});

describe('mock transport folder config', () => {
  it('overrides, resets, creates folders and saves behaviour', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const acc = (await api.listAccounts()).find((a) => a.kind === 'imap')!;
    const fs = await api.listFolders(acc.id);
    let cfg = await api.getFolderConfig(acc.id);
    const sent = cfg.roles.find((r) => r.role === 'sent')!;
    expect(sent.overridden).toBe(false);
    const other = fs.find((x) => !x.role)!;
    cfg = await api.setFolderRole(acc.id, 'sent', other.id);
    expect(cfg.roles.find((r) => r.role === 'sent')).toMatchObject({ folderId: other.id, source: 'user', overridden: true });
    expect((await api.listFolders(acc.id)).filter((x) => x.role === 'sent').map((x) => x.id)).toEqual([other.id]);
    cfg = await api.resetFolderRoles(acc.id);
    expect(cfg.roles.find((r) => r.role === 'sent')).toMatchObject({ folderId: sent.folderId, overridden: false });
    cfg = await api.createRoleFolder(acc.id, 'templates', 'Vorlagen');
    expect(cfg.roles.find((r) => r.role === 'templates')?.folderId).toBeGreaterThan(0);
    cfg = await api.saveFolderBehaviour(acc.id, { ...cfg.behaviour, saveSentAuto: false, saveSent: false, trashMode: 'mark', archiveMode: 'yearly' });
    expect(cfg.behaviour).toMatchObject({ saveSent: false, saveSentAuto: false, trashMode: 'mark', archiveMode: 'yearly' });
    const ident = cfg.identities[0];
    if (ident) {
      cfg = await api.setIdentityFolders(ident.identityId, other.id, 0);
      expect(cfg.identities[0]).toMatchObject({ sentFolderId: other.id });
    }
  });
});
