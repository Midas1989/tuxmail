<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Search from '@lucide/svelte/icons/search';
  import History from '@lucide/svelte/icons/history';
  import Bookmark from '@lucide/svelte/icons/bookmark';
  import BookmarkPlus from '@lucide/svelte/icons/bookmark-plus';
  import Trash from '@lucide/svelte/icons/trash';
  import Mail from '@lucide/svelte/icons/mail';
  import MessageCircle from '@lucide/svelte/icons/message-circle';
  import Calendar from '@lucide/svelte/icons/calendar';
  import ListTodo from '@lucide/svelte/icons/list-todo';
  import Contact from '@lucide/svelte/icons/contact';
  import FileIcon from '@lucide/svelte/icons/file';
  import Paperclip from '@lucide/svelte/icons/paperclip';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import SearchX from '@lucide/svelte/icons/search-x';
  import { app, search } from '../state';
  import { ALL_SCOPES } from '../state/search.svelte';
  import { fmt, formatListDate, formatSize, t } from '../i18n/index.svelte';
  import type { FacetBucket, Folder, SearchFacets, SearchHit, SearchScope, SearchSort } from '../types';
  import type { SuggestData } from '../search/suggest';
  import QueryBar from '../components/search/QueryBar.svelte';
  import Popover from '../components/common/Popover.svelte';
  import ChannelIcon from '../components/common/ChannelIcon.svelte';

  onMount(() => {
    void search.loadProfiles();
    void search.loadHistory();
    if (search.text && !search.result) void search.run();
  });

  const r = $derived(search.result);

  const suggestData = $derived<SuggestData>({
    accounts: app.accounts.map((a) => ({ id: a.id, name: a.displayName, address: a.extra?.address })),
    folders: app.allFolders.map((f) => ({ id: f.id, name: folderName(f), account: app.account(f.accountId)?.displayName ?? '' })),
    people: app.persons.filter((p) => !p.isMe).flatMap((p) => p.handles.filter((h) => h.kind === 'email').map((h) => ({ name: p.displayName, email: h.value }))),
  });

  function folderName(f: Folder): string {
    return f.role ? t(`mail.folder.role.${f.role}` as 'mail.folder.role.inbox') : f.name;
  }

  // ----- highlight rendering ---------------------------------------------------
  function segments(text: string, ranges: [number, number][]): { s: string; hit: boolean }[] {
    const out: { s: string; hit: boolean }[] = [];
    let pos = 0;
    for (const [a, b] of ranges) {
      if (a > pos) out.push({ s: text.slice(pos, a), hit: false });
      out.push({ s: text.slice(a, b), hit: true });
      pos = b;
    }
    if (pos < text.length) out.push({ s: text.slice(pos), hit: false });
    return out;
  }

  const kindIcon: Record<string, typeof Mail> = { mail: Mail, chat: MessageCircle, event: Calendar, task: ListTodo, contact: Contact, file: FileIcon };

  async function open(hit: SearchHit) {
    if ((hit.kind === 'mail' || hit.kind === 'file') && hit.messageId != null) await app.revealMessage(hit.messageId);
    else if (hit.kind === 'chat' && hit.conversationId != null) await app.revealItem(hit.conversationId, hit.itemId);
    else if (hit.kind === 'contact') app.navigate('contacts');
  }

  /** Arrow keys / j k move between hits (each hit is a button). */
  function onListKey(e: KeyboardEvent) {
    const hits = r?.hits ?? [];
    if (!hits.length) return;
    let next = search.activeHit;
    if (e.key === 'ArrowDown' || e.key === 'j') next = Math.min(hits.length - 1, next + 1);
    else if (e.key === 'ArrowUp' || e.key === 'k') next = Math.max(0, next - 1);
    else return;
    e.preventDefault();
    search.activeHit = next;
    document.querySelector<HTMLButtonElement>(`#hit-${next} button`)?.focus();
  }

  // ----- filters ---------------------------------------------------------------
  const scopeLabel = (s: SearchScope) => t(`search.scope.${s}`);
  const accountsLabel = $derived(
    search.accountIds.length === 0 ? t('search.allAccounts') : search.accountIds.map((id) => app.account(id)?.displayName).filter(Boolean).join(', '),
  );
  const foldersLabel = $derived(
    search.folderIds.length === 0 ? t('search.allFolders') : search.folderIds.map((id) => (app.folder(id) ? folderName(app.folder(id)!) : '')).join(', '),
  );
  const dateLabel = $derived(search.from || search.to ? `${search.from || '…'} – ${search.to || '…'}` : t('search.anyTime'));

  function toggle<T>(list: T[], v: T): T[] {
    return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
  }

  const sorts: SearchSort[] = ['relevance', 'date', 'sender', 'size'];
  const facetGroups: (keyof SearchFacets)[] = ['account', 'folder', 'sender', 'domain', 'channel', 'type', 'attachment'];

  function facetLabel(group: keyof SearchFacets, b: FacetBucket): string {
    if (group === 'type') return t(`search.scope.${b.key}` as 'search.scope.mail');
    if (group === 'channel') return t(`common.channel.${b.key}` as 'common.channel.email');
    if (group === 'attachment') return b.key === 'yes' ? t('search.withAttachment') : t('search.withoutAttachment');
    return b.label;
  }

  function monthLabel(key: string): string {
    const [y, m] = key.split('-').map(Number);
    return y && m ? fmt().monthLabel(y, m - 1) : key;
  }

  const histMax = $derived(Math.max(1, ...(r?.histogram ?? []).map((b) => b.count)));

  let profileName = $state('');
  async function saveProfile() {
    const name = profileName.trim();
    if (!name) return;
    await search.saveProfile(name);
    profileName = '';
    app.toast(t('search.profileSaved', { name }));
  }
