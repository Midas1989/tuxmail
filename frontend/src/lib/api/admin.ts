// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Administration of a TuxMail server in the web UI (Settings -> Administration):
// users, invites, groups and the audit log. Web client against `tuxmail serve`
// only: the desktop shell (Wails) and the native app have no AdminClient
// (createAdminClient returns null there), and the server answers every route
// below with 403 unless the session belongs to an administrator.
//
// Routes (internal/server/api_admin.go; all behind the admin role):
//   GET    /api/auth/me                              -> {user:{id,username,role,...}, mfaSetupRequired}
//   GET    /api/auth/config                          -> {registration, passwordMinLength, ...} (public)
//   GET/POST /api/admin/users                        -> {users:[AdminUser]} / {user, password?}
//   PATCH/DELETE /api/admin/users/{id}[?keepData=true]
//   POST   /api/admin/users/{id}/reset-password      -> {password}   (generated, returned once)
//   POST   /api/admin/users/{id}/reset-2fa | /unlock
//   GET/DELETE /api/admin/users/{id}/sessions[/{sid}]
//   GET/POST /api/admin/groups; DELETE /api/admin/groups/{id}; PUT/DELETE /api/admin/groups/{id}/members/{uid}
//   GET/POST /api/admin/invites; DELETE /api/admin/invites/{id}   (create -> {invite, token, url}, token once)
//   GET    /api/admin/audit?limit=&before=&action=&actor=&result=failed -> {entries}
//
// The pure parts (types, error mapping, the in-memory MockAdminClient used by
// `?api=mock`) live here; the logic of the screens is in lib/admin/.

import type { Api } from './api';
import { ApiError } from './api';
import type { HttpTransportOptions } from './http';
import { isNativeShell } from '../platform';

export type AdminRole = 'admin' | 'user' | 'readonly';
export const ADMIN_ROLES: readonly AdminRole[] = ['admin', 'user', 'readonly'];

export interface AdminUser {
  id: number;
  username: string;
  email: string;
  displayName: string;
  role: AdminRole;
  disabled: boolean;
  hasPassword: boolean;
  totpEnabled: boolean;
  oidc: boolean;
  createdAt: string;
  lastLoginAt?: string;
  /** A network is locked out for this login name (failed logins). The owner on another network can still sign in. */
  locked: boolean;
  lockedUntil?: string;
  /** Distributed guessing in progress: only known networks may sign in. */
  underPressure: boolean;
  pressureUntil?: string;
}

export interface AdminMe {
  id: number;
  username: string;
  role: AdminRole;
  /** The administrator still has to enrol a second factor; admin routes answer 403 mfa_setup_required until then. */
  mfaSetupRequired: boolean;
}

export interface AdminServerConfig {
  /** "invite" enables invite links. */
  registration: string;
  passwordMinLength: number;
}

export interface NewAdminUser {
  username: string;
  email: string;
  displayName: string;
  role: AdminRole;
  /** Empty: the server generates a password and returns it once. */
  password?: string;
}

export interface AdminUserPatch {
  displayName?: string;
  email?: string;
  role?: AdminRole;
  disabled?: boolean;
}

export interface AdminSession {
  id: number;
  userId: number;
  kind: string;
  deviceName: string;
  ip: string;
  userAgent: string;
  mfa: boolean;
  loginMethod: string;
  createdAt: string;
  expiresAt: string;
  lastSeenAt: string;
  /** The session the administrator is using right now. */
  current: boolean;
}

export interface AdminGroup {
  id: number;
  name: string;
  description: string;
  createdAt: string;
  /** User ids. */
  members: number[];
}

export interface AdminInvite {
  id: number;
  email?: string;
  role: AdminRole;
  createdBy?: number;
  createdAt: string;
  expiresAt: string;
  usedAt?: string;
  usedBy?: number;
}

export interface NewAdminInvite {
  role: AdminRole;
  /** Optional: the invite can then only be used with this address. */
  email?: string;
  /** 0 / undefined = the server default (7 days). */
  expiresInHours?: number;
}

export interface CreatedInvite {
  invite: AdminInvite;
  /** Registration link; the token is shown only now. */
  url: string;
}

export interface AdminAuditEntry {
  id: number;
  at: string;
  actorId?: number;
  actor: string;
  action: string;
  target?: string;
  ip?: string;
  success: boolean;
  details?: string;
}

