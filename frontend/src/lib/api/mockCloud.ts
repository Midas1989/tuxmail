// SPDX-License-Identifier: AGPL-3.0-or-later
//
// In-memory cloud backend of the MockTransport (KONZEPT 12): a Nextcloud,
// an OpenCloud account with spaces and a Vaultwarden Send account with
// German demo files and shares. Semantics follow internal/app/cloud_*.go:
// unique names on upload, OpenCloud paths start with the space id, revoked
// links stay listed.

import { ApiError } from './api';
import type {
  CloudAccount,
  CloudEntry,
  CloudImportResult,
  CloudListing,
  CloudLoginFlow,
  CloudLoginInput,
  CloudQuota,
  CloudSaveInput,
  CloudShare,
  CloudShareInput,
} from '../types';

interface Node {
  entry: CloudEntry;
  data?: string;
}

const DAY = 86400_000;

function join(dir: string, name: string): string {
  return (dir.replace(/\/+$/, '') + '/' + name).replace(/\/+/g, '/');
}

function clean(p: string): string {
  const parts: string[] = [];
  for (const s of p.split('/')) {
    if (!s || s === '.') continue;
    if (s === '..') parts.pop();
    else parts.push(s);
  }
  return '/' + parts.join('/');
}

function parent(p: string): string {
  const c = clean(p);
  return c.slice(0, c.lastIndexOf('/')) || '/';
}

function baseName(p: string): string {
  return clean(p).split('/').pop() ?? '';
}

const NOT_FOUND = () => new ApiError('Nicht gefunden', 404, 'not_found');

/** Policy-compliant demo password (16 chars, all classes, no ambiguous chars). */
export function mockPassword(len = 16): string {
  const classes = ['abcdefghijkmnopqrstuvwxyz', 'ABCDEFGHJKLMNPQRSTUVWXYZ', '23456789', '#+-_!?%=@*'];
  const all = classes.join('');
  const out = classes.map((c) => c[Math.floor(Math.random() * c.length)]);
  while (out.length < len) out.push(all[Math.floor(Math.random() * all.length)]);
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [out[i], out[j]] = [out[j], out[i]];
  }
  return out.join('');
}

export class MockCloud {
  accounts: CloudAccount[] = [];
  private files = new Map<number, Map<string, Node>>();
  private shares: CloudShare[] = [];
  private passwords = new Map<number, string>();
  private flows = new Map<string, { flow: CloudLoginFlow; polls: number; input: CloudLoginInput }>();
  private nextId = 1;
  private nextAccount = 904;

  constructor(private readonly now: () => number = () => Date.now(), seed = true) {
    if (seed) this.seed();
  }

