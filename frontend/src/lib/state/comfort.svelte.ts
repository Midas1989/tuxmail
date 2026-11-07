// SPDX-License-Identifier: AGPL-3.0-or-later
//
// UI state of the mail comfort features: smart category tabs/filters,
// per-message insights cache and the settings.

import type { Api } from '../api';
import { comfortFor, type CategoryId, type CategoryInfo, type ComfortApi, type ComfortSettings, type MessageInsights } from '../api/comfort';
import { hasNativeSession, nativeHttpOptions } from '../native/session';
import { bridgeFetch, isDesktopServerMode } from '../desktop/bridge';
import { isNativeShell } from '../platform';
import type { Conversation, MessageSummary } from '../types';

export type CategoryFilter = 'all' | CategoryId;

export class ComfortState {
  readonly api: ComfortApi;
  settings = $state<ComfortSettings | null>(null);
  /** Selected category tab ('all' shows everything). */
  category = $state<CategoryFilter>('all');
  counts = $state<CategoryInfo[]>([]);
  /** Category per message id (loaded for the visible list). */
  categoryOf = $state<Record<number, CategoryId>>({});
  /** Conversations containing mails of the selected category (chat/hybrid filter). */
  conversationIds = $state<Set<number> | null>(null);
  insights = $state<Record<number, MessageInsights>>({});
  /** Mail merge wizard (null = closed). */
  mergeOpen = $state<{ subject?: string; text?: string; html?: string; identityId?: number } | null>(null);
  subscriptionsOpen = $state(false);

  constructor(main: Api) {
    const native = isNativeShell() && hasNativeSession() ? nativeHttpOptions() : undefined;
    this.api = comfortFor(main, native ? { baseUrl: native.baseUrl, bearer: native.authToken } : isDesktopServerMode() ? { fetchImpl: bridgeFetch() } : {});
  }

  async loadSettings(): Promise<ComfortSettings | null> {
    if (!this.settings) {
      try {
        this.settings = await this.api.getSettings();
      } catch {
        /* backend without comfort features */
      }
    }
    return this.settings;
  }

  async saveSettings(s: ComfortSettings): Promise<void> {
    this.settings = await this.api.saveSettings(s);
  }

  async loadCounts(role = 'inbox'): Promise<void> {
    try {
      this.counts = await this.api.listCategories(role);
    } catch {
      this.counts = [];
    }
  }

  /** Loads the categories of the given messages (only missing ones). */
  async loadCategories(ids: number[]): Promise<void> {
    const missing = ids.filter((id) => !(id in this.categoryOf));
    if (!missing.length) return;
    try {
      const res = await this.api.messageCategories(missing);
      const next = { ...this.categoryOf };
      for (const id of missing) next[id] = res[id] ?? ('' as CategoryId);
      this.categoryOf = next;
    } catch {
      /* ignore */
    }
  }

  async setFilter(c: CategoryFilter): Promise<void> {
    this.category = c;
    if (c === 'all') {
      this.conversationIds = null;
      return;
    }
    try {
      const ms = await this.api.categoryMessages(c, '', 500);
      this.conversationIds = new Set(ms.map((m) => m.conversationId));
      const next = { ...this.categoryOf };
      for (const m of ms) next[m.id] = c;
      this.categoryOf = next;
    } catch {
      this.conversationIds = new Set();
    }
  }

  filterMessages(list: MessageSummary[]): MessageSummary[] {
    if (this.category === 'all') return list;
    return list.filter((m) => this.categoryOf[m.id] === this.category);
  }

  filterConversations(list: Conversation[]): Conversation[] {
    if (this.category === 'all' || !this.conversationIds) return list;
    const ids = this.conversationIds;
    return list.filter((c) => ids.has(c.id));
  }

  async loadInsights(id: number, force = false): Promise<MessageInsights | null> {
    if (!force && this.insights[id]) return this.insights[id];
    try {
      const ins = await this.api.insights(id);
      this.insights = { ...this.insights, [id]: ins };
      if (ins.category) this.categoryOf = { ...this.categoryOf, [id]: ins.category };
      return ins;
    } catch {
      return null;
    }
  }

  async changeCategory(ids: number[], c: CategoryId): Promise<void> {
    await this.api.setCategory(ids, c);
    const next = { ...this.categoryOf };
    for (const id of ids) next[id] = c;
    this.categoryOf = next;
    for (const id of ids) void this.loadInsights(id, true);
    void this.loadCounts();
  }
}
