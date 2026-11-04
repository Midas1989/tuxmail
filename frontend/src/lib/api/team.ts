// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Shared team mailboxes (server mode, KONZEPT 8.14).
//
// A team mailbox is synced once on the server into its own store. The
// frontend reads it through the same API as a personal mailbox, mounted
// under /api/team/{teamId}/… (internal/server/api/team_routes.go): while a
// team mailbox is open, the app's transport is switched to a scoped Api
// (createSwitchableApi) so the classic, hybrid and chat views work
// unchanged. Collaboration (assignment, internal comments, @mentions,
// presence, drafts, stats) and administration use TeamClient.
//
// Routes (besides the read-through ones):
//   GET    /api/team/mailboxes                               -> TeamMailbox[]
//   GET    /api/team/mentions?unread=1                       -> TeamMention[]
//   POST   /api/team/mentions/read                           {mailboxId, conversationId}
//   GET    /api/team/{id}/members | /stats
//   GET    /api/team/{id}/conversations?view=mine|unassigned|open|done
//   GET    /api/team/{id}/conversations/{cid}                -> TeamConversationDetail
//   PATCH  /api/team/{id}/conversations/{cid}/assignment     TeamAssignmentPatch
//   GET/POST /api/team/{id}/conversations/{cid}/comments; DELETE /api/team/{id}/comments/{commentId}
//   POST   /api/team/{id}/conversations/{cid}/presence       {state}
//   GET    /api/team/{id}/conversations/{cid}/drafts
//   GET/PUT /api/team/{id}/grants; GET /api/team/directory
//   GET/POST /api/admin/team/mailboxes; PATCH/DELETE /api/admin/team/mailboxes/{id}

import type { Api, ConversationQuery, MessageQuery, ConversationScope, MessageScope } from './api';
import { ApiError } from './api';
import { HttpTransport, type HttpTransportOptions } from './http';
import { MockTransport } from './mock';
import type { Account, ApiEvent, Conversation, NewAccountInput, Page } from '../types';

export type TeamRole = 'read' | 'reply' | 'assign' | 'manage';
export type TeamStatus = 'open' | 'in_progress' | 'waiting' | 'done';
export type TeamPriority = 'low' | 'normal' | 'high' | 'urgent';
/** Sidebar views: '' = all. */
export type TeamView = '' | 'mine' | 'unassigned' | 'done';
export type PresenceState = 'viewing' | 'replying';

export const ROLE_LEVEL: Record<TeamRole, number> = { read: 1, reply: 2, assign: 3, manage: 4 };
export function roleAllows(role: TeamRole | null | undefined, need: TeamRole): boolean {
  return !!role && ROLE_LEVEL[role] >= ROLE_LEVEL[need];
}

export interface TeamMailbox {
  id: number;
  name: string;
  address: string;
  description: string;
  kind: 'mail' | 'messenger';
  sharedDrafts: boolean;
  disabled: boolean;
  role: TeamRole;
  mine: number;
  unassigned: number;
  open: number;
  mentions: number;
  /** The asking user. */
  userId: number;
}

export interface TeamAssignment {
  mailboxId: number;
  conversationId: number;
  assigneeId?: number;
  assigneeName?: string;
  status: TeamStatus;
  priority: TeamPriority;
  dueAt?: string;
  openedAt: string;
  firstResponseAt?: string;
  doneAt?: string;
  updatedAt: string;
  comments: number;
}

export interface TeamAssignmentPatch {
  /** 0 unassigns. */
  assigneeId?: number;
  status?: TeamStatus;
  priority?: TeamPriority;
  /** RFC 3339 or YYYY-MM-DD; '' clears. */
  dueAt?: string;
}

export interface TeamComment {
  id: number;
  mailboxId: number;
  conversationId: number;
  authorId?: number;
  authorName: string;
  body: string;
  mentions: number[];
  createdAt: string;
}

