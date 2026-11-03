<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  /** Simple fuzzy score: substring beats subsequence; -1 = no match. */
  export function fuzzyScore(text: string, query: string): number {
    const t = text.toLowerCase();
    const q = query.toLowerCase().trim();
    if (!q) return 0;
    const idx = t.indexOf(q);
    if (idx >= 0) return 1000 - idx * 2 - (t.length - q.length) * 0.1;
    let ti = 0;
    let score = 0;
    for (const ch of q) {
      const found = t.indexOf(ch, ti);
      if (found < 0) return -1;
      score += found === ti ? 3 : 1;
      ti = found + 1;
    }
    return score;
  }
</script>

<script lang="ts">
  import Search from '@lucide/svelte/icons/search';
  import CornerDownLeft from '@lucide/svelte/icons/corner-down-left';
  import { app, prefs } from '../state';
  import { t } from '../i18n/index.svelte';
  import Modal from './common/Modal.svelte';

  interface Command {
    id: string;
    label: string;
    section: string;
    hint?: string;
    run: () => void | Promise<void>;
  }

  let query = $state('');
  let active = $state(0);
  let listEl = $state<HTMLUListElement>();

  const close = () => (app.paletteOpen = false);

  const commands = $derived.by((): Command[] => {
    const nav = t('palette.section.navigation');
    const act = t('palette.section.actions');
    const view = t('palette.section.view');
    const go = t('palette.section.folders');
    const conv = t('palette.section.conversations');
    const list: Command[] = [
      { id: 'compose', label: t('action.compose'), section: act, hint: 'C', run: () => app.openCompose() },
      { id: 'view.toggle', label: prefs.viewMode === 'classic' ? t('palette.toChat') : t('palette.toClassic'), section: view, hint: 'Ctrl+Shift+V', run: () => app.toggleViewMode() },
      { id: 'nav.mail', label: t('nav.mail'), section: nav, run: () => (app.navigate('mail'), app.setViewMode('classic')) },
      { id: 'nav.chats', label: t('nav.chats'), section: nav, run: () => (app.navigate('mail'), app.setViewMode('chat')) },
      { id: 'nav.calendar', label: t('nav.calendar'), section: nav, run: () => app.navigate('calendar') },
      { id: 'nav.contacts', label: t('nav.contacts'), section: nav, run: () => app.navigate('contacts') },
      { id: 'nav.files', label: t('nav.files'), section: nav, run: () => app.navigate('files') },
      { id: 'nav.settings', label: t('nav.settings'), section: nav, run: () => app.navigate('settings') },
      { id: 'group.person', label: t('palette.groupByPerson'), section: view, run: () => app.setGrouping('person') },
      { id: 'group.thread', label: t('palette.groupByThread'), section: view, run: () => app.setGrouping('thread') },
      { id: 'theme.light', label: t('palette.themeLight'), section: view, run: () => ((prefs.theme = 'light'), prefs.save()) },
      { id: 'theme.dark', label: t('palette.themeDark'), section: view, run: () => ((prefs.theme = 'dark'), prefs.save()) },
      { id: 'theme.system', label: t('palette.themeSystem'), section: view, run: () => ((prefs.theme = 'system'), prefs.save()) },
      { id: 'density.compact', label: t('palette.densityCompact'), section: view, run: () => ((prefs.density = 'compact'), prefs.save()) },
      { id: 'density.normal', label: t('palette.densityNormal'), section: view, run: () => ((prefs.density = 'normal'), prefs.save()) },
      { id: 'density.comfortable', label: t('palette.densityComfortable'), section: view, run: () => ((prefs.density = 'comfortable'), prefs.save()) },
      { id: 'lang.de', label: 'Sprache: Deutsch', section: view, run: () => prefs.setLocale('de') },
      { id: 'lang.en', label: 'Language: English', section: view, run: () => prefs.setLocale('en') },
    ];
    if (app.message && prefs.viewMode === 'classic') {
      const id = app.message.id;
      list.unshift(
        { id: 'reply', label: t('action.reply'), section: act, hint: 'R', run: () => app.replyTo('reply') },
        { id: 'replyAll', label: t('action.replyAll'), section: act, hint: 'Shift+R', run: () => app.replyTo('replyAll') },
        { id: 'forward', label: t('action.forward'), section: act, hint: 'F', run: () => app.replyTo('forward') },
        { id: 'archive', label: t('action.archive'), section: act, hint: 'E', run: () => app.moveToRole(id, 'archive') },
        { id: 'delete', label: t('action.delete'), section: act, hint: 'Del', run: () => app.moveToRole(id, 'trash') },
        { id: 'flag', label: t('action.flag'), section: act, hint: 'S', run: () => app.toggleFlag(id) },
      );
    }
    list.push({ id: 'unified.inbox', label: t('folder.unifiedInbox'), section: go, run: () => app.select({ kind: 'unified', role: 'inbox' }) });
    for (const acc of app.mailAccounts) {
      for (const f of app.folders[acc.id] ?? []) {
        const name = f.role ? t(`folder.role.${f.role}` as 'folder.role.inbox') : f.name;
        list.push({ id: `f${f.id}`, label: `${acc.displayName} › ${name}`, section: go, run: () => app.select({ kind: 'folder', accountId: acc.id, folderId: f.id }) });
      }
    }
    for (const acc of app.messengerAccounts) {
      list.push({ id: `a${acc.id}`, label: acc.displayName, section: go, run: () => app.select({ kind: 'account', accountId: acc.id }) });
    }
    if (prefs.viewMode === 'chat') {
      for (const c of app.conversations.slice(0, 200)) {
        list.push({ id: `c${c.id}`, label: c.title, section: conv, run: () => app.selectConversation(c.id) });
      }
    }
    return list;
  });

  const results = $derived.by(() => {
    const q = query.trim();
    const scored = commands.map((c) => ({ c, s: q ? fuzzyScore(`${c.label} ${c.section}`, q) : 0 })).filter((x) => x.s >= 0);
    if (q) scored.sort((a, b) => b.s - a.s);
    const out = scored.slice(0, 50).map((x) => x.c);
    if (q) out.push({ id: 'search', label: t('palette.searchFor', { q }), section: t('palette.section.search'), hint: 'Enter', run: () => app.setSearch(q) });
    return out;
  });

  $effect(() => {
    void query;
    active = 0;
  });

  $effect(() => {
    listEl?.querySelector(`#pal-${active}`)?.scrollIntoView({ block: 'nearest' });
  });

  async function run(c: Command | undefined) {
    if (!c) return;
    close();
    await c.run();
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      active = Math.min(results.length - 1, active + 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      active = Math.max(0, active - 1);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      void run(results[active]);
    }
  }
