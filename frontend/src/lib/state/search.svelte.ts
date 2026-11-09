// SPDX-License-Identifier: AGPL-3.0-or-later
//
// State of the search screen (KONZEPT 8.4): query + filter chips, results,
// facets, profiles and history.

import type { Api } from '../api/api';
import type { SearchHistoryEntry, SearchProfile, SearchQuery, SearchResult, SearchScope, SearchSort } from '../types';

export const ALL_SCOPES: SearchScope[] = ['mail', 'chat', 'event', 'task', 'contact', 'file'];

export class SearchState {
  text = $state('');
  mode = $state<'simple' | 'expert'>('simple');
  scopes = $state<SearchScope[]>([]);
  accountIds = $state<number[]>([]);
  folderIds = $state<number[]>([]);
  includeSubfolders = $state(true);
  excludeTrashSpam = $state(true);
  from = $state('');
  to = $state('');
  location = $state<'local' | 'server' | 'auto'>('auto');
  sort = $state<SearchSort>('relevance');

  result = $state<SearchResult | null>(null);
  loading = $state(false);
  loadingMore = $state(false);
  profiles = $state<SearchProfile[]>([]);
  history = $state<SearchHistoryEntry[]>([]);
  /** Selected hit (keyboard navigation in the result list). */
  activeHit = $state(0);

  private seq = 0;

  constructor(private readonly api: Api) {}

  query(cursor?: string): SearchQuery {
    return {
      text: this.text,
      mode: this.mode,
      scopes: [...this.scopes],
      accountIds: [...this.accountIds],
      folderIds: [...this.folderIds],
      includeSubfolders: this.includeSubfolders,
      excludeTrashSpam: this.excludeTrashSpam,
      from: this.from || undefined,
      to: this.to || undefined,
      location: this.location,
      sort: this.sort,
      cursor,
      limit: 50,
    };
  }

  apply(q: SearchQuery): void {
    this.text = q.text;
    this.mode = q.mode;
    this.scopes = [...q.scopes];
    this.accountIds = [...q.accountIds];
    this.folderIds = [...q.folderIds];
    this.includeSubfolders = q.includeSubfolders;
    this.excludeTrashSpam = q.excludeTrashSpam;
    this.from = q.from ?? '';
    this.to = q.to ?? '';
    this.location = q.location;
    this.sort = q.sort;
  }

  async run(): Promise<void> {
    const id = ++this.seq;
    this.loading = true;
    try {
      const r = await this.api.search(this.query());
      if (id !== this.seq) return;
      this.result = r;
      this.activeHit = 0;
      void this.loadHistory();
    } finally {
      if (id === this.seq) this.loading = false;
    }
  }

  async more(): Promise<void> {
    const r = this.result;
    if (!r?.nextCursor || this.loadingMore) return;
    this.loadingMore = true;
    try {
      const next = await this.api.search(this.query(r.nextCursor));
      this.result = { ...next, hits: [...r.hits, ...next.hits] };
    } finally {
      this.loadingMore = false;
    }
  }

  /** Facet / histogram click: add the clause (switches to expert mode). */
  addClause(clause: string): void {
    const base = this.text.trim();
    if (this.mode === 'simple') {
      // Simple words stay valid expert terms; quote nothing else.
      this.mode = 'expert';
    }
    this.text = base ? `${base} AND ${clause}` : clause;
    void this.run();
  }

  toggleScope(s: SearchScope): void {
    this.scopes = this.scopes.includes(s) ? this.scopes.filter((x) => x !== s) : [...this.scopes, s];
  }

  async loadProfiles(): Promise<void> {
    this.profiles = await this.api.listSearchProfiles();
  }

  async loadHistory(): Promise<void> {
    // Offline (apps in server mode) the history is not available: the list just stays as it is.
    this.history = await this.api.getSearchHistory().catch(() => this.history);
  }

  async saveProfile(name: string): Promise<void> {
    await this.api.saveSearchProfile({ id: '', name, query: this.query(), pinned: false });
    await this.loadProfiles();
  }

  async deleteProfile(id: string): Promise<void> {
    await this.api.deleteSearchProfile(id);
    await this.loadProfiles();
  }

  async clearHistory(): Promise<void> {
    await this.api.clearSearchHistory();
    this.history = [];
  }
}