export interface TeamMention extends TeamComment {
  mailboxName: string;
  readAt?: string;
}

export interface TeamMember {
  userId: number;
  username: string;
  displayName: string;
  role: TeamRole;
}

export interface TeamPresence {
  mailboxId: number;
  conversationId: number;
  userId: number;
  userName: string;
  state: PresenceState;
  since: string;
  expiresAt: string;
}

export interface TeamDraftInfo {
  userId: number;
  userName: string;
  /** Only present when the mailbox shares drafts. */
  body?: { text: string };
  updatedAt: string;
}

export type TeamConversation = Conversation & { team: TeamAssignment };

export interface TeamConversationDetail extends TeamConversation {
  presence: TeamPresence[];
  drafts: TeamDraftInfo[];
  role: TeamRole;
}

export interface TeamStats {
  total: number;
  open: number;
  inProgress: number;
  waiting: number;
  done: number;
  unassigned: number;
  overdue: number;
  medianFirstResponseSeconds: number;
  responded: number;
  byAssignee: { userId: number; name: string; open: number; done: number }[];
}

export interface TeamGrant {
  mailboxId: number;
  userId?: number;
  groupId?: number;
  role: TeamRole;
  name: string;
}

export interface TeamMailboxInput {
  name: string;
  address: string;
  description: string;
  kind?: 'mail' | 'messenger';
  sharedDrafts: boolean;
  disabled?: boolean;
}

export interface TeamAdminEntry {
  mailbox: Omit<TeamMailbox, 'role' | 'mine' | 'unassigned' | 'open' | 'mentions' | 'userId'>;
  grants: TeamGrant[];
  accounts: Account[];
  running: boolean;
}

export interface TeamDirectory {
  users: { id: number; name: string; displayName?: string }[];
  groups: { id: number; name: string }[];
}

/** WebSocket events of team mailboxes (internal/app/team_events.go). */
export type TeamApiEvent =
  | { type: 'team.changed'; team: { mailboxId: number; conversationId?: number; kind: string; actorId?: number; actorName?: string; assignment?: TeamAssignment } }
  | { type: 'team.presence'; team: { mailboxId: number; conversationId: number; kind: 'presence'; presence?: TeamPresence[] } }
  | { type: 'team.notice'; team: { kind: 'mention' | 'assigned' | 'workflow'; mailboxId: number; mailboxName: string; conversationId: number; actorName: string; text: string } }
  | { type: 'team.mailboxes'; team: { mailboxId: number } };

export function asTeamEvent(e: ApiEvent | TeamApiEvent): TeamApiEvent | null {
  return typeof e.type === 'string' && e.type.startsWith('team.') && 'team' in e ? (e as TeamApiEvent) : null;
}

export interface TeamClient {
  listMailboxes(): Promise<TeamMailbox[]>;
  mentions(unreadOnly: boolean): Promise<TeamMention[]>;
  markMentionsRead(mailboxId: number, conversationId: number): Promise<void>;
  members(mailboxId: number): Promise<TeamMember[]>;
  stats(mailboxId: number): Promise<TeamStats>;
  conversation(mailboxId: number, conversationId: number): Promise<TeamConversationDetail>;
  updateAssignment(mailboxId: number, conversationId: number, patch: TeamAssignmentPatch): Promise<TeamAssignment>;
  comments(mailboxId: number, conversationId: number): Promise<TeamComment[]>;
  addComment(mailboxId: number, conversationId: number, body: string): Promise<TeamComment>;
  deleteComment(mailboxId: number, commentId: number): Promise<void>;
  presence(mailboxId: number, conversationId: number, state: PresenceState | ''): Promise<TeamPresence[]>;
  /** Api of the mailbox for the normal views; view filters conversation lists. */
  scoped(mailboxId: number, view: TeamView): Api;
  // Administration
  adminList(): Promise<TeamAdminEntry[]>;
  createMailbox(mailbox: TeamMailboxInput, account?: NewAccountInput): Promise<void>;
  updateMailbox(id: number, mailbox: Partial<TeamMailboxInput>): Promise<void>;
  deleteMailbox(id: number): Promise<void>;
  grants(mailboxId: number): Promise<TeamGrant[]>;
  setGrant(mailboxId: number, who: { userId?: number; groupId?: number }, role: TeamRole | ''): Promise<TeamGrant[]>;
  directory(): Promise<TeamDirectory>;
}

