// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Team mailbox workspace (KONZEPT 8.14): which team mailbox is open, its
// view (Mir zugewiesen, Nicht zugewiesen, Erledigt), the collaboration state
// of the selected conversation (assignment, comments, presence, drafts of
// colleagues) and the presence heartbeat. Opening a team mailbox switches
// the mailbox methods of `app.api` to the team mailbox (createSwitchableApi),
// so every view (classic, hybrid, chat) shows it without own components.

import type { Api } from '../api/api';
import {
  asTeamEvent,
  createTeamClient,
  roleAllows,
  type PresenceState,
  type TeamAssignment,
  type TeamAssignmentPatch,
  type TeamApiEvent,
  type TeamClient,
  type TeamComment,
  type TeamConversationDetail,
  type TeamMailbox,
  type TeamMember,
  type TeamPresence,
  type TeamRole,
  type TeamStats,
  type TeamView,
} from '../api/team';
import type { HttpTransportOptions } from '../api/http';
import type { AppState } from './app.svelte';
import { prefs } from './prefs.svelte';
import { t } from '../i18n/index.svelte';

export const HEARTBEAT_MS = 20_000;
/** Typing in the composer marks the user as replying for this long. */
export const REPLYING_MS = 30_000;

interface Workspace {
  setTarget(target: Api | null): void;
}