  private seed(): void {
    const t = this.now();
    this.accounts = [
      {
        id: 901,
        provider: 'nextcloud',
        displayName: 'Nextcloud (cloud.becker-familie.de)',
        server: 'https://cloud.becker-familie.de',
        user: 'lena',
        capabilities: { files: true, share: true, send: false },
        dav: { caldav: 'https://cloud.becker-familie.de/remote.php/dav/calendars/lena/', carddav: 'https://cloud.becker-familie.de/remote.php/dav/addressbooks/users/lena/' },
        uploadDir: '/TuxMail/Mail-Anhänge',
      },
      {
        id: 902,
        provider: 'opencloud',
        displayName: 'OpenCloud (Kanzlei Sommer)',
        server: 'https://opencloud.kanzlei-sommer.de',
        user: 'lena.becker@kanzlei-sommer.de',
        capabilities: { files: true, share: true, send: false },
        uploadDir: '/TuxMail/Mail-Anhänge',
      },
      {
        id: 903,
        provider: 'vaultwarden',
        displayName: 'Vaultwarden Send',
        server: 'https://vault.becker-familie.de',
        user: 'lena@becker-familie.de',
        capabilities: { files: false, share: false, send: true },
      },
    ];
    const iso = (daysAgo: number) => new Date(t - daysAgo * DAY).toISOString();
    const nc = new Map<string, Node>();
    const dir = (p: string, d: number) => nc.set(p, { entry: { name: baseName(p), path: p, dir: true, size: 0, modified: iso(d) } });
    const file = (m: Map<string, Node>, p: string, size: number, d: number, ct: string) =>
      m.set(p, { entry: { name: baseName(p), path: p, dir: false, size, modified: iso(d), contentType: ct }, data: `Demo ${baseName(p)}` });
    dir('/Dokumente', 40);
    dir('/Dokumente/Rechnungen', 12);
    dir('/Dokumente/Rechnungen/2026', 2);
    dir('/Fotos', 30);
    dir('/Projekte', 20);
    dir('/Projekte/Relaunch', 3);
    dir('/TuxMail', 9);
    dir('/TuxMail/Mail-Anhänge', 9);
    file(nc, '/Dokumente/Rechnungen/2026/RE-2026-1042.pdf', 184_320, 2, 'application/pdf');
    file(nc, '/Dokumente/Rechnungen/2026/Stadtwerke-Oktober.pdf', 96_256, 6, 'application/pdf');
    file(nc, '/Dokumente/Mietvertrag.pdf', 1_245_184, 120, 'application/pdf');
    file(nc, '/Projekte/Relaunch/Moodboard.png', 3_984_102, 3, 'image/png');
    file(nc, '/Projekte/Relaunch/Angebot-2026-117.pdf', 248_812, 4, 'application/pdf');
    file(nc, '/Projekte/Relaunch/Zeitplan.xlsx', 41_210, 5, 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
    file(nc, '/Fotos/Sommerfest-2026.jpg', 5_212_004, 60, 'image/jpeg');
    file(nc, '/Notizen.md', 2_310, 1, 'text/markdown');
    this.files.set(901, nc);

    const oc = new Map<string, Node>();
    const space = (id: string, name: string) => oc.set('/' + id, { entry: { name, path: '/' + id, dir: true, size: 0, modified: iso(1), contentType: 'space/' + (id === 'personal' ? 'personal' : 'project') } });
    space('personal', 'Lena Becker');
    space('mandate', 'Mandat Wagner GmbH');
    oc.set('/personal/Entwürfe', { entry: { name: 'Entwürfe', path: '/personal/Entwürfe', dir: true, size: 0, modified: iso(2) } });
    file(oc, '/personal/Entwürfe/Schriftsatz-v3.docx', 88_432, 2, 'application/vnd.openxmlformats-officedocument.wordprocessingml.document');
    file(oc, '/mandate/Vollmacht.pdf', 120_331, 14, 'application/pdf');
    file(oc, '/mandate/Gutachten-Entwurf.pdf', 2_450_112, 3, 'application/pdf');
    this.files.set(902, oc);

    const share = (s: Omit<CloudShare, 'id' | 'active'>, pw?: string) => {
      const id = this.nextId++;
      const full: CloudShare = { ...s, id, active: !s.revokedAt && (!s.expiresAt || Date.parse(s.expiresAt) > t) };
      this.shares.unshift(full);
      if (pw) this.passwords.set(id, pw);
    };
    share({ accountId: 901, provider: 'nextcloud', fileName: 'Mietvertrag.pdf', remotePath: '/Dokumente/Mietvertrag.pdf', url: 'https://cloud.becker-familie.de/s/Hk3mPq9xLe', hasPassword: true, expiresAt: iso(-2), createdAt: iso(12), source: 'files', size: 1_245_184 }, 'Tq7#mK2p-Lx9w!Rz');
    share({ accountId: 902, provider: 'opencloud', fileName: 'Gutachten-Entwurf.pdf', remotePath: '/mandate/Gutachten-Entwurf.pdf', url: 'https://opencloud.kanzlei-sommer.de/s/QyTRbUVwTZ', hasPassword: true, expiresAt: iso(-11), createdAt: iso(3), source: 'mail', size: 2_450_112, messageId: 3 }, 'Gv4@nP8s#Kd2m?Ya');
    share({ accountId: 903, provider: 'vaultwarden', fileName: 'Steuerunterlagen-2025.zip', url: 'https://vault.becker-familie.de/#/send/a7Xk2Lm9QwPe/c2VuZGtleWRlbW8', hasPassword: true, expiresAt: iso(-5), createdAt: iso(2), source: 'mail', size: 18_874_368 }, 'Wn6=hT3r%Bq8z+Je');
    share({ accountId: 901, provider: 'nextcloud', fileName: 'Moodboard.png', remotePath: '/Projekte/Relaunch/Moodboard.png', url: 'https://cloud.becker-familie.de/s/Zr8wQp2sNa', hasPassword: false, expiresAt: iso(1), createdAt: iso(15), source: 'workflow', size: 3_984_102 });
    share({ accountId: 901, provider: 'nextcloud', fileName: 'Angebot-2026-117.pdf', remotePath: '/Projekte/Relaunch/Angebot-2026-117.pdf', url: 'https://cloud.becker-familie.de/s/Mn4kLr7tPb', hasPassword: true, expiresAt: iso(-8), createdAt: iso(6), revokedAt: iso(1), source: 'mail', size: 248_812 });
  }

  private account(id: number): CloudAccount {
    const a = this.accounts.find((x) => x.id === id);
    if (!a) throw NOT_FOUND();
    return a;
  }

  private tree(id: number): Map<string, Node> {
    const a = this.account(id);
    if (!a.capabilities.files) throw new ApiError(`${a.provider} hat keinen Dateispeicher`, 501, 'not_supported');
    let m = this.files.get(id);
    if (!m) {
      m = new Map();
      this.files.set(id, m);
    }
    return m;
  }

  // ---- accounts & login ----------------------------------------------------

  listAccounts(): CloudAccount[] {
    return this.accounts;
  }

  startLogin(input: CloudLoginInput): CloudLoginFlow {
    const server = (input.server.includes('://') ? input.server : 'https://' + input.server).replace(/\/+$/, '');
    if (!/^https?:\/\/[^/]+/.test(server)) throw new ApiError('Ungültige Server-Adresse', 400, 'invalid_input');
    const id = 'cl_' + Math.random().toString(36).slice(2, 10);
    if (input.provider === 'vaultwarden') {
      if (!input.email || !input.password) throw new ApiError('E-Mail und Master-Passwort erforderlich', 400, 'invalid_input');
      if (input.password === 'falsch') throw new ApiError('Der Server hat die Anmeldung abgelehnt', 401, 'cloud_auth');
      if (!input.totp) return { id, provider: 'vaultwarden', status: 'two_factor', error: 'two_factor_required' };
      return { id, provider: 'vaultwarden', status: 'done', account: this.addAccount(input, server) };
    }
    if (input.provider === 'webdav') return { id, provider: 'webdav', status: 'done', account: this.addAccount(input, server) };
    const loginUrl = input.provider === 'nextcloud' ? `${server}/index.php/login/v2/flow/${id}` : `${server}/signin/v1/identifier/_/authorize?client_id=OpenCloudDesktop&state=${id}`;
    const flow: CloudLoginFlow = { id, provider: input.provider, status: 'pending', loginUrl };
    this.flows.set(id, { flow, polls: 0, input: { ...input, server } });
    return flow;
  }

  pollLogin(flowId: string): CloudLoginFlow {
    const f = this.flows.get(flowId);
    if (!f) throw NOT_FOUND();
    f.polls++;
    if (f.flow.status === 'pending' && f.polls >= 2) {
      f.flow = { ...f.flow, status: 'done', loginUrl: undefined, account: this.addAccount(f.input, f.input.server) };
    }
    return f.flow;
  }

  cancelLogin(flowId: string): void {
    if (!this.flows.delete(flowId)) throw NOT_FOUND();
  }

  private addAccount(input: CloudLoginInput, server: string): CloudAccount {
    const host = server.replace(/^https?:\/\//, '').split('/')[0];
    const label = { nextcloud: 'Nextcloud', opencloud: 'OpenCloud', vaultwarden: 'Vaultwarden Send', webdav: 'WebDAV' }[input.provider];
    const existing = this.accounts.find((a) => a.provider === input.provider && a.server === server);
    if (existing) return existing;
    const acc: CloudAccount = {
      id: this.nextAccount++,
      provider: input.provider,
      displayName: input.displayName || `${label} (${host})`,
      server,
      user: input.email || input.username || 'lena',
      capabilities: { files: input.provider !== 'vaultwarden', share: input.provider === 'nextcloud' || input.provider === 'opencloud', send: input.provider === 'vaultwarden' },
    };
    if (acc.provider === 'nextcloud') acc.dav = { caldav: `${server}/remote.php/dav/calendars/lena/`, carddav: `${server}/remote.php/dav/addressbooks/users/lena/` };
    if (acc.capabilities.share) acc.uploadDir = '/TuxMail/Mail-Anhänge';
    this.accounts.push(acc);
    if (acc.provider === 'opencloud') {
      this.files.set(acc.id, new Map([['/personal', { entry: { name: 'Persönlich', path: '/personal', dir: true, size: 0, contentType: 'space/personal' } }]]));
    }
    return acc;
  }

  deleteAccount(id: number): void {
    this.account(id);
    this.accounts = this.accounts.filter((a) => a.id !== id);
    this.files.delete(id);
  }

  // ---- files ----------------------------------------------------------------

  list(accountId: number, path: string): CloudListing {
    const m = this.tree(accountId);
    const dir = clean(path);
    if (dir !== '/' && !m.get(dir)?.entry.dir) throw NOT_FOUND();
    const entries = [...m.values()].filter((n) => parent(n.entry.path) === dir && n.entry.path !== dir).map((n) => n.entry);
    entries.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name.localeCompare(b.name, 'de')));
    return { accountId, path: dir, entries };
  }