</script>

<section class="search" aria-labelledby="search-title">
  <header class="head">
    <h1 id="search-title" class="sr-only">{t('search.title')}</h1>
    <div class="row1">
      <div class="seg" role="radiogroup" aria-label={t('search.mode')}>
        <button role="radio" aria-checked={search.mode === 'simple'} onclick={() => (search.mode = 'simple')}>{t('search.simple')}</button>
        <button role="radio" aria-checked={search.mode === 'expert'} onclick={() => (search.mode = 'expert')}>{t('search.expert')}</button>
      </div>
      {#if search.mode === 'expert'}
        <QueryBar bind:value={search.text} data={suggestData} error={r?.error ?? null} onsubmit={() => search.run()} placeholder={t('search.expertPlaceholder')} />
      {:else}
        <div class="simple">
          <Search size={17} aria-hidden="true" />
          <input
            type="search"
            bind:value={search.text}
            placeholder={t('search.simplePlaceholder')}
            aria-label={t('search.title')}
            onkeydown={(e) => e.key === 'Enter' && search.run()}
            data-search-input
          />
        </div>
      {/if}
      <button class="btn btn-primary go" onclick={() => search.run()} disabled={search.loading}><Search size={15} /> {t('search.run')}</button>

      <Popover label={t('search.history')} chip={false} align="right" width="320px">
        {#snippet icon()}<History size={15} />{/snippet}
        {#if search.history.length}
          <ul class="plist">
            {#each search.history as h (h.at + h.text)}
              <li>
                <button
                  onclick={() => {
                    search.text = h.text;
                    search.mode = h.mode;
                    void search.run();
                  }}><code>{h.text}</code></button
                >
              </li>
            {/each}
          </ul>
          <button class="btn btn-ghost sm" onclick={() => search.clearHistory()}><Trash size={13} /> {t('search.clearHistory')}</button>
        {:else}
          <p class="muted">{t('search.noHistory')}</p>
        {/if}
      </Popover>

      <Popover label={t('search.profiles')} chip={false} align="right" width="320px">
        {#snippet icon()}<Bookmark size={15} />{/snippet}
        <ul class="plist">
          {#each search.profiles as p (p.id)}
            <li class="prow">
              <button
                onclick={() => {
                  search.apply(p.query);
                  void search.run();
                }}
              >
                <strong>{p.name}</strong>
                <code>{p.query.text}</code>
              </button>
              <button class="icon-btn" aria-label={t('search.deleteProfile', { name: p.name })} onclick={() => search.deleteProfile(p.id)}><Trash size={13} /></button>
            </li>
          {/each}
        </ul>
        <form
          class="save"
          onsubmit={(e) => {
            e.preventDefault();
            void saveProfile();
          }}
        >
          <input class="input" bind:value={profileName} placeholder={t('search.profileName')} aria-label={t('search.profileName')} />
          <button class="btn" type="submit" disabled={!profileName.trim()}><BookmarkPlus size={14} /> {t('search.saveProfile')}</button>
        </form>
      </Popover>
    </div>

    <div class="row2" role="group" aria-label={t('search.filters')}>
      {#each ALL_SCOPES as s (s)}
        <button class="fchip" aria-pressed={search.scopes.includes(s)} onclick={() => search.toggleScope(s)}>{scopeLabel(s)}</button>
      {/each}
      <span class="sep"></span>
      <Popover label={accountsLabel} active={search.accountIds.length > 0}>
        {#each app.accounts as a (a.id)}
          <label class="check"><input type="checkbox" checked={search.accountIds.includes(a.id)} onchange={() => (search.accountIds = toggle(search.accountIds, a.id))} /> <span class="dot" style:background={a.color}></span> {a.displayName}</label>
        {/each}
      </Popover>
      <Popover label={foldersLabel} active={search.folderIds.length > 0} width="300px">
        {#each app.mailAccounts as a (a.id)}
          <div class="fgroup">{a.displayName}</div>
          {#each app.folders[a.id] ?? [] as f (f.id)}
            <label class="check" style:padding-left="{f.parentId ? 22 : 4}px"><input type="checkbox" checked={search.folderIds.includes(f.id)} onchange={() => (search.folderIds = toggle(search.folderIds, f.id))} /> {folderName(f)}</label>
          {/each}
        {/each}
        <label class="check sub"><input type="checkbox" bind:checked={search.includeSubfolders} /> {t('search.includeSubfolders')}</label>
      </Popover>
      <button class="fchip" aria-pressed={!search.excludeTrashSpam} onclick={() => (search.excludeTrashSpam = !search.excludeTrashSpam)}>{t('search.includeTrashSpam')}</button>
      <Popover label={dateLabel} active={!!(search.from || search.to)} width="260px">
        <label class="dl"><span class="label">{t('search.dateFrom')}</span><input class="input" type="date" bind:value={search.from} /></label>
        <label class="dl"><span class="label">{t('search.dateTo')}</span><input class="input" type="date" bind:value={search.to} /></label>
        <button
          class="btn btn-ghost sm"
          onclick={() => {
            search.from = '';
            search.to = '';
          }}>{t('search.anyTime')}</button
        >
      </Popover>
      <div class="seg loc" role="radiogroup" aria-label={t('search.location')}>
        {#each ['auto', 'local', 'server'] as const as l (l)}
          <button role="radio" aria-checked={search.location === l} onclick={() => (search.location = l)}>{t(`search.loc.${l}`)}</button>
        {/each}
      </div>
    </div>
  </header>

  <div class="body">
    <aside class="facets" aria-label={t('search.facets')}>
      {#if r && r.histogram.length}
        <div class="fsec">
          <h2>{t('search.timeline')}</h2>
          <div class="hist">
            {#each r.histogram as b (b.key)}
              <button
                class="bar"
                style:--h="{Math.round((b.count / histMax) * 100)}%"
                title="{monthLabel(b.key)}: {b.count}"
                aria-label={t('search.histBar', { label: monthLabel(b.key), count: b.count })}
                disabled={b.count === 0}
                onclick={() => search.addClause(b.clause)}
              >
                <span class="fill"></span>
                <span class="bl">{monthLabel(b.key)}</span>
              </button>
            {/each}
          </div>
        </div>
      {/if}
      {#if r}
        {#each facetGroups as g (g)}
          {#if r.facets[g].length}
            <div class="fsec">
              <h2>{t(`search.facet.${g}`)}</h2>
              <ul>
                {#each r.facets[g] as b (b.key)}
                  <li>
                    <button class="fb" onclick={() => search.addClause(b.clause)} title={b.clause}>
                      {#if g === 'channel'}<ChannelIcon channel={b.key as 'email'} size={13} />{/if}
                      <span class="fl">{facetLabel(g, b)}</span>
                      <span class="fc">{b.count}</span>
                      <span class="fbar" style:width="{(b.count / Math.max(1, r.total)) * 100}%"></span>
                    </button>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}
        {/each}
      {/if}
    </aside>

    <div class="results">
      {#if r}
        <div class="rhead">
          <span class="count" role="status">{t('search.resultCount', { count: r.total })} · {t('search.took', { ms: r.tookMs })}</span>
          <label class="sort">
            <span>{t('search.sortBy')}</span>
            <select class="input" bind:value={search.sort} onchange={() => search.run()}>
              {#each sorts as s (s)}<option value={s}>{t(`search.sort.${s}`)}</option>{/each}
            </select>
          </label>
        </div>
        {#if r.fallback}
          <div class="notice" role="note"><TriangleAlert size={15} /> {t('search.fallback')}</div>
        {/if}
        <ol class="hits" aria-label={t('search.results')}>
          {#each r.hits as h, i (h.id)}
            {@const Icon = kindIcon[h.kind] ?? Mail}
            {@const acc = h.accountId != null ? app.account(h.accountId) : undefined}
            <li id="hit-{i}" class="hit" class:active={i === search.activeHit} class:unread={h.unread}>
              <button class="hit-btn" onclick={() => open(h)} onfocus={() => (search.activeHit = i)} onkeydown={onListKey}>
                <span class="kind kind-{h.kind}" title={t(`search.scope.${h.kind}`)}>
                  {#if h.channel && h.kind === 'chat'}<ChannelIcon channel={h.channel} size={15} />{:else}<Icon size={15} />{/if}
                </span>
                <span class="hit-main">
                  <span class="hit-title">
                    {#each segments(h.title, h.titleHighlights) as seg, si (si)}{#if seg.hit}<mark>{seg.s}</mark>{:else}{seg.s}{/if}{/each}
                    {#if h.hasAttachments}<Paperclip size={12} class="pc" />{/if}
                  </span>
                  <span class="hit-meta">
                    {#if h.sender}<span>{h.sender}</span>{/if}
                    {#if acc}<span class="acc"><span class="dot" style:background={acc.color}></span>{acc.displayName}</span>{/if}
                    {#if h.folderId && app.folder(h.folderId)}<span>{folderName(app.folder(h.folderId)!)}</span>{/if}
                    {#if h.kind === 'file' && h.size}<span>{formatSize(h.size)}</span>{/if}
                  </span>
                  <span class="snippet">
                    {#each segments(h.snippet, h.highlights) as seg, si (si)}{#if seg.hit}<mark>{seg.s}</mark>{:else}{seg.s}{/if}{/each}
                  </span>
                </span>
                {#if h.date}<time class="hit-date" datetime={h.date}>{formatListDate(h.date)}</time>{/if}
              </button>
            </li>
          {/each}
        </ol>
        {#if r.hits.length === 0}
          <div class="empty"><SearchX size={36} strokeWidth={1.4} /><p>{t('search.noResults')}</p></div>
        {/if}
        {#if r.nextCursor}
          <button class="btn more" onclick={() => search.more()} disabled={search.loadingMore}>{t('search.loadMore')}</button>
        {/if}
      {:else}
        <div class="empty intro">
          <Search size={40} strokeWidth={1.3} />
          <p class="e1">{t('search.introTitle')}</p>
          <p>{t('search.introText')}</p>
          <code class="example">(from:anna OR from:max) AND subject:angebot* AND date:&gt;now-30d</code>
        </div>
      {/if}
    </div>
  </div>
</section>

<style>
  .search {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    background: var(--tm-bg);
  }
  .head {
    padding: 14px 20px 10px;
    background: var(--tm-surface);
    border-bottom: 1px solid var(--tm-line);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .row1 {
    display: flex;
    gap: 8px;
    align-items: flex-start;
  }
  .row1 > .seg {
    margin-top: 5px;
    flex: none;
  }
  .row1 .go {
    height: 42px;
    flex: none;
  }
  .row1 :global(.btn) {
    height: 42px;
  }
  .simple {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 8px;
    height: 42px;
    padding: 0 12px;
    border-radius: 12px;
    border: 1px solid var(--tm-line-strong);
    color: var(--tm-muted);
  }
  .simple:focus-within {
    border-color: var(--tm-accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-accent) 18%, transparent);
  }
  .simple input {
    flex: 1;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 15px;
    outline: none;
  }
  .row2 {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .sep {
    width: 1px;
    height: 20px;
    background: var(--tm-line);
    margin: 0 4px;
  }
  .loc {
    margin-left: auto;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px;
    font-size: 13px;
  }
  .check input {
    accent-color: var(--tm-accent);
  }
  .check.sub {
    border-top: 1px solid var(--tm-line);
    margin-top: 6px;
    padding-top: 8px;
  }
  .fgroup {
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--tm-faint);
    padding: 6px 4px 2px;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 9px;
    flex: none;
  }
  .dl {
    display: block;
    margin-bottom: 8px;
  }
  .sm {
    height: 28px;
    font-size: 12px;
  }
  .plist {
    list-style: none;
    margin: 0 0 8px;
    padding: 0;
  }
  .plist button:not(.icon-btn) {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    width: 100%;
    text-align: left;
    padding: 6px 8px;
    border-radius: 8px;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 13px;
  }
  .plist button:hover {
    background: var(--tm-surface-2);
  }
  .plist code {
    font-size: 11.5px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 260px;
  }
  .prow {
    display: flex;
    align-items: center;
  }
  .save {
    display: flex;
    gap: 6px;
    border-top: 1px solid var(--tm-line);
    padding-top: 8px;
  }
  .save .btn {
    height: 34px;
  }
  .muted {
    color: var(--tm-faint);
    font-size: 13px;
    margin: 4px;
  }

  .body {
    flex: 1;
    min-height: 0;
    display: grid;
    grid-template-columns: 270px 1fr;
    grid-template-rows: minmax(0, 1fr);
  }
  .facets {
    position: relative;
    overflow-y: auto;
    padding: 14px 14px 24px 20px;
    border-right: 1px solid var(--tm-line);
    background: var(--tm-sidebar);
  }
  .fsec {
    margin-bottom: 16px;
  }
  .fsec h2 {
    margin: 0 0 6px;
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--tm-faint);
  }
  .fsec ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .fb {
    position: relative;
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 4px 6px;
    border: none;
    border-radius: 6px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 13px;
    text-align: left;
    overflow: hidden;
  }
  .fb:hover {
    background: var(--tm-surface-3);
  }
  .fl {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    position: relative;
  }
  .fc {
    font-size: 11.5px;
    color: var(--tm-muted);
    position: relative;
  }
  .fbar {
    position: absolute;
    left: 0;
    bottom: 1px;
    height: 2px;
    background: color-mix(in srgb, var(--tm-accent) 45%, transparent);
    border-radius: 2px;
  }
  .hist {
    display: flex;
    align-items: flex-end;
    gap: 3px;
    height: 92px;
    padding-top: 4px;
  }
  .bar {
    flex: 1;
    min-width: 8px;
    height: 100%;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    align-items: stretch;
    gap: 3px;
    border: none;
    background: transparent;
    padding: 0;
  }
  .fill {
    display: block;
    height: var(--h);
    min-height: 2px;
    border-radius: 3px 3px 0 0;
    background: var(--tm-accent);
    opacity: 0.75;
    transition: opacity var(--tm-dur);
  }
  .bar:hover .fill {
    opacity: 1;
  }
  .bar:disabled .fill {
    background: var(--tm-line-strong);
  }
  .bl {
    font-size: 9.5px;
    color: var(--tm-faint);
    white-space: nowrap;
    overflow: hidden;
    text-align: center;
  }

  .results {
    position: relative;
    overflow-y: auto;
    padding: 12px 20px 32px;
    min-width: 0;
  }
  .rhead {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 8px;
  }
  .count {
    font-size: 13px;
    color: var(--tm-muted);
  }
  .sort {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .sort .input {
    width: 150px;
    height: 30px;
  }
  .notice {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    margin-bottom: 8px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--tm-warn) 12%, var(--tm-surface));
    font-size: 13px;
  }
  .hits {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    outline: none;
  }
  .hit-btn {
    display: flex;
    gap: 12px;
    width: 100%;
    padding: 10px 12px;
    border-radius: 12px;
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    transition:
      border-color var(--tm-dur),
      box-shadow var(--tm-dur);
  }
  .hit-btn:hover {
    border-color: var(--tm-line-strong);
  }
  .hit.active .hit-btn {
    border-color: var(--tm-accent);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--tm-accent) 20%, transparent);
  }
  .kind {
    flex: none;
    width: 30px;
    height: 30px;
    border-radius: 9px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
  }
  .hit-main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .hit-title {
    font-weight: 600;
    font-size: 14px;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .hit.unread .hit-title {
    font-weight: 700;
  }
  .hit-title :global(.pc) {
    color: var(--tm-faint);
  }
  .hit-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .acc {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .snippet {
    font-size: 13px;
    color: var(--tm-muted);
    line-height: 1.45;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  mark {
    background: color-mix(in srgb, #f59e0b 35%, transparent);
    color: inherit;
    border-radius: 3px;
    padding: 0 1px;
  }
  .hit-date {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
  }
  .more {
    margin: 12px auto 0;
    display: flex;
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 48px 16px;
    color: var(--tm-faint);
    text-align: center;
  }
  .empty p {
    margin: 4px 0;
    max-width: 520px;
  }
  .e1 {
    font-weight: 600;
    color: var(--tm-muted);
    margin-top: 12px !important;
  }
  .example {
    margin-top: 10px;
    padding: 8px 12px;
    border-radius: 8px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    color: var(--tm-fg);
    font-size: 12.5px;
  }
  @media (max-width: 899px) {
    .body {
      grid-template-columns: 1fr;
    }
    .facets {
      display: none;
    }
    .row1 {
      flex-wrap: wrap;
    }
    .loc {
      margin-left: 0;
    }
  }
</style>
