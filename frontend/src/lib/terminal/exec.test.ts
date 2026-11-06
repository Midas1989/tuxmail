// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import type { Folder, MessageDetail } from '../types';
import { TermError, findFolder, runCommand, type TermHost } from './exec';

const folders = [
  { id: 1, accountId: 1, name: 'Posteingang', role: 'inbox', unread: 2, total: 3 },
  { id: 2, accountId: 1, name: 'Archiv', role: 'archive', unread: 0, total: 1 },
  { id: 3, accountId: 2, name: 'Posteingang', role: 'inbox', unread: 0, total: 0 },
  { id: 4, accountId: 2, name: 'Kunde', remoteId: 'Projekte.Kunde', delimiter: '.', unread: 0, total: 0 },
] as unknown as Folder[];

function host(over: Partial<TermHost> = {}) {
  const api = {
    listMessages: vi.fn(async () => ({ items: [{ id: 9, date: '2026-01-02T00:00:00Z', from: [{ email: 'a@x.test', name: 'B\u001b[31mob' }], subject: 'Hi\u0007' }], total: 1, offset: 0 })),
    setFlags: vi.fn(async () => {}),
    move: vi.fn(async () => {}),
    getMessage: vi.fn(async () => ({ id: 7, accountId: 1 })),
  };
  const h = {
    api,
    folders: { 1: folders.slice(0, 2), 2: folders.slice(2) },
    selectedMessageId: 7,
    message: { id: 7, accountId: 1 } as MessageDetail,
    select: vi.fn(async () => {}),
    selectMessage: vi.fn(async () => {}),
    openCompose: vi.fn(),
    replyInit: vi.fn(() => ({ mode: 'reply' as const })),
    setViewMode: vi.fn(async () => {}),
    ...over,
  };
  return h as unknown as TermHost & { api: typeof api } & typeof h;
}

describe('findFolder', () => {
  it('resolves id, path and last name', () => {
    expect(findFolder(folders, '2').id).toBe(2);
    expect(findFolder(folders, 'projekte/kunde').id).toBe(4);
    expect(findFolder(folders, 'KUNDE').id).toBe(4);
  });
  it('reports ambiguous and unknown names', () => {
    expect(() => findFolder(folders, 'inbox')).toThrow(TermError);
    expect(() => findFolder(folders, 'gibtsnicht')).toThrow(TermError);
  });
});

describe('runCommand', () => {
  it('search lists sanitized rows over all mail', async () => {
    const h = host();
    const out = await runCommand('search rechnung --limit 5', h);
    expect(h.api.listMessages).toHaveBeenCalledWith({ scope: { kind: 'all' }, search: 'rechnung', offset: 0, limit: 5 });
    expect(out).toEqual([{ kind: 'row', id: 9, text: '2026-01-02  Bob <a@x.test>  Hi' }]);
  });
  it('mark acts on the selection or on given ids', async () => {
    const h = host();
    await runCommand('mark read', h);
    expect(h.api.setFlags).toHaveBeenLastCalledWith([7], ['\\Seen'], []);
    await runCommand('mark 1,2 unflagged', h);
    expect(h.api.setFlags).toHaveBeenLastCalledWith([1, 2], [], ['\\Flagged']);
  });
  it('move resolves the folder in the account of the message', async () => {
    const h = host();
    await runCommand('move archive', h);
    expect(h.api.move).toHaveBeenCalledWith([7], 2);
    expect(h.selectMessage).toHaveBeenCalledWith(null);
  });
  it('without selection a target is required', async () => {
    await expect(runCommand('mark read', host({ selectedMessageId: null }))).rejects.toThrow(TermError);
  });
  it('send and reply only open the composer', async () => {
    const h = host();
    await runCommand('send --to "Max <m@x.test>",b@x.test --subject Hallo --text "a<b"', h);
    expect(h.openCompose).toHaveBeenCalledWith({
      mode: 'new',
      identityId: undefined,
      to: [{ name: 'Max', email: 'm@x.test' }, { email: 'b@x.test' }],
      cc: [],
      subject: 'Hallo',
      bodyHtml: 'a&lt;b',
    });
    await runCommand('reply --all', h);
    expect(h.replyInit).toHaveBeenCalledWith(h.message, 'replyAll', '');
  });
  it('list selects folders and unified lists', async () => {
    const h = host();
    await runCommand('list', h);
    expect(h.select).toHaveBeenLastCalledWith({ kind: 'unified', role: 'inbox' });
    await runCommand('list unread', h);
    expect(h.select).toHaveBeenLastCalledWith({ kind: 'unified', role: 'unread' });
    await runCommand('list Kunde', h);
    expect(h.select).toHaveBeenLastCalledWith({ kind: 'folder', accountId: 2, folderId: 4 });
  });
  it('has no way to run anything but the mail commands', async () => {
    await expect(runCommand('rm -rf /', host())).rejects.toThrow();
    await expect(runCommand('!ls', host())).rejects.toThrow();
  });
});