  quota(accountId: number): CloudQuota {
    const m = this.tree(accountId);
    let used = 0;
    for (const n of m.values()) used += n.entry.size;
    return { used: used + 4_200_000_000, available: 10_737_418_240 - used - 4_200_000_000 };
  }

  private resolve(accountId: number, p: string): string {
    const c = clean(p);
    if (this.account(accountId).provider !== 'opencloud') return c;
    const first = c.split('/')[1];
    return this.files.get(accountId)?.has('/' + first) ? c : clean('/personal' + c);
  }

  mkdir(accountId: number, path: string): CloudEntry {
    const m = this.tree(accountId);
    const p = this.resolve(accountId, path);
    if (p === '/') throw new ApiError('Ordnername erforderlich', 400, 'invalid_input');
    let cur = '';
    for (const part of p.split('/').slice(1)) {
      cur += '/' + part;
      const n = m.get(cur);
      if (n && !n.entry.dir) throw new ApiError('Existiert bereits', 409, 'conflict');
      if (!n) m.set(cur, { entry: { name: part, path: cur, dir: true, size: 0, modified: new Date(this.now()).toISOString() } });
    }
    return m.get(p)!.entry;
  }

  move(accountId: number, from: string, to: string): void {
    const m = this.tree(accountId);
    const f = clean(from);
    const t = clean(to);
    if (!m.has(f)) throw NOT_FOUND();
    if (m.has(t)) throw new ApiError('Existiert bereits', 409, 'conflict');
    for (const [k, n] of [...m.entries()]) {
      if (k === f || k.startsWith(f + '/')) {
        m.delete(k);
        const np = t + k.slice(f.length);
        m.set(np, { ...n, entry: { ...n.entry, path: np, name: baseName(np) } });
      }
    }
  }