export interface AuditQuery {
  limit?: number;
  /** Entry id: only older entries (paging backwards). */
  before?: number;
  /** Prefix, e.g. "user." */
  action?: string;
  /** Prefix of the acting user name. */
  actor?: string;
  onlyFailed?: boolean;
}

export interface AdminClient {
  me(): Promise<AdminMe>;
  config(): Promise<AdminServerConfig>;
  listUsers(): Promise<AdminUser[]>;
  createUser(input: NewAdminUser): Promise<{ user: AdminUser; password?: string }>;
  updateUser(id: number, patch: AdminUserPatch): Promise<AdminUser>;
  deleteUser(id: number, keepData: boolean): Promise<void>;
  /** Sets a random password, ends all sessions of the user and returns the password (shown once). */
  resetPassword(id: number): Promise<string>;
  reset2FA(id: number): Promise<void>;
  /** Clears the failed-login locks of the account. */
  unlock(id: number): Promise<void>;
  listSessions(id: number): Promise<AdminSession[]>;
  revokeSessions(id: number): Promise<void>;
  revokeSession(id: number, sessionId: number): Promise<void>;
  listGroups(): Promise<AdminGroup[]>;
  createGroup(name: string, description: string): Promise<AdminGroup>;
  deleteGroup(id: number): Promise<void>;
  addMember(groupId: number, userId: number): Promise<void>;
  removeMember(groupId: number, userId: number): Promise<void>;
  listInvites(): Promise<AdminInvite[]>;
  createInvite(input: NewAdminInvite): Promise<CreatedInvite>;
  deleteInvite(id: number): Promise<void>;
  auditLog(q?: AuditQuery): Promise<AdminAuditEntry[]>;
}

/** Server error codes (writeError in internal/server) the screens show in their own words. */
export const ADMIN_ERROR_CODES = [
  'weak_password',
  'exists',
  'last_admin',
  'invalid_input',
  'not_found',
  'mfa_setup_required',
  'forbidden',
  'registration_disabled',
  'throttled',
  'reauth_required',
  'network',
] as const;
export type AdminErrorCode = (typeof ADMIN_ERROR_CODES)[number];

export function adminErrorCode(err: unknown): AdminErrorCode | null {
  if (!(err instanceof ApiError)) return null;
  return (ADMIN_ERROR_CODES as readonly string[]).includes(err.code) ? (err.code as AdminErrorCode) : null;
}