</script>

<Modal title={t('palette.title')} onclose={close} bare align="top" width="620px">
  <div class="pal">
    <div class="search">
      <Search size={18} aria-hidden="true" />
      <input
        bind:value={query}
        {onkeydown}
        placeholder={t('palette.placeholder')}
        role="combobox"
        aria-expanded="true"
        aria-controls="palette-list"
        aria-activedescendant={results.length ? `pal-${active}` : undefined}
        aria-label={t('palette.title')}
        data-autofocus
      />
      <span class="kbd">Esc</span>
    </div>
    <ul id="palette-list" role="listbox" bind:this={listEl} aria-label={t('palette.title')}>
      {#each results as c, i (c.id)}
        <li id="pal-{i}" role="option" aria-selected={i === active} onmousemove={() => (active = i)} onclick={() => run(c)} onkeydown={() => {}}>
          <span class="label">{c.label}</span>
          <span class="section">{c.section}</span>
          {#if c.hint}<span class="kbd">{c.hint}</span>{/if}
          {#if i === active}<CornerDownLeft size={14} class="enter" aria-hidden="true" />{/if}
        </li>
      {:else}
        <li class="none" role="presentation">{t('palette.noResults')}</li>
      {/each}
    </ul>
  </div>
</Modal>

<style>
  .pal {
    display: flex;
    flex-direction: column;
    max-height: 60vh;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--tm-line);
    color: var(--tm-muted);
  }
  .search input {
    flex: 1;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 16px;
    outline: none;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 6px;
    overflow: auto;
  }
  li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border-radius: 8px;
    cursor: pointer;
    font-size: 14px;
  }
  li[aria-selected='true'] {
    background: var(--tm-accent-soft);
  }
  .label {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .section {
    font-size: 12px;
    color: var(--tm-faint);
  }
  li :global(.enter) {
    color: var(--tm-muted);
  }
  .none {
    color: var(--tm-faint);
    cursor: default;
  }
</style>