  remove(accountId: number, path: string): void {
    const m = this.tree(accountId);
    const p = clean(path);
    if (p === '/' || !m.has(p)) throw NOT_FOUND();
    for (const k of [...m.keys()]) if (k === p || k.startsWith(p + '/')) m.delete(k);
  }

  private unique(m: Map<string, Node>, p: string): string {
    if (!m.has(p)) return p;
    const dot = p.lastIndexOf('.');
    const [base, ext] = dot > p.lastIndexOf('/') ? [p.slice(0, dot), p.slice(dot)] : [p, ''];
    for (let i = 2; ; i++) {
      const c = `${base} (${i})${ext}`;
      if (!m.has(c)) return c;
    }
  }

  store(accountId: number, dir: string, name: string, size: number, contentType = 'application/octet-stream'): CloudEntry {
    const m = this.tree(accountId);
    const d = this.resolve(accountId, dir);
    if (d !== '/') this.mkdir(accountId, d);
    const p = this.unique(m, join(d, name.replace(/[/\\:]/g, '-')));
    const entry: CloudEntry = { name: baseName(p), path: p, dir: false, size, modified: new Date(this.now()).toISOString(), contentType };
    m.set(p, { entry, data: `Demo ${name}` });
    return entry;
  }

  read(accountId: number, path: string): { entry: CloudEntry; data: string } {
    const n = this.tree(accountId).get(clean(path));
    if (!n || n.entry.dir) throw NOT_FOUND();
    return { entry: n.entry, data: n.data ?? '' };
  }

