// SPDX-License-Identifier: AGPL-3.0-or-later
// Mock of the account edit dialog ("Konto bearbeiten") for the demo transport:
// per-account sync/index options, connection tests and diagnostics.

import { ApiError } from './api';
import type {
  Account,
  AccountDiagnostics,
  AccountIndexOptions,
  AccountSettings,
  AccountUpdate,
  BodyPolicy,
  ConnectionTestInput,
  ConnectionTestResult,
  ConnectionTestStep,
  Folder,
  IdleMode,
  Pop3Options,
  ServerConfig,
  SieveConfig,
} from '../types';

interface Options {
  bodyPolicy: BodyPolicy;
  pollMinutes: number;
  idleMode: IdleMode;
  idleFolders: number[];
  index: AccountIndexOptions;
  pop3?: Pop3Options;
  lastSync: string;
  error?: string;
}

const EDITABLE = new Set(['imap', 'pop3', 'jmap', 'eas', 'ews', 'graph', 'gmail', 'caldav', 'carddav']);
const TLS = new Set(['tls', 'starttls', 'none']);
const AUTH = new Set(['password', 'oauth2', 'client_cert']);

function invalid(msg: string): never {
  throw new ApiError(msg, 400, 'invalid_input');
}

/** Connection test steps of an account (mirror of connectionTests in account_edit.go). */
export function connectionTests(acc: Account, sieve?: SieveConfig): string[] {
  if (acc.kind === 'caldav' || acc.kind === 'carddav') return [acc.kind];
  if (!['imap', 'pop3', 'jmap', 'eas', 'ews', 'graph', 'gmail'].includes(acc.kind)) return [];
  const out = [acc.kind as string];
  if (acc.outgoing?.host && (acc.kind === 'imap' || acc.kind === 'pop3')) out.push('smtp');
  if (acc.kind === 'imap' && !sieve?.disabled) out.push('sieve');
  return out;
}

function validServer(which: string, c: ServerConfig | undefined): void {
  if (!c) return;
  if (!c.host.trim()) invalid(`${which}: host required`);
  if (!(c.port >= 1 && c.port <= 65535)) invalid(`${which}: invalid port ${c.port}`);
  if (!TLS.has(c.tls)) invalid(`${which}: tls must be tls, starttls or none`);
  if (!AUTH.has(c.auth)) invalid(`${which}: invalid authentication method`);
}

export class MockAccountEdit {
  private readonly opts = new Map<number, Options>();
  /** Demo password of every account; "wrong" fails the login. */
  private readonly passwords = new Map<number, string>();

  constructor(private readonly now: () => Date = () => new Date()) {}

  private o(id: number): Options {
    let o = this.opts.get(id);
    if (!o) {
      o = {
        bodyPolicy: 'recent',
        pollMinutes: 0,
        idleMode: 'default',
        idleFolders: [],
        index: { content: true, attachments: id === 1, encrypted: false },
        lastSync: new Date(this.now().getTime() - 3 * 60_000).toISOString(),
        // Demo: the Exchange account cannot log in until the password is changed.
        error: id === 2 ? 'Anmeldung fehlgeschlagen: 401 Unauthorized' : undefined,
      };
      this.opts.set(id, o);
    }
    return o;
  }

  settings(acc: Account, sieve?: SieveConfig): AccountSettings {
    const o = this.o(acc.id);
    const oauth = acc.incoming?.auth === 'oauth2' || !!acc.extra?.oauthProvider;
    return {
      account: acc,
      email: acc.extra?.address ?? '',
      status: { accountId: acc.id, state: !acc.enabled ? 'stopped' : o.error ? 'error' : 'idle', error: o.error, lastSync: o.lastSync },
      hasPassword: EDITABLE.has(acc.kind) && !oauth,
      oauthProvider: acc.extra?.oauthProvider ?? (oauth ? 'oauth2' : undefined),
      easUrl: acc.kind === 'eas' ? (acc.extra?.eas_url ?? `https://${acc.incoming?.host ?? ''}/Microsoft-Server-ActiveSync`) : undefined,
      jmapUrl: acc.extra?.jmapUrl,
      pop3: acc.kind === 'pop3' ? (o.pop3 ?? { leaveOnServer: true, deleteAfterDays: 0, deleteOnLocalDelete: false }) : undefined,
      bodyPolicy: o.bodyPolicy,
      pollMinutes: o.pollMinutes,
      idleMode: o.idleMode,
      idleFolders: [...o.idleFolders],
      index: { ...o.index },
      sieve: acc.kind === 'imap' ? sieve : undefined,
      tests: connectionTests(acc, sieve),
      editable: EDITABLE.has(acc.kind),
    };
  }