/** Fills the defaults of fields an older server does not send. */
export function normalizeAdminUser(raw: Partial<AdminUser> & { id: number; username: string }): AdminUser {
  return {
    email: '',
    displayName: '',
    role: 'user',
    disabled: false,
    hasPassword: true,
    totpEnabled: false,
    oidc: false,
    createdAt: '',
    locked: false,
    underPressure: false,
    ...raw,
  };
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

function csrfToken(): string | undefined {
  if (typeof document === 'undefined') return undefined;
  const m = document.cookie.match(/(?:^|;\s*)(?:__Host-)?tuxmail_csrf=([^;]+)/);
  if (m) return decodeURIComponent(m[1]);
  return document.querySelector<HTMLMetaElement>('meta[name="tuxmail-csrf"]')?.content || undefined;
}

export class HttpAdminClient implements AdminClient {
  constructor(private readonly opts: HttpTransportOptions = {}) {}

  private async req<T>(method: string, path: string, body?: unknown): Promise<T> {
    const h: Record<string, string> = { Accept: 'application/json' };
    const bearer = this.opts.authToken?.();
    if (bearer) h.Authorization = `Bearer ${bearer}`;
    else {
      const c = csrfToken();
      if (c) h['X-CSRF-Token'] = c;
    }
    if (body !== undefined) h['Content-Type'] = 'application/json';
    const f = this.opts.fetchImpl ?? ((...a: Parameters<typeof fetch>) => fetch(...a));
    let res: Response;
    try {
      res = await f((this.opts.baseUrl ?? '').replace(/\/$/, '') + path, {
        method,
        headers: h,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        credentials: bearer ? 'omit' : 'same-origin',
      });
    } catch (err) {
      throw new ApiError(err instanceof Error ? err.message : 'network error', 0, 'network');
    }
    if (!res.ok) {
      let code = 'http_' + res.status;
      let msg = res.statusText;
      try {
        const j = (await res.json()) as { error?: string; message?: string };
        if (j.error) code = j.error;
        if (j.message) msg = j.message;
      } catch {
        /* not JSON */
      }
      if (res.status === 401 && code === 'unauthenticated') this.opts.onUnauthorized?.();
      throw new ApiError(msg, res.status, code);
    }
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  async me(): Promise<AdminMe> {
    const r = await this.req<{ user: { id: number; username: string; role: AdminRole }; mfaSetupRequired?: boolean }>('GET', '/api/auth/me');
    return { id: r.user.id, username: r.user.username, role: r.user.role, mfaSetupRequired: !!r.mfaSetupRequired };
  }
  async config(): Promise<AdminServerConfig> {
    const r = await this.req<Partial<AdminServerConfig>>('GET', '/api/auth/config');
    return { registration: r.registration ?? '', passwordMinLength: r.passwordMinLength ?? 0 };
  }
  async listUsers(): Promise<AdminUser[]> {
    const r = await this.req<{ users: AdminUser[] }>('GET', '/api/admin/users');
    return r.users.map(normalizeAdminUser);
  }
  async createUser(input: NewAdminUser) {
    const r = await this.req<{ user: AdminUser; password?: string }>('POST', '/api/admin/users', input);
    return { user: normalizeAdminUser(r.user), password: r.password };
  }
  async updateUser(id: number, patch: AdminUserPatch) {
    const r = await this.req<{ user: AdminUser }>('PATCH', `/api/admin/users/${id}`, patch);
    return normalizeAdminUser(r.user);
  }
  deleteUser(id: number, keepData: boolean) {
    return this.req<void>('DELETE', `/api/admin/users/${id}${keepData ? '?keepData=true' : ''}`);
  }
  async resetPassword(id: number) {
    const r = await this.req<{ password: string }>('POST', `/api/admin/users/${id}/reset-password`, {});
    return r.password;
  }
  reset2FA(id: number) {
    return this.req<void>('POST', `/api/admin/users/${id}/reset-2fa`, {});
  }
  unlock(id: number) {
    return this.req<void>('POST', `/api/admin/users/${id}/unlock`, {});
  }
  async listSessions(id: number) {
    const r = await this.req<{ sessions: AdminSession[] }>('GET', `/api/admin/users/${id}/sessions`);
    return r.sessions;
  }
  revokeSessions(id: number) {
    return this.req<void>('DELETE', `/api/admin/users/${id}/sessions`);
  }
  revokeSession(id: number, sessionId: number) {
    return this.req<void>('DELETE', `/api/admin/users/${id}/sessions/${sessionId}`);
  }
  async listGroups() {
    const r = await this.req<{ groups: AdminGroup[] | null }>('GET', '/api/admin/groups');
    return (r.groups ?? []).map((g) => ({ ...g, members: g.members ?? [] }));
  }
  async createGroup(name: string, description: string) {
    const r = await this.req<{ group: AdminGroup }>('POST', '/api/admin/groups', { name, description });
    return { ...r.group, members: r.group.members ?? [] };
  }
  deleteGroup(id: number) {
    return this.req<void>('DELETE', `/api/admin/groups/${id}`);
  }
  addMember(groupId: number, userId: number) {
    return this.req<void>('PUT', `/api/admin/groups/${groupId}/members/${userId}`, {});
  }
  removeMember(groupId: number, userId: number) {
    return this.req<void>('DELETE', `/api/admin/groups/${groupId}/members/${userId}`);
  }
  async listInvites() {
    const r = await this.req<{ invites: AdminInvite[] | null }>('GET', '/api/admin/invites');
    return r.invites ?? [];
  }
  async createInvite(input: NewAdminInvite) {
    const r = await this.req<{ invite: AdminInvite; url: string }>('POST', '/api/admin/invites', {
      role: input.role,
      email: input.email?.trim() ?? '',
      expiresInHours: input.expiresInHours ?? 0,
    });
    return { invite: r.invite, url: r.url };
  }
  deleteInvite(id: number) {
    return this.req<void>('DELETE', `/api/admin/invites/${id}`);
  }
  async auditLog(q: AuditQuery = {}) {
    const sp = new URLSearchParams();
    if (q.limit) sp.set('limit', String(q.limit));
    if (q.before) sp.set('before', String(q.before));
    if (q.action?.trim()) sp.set('action', q.action.trim());
    if (q.actor?.trim()) sp.set('actor', q.actor.trim());
    if (q.onlyFailed) sp.set('result', 'failed');
    const qs = sp.toString();
    const r = await this.req<{ entries: AdminAuditEntry[] | null }>('GET', '/api/admin/audit' + (qs ? '?' + qs : ''));
    return r.entries ?? [];
  }
}

// ---------------------------------------------------------------------------
// Mock (`?api=mock` development and demo): an in-memory server with the same
// rules as internal/auth (last administrator, duplicates, password policy).
// ---------------------------------------------------------------------------

export interface MockAdminOptions {
  latencyMs?: number;
  /** Role of the mock session (default admin). */
  role?: AdminRole;
  now?: () => Date;
}

const MOCK_MIN_PASSWORD = 12;

export function randomPassword(length = 20): string {
  const alphabet = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789-_';
  const bytes = new Uint8Array(length);
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) crypto.getRandomValues(bytes);
  else for (let i = 0; i < length; i++) bytes[i] = Math.floor(Math.random() * 256);
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join('');
}

export class MockAdminClient implements AdminClient {
  private readonly latency: number;
  private readonly role: AdminRole;
  private readonly now: () => Date;
  private nextId = 100;
  private users: AdminUser[];
  private sessions = new Map<number, AdminSession[]>();
  private groups: AdminGroup[];
  private invites: AdminInvite[] = [];
  private audit: AdminAuditEntry[] = [];

  /** The signed-in mock administrator. */
  static readonly ME = 1;

  constructor(opts: MockAdminOptions = {}) {
    this.latency = opts.latencyMs ?? 120;
    this.role = opts.role ?? 'admin';
    this.now = opts.now ?? (() => new Date());
    const ago = (days: number) => new Date(this.now().getTime() - days * 86_400_000).toISOString();
    const ahead = (min: number) => new Date(this.now().getTime() + min * 60_000).toISOString();
    const u = (id: number, username: string, displayName: string, role: AdminRole, extra: Partial<AdminUser> = {}) =>
      normalizeAdminUser({ id, username, displayName, role, email: `${username}@example.de`, createdAt: ago(200 - id * 7), lastLoginAt: ago(id), ...extra });
    this.users = [
      u(1, 'admin', 'Admin Adler', 'admin', { totpEnabled: true, lastLoginAt: ago(0) }),
      u(2, 'anna', 'Anna Weber', 'user', { totpEnabled: true }),
      u(3, 'ben', 'Ben Keller', 'user', { locked: true, lockedUntil: ahead(23) }),
      u(4, 'carla', 'Carla Meyer', 'user', { underPressure: true, pressureUntil: ahead(11) }),
      u(5, 'dieter', 'Dieter Voss', 'readonly', { disabled: true }),
      u(6, 'erika', 'Erika Roth', 'admin', { oidc: true, hasPassword: false }),
    ];
    this.groups = [
      { id: 1, name: 'Sekretariat', description: 'Gemeinsames Postfach', createdAt: ago(60), members: [2, 3] },
      { id: 2, name: 'Geschaeftsfuehrung', description: '', createdAt: ago(30), members: [6] },
    ];
    this.sessions.set(1, [
      { id: 11, userId: 1, kind: 'web', deviceName: '', ip: '203.0.113.7', userAgent: 'Firefox 140 / Linux', mfa: true, loginMethod: 'password', createdAt: ago(0), expiresAt: ago(-30), lastSeenAt: ago(0), current: true },
    ]);
    this.sessions.set(2, [
      { id: 21, userId: 2, kind: 'web', deviceName: '', ip: '198.51.100.20', userAgent: 'Chrome 138 / Windows', mfa: true, loginMethod: 'password', createdAt: ago(2), expiresAt: ago(-28), lastSeenAt: ago(1), current: false },
      { id: 22, userId: 2, kind: 'device', deviceName: 'Pixel 9', ip: '198.51.100.21', userAgent: 'TuxMail Android', mfa: true, loginMethod: 'password', createdAt: ago(9), expiresAt: ago(-21), lastSeenAt: ago(3), current: false },
    ]);
    this.invites = [{ id: 1, role: 'user', email: 'neu@example.de', createdBy: 1, createdAt: ago(1), expiresAt: ago(-6) }];
    this.log('login', 'ben', false, 'wrong password', 'ben');
    this.log('user.create', 'carla', true, 'role=user');
  }

  private async wait<T>(v: T): Promise<T> {
    if (this.latency > 0) await new Promise((r) => setTimeout(r, this.latency));
    return structuredClone(v);
  }

  private log(action: string, target: string, success = true, details = '', actor = 'admin') {
    this.audit.unshift({ id: (this.audit[0]?.id ?? 0) + 1, at: this.now().toISOString(), actorId: actor === 'admin' ? 1 : undefined, actor, action, target, ip: '203.0.113.7', success, details });
  }

  private user(id: number): AdminUser {
    const u = this.users.find((x) => x.id === id);
    if (!u) throw new ApiError('not found', 404, 'not_found');
    return u;
  }

  private ensureNotLastAdmin(u: AdminUser) {
    if (u.role === 'admin' && !u.disabled && !this.users.some((x) => x.id !== u.id && x.role === 'admin' && !x.disabled)) {
      throw new ApiError('cannot remove the last administrator', 409, 'last_admin');
    }
  }

  private checkPassword(pw: string) {
    if (pw.length < MOCK_MIN_PASSWORD) throw new ApiError(`at least ${MOCK_MIN_PASSWORD} characters required`, 400, 'weak_password');
  }

  async me(): Promise<AdminMe> {
    return this.wait({ id: MockAdminClient.ME, username: 'admin', role: this.role, mfaSetupRequired: false });
  }
  async config(): Promise<AdminServerConfig> {
    return this.wait({ registration: 'invite', passwordMinLength: MOCK_MIN_PASSWORD });
  }
  private requireAdmin() {
    if (this.role !== 'admin') throw new ApiError('administrator role required', 403, 'forbidden');
  }
  async listUsers() {
    this.requireAdmin();
    return this.wait([...this.users].sort((a, b) => a.username.localeCompare(b.username)));
  }
  async createUser(input: NewAdminUser) {
    this.requireAdmin();
    const username = input.username.trim();
    if (!/^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$/.test(username)) throw new ApiError('username must be 1-64 characters of letters, digits, ".", "_", "-", "@"', 400, 'invalid_input');
    if (this.users.some((x) => x.username.toLowerCase() === username.toLowerCase() || (input.email && x.email === input.email))) throw new ApiError('username or email already in use', 409, 'exists');
    const generated = input.password ? undefined : randomPassword(20);
    this.checkPassword(input.password || generated!);
    const user = normalizeAdminUser({ id: this.nextId++, username, email: input.email.trim(), displayName: input.displayName.trim(), role: input.role, createdAt: this.now().toISOString() });
    this.users.push(user);
    this.log('user.create', username, true, `role=${input.role}`);
    return this.wait({ user, password: generated });
  }
  async updateUser(id: number, patch: AdminUserPatch) {
    this.requireAdmin();
    const u = this.user(id);
    const next = { ...u };
    if (patch.displayName !== undefined) next.displayName = patch.displayName.trim();
    if (patch.email !== undefined) next.email = patch.email.trim();
    if (patch.role !== undefined) {
      if (patch.role !== 'admin') this.ensureNotLastAdmin(u);
      next.role = patch.role;
      this.log('user.set_role', u.username, true, `role=${patch.role}`);
    }
    if (patch.disabled !== undefined) {
      if (patch.disabled) this.ensureNotLastAdmin(u);
      next.disabled = patch.disabled;
      if (patch.disabled) this.sessions.delete(id);
      this.log(patch.disabled ? 'user.disable' : 'user.enable', u.username);
    }
    Object.assign(u, next);
    return this.wait(u);
  }
  async deleteUser(id: number, keepData: boolean) {
    this.requireAdmin();
    const u = this.user(id);
    this.ensureNotLastAdmin(u);
    this.users = this.users.filter((x) => x.id !== id);
    for (const g of this.groups) g.members = g.members.filter((m) => m !== id);
    this.sessions.delete(id);
    this.log('user.delete', u.username, true, `remove_data=${!keepData}`);
    return this.wait(undefined);
  }
  async resetPassword(id: number) {
    this.requireAdmin();
    const u = this.user(id);
    const pw = randomPassword(20);
    u.hasPassword = true;
    this.sessions.delete(id);
    this.log('user.set_password', u.username);
    return this.wait(pw);
  }
  async reset2FA(id: number) {
    this.requireAdmin();
    const u = this.user(id);
    u.totpEnabled = false;
    this.log('user.reset_totp', u.username);
    return this.wait(undefined);
  }
  async unlock(id: number) {
    this.requireAdmin();
    const u = this.user(id);
    u.locked = false;
    u.lockedUntil = undefined;
    u.underPressure = false;
    u.pressureUntil = undefined;
    this.log('user.unlock', u.username);
    return this.wait(undefined);
  }
  async listSessions(id: number) {
    this.requireAdmin();
    this.user(id);
    return this.wait(this.sessions.get(id) ?? []);
  }
  async revokeSessions(id: number) {
    this.requireAdmin();
    this.user(id);
    this.sessions.delete(id);
    return this.wait(undefined);
  }
  async revokeSession(id: number, sessionId: number) {
    this.requireAdmin();
    this.sessions.set(id, (this.sessions.get(id) ?? []).filter((s) => s.id !== sessionId));
    return this.wait(undefined);
  }
  async listGroups() {
    this.requireAdmin();
    return this.wait(this.groups);
  }
  async createGroup(name: string, description: string) {
    this.requireAdmin();
    const n = name.trim();
    if (!n || n.length > 100) throw new ApiError('group name must be 1-100 characters', 400, 'invalid_input');
    if (this.groups.some((g) => g.name === n)) throw new ApiError(`group "${n}" exists`, 409, 'exists');
    const g: AdminGroup = { id: this.nextId++, name: n, description, createdAt: this.now().toISOString(), members: [] };
    this.groups.push(g);
    this.log('group.create', n);
    return this.wait(g);
  }
  async deleteGroup(id: number) {
    this.requireAdmin();
    this.groups = this.groups.filter((g) => g.id !== id);
    return this.wait(undefined);
  }
  async addMember(groupId: number, userId: number) {
    this.requireAdmin();
    const g = this.groups.find((x) => x.id === groupId);
    if (!g) throw new ApiError('not found', 404, 'not_found');
    this.user(userId);
    if (!g.members.includes(userId)) g.members.push(userId);
    return this.wait(undefined);
  }
  async removeMember(groupId: number, userId: number) {
    this.requireAdmin();
    const g = this.groups.find((x) => x.id === groupId);
    if (g) g.members = g.members.filter((m) => m !== userId);
    return this.wait(undefined);
  }
  async listInvites() {
    this.requireAdmin();
    return this.wait([...this.invites].reverse());
  }
  async createInvite(input: NewAdminInvite) {
    this.requireAdmin();
    const hours = input.expiresInHours && input.expiresInHours > 0 ? input.expiresInHours : 24 * 7;
    const inv: AdminInvite = {
      id: this.nextId++,
      email: input.email?.trim() || undefined,
      role: input.role,
      createdBy: MockAdminClient.ME,
      createdAt: this.now().toISOString(),
      expiresAt: new Date(this.now().getTime() + hours * 3_600_000).toISOString(),
    };
    this.invites.push(inv);
    this.log('invite.create', inv.email ?? '', true, `role=${inv.role}`);
    return this.wait({ invite: inv, url: `${typeof location !== 'undefined' ? location.origin : 'https://mail.example.de'}/register?invite=${randomPassword(32)}` });
  }
  async deleteInvite(id: number) {
    this.requireAdmin();
    this.invites = this.invites.filter((i) => i.id !== id);
    this.log('invite.delete', String(id));
    return this.wait(undefined);
  }
  async auditLog(q: AuditQuery = {}) {
    this.requireAdmin();
    const lower = (s: string) => s.toLowerCase();
    let list = this.audit;
    if (q.before) list = list.filter((e) => e.id < q.before!);
    if (q.action?.trim()) list = list.filter((e) => lower(e.action).startsWith(lower(q.action!.trim())));
    if (q.actor?.trim()) list = list.filter((e) => lower(e.actor).startsWith(lower(q.actor!.trim())));
    if (q.onlyFailed) list = list.filter((e) => !e.success);
    return this.wait(list.slice(0, q.limit && q.limit > 0 ? Math.min(q.limit, 1000) : 100));
  }
}

/**
 * The admin client for a transport: web client against `tuxmail serve` only.
 * No client in the desktop shell (Wails) and in the native app, which manage
 * the server from its web UI.
 */
export function createAdminClient(root: Api, opts: HttpTransportOptions = {}, native: boolean = isNativeShell()): AdminClient | null {
  if (native) return null;
  if (root.kind === 'http') return new HttpAdminClient(opts);
  if (root.kind === 'mock') return new MockAdminClient();
  return null;
}