  importFile(accountId: number, path: string): CloudImportResult {
    const { entry } = this.read(accountId, path);
    return { uploadToken: `mock-cloud-${accountId}-${encodeURIComponent(entry.path)}`, filename: entry.name, mimeType: entry.contentType ?? 'application/octet-stream', size: entry.size };
  }

  save(input: CloudSaveInput, name: string, size: number, contentType?: string): CloudEntry {
    return this.store(input.accountId, input.dir, input.filename || name, size, contentType);
  }

  // ---- shares ---------------------------------------------------------------

  createShare(input: CloudShareInput, file?: { name: string; size: number }): CloudShare {
    const acc = this.account(input.accountId);
    const t = this.now();
    const exp = input.expiresAt ? Date.parse(input.expiresAt) : t + 14 * DAY;
    if (!(exp > t)) throw new ApiError('Ablaufdatum muss in der Zukunft liegen', 400, 'invalid_input');
    const password = input.password || (input.generatePassword ? mockPassword(acc.provider === 'nextcloud' ? 16 : 12) : '');
    const token = Math.random().toString(36).slice(2, 12);
    let fileName = input.name ?? '';
    let size = 0;
    let url: string;
    if (acc.capabilities.send) {
      if (!input.text && !file) throw new ApiError('Datei oder Text erforderlich', 400, 'invalid_input');
      fileName ||= input.text ? 'Text' : file!.name;
      size = file?.size ?? input.text?.length ?? 0;
      url = `${acc.server}/#/send/${token}/${btoa(token).replace(/=+$/, '')}`;
    } else if (acc.capabilities.share) {
      const { entry } = this.findEntry(input.accountId, input.path ?? '');
      fileName ||= entry.name;
      size = entry.size;
      url = `${acc.server}/s/${token}`;
    } else {
      throw new ApiError('Dieses Konto kann keine Freigaben erstellen', 501, 'not_supported');
    }
    const id = this.nextId++;
    const share: CloudShare = {
      id,
      accountId: acc.id,
      provider: acc.provider,
      fileName,
      remotePath: input.path,
      url,
      hasPassword: !!password,
      expiresAt: new Date(exp).toISOString(),
      createdAt: new Date(t).toISOString(),
      active: true,
      source: 'files',
      size,
    };
    this.shares.unshift(share);
    if (password) this.passwords.set(id, password);
    return { ...share, password: password || undefined };
  }

  private findEntry(accountId: number, p: string): Node {
    const n = this.tree(accountId).get(clean(p));
    if (!n || clean(p) === '/') throw NOT_FOUND();
    return n;
  }

  listShares(): CloudShare[] {
    const t = this.now();
    return this.shares.map((s) => ({ ...s, active: !s.revokedAt && (!s.expiresAt || Date.parse(s.expiresAt) > t) }));
  }

  revoke(id: number): CloudShare {
    const s = this.shares.find((x) => x.id === id);
    if (!s) throw NOT_FOUND();
    s.revokedAt ??= new Date(this.now()).toISOString();
    s.active = false;
    this.passwords.delete(id);
    return { ...s };
  }

  password(id: number): { password: string } {
    const pw = this.passwords.get(id);
    if (!pw) throw NOT_FOUND();
    return { password: pw };
  }

  generatePassword(accountId: number): { password: string } {
    const acc = this.account(accountId);
    return { password: mockPassword(acc.provider === 'nextcloud' ? 16 : 12) };
  }
}