export class TeamState {
  readonly client: TeamClient | null;
  mailboxes = $state<TeamMailbox[]>([]);
  activeId = $state<number | null>(null);
  view = $state<TeamView>('');
  members = $state<TeamMember[]>([]);
  /** Collaboration state of the open conversation. */
  detail = $state<TeamConversationDetail | null>(null);
  comments = $state<TeamComment[]>([]);
  stats = $state<TeamStats | null>(null);
  statsOpen = $state(false);
  private replyingUntil = 0;
  private beat: ReturnType<typeof setInterval> | null = null;
  private beatConv: number | null = null;
  private reloadTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly root: Api,
    private readonly workspace: Workspace,
    private readonly app: AppState,
    httpOpts: HttpTransportOptions = {},
  ) {
    this.client = createTeamClient(root, httpOpts, (e) => this.onEvent(e));
  }

  get available(): boolean {
    return this.client !== null && this.mailboxes.length > 0;
  }
  get active(): TeamMailbox | null {
    return this.mailboxes.find((m) => m.id === this.activeId) ?? null;
  }
  get role(): TeamRole | null {
    return this.active?.role ?? null;
  }
  can(need: TeamRole): boolean {
    return roleAllows(this.role, need);
  }

  async init(): Promise<void> {
    if (!this.client) return;
    this.root.subscribe((e) => {
      const te = asTeamEvent(e);
      if (te) this.onEvent(te);
    });
    await this.loadMailboxes();
    if (typeof window !== 'undefined') {
      window.addEventListener('hashchange', () => void this.followHash());
      await this.followHash();
    }
  }

  /** Deep link of a notification (service worker click): #/mail?team=1&conversation=7. */
  private async followHash(): Promise<void> {
    const q = new URLSearchParams(location.hash.split('?')[1] ?? '');
    const tid = Number(q.get('team'));
    if (!tid || !this.mailboxes.some((m) => m.id === tid)) return;
    if (this.activeId !== tid) await this.enter(tid);
    const cid = Number(q.get('conversation'));
    if (cid) await this.app.selectConversation(cid);
  }

  async loadMailboxes(): Promise<void> {
    if (!this.client) return;
    try {
      this.mailboxes = await this.client.listMailboxes();
    } catch {
      this.mailboxes = []; // not logged in to a server or no permission
    }
    if (this.activeId != null && !this.active) await this.leave();
  }

  /** Opens a team mailbox (or switches its view) in the normal views. */
  async enter(id: number, view: TeamView = ''): Promise<void> {
    if (!this.client) return;
    const switching = this.activeId !== id;
    this.activeId = id;
    this.view = view;
    this.workspace.setTarget(this.client.scoped(id, view));
    // Views are conversation lists; the classic list shows messages.
    if (view && prefs.viewMode === 'classic') prefs.viewMode = 'hybrid';
    if (switching) {
      this.detail = null;
      this.comments = [];
      this.stats = null;
      void this.client.members(id).then((m) => (this.members = m)).catch(() => (this.members = []));
      await this.reloadWorkspace();
    } else {
      await this.selectInbox();
    }
  }

  /** Back to the personal mailboxes. */
  async leave(): Promise<void> {
    this.stopHeartbeat();
    this.activeId = null;
    this.view = '';
    this.detail = null;
    this.comments = [];
    this.workspace.setTarget(null);
    await this.reloadWorkspace();
  }

  /** Reloads accounts, folders, identities and lists through the (switched) api. */
  private async reloadWorkspace(): Promise<void> {
    const a = this.app;
    const [accounts, identities, signatures, persons, snippets] = await Promise.all([
      a.api.listAccounts(), a.api.listIdentities(), a.api.listSignatures(), a.api.listPersons(), a.api.listSnippets().catch(() => a.snippets),
    ]);
    a.folders = {};
    a.accounts = accounts.sort((x, y) => x.sortOrder - y.sortOrder);
    a.identities = identities;
    a.signatures = signatures;
    a.persons = persons;
    a.snippets = snippets;
    await Promise.all(a.mailAccounts.map((acc) => a.reloadFolders(acc.id)));
    await this.selectInbox();
  }

  /** Shows the inbox of the workspace without leaving the current view mode (select() leaves the chat view). */
  private async selectInbox(): Promise<void> {
    const mode = prefs.viewMode;
    await this.app.select({ kind: 'unified', role: 'inbox' });
    if (prefs.viewMode !== mode) await this.app.setViewMode(mode);
  }

  // -------------------------------------------------------------------------
  // Conversation collaboration
  // -------------------------------------------------------------------------

  /** Loads assignment, presence, drafts and comments; starts the heartbeat. */
  async open(conversationId: number | null): Promise<void> {
    if (!this.client || this.activeId == null || conversationId == null) {
      this.stopHeartbeat();
      this.detail = null;
      this.comments = [];
      return;
    }
    const mid = this.activeId;
    const [detail, comments] = await Promise.all([this.client.conversation(mid, conversationId), this.client.comments(mid, conversationId)]);
    if (this.activeId !== mid) return;
    this.detail = detail;
    this.comments = comments;
    const mb = this.active;
    if (mb && mb.mentions > 0) void this.loadMailboxes();
    this.startHeartbeat(conversationId);
  }

  private presenceState(): PresenceState {
    return Date.now() < this.replyingUntil && this.can('reply') ? 'replying' : 'viewing';
  }

  /** Called on input in a composer of a team conversation. */
  noteTyping(): void {
    const was = this.presenceState();
    this.replyingUntil = Date.now() + REPLYING_MS;
    if (was !== 'replying' && this.beatConv != null) void this.sendBeat();
  }

  private startHeartbeat(conv: number): void {
    if (this.beatConv === conv && this.beat) return;
    this.stopHeartbeat();
    this.beatConv = conv;
    void this.sendBeat();
    this.beat = setInterval(() => void this.sendBeat(), HEARTBEAT_MS);
  }

  private stopHeartbeat(): void {
    if (this.beat) clearInterval(this.beat);
    this.beat = null;
    if (this.beatConv != null && this.client && this.activeId != null) void this.client.presence(this.activeId, this.beatConv, '').catch(() => {});
    this.beatConv = null;
    this.replyingUntil = 0;
  }

  private async sendBeat(): Promise<void> {
    if (!this.client || this.activeId == null || this.beatConv == null) return;
    try {
      const p = await this.client.presence(this.activeId, this.beatConv, this.presenceState());
      if (this.detail?.id === this.beatConv) this.detail.presence = p;
    } catch {
      /* offline: next beat */
    }
  }

  /** The own user id (reported with the mailbox list). */
  get myId(): number | null {
    return this.mailboxes[0]?.userId ?? null;
  }

  /** Other members currently on the conversation. */
  get others(): TeamPresence[] {
    return (this.detail?.presence ?? []).filter((p) => p.userId !== this.myId);
  }

  async assign(patch: TeamAssignmentPatch): Promise<void> {
    if (!this.client || this.activeId == null || !this.detail) return;
    try {
      const a = await this.client.updateAssignment(this.activeId, this.detail.id, patch);
      this.applyAssignment(a);
      void this.loadMailboxes();
    } catch (err) {
      this.app.toast(t('team.error.permission'), undefined, 'error');
      throw err;
    }
  }

  private applyAssignment(a: TeamAssignment): void {
    if (this.detail?.id === a.conversationId) this.detail.team = a;
    const c = this.app.conversations.find((x) => x.id === a.conversationId) as (typeof this.app.conversations)[number] & { team?: TeamAssignment };
    if (c) c.team = a;
  }

  async takeOver(meId: number): Promise<void> {
    await this.assign({ assigneeId: meId });
  }

  async addComment(body: string): Promise<void> {
    if (!this.client || this.activeId == null || !this.detail) return;
    const c = await this.client.addComment(this.activeId, this.detail.id, body);
    this.comments = [...this.comments, c];
  }

  async deleteComment(id: number): Promise<void> {
    if (!this.client || this.activeId == null) return;
    await this.client.deleteComment(this.activeId, id);
    this.comments = this.comments.filter((c) => c.id !== id);
  }

  async loadStats(): Promise<void> {
    if (!this.client || this.activeId == null) return;
    this.stats = await this.client.stats(this.activeId);
  }

  // -------------------------------------------------------------------------
  // Events
  // -------------------------------------------------------------------------

  private onEvent(e: TeamApiEvent): void {
    switch (e.type) {
      case 'team.mailboxes':
        void this.loadMailboxes();
        break;
      case 'team.notice':
        this.app.toast(`${e.team.mailboxName || 'Team'}: ${e.team.actorName} – ${e.team.text}`, e.team.conversationId
          ? { label: t('team.open'), run: () => void this.enter(e.team.mailboxId).then(() => this.app.selectConversation(e.team.conversationId)) }
          : undefined);
        void this.loadMailboxes();
        break;
      case 'team.presence':
        if (e.team.mailboxId === this.activeId && this.detail?.id === e.team.conversationId) this.detail.presence = e.team.presence ?? [];
        break;
      case 'team.changed': {
        if (e.team.mailboxId !== this.activeId) {
          void this.loadMailboxes();
          break;
        }
        if (e.team.assignment) this.applyAssignment(e.team.assignment);
        if (this.detail && e.team.conversationId === this.detail.id && (e.team.kind === 'comment' || e.team.kind === 'draft' || e.team.kind === 'assigned' || e.team.kind === 'updated')) {
          void this.open(this.detail.id);
        }
        if (this.reloadTimer) clearTimeout(this.reloadTimer);
        this.reloadTimer = setTimeout(() => {
          void this.app.reloadList({ keepSelection: true });
          void this.loadMailboxes();
        }, 250);
        break;
      }
    }
  }
}

export type { TeamView };