  /** Applies an update to acc (in place) and the options; returns whether servers changed. */
  apply(acc: Account, u: AccountUpdate): boolean {
    if (u.displayName !== undefined && !u.displayName.trim()) invalid('display name required');
    if (u.color !== undefined && u.color && !/^#[0-9a-fA-F]{6}$/.test(u.color)) invalid('color must be #rrggbb');
    if (u.email !== undefined && u.email && !/^[^\s@]+@[^\s@]+$/.test(u.email)) invalid('invalid e-mail address');
    if ((u.incoming || u.outgoing) && !EDITABLE.has(acc.kind)) invalid(`the server settings of ${acc.kind} accounts cannot be edited`);
    validServer('incoming', u.incoming);
    validServer('outgoing', u.outgoing);
    if (u.pollMinutes !== undefined && (u.pollMinutes < 0 || u.pollMinutes > 1440)) invalid('pollMinutes must be between 0 and 1440');
    const o = this.o(acc.id);
    const changed = !!(u.incoming || u.outgoing || u.easUrl !== undefined || u.jmapUrl !== undefined);
    if (u.displayName !== undefined) acc.displayName = u.displayName.trim();
    if (u.color !== undefined) acc.color = u.color;
    if (u.enabled !== undefined) acc.enabled = u.enabled;
    if (u.email !== undefined) acc.extra = { ...acc.extra, address: u.email.toLowerCase() };
    if (u.incoming) acc.incoming = { ...u.incoming };
    if (u.outgoing) acc.outgoing = { ...u.outgoing };
    if (u.easUrl !== undefined) acc.extra = { ...acc.extra, eas_url: u.easUrl };
    if (u.jmapUrl !== undefined) acc.extra = { ...acc.extra, jmapUrl: u.jmapUrl };
    if (u.pop3) o.pop3 = { ...u.pop3 };
    if (u.bodyPolicy) o.bodyPolicy = u.bodyPolicy;
    if (u.pollMinutes !== undefined) o.pollMinutes = u.pollMinutes;
    if (u.idleMode) {
      o.idleMode = u.idleMode;
      o.idleFolders = u.idleMode === 'custom' ? [...new Set(u.idleFolders ?? [])].sort((a, b) => a - b) : [];
    }
    if (u.index) o.index = { ...u.index };
    if (changed) o.error = undefined;
    return changed;
  }

  test(acc: Account, input: ConnectionTestInput, sieve?: SieveConfig): ConnectionTestResult {
    const cand: Account = { ...acc, incoming: input.update?.incoming ?? acc.incoming, outgoing: input.update?.outgoing ?? acc.outgoing };
    const pw = input.password ?? this.passwords.get(acc.id) ?? (acc.id === 2 ? 'wrong' : 'demo');
    const steps: ConnectionTestStep[] = [];
    for (const kind of connectionTests(cand, sieve)) {
      if (input.kinds?.length && !input.kinds.includes(kind)) continue;
      const srv = kind === 'smtp' ? cand.outgoing : cand.incoming;
      const target = kind === 'sieve' ? `${sieve?.host || srv?.host}:${sieve?.port ?? 4190}` : srv ? `${srv.host}:${srv.port}` : '';
      const step: ConnectionTestStep = { kind, target, ok: true, millis: 40 + Math.round(Math.random() * 120) };
      if (!srv?.host || srv.host.includes('invalid') || srv.port === 1) {
        Object.assign(step, { ok: false, code: 'unreachable', error: `dial tcp ${target}: connection refused` });
      } else if (pw === 'wrong' && kind !== 'sieve') {
        Object.assign(step, { ok: false, code: 'auth_failed', error: kind === 'smtp' ? '535 5.7.8 Authentication failed' : 'LOGIN failed: invalid credentials' });
      }
      steps.push(step);
    }
    return { ok: steps.every((s) => s.ok), steps };
  }

  setPassword(id: number, password: string): void {
    this.passwords.set(id, password);
    this.o(id).error = undefined;
  }

  syncNow(id: number): void {
    const o = this.o(id);
    if (!o.error) o.lastSync = this.now().toISOString();
  }

  forget(id: number): void {
    this.opts.delete(id);
    this.passwords.delete(id);
  }

  diagnostics(s: AccountSettings, folders: Folder[], identities: number, version: string): AccountDiagnostics {
    const account = { ...s.account, secretRef: undefined };
    return {
      generatedAt: this.now().toISOString(),
      version,
      mode: 'mock',
      account,
      status: s.status,
      folders: folders.map((f) => ({ name: f.remoteId, role: f.role, total: f.total, unread: f.unread })),
      identities,
      pendingOps: 0,
      failedOps: s.status?.error ? [{ kind: 'sync', attempts: 3, lastError: s.status.error, createdAt: this.now().toISOString() }] : [],
      index: s.index,
      settings: { ...s, account },
    };
  }
}
