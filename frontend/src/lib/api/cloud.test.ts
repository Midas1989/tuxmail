// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { MockTransport } from './mock';
import { HttpTransport } from './http';
import { WailsTransport, type WailsWindow } from './wails';
import { mockPassword } from './mockCloud';
import { attachmentSource, breadcrumbs, joinPath, parentPath } from '../state/cloud.svelte';
import { daysLeft, filterShares } from '../components/cloud/CloudSharesList.svelte';
import { endOfDay } from '../components/cloud/CloudShareDialog.svelte';
import type { CloudShare } from '../types';

const api = () => new MockTransport({ latencyMs: 0, autoReply: false });

describe('cloud helpers', () => {
  it('builds breadcrumbs and joins paths', () => {
    expect(breadcrumbs('/Projekte/Relaunch').map((c) => c.path)).toEqual(['/', '/Projekte', '/Projekte/Relaunch']);
    expect(breadcrumbs('/')).toHaveLength(1);
    expect(joinPath('/a/', '/b')).toBe('/a/b');
    expect(joinPath('/', 'x.pdf')).toBe('/x.pdf');
    expect(parentPath('/a/b/c.pdf')).toBe('/a/b');
    expect(parentPath('/a')).toBe('/');
  });

  it('finds the source of stored attachments', () => {
    expect(attachmentSource({ id: 0, filename: 'a.pdf', mimeType: 'application/pdf', size: 3, url: '/api/messages/12/attachments/1' })).toEqual({
      filename: 'a.pdf',
      size: 3,
      messageId: 12,
      index: 1,
    });
    expect(attachmentSource({ id: 0, filename: 'b', mimeType: 'x', size: 1 })).toBeNull();
  });

  it('filters shares and computes the days left', () => {
    const now = Date.parse('2026-11-03T12:00:00Z');
    const s = (over: Partial<CloudShare>): CloudShare => ({ id: 1, accountId: 1, provider: 'nextcloud', fileName: 'x', url: 'u', hasPassword: false, createdAt: '', active: true, source: 'mail', ...over });
    const list = [s({ id: 1 }), s({ id: 2, active: false, revokedAt: '2026-11-01T00:00:00Z' })];
    expect(filterShares(list, 'active').map((x) => x.id)).toEqual([1]);
    expect(filterShares(list, 'all')).toHaveLength(2);
    expect(daysLeft(s({ expiresAt: '2026-11-05T12:00:00Z' }), now)).toBe(2);
    expect(daysLeft(s({}), now)).toBeNull();
    expect(new Date(endOfDay('2026-11-17')).getDate()).toBe(17);
  });

  it('generates policy-compliant passwords', () => {
    for (let i = 0; i < 20; i++) {
      const p = mockPassword();
      expect(p).toHaveLength(16);
      expect(p).toMatch(/[a-z]/);
      expect(p).toMatch(/[A-Z]/);
      expect(p).toMatch(/[2-9]/);
      expect(p).toMatch(/[#+\-_!?%=@*]/);
    }
  });
});

describe('MockTransport cloud', () => {
  it('lists demo accounts, browses, uploads with unique names and moves', async () => {
    const a = api();
    const accs = await a.listCloudAccounts();
    expect(accs.map((x) => x.provider)).toEqual(['nextcloud', 'opencloud', 'vaultwarden']);
    const root = await a.listCloudFiles(901, '/');
    expect(root.entries[0].dir).toBe(true);
    expect(root.entries.at(-1)?.name).toBe('Notizen.md');
    const e1 = await a.uploadCloudFile(901, '/Projekte/Relaunch', new Blob(['x']), 'Plan.pdf');
    const e2 = await a.uploadCloudFile(901, '/Projekte/Relaunch', new Blob(['y']), 'Plan.pdf');
    expect(e1.path).toBe('/Projekte/Relaunch/Plan.pdf');
    expect(e2.path).toBe('/Projekte/Relaunch/Plan (2).pdf');
    await a.createCloudFolder(901, '/Neu/Unterordner');
    await a.moveCloudFile(901, e2.path, '/Neu/Plan.pdf');
    expect((await a.listCloudFiles(901, '/Neu')).entries.map((x) => x.name)).toEqual(['Unterordner', 'Plan.pdf']);
    await a.deleteCloudFile(901, '/Neu');
    await expect(a.listCloudFiles(901, '/Neu')).rejects.toMatchObject({ code: 'not_found' });
    await expect(a.listCloudFiles(903, '/')).rejects.toMatchObject({ code: 'not_supported' });
    const blob = await a.downloadCloudFile(901, '/Notizen.md');
    expect(await blob.text()).toContain('Notizen.md');
    const imp = await a.importCloudFile(901, '/Dokumente/Mietvertrag.pdf');
    expect(imp).toMatchObject({ filename: 'Mietvertrag.pdf', size: 1_245_184 });
    // OpenCloud paths without a space go to the personal space.
    const oc = await a.uploadCloudFile(902, '/Ablage', new Blob(['z']), 'Notiz.txt');
    expect(oc.path).toBe('/personal/Ablage/Notiz.txt');
  });

  it('runs login flows', async () => {
    const a = api();
    const f = await a.startCloudLogin({ provider: 'nextcloud', server: 'cloud.example.org' });
    expect(f.status).toBe('pending');
    expect(f.loginUrl).toContain('https://cloud.example.org/index.php/login/v2/flow/');
    expect((await a.pollCloudLogin(f.id)).status).toBe('pending');
    const done = await a.pollCloudLogin(f.id);
    expect(done.status).toBe('done');
    expect(done.account?.dav?.caldav).toContain('/remote.php/dav/calendars/');
    const vw = await a.startCloudLogin({ provider: 'vaultwarden', server: 'vault.example.org', email: 'a@b.de', password: 'x' });
    expect(vw.status).toBe('two_factor');
    const vw2 = await a.startCloudLogin({ provider: 'vaultwarden', server: 'vault.example.org', email: 'a@b.de', password: 'x', totp: '123456' });
    expect(vw2.account?.capabilities.send).toBe(true);
    expect((await a.listCloudAccounts()).length).toBe(5);
  });

  it('creates, lists and revokes shares', async () => {
    const a = api();
    const before = await a.listCloudShares();
    const s = await a.createCloudShare({ accountId: 901, path: '/Notizen.md', generatePassword: true });
    expect(s.password).toHaveLength(16);
    expect(s.url).toMatch(/^https:\/\/cloud\.becker-familie\.de\/s\//);
    const list = await a.listCloudShares();
    expect(list).toHaveLength(before.length + 1);
    expect(list[0].password).toBeUndefined();
    expect((await a.cloudSharePassword(s.id)).password).toBe(s.password);
    const r = await a.revokeCloudShare(s.id);
    expect(r.active).toBe(false);
    expect(r.revokedAt).toBeTruthy();
    await expect(a.cloudSharePassword(s.id)).rejects.toMatchObject({ code: 'not_found' });
    const send = await a.createCloudShare({ accountId: 903, text: 'WLAN: geheim' });
    expect(send.url).toContain('/#/send/');
    const saved = await a.saveAttachmentToCloud({ accountId: 901, dir: '/Ablage', messageId: (await a.listMessages({ scope: { kind: 'unified', role: 'inbox' }, offset: 0, limit: 200 })).items.find((m) => m.hasAttachments)!.id, index: 0 });
    expect(saved.path.startsWith('/Ablage/')).toBe(true);
  });
});

describe('transports', () => {
  it('HttpTransport maps the cloud routes', async () => {
    const calls: [string, RequestInit][] = [];
    const fetchImpl = vi.fn(async (url: string, init: RequestInit) => {
      calls.push([url, init]);
      if (url.endsWith('/api/uploads')) return new Response(JSON.stringify({ uploadToken: 'up_1' }), { status: 200 });
      if (url.includes('/download')) return new Response('file', { status: 200 });
      return new Response(JSON.stringify({}), { status: 200 });
    });
    const h = new HttpTransport({ fetchImpl: fetchImpl as unknown as typeof fetch, webSocketImpl: undefined });
    await h.listCloudFiles(3, '/Dokumente/Rechnungen 2026');
    await h.uploadCloudFile(3, '/A', new Blob(['x']), 'a.pdf');
    await h.revokeCloudShare(9);
    const blob = await h.downloadCloudFile(3, '/a.pdf');
    expect(await blob.text()).toBe('file');
    expect(calls[0][0]).toBe('/api/cloud/accounts/3/files?path=%2FDokumente%2FRechnungen+2026');
    expect(calls[2][0]).toBe('/api/cloud/accounts/3/upload');
    expect(JSON.parse(calls[2][1].body as string)).toEqual({ dir: '/A', uploadToken: 'up_1' });
    expect(calls[3][1].method).toBe('DELETE');
    expect(calls[4][0]).toBe('/api/cloud/accounts/3/download?path=%2Fa.pdf');
  });

  it('WailsTransport calls the bound Go methods', async () => {
    const bound: Record<string, (...args: unknown[]) => Promise<unknown>> = {
      ListCloudFiles: vi.fn(async () => ({ accountId: 1, path: '/', entries: [] })),
      DownloadCloudFile: vi.fn(async () => ({ filename: 'a.txt', mimeType: 'text/plain', data: btoa('hallo') })),
      StartCloudLogin: vi.fn(async () => {
        throw JSON.stringify({ code: 'cloud_auth', message: 'rejected' });
      }),
    };
    const w = new WailsTransport({ go: { app: { Api: bound } } } as WailsWindow);
    await w.listCloudFiles(1, '/x');
    expect(bound.ListCloudFiles).toHaveBeenCalledWith(1, '/x');
    expect(await (await w.downloadCloudFile(1, '/a.txt')).text()).toBe('hallo');
    await expect(w.startCloudLogin({ provider: 'nextcloud', server: 'x' })).rejects.toMatchObject({ code: 'cloud_auth' });
  });

  it('WailsTransport unpacks the error even if WebView2 prefixes it with "Error: "', async () => {
    const payload = JSON.stringify({ code: 'cloud_error', message: "invalid character '<' looking for beginning of value" });
    const bound = {
      StartCloudLogin: vi.fn(async () => {
        throw new Error(payload);
      }),
      ListCloudFiles: vi.fn(async () => {
        throw 'Error: ' + payload;
      }),
    };
    const w = new WailsTransport({ go: { app: { Api: bound } } } as WailsWindow);
    await expect(w.startCloudLogin({ provider: 'vaultwarden', server: 'x' })).rejects.toMatchObject({ code: 'cloud_error' });
    await expect(w.listCloudFiles(1, '/')).rejects.toMatchObject({ code: 'cloud_error' });
    await expect(w.listCloudFiles(1, '/')).rejects.not.toHaveProperty('message', expect.stringContaining('{"code"'));
  });
});