/** Methods of Api that read or change mailbox data and therefore go to the team mailbox while one is open. */
export const TEAM_METHODS = new Set<string>([
  'listAccounts', 'listIdentities', 'listSignatures', 'listPersons', 'frequentContacts', 'listFolders',
  'listMessages', 'getMessage', 'conversationForMessage', 'move', 'setFlags',
  'listConversations', 'getConversation', 'listConversationItems', 'markConversationRead', 'updateConversation',
  'reactToItem', 'getDraft', 'saveDraft', 'deleteDraft', 'send', 'uploadAttachment', 'listSnippets', 'saveSnippet', 'deleteSnippet',
  'listTags', 'setItemTags', 'search',
]);

/**
 * An Api whose mailbox methods (TEAM_METHODS) go to a team mailbox while one
 * is active; everything else (preferences, events, cloud, calendar …) stays on
 * the personal root Api.
 */
export function createSwitchableApi(root: Api): { api: Api; setTarget(target: Api | null): void; target(): Api | null } {
  let scoped: Api | null = null;
  const api = new Proxy(root, {
    get(r, prop, receiver) {
      const src = scoped && typeof prop === 'string' && TEAM_METHODS.has(prop) ? scoped : r;
      const v = Reflect.get(src, prop, src === r ? receiver : src);
      return typeof v === 'function' ? v.bind(src) : v;
    },
  }) as Api;
  return { api, setTarget: (t) => (scoped = t), target: () => scoped };
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

function csrf(): string | undefined {
  if (typeof document === 'undefined') return undefined;
  const m = document.cookie.match(/(?:^|;\s*)(?:__Host-)?tuxmail_csrf=([^;]+)/);
  if (m) return decodeURIComponent(m[1]);
  return document.querySelector<HTMLMetaElement>('meta[name="tuxmail-csrf"]')?.content || undefined;
}

/** Path of a personal API route inside a team mailbox (view and thread grouping applied). */
export function teamPath(mailboxId: number, view: TeamView, path: string): string {
  if (!path.startsWith('/api/')) return path;
  let p = `/api/team/${mailboxId}/` + path.slice(5);
  // Team conversations are always threads (assignments belong to threads).
  p = p.replace(/([?&])grouping=person\b/, '$1grouping=thread');
  if (view && /^\/api\/team\/\d+\/conversations(\?|$)/.test(p)) p += (p.includes('?') ? '&' : '?') + 'view=' + view;
  return p;
}

export class HttpTeamClient implements TeamClient {
  constructor(private readonly opts: HttpTransportOptions = {}) {}

  private async req<T>(method: string, path: string, body?: unknown): Promise<T> {
    const h: Record<string, string> = { Accept: 'application/json' };
    const bearer = this.opts.authToken?.();
    if (bearer) h.Authorization = `Bearer ${bearer}`;
    else {
      const c = csrf();
      if (c) h['X-CSRF-Token'] = c;
    }
    if (body !== undefined) h['Content-Type'] = 'application/json';
    const f = this.opts.fetchImpl ?? ((...a: Parameters<typeof fetch>) => fetch(...a));
    let res: Response;
    try {
      res = await f((this.opts.baseUrl ?? '').replace(/\/$/, '') + path, {
        method, headers: h, body: body !== undefined ? JSON.stringify(body) : undefined, credentials: bearer ? 'omit' : 'same-origin',
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
      throw new ApiError(msg, res.status, code);
    }
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  listMailboxes() { return this.req<TeamMailbox[]>('GET', '/api/team/mailboxes'); }
  mentions(unreadOnly: boolean) { return this.req<TeamMention[]>('GET', '/api/team/mentions' + (unreadOnly ? '?unread=1' : '')); }
  markMentionsRead(mailboxId: number, conversationId: number) { return this.req<void>('POST', '/api/team/mentions/read', { mailboxId, conversationId }); }
  members(id: number) { return this.req<TeamMember[]>('GET', `/api/team/${id}/members`); }
  stats(id: number) { return this.req<TeamStats>('GET', `/api/team/${id}/stats`); }
  conversation(id: number, cid: number) { return this.req<TeamConversationDetail>('GET', `/api/team/${id}/conversations/${cid}`); }
  updateAssignment(id: number, cid: number, patch: TeamAssignmentPatch) {
    return this.req<TeamAssignment>('PATCH', `/api/team/${id}/conversations/${cid}/assignment`, patch);
  }
  comments(id: number, cid: number) { return this.req<TeamComment[]>('GET', `/api/team/${id}/conversations/${cid}/comments`); }
  addComment(id: number, cid: number, body: string) { return this.req<TeamComment>('POST', `/api/team/${id}/conversations/${cid}/comments`, { body }); }
  deleteComment(id: number, commentId: number) { return this.req<void>('DELETE', `/api/team/${id}/comments/${commentId}`); }
  presence(id: number, cid: number, state: PresenceState | '') {
    return this.req<TeamPresence[]>('POST', `/api/team/${id}/conversations/${cid}/presence`, { state });
  }
  scoped(id: number, view: TeamView): Api {
    return new HttpTransport({ ...this.opts, rewritePath: (p) => teamPath(id, view, p) });
  }
  adminList() { return this.req<TeamAdminEntry[]>('GET', '/api/admin/team/mailboxes'); }
  async createMailbox(mailbox: TeamMailboxInput, account?: NewAccountInput) { await this.req('POST', '/api/admin/team/mailboxes', { mailbox, account }); }
  async updateMailbox(id: number, mailbox: Partial<TeamMailboxInput>) { await this.req('PATCH', `/api/admin/team/mailboxes/${id}`, mailbox); }
  deleteMailbox(id: number) { return this.req<void>('DELETE', `/api/admin/team/mailboxes/${id}`); }
  grants(id: number) { return this.req<TeamGrant[]>('GET', `/api/team/${id}/grants`); }
  setGrant(id: number, who: { userId?: number; groupId?: number }, role: TeamRole | '') {
    return this.req<TeamGrant[]>('PUT', `/api/team/${id}/grants`, { ...who, role });
  }
  directory() { return this.req<TeamDirectory>('GET', '/api/team/directory'); }
}

// ---------------------------------------------------------------------------
// Mock (demo / development): the work account of the demo data acts as the
// shared mailbox "Kanzlei – Sekretariat" with three colleagues.
// ---------------------------------------------------------------------------

const TEAM_ACCOUNT = 2;
const ME_USER = 1;

interface MockState {
  mailboxes: TeamMailbox[];
  members: TeamMember[];
  assignments: Map<number, TeamAssignment>;
  comments: TeamComment[];
  grants: TeamGrant[];
  nextId: number;
}

/** Wraps a MockTransport so it looks like a team store holding only the work account. */
function mockScoped(inner: MockTransport, team: MockTeamClient, mailboxId: number, view: TeamView): Api {
  const folderFor = async (role: string): Promise<number | null> => {
    const fs = await inner.listFolders(TEAM_ACCOUNT);
    return fs.find((f) => f.role === (role === 'unread' || role === 'flagged' ? 'inbox' : role))?.id ?? null;
  };
  const convScope = async (s: ConversationScope): Promise<ConversationScope> => {
    if (s.kind === 'all' || s.kind === 'account') return { kind: 'account', accountId: TEAM_ACCOUNT };
    if (s.kind === 'unified') {
      const f = await folderFor(s.role);
      return f != null ? { kind: 'folder', folderId: f } : { kind: 'account', accountId: TEAM_ACCOUNT };
    }
    return s;
  };
  const msgScope = async (s: MessageScope): Promise<MessageScope> => {
    if (s.kind === 'unified') {
      const f = await folderFor(s.role);
      if (f != null) return { kind: 'folder', folderId: f };
    }
    return s;
  };
  const overrides: Partial<Api> = {
    async listAccounts() {
      const mb = team.state.mailboxes.find((m) => m.id === mailboxId);
      const accs = await inner.listAccounts();
      return accs.filter((a) => a.id === TEAM_ACCOUNT).map((a) => ({ ...a, displayName: mb?.name ?? a.displayName, extra: { ...a.extra, address: mb?.address ?? '' } }));
    },
    async listIdentities() {
      return (await inner.listIdentities()).filter((i) => i.accountId === TEAM_ACCOUNT);
    },
    async listMessages(q: MessageQuery) {
      return inner.listMessages({ ...q, scope: await msgScope(q.scope) });
    },
    async listConversations(q: ConversationQuery): Promise<Page<Conversation>> {
      const page = await inner.listConversations({ ...q, grouping: 'thread', scope: await convScope(q.scope), limit: view ? 500 : q.limit, offset: view ? 0 : q.offset });
      let items: TeamConversation[] = page.items.map((c) => ({ ...c, team: team.assignmentOf(mailboxId, c.id) }));
      if (view) {
        items = items.filter((c) => matchesView(view, c.team));
        return { items: items.slice(q.offset, q.offset + q.limit), total: items.length, offset: q.offset };
      }
      return { ...page, items };
    },
    async getConversation(id: number) {
      return { ...(await inner.getConversation(id)), team: team.assignmentOf(mailboxId, id) } as TeamConversation;
    },
  };
  return new Proxy(inner, {
    get(target, prop, receiver) {
      if (typeof prop === 'string' && prop in overrides) return (overrides as Record<string, unknown>)[prop];
      const v = Reflect.get(target, prop, receiver);
      return typeof v === 'function' ? v.bind(target) : v;
    },
  }) as Api;
}

export function matchesView(view: TeamView, a: TeamAssignment | undefined, me = ME_USER): boolean {
  const st = a?.status ?? 'open';
  const who = a?.assigneeId ?? 0;
  if (view === 'mine') return who === me && st !== 'done';
  if (view === 'unassigned') return !who && st !== 'done';
  if (view === 'done') return st === 'done';
  return true;
}

export class MockTeamClient implements TeamClient {
  readonly state: MockState;
  private inner: MockTransport | null = null;
  private readonly emit: (e: TeamApiEvent) => void;
  private readonly now: () => Date;

  constructor(emit: (e: TeamApiEvent) => void = () => {}, now: () => Date = () => new Date()) {
    this.emit = emit;
    this.now = now;
    const iso = (h: number) => new Date(this.now().getTime() - h * 3600_000).toISOString();
    this.state = {
      mailboxes: [
        { id: 1, name: 'Sekretariat', address: 'sekretariat@kanzlei-sommer.de', description: 'Mandantenanfragen', kind: 'mail', sharedDrafts: true, disabled: false,
          role: 'manage', mine: 0, unassigned: 0, open: 0, mentions: 1, userId: ME_USER },
        { id: 2, name: 'Bewerbungen', address: 'jobs@kanzlei-sommer.de', description: '', kind: 'mail', sharedDrafts: false, disabled: false,
          role: 'read', mine: 0, unassigned: 2, open: 2, mentions: 0, userId: ME_USER },
      ],
      members: [
        { userId: ME_USER, username: 'jonas', displayName: 'Jonas Becker', role: 'manage' },
        { userId: 2, username: 'anna', displayName: 'Anna Schulz', role: 'assign' },
        { userId: 3, username: 'mehmet', displayName: 'Mehmet Yilmaz', role: 'reply' },
        { userId: 4, username: 'lena', displayName: 'Lena Vogt', role: 'read' },
      ],
      assignments: new Map(),
      comments: [],
      grants: [
        { mailboxId: 1, groupId: 1, role: 'reply', name: 'Sekretariat' },
        { mailboxId: 1, userId: 2, role: 'assign', name: 'anna' },
        { mailboxId: 1, userId: 4, role: 'read', name: 'lena' },
      ],
      nextId: 100,
    };
    this.iso = iso;
  }

  private iso: (hoursAgo: number) => string;

  /** The shared MockTransport of the team store (created on first use). */
  private store(): MockTransport {
    if (!this.inner) {
      this.inner = new MockTransport({ latencyMs: 40 });
      void this.seed();
    }
    return this.inner;
  }

  private seeded: Promise<void> | null = null;
  private seed(): Promise<void> {
    if (this.seeded) return this.seeded;
    this.seeded = (async () => {
      const page = await this.inner!.listConversations({ scope: { kind: 'account', accountId: TEAM_ACCOUNT }, grouping: 'thread', offset: 0, limit: 50 });
      const ids = page.items.map((c) => c.id);
      const set = (i: number, a: Partial<TeamAssignment>) => {
        if (ids[i] == null) return;
        this.state.assignments.set(ids[i], { ...this.assignmentOf(1, ids[i]), ...a });
      };
      set(0, { assigneeId: 2, assigneeName: 'Anna Schulz', status: 'in_progress', priority: 'high', dueAt: this.iso(-20), firstResponseAt: this.iso(3) });
      set(1, { assigneeId: ME_USER, assigneeName: 'Jonas Becker', status: 'waiting', firstResponseAt: this.iso(20) });
      set(2, { assigneeId: ME_USER, assigneeName: 'Jonas Becker', status: 'in_progress', priority: 'urgent', dueAt: this.iso(-4) });
      set(4, { status: 'done', assigneeId: 3, assigneeName: 'Mehmet Yilmaz', doneAt: this.iso(30), firstResponseAt: this.iso(40) });
      if (ids[0] != null) {
        this.state.comments.push(
          { id: 1, mailboxId: 1, conversationId: ids[0], authorId: 2, authorName: 'Anna Schulz', body: '@jonas Kannst du kurz auf die Fristen schauen? Ich bereite das Angebot vor.', mentions: [ME_USER], createdAt: this.iso(2) },
          { id: 2, mailboxId: 1, conversationId: ids[0], authorId: ME_USER, authorName: 'Jonas Becker', body: 'Passt, Frist ist der 14.11. – bitte nicht vor Freitag antworten.', mentions: [], createdAt: this.iso(1) },
        );
      }
      this.recount();
    })();
    return this.seeded;
  }

  assignmentOf(mailboxId: number, conversationId: number): TeamAssignment {
    const a = this.state.assignments.get(conversationId);
    if (a) return { ...a, comments: this.state.comments.filter((c) => c.conversationId === conversationId).length };
    return { mailboxId, conversationId, status: 'open', priority: 'normal', openedAt: this.iso(5), updatedAt: this.iso(5),
      comments: this.state.comments.filter((c) => c.conversationId === conversationId).length };
  }

  private recount(): void {
    const mb = this.state.mailboxes[0];
    const all = [...this.state.assignments.values()];
    mb.mine = all.filter((a) => a.assigneeId === ME_USER && a.status !== 'done').length;
    mb.open = 6 - all.filter((a) => a.status === 'done').length;
    mb.unassigned = Math.max(0, mb.open - all.filter((a) => a.assigneeId && a.status !== 'done').length);
  }

  private wait<T>(v: T): Promise<T> {
    return new Promise((r) => setTimeout(() => r(structuredClone(v)), 30));
  }

  async listMailboxes() {
    this.store();
    await this.seed();
    return this.wait(this.state.mailboxes);
  }
  mentions(unreadOnly: boolean) {
    const ms = this.state.comments.filter((c) => c.mentions.includes(ME_USER)).map((c) => ({ ...c, mailboxName: 'Sekretariat' }) as TeamMention);
    return this.wait(unreadOnly ? ms.filter((m) => !m.readAt) : ms);
  }
  async markMentionsRead(mailboxId: number) {
    const mb = this.state.mailboxes.find((m) => m.id === mailboxId);
    if (mb) mb.mentions = 0;
  }
  members(_id: number) { return this.wait(this.state.members); }
  async stats(id: number): Promise<TeamStats> {
    const all = [...this.state.assignments.values()];
    const total = 6;
    const by = new Map<number, { userId: number; name: string; open: number; done: number }>();
    for (const a of all) {
      if (!a.assigneeId) continue;
      const e = by.get(a.assigneeId) ?? { userId: a.assigneeId, name: a.assigneeName ?? '', open: 0, done: 0 };
      if (a.status === 'done') e.done++;
      else e.open++;
      by.set(a.assigneeId, e);
    }
    const done = all.filter((a) => a.status === 'done').length;
    return this.wait({ total, open: total - done - all.filter((a) => a.status === 'in_progress' || a.status === 'waiting').length,
      inProgress: all.filter((a) => a.status === 'in_progress').length, waiting: all.filter((a) => a.status === 'waiting').length, done,
      unassigned: this.state.mailboxes.find((m) => m.id === id)?.unassigned ?? 0, overdue: 1, medianFirstResponseSeconds: 47 * 60, responded: 3,
      byAssignee: [...by.values()] });
  }
  async conversation(id: number, cid: number): Promise<TeamConversationDetail> {
    const c = await this.store().getConversation(cid);
    const a = this.assignmentOf(id, cid);
    const others: TeamPresence[] = a.assigneeId === 2
      ? [{ mailboxId: id, conversationId: cid, userId: 2, userName: 'Anna Schulz', state: 'replying', since: this.iso(0.05), expiresAt: this.iso(-0.01) }]
      : [];
    const drafts: TeamDraftInfo[] = a.assigneeId === 2 ? [{ userId: 2, userName: 'Anna Schulz', body: { text: 'Sehr geehrter Herr Weber, vielen Dank für Ihre Anfrage …' }, updatedAt: this.iso(0.1) }] : [];
    return this.wait({ ...c, team: a, presence: others, drafts, role: this.state.mailboxes.find((m) => m.id === id)?.role ?? 'read' });
  }
  async updateAssignment(id: number, cid: number, patch: TeamAssignmentPatch) {
    const cur = this.assignmentOf(id, cid);
    const next: TeamAssignment = { ...cur, updatedAt: this.now().toISOString() };
    if (patch.assigneeId !== undefined) {
      next.assigneeId = patch.assigneeId || undefined;
      next.assigneeName = this.state.members.find((m) => m.userId === patch.assigneeId)?.displayName;
      if (next.assigneeId && cur.status === 'open' && !patch.status) next.status = 'in_progress';
    }
    if (patch.status) next.status = patch.status;
    if (patch.priority) next.priority = patch.priority;
    if (patch.dueAt !== undefined) next.dueAt = patch.dueAt ? new Date(patch.dueAt.length === 10 ? patch.dueAt + 'T23:59:59Z' : patch.dueAt).toISOString() : undefined;
    this.state.assignments.set(cid, next);
    this.recount();
    this.emit({ type: 'team.changed', team: { mailboxId: id, conversationId: cid, kind: 'assigned', assignment: next } });
    return this.wait(next);
  }
  comments(_id: number, cid: number) { return this.wait(this.state.comments.filter((c) => c.conversationId === cid)); }
  async addComment(id: number, cid: number, body: string) {
    const names = [...body.matchAll(/(?:^|[^\w@.])@([A-Za-z0-9][A-Za-z0-9._-]*)/g)].map((m) => m[1].toLowerCase());
    const mentions = this.state.members.filter((m) => names.includes(m.username) && m.userId !== ME_USER).map((m) => m.userId);
    const c: TeamComment = { id: this.state.nextId++, mailboxId: id, conversationId: cid, authorId: ME_USER, authorName: 'Jonas Becker', body, mentions, createdAt: this.now().toISOString() };
    this.state.comments.push(c);
    this.emit({ type: 'team.changed', team: { mailboxId: id, conversationId: cid, kind: 'comment' } });
    return this.wait(c);
  }
  async deleteComment(_id: number, commentId: number) {
    this.state.comments = this.state.comments.filter((c) => c.id !== commentId);
  }
  async presence(id: number, cid: number) {
    return (await this.conversation(id, cid)).presence;
  }
  scoped(id: number, view: TeamView): Api {
    return mockScoped(this.store(), this, id, view);
  }
  async adminList(): Promise<TeamAdminEntry[]> {
    const accs = (await this.store().listAccounts()).filter((a) => a.id === TEAM_ACCOUNT);
    return this.wait(this.state.mailboxes.map((m) => ({ mailbox: { id: m.id, name: m.name, address: m.address, description: m.description, kind: m.kind, sharedDrafts: m.sharedDrafts, disabled: m.disabled },
      grants: this.state.grants.filter((g) => g.mailboxId === m.id), accounts: m.id === 1 ? accs : [], running: !m.disabled })));
  }
  async createMailbox(input: TeamMailboxInput) {
    this.state.mailboxes.push({ id: this.state.nextId++, kind: 'mail', disabled: false, ...input, role: 'manage', mine: 0, unassigned: 0, open: 0, mentions: 0, userId: ME_USER });
    this.emit({ type: 'team.mailboxes', team: { mailboxId: 0 } });
  }
  async updateMailbox(id: number, patch: Partial<TeamMailboxInput>) {
    const m = this.state.mailboxes.find((x) => x.id === id);
    if (m) Object.assign(m, patch);
  }
  async deleteMailbox(id: number) {
    this.state.mailboxes = this.state.mailboxes.filter((m) => m.id !== id);
    this.emit({ type: 'team.mailboxes', team: { mailboxId: id } });
  }
  grants(id: number) { return this.wait(this.state.grants.filter((g) => g.mailboxId === id)); }
  async setGrant(id: number, who: { userId?: number; groupId?: number }, role: TeamRole | '') {
    this.state.grants = this.state.grants.filter((g) => !(g.mailboxId === id && ((who.userId && g.userId === who.userId) || (who.groupId && g.groupId === who.groupId))));
    if (role) {
      const dir = await this.directory();
      const name = who.userId ? dir.users.find((u) => u.id === who.userId)?.name : dir.groups.find((g) => g.id === who.groupId)?.name;
      this.state.grants.push({ mailboxId: id, ...who, role, name: name ?? '' });
    }
    return this.grants(id);
  }
  directory(): Promise<TeamDirectory> {
    return this.wait({
      users: this.state.members.map((m) => ({ id: m.userId, name: m.username, displayName: m.displayName })).concat([{ id: 5, name: 'paul', displayName: 'Paul Brandt' }]),
      groups: [{ id: 1, name: 'Sekretariat' }, { id: 2, name: 'Partner' }],
    });
  }
}

/** The team client for a transport kind (desktop builds have no team mailboxes). */
export function createTeamClient(root: Api, opts: HttpTransportOptions = {}, emit?: (e: TeamApiEvent) => void): TeamClient | null {
  if (root.kind === 'http') return new HttpTeamClient(opts);
  if (root.kind === 'mock') return new MockTeamClient(emit);
  return null;
}
