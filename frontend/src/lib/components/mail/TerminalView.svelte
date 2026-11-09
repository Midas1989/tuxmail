<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Terminal view: a full-screen, monospaced mail client that is used completely with the keyboard,
  modelled on aerc and vim (folders | list | reader, count prefixes, gg / G / dd, : and / lines,
  ? for the key list). It is an ordinary part of the page (no terminal emulator, no shell, no PTY);
  `:` commands go through lib/terminal/exec.ts to the normal API. Mail text is only ever rendered
  as text, never as HTML. The mouse still works, but nothing needs it.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { app } from '../../state';
  import { FLAG_FLAGGED, FLAG_SEEN, type FolderRole } from '../../types';
  import { t } from '../../i18n/index.svelte';
  import { TERM_INPUT_ATTR, isEditable } from '../../keyboard';
  import { folderTree } from '../../folders';
  import { ParseError, bodyText, cleanLine } from '../../terminal/parse';
  import { specs } from '../../terminal/parse';
  import { TermError, rowText, runCommand, type OutLine } from '../../terminal/exec';
  import { KeyParser, type Action, type Pane } from '../../terminal/keymap';
  import type { SidebarSel } from '../../state/app.svelte';

  // ---- state ---------------------------------------------------------------------------
  type Mode = 'normal' | 'command' | 'search';
  let pane = $state<Pane>('list');
  let mode = $state<Mode>('normal');
  let helpOpen = $state(false);
  let line = $state('');
  let output = $state<OutLine[]>([]);
  let busy = $state(false);
  let pending = $state('');
  let history: string[] = [];
  let historyPos = -1;
  let inputEl = $state<HTMLInputElement>();
  let listEl = $state<HTMLElement>();
  let bodyEl = $state<HTMLElement>();
  let folderEl = $state<HTMLElement>();
  const parser = new KeyParser();
  let seqTimer: ReturnType<typeof setTimeout> | undefined;

  const message = $derived(app.message);
  const body = $derived(message ? bodyText(message.text, message.html) : '');
  const inputAttrs = { [TERM_INPUT_ATTR]: '' };

  // ---- folder pane ---------------------------------------------------------------------
  interface Entry {
    key: string;
    label: string;
    depth: number;
    unread: number;
    sel?: SidebarSel; // undefined: account heading (not selectable)
  }
  const entries = $derived.by<Entry[]>(() => {
    const out: Entry[] = [
      { key: 'u:inbox', label: t('mail.folder.unifiedInbox'), depth: 0, unread: app.unifiedUnread, sel: { kind: 'unified', role: 'inbox' } },
      { key: 'u:flagged', label: t('mail.folder.flagged'), depth: 0, unread: 0, sel: { kind: 'unified', role: 'flagged' } },
      { key: 'u:unread', label: t('mail.folder.unread'), depth: 0, unread: 0, sel: { kind: 'unified', role: 'unread' } },
    ];
    for (const a of app.mailAccounts) {
      out.push({ key: `a:${a.id}`, label: a.displayName, depth: 0, unread: 0 });
      for (const n of folderTree(app.folders[a.id] ?? [], (r) => t(`mail.folder.role.${r}` as 'mail.folder.role.inbox'))) {
        out.push({ key: `f:${n.folder.id}`, label: n.label, depth: n.depth + 1, unread: n.folder.unread, sel: { kind: 'folder', accountId: a.id, folderId: n.folder.id } });
      }
    }
    return out;
  });
  const sameSel = (a: SidebarSel | undefined, b: SidebarSel): boolean =>
    !!a && a.kind === b.kind && (a.kind === 'unified' ? a.role === (b as typeof a).role : a.kind === 'folder' ? a.folderId === (b as typeof a).folderId : false);
  const currentEntry = $derived(entries.findIndex((e) => sameSel(e.sel, app.sel)));
  let cursor = $state(0);
  $effect(() => {
    if (pane !== 'folders' && currentEntry >= 0) cursor = currentEntry;
  });

  const scopeLabel = $derived.by(() => {
    const s = app.sel;
    if (s.kind === 'folder') return `${app.account(s.accountId)?.displayName ?? ''}/${app.folderPath(s.folderId)}`;
    if (s.kind === 'unified') return t(s.role === 'inbox' ? 'mail.folder.unifiedInbox' : s.role === 'flagged' ? 'mail.folder.flagged' : 'mail.folder.unread');
    return '';
  });
  const unreadHere = $derived(app.messages.filter((m) => !m.flags.includes(FLAG_SEEN)).length);

  // ---- helpers -------------------------------------------------------------------------
  function errorText(e: unknown): string {
    if (e instanceof ParseError) return t(`mail.terminal.error.${e.code}`, { detail: cleanLine(e.detail) });
    if (e instanceof TermError) return t(`mail.terminal.error.${e.key}`, { detail: e.detail });
    return cleanLine(e instanceof Error ? e.message : String(e));
  }
  function say(text: string, kind: 'text' | 'error' = 'text') {
    output = [{ kind, text }];
  }
  const rows = () => Math.max(3, Math.floor((listEl?.clientHeight ?? 400) / 24));
  const clamp = (n: number, max: number) => Math.max(0, Math.min(max, n));

  function marks(m: { id: number; flags: string[]; hasAttachments?: boolean }): string {
    return (app.checked.includes(m.id) ? '+' : ' ') + (m.flags.includes(FLAG_SEEN) ? ' ' : '*') + (m.flags.includes(FLAG_FLAGGED) ? '!' : ' ') + (m.hasAttachments ? '@' : ' ');
  }

  $effect(() => {
    void app.listScrollTick;
    const id = app.selectedMessageId;
    if (id == null || !listEl) return;
    listEl.querySelector(`#term-msg-${id}`)?.scrollIntoView?.({ block: 'nearest' });
  });
  $effect(() => {
    void cursor;
    folderEl?.querySelector('.cur')?.scrollIntoView?.({ block: 'nearest' });
  });

  // ---- movement ------------------------------------------------------------------------
  function listGo(index: number) {
    const list = app.messages;
    if (!list.length) return;
    const i = clamp(index, list.length - 1);
    void app.selectMessage(list[i].id, { scroll: true });
    if (i > list.length - 12) void app.loadMoreMessages();
  }
  const listIndex = () => Math.max(0, app.messages.findIndex((m) => m.id === app.selectedMessageId));
  function folderGo(index: number, dir: 1 | -1) {
    let i = clamp(index, entries.length - 1);
    while (i >= 0 && i < entries.length && !entries[i].sel) i += dir; // skip account headings
    if (i >= 0 && i < entries.length) cursor = i;
  }
  function readerScroll(px: number | 'top' | 'bottom') {
    if (!bodyEl) return;
    if (px === 'top') bodyEl.scrollTo({ top: 0 });
    else if (px === 'bottom') bodyEl.scrollTo({ top: bodyEl.scrollHeight });
    else bodyEl.scrollBy({ top: px });
  }
  function move(dir: 1 | -1, amount: number | 'half' | 'page') {
    const n = typeof amount === 'number' ? amount : amount === 'half' ? Math.max(1, Math.floor(rows() / 2)) : Math.max(1, rows() - 1);
    if (pane === 'reader') {
      const h = bodyEl?.clientHeight ?? 300;
      readerScroll(dir * (typeof amount === 'number' ? amount * 24 : amount === 'half' ? h / 2 : h * 0.9));
    } else if (pane === 'folders') folderGo(cursor + dir * n, dir);
    else listGo((app.selectedMessageId == null ? (dir > 0 ? -1 : 1) : listIndex()) + dir * n);
  }

  function nextUnread(dir: 1 | -1) {
    const list = app.messages;
    for (let i = listIndex() + dir; i >= 0 && i < list.length; i += dir) {
      if (!list[i].flags.includes(FLAG_SEEN)) return listGo(i);
    }
    say(t('mail.terminal.noMoreUnread'));
  }

  async function chooseEntry(i: number) {
    const e = entries[i];
    if (!e?.sel) return;
    cursor = i;
    await app.select(e.sel);
    if (app.messages.length) listGo(0);
  }
  async function gotoRole(role: string) {
    if (role === 'inbox') {
      const i = entries.findIndex((e) => e.key === 'u:inbox');
      return chooseEntry(i);
    }
    const f = app.allFolders.find((x) => x.role === (role as FolderRole));
    if (!f) return say(t('mail.terminal.error.notFound', { detail: role }), 'error');
    const i = entries.findIndex((e) => e.key === `f:${f.id}`);
    if (i >= 0) await chooseEntry(i);
  }

  // ---- actions on messages -------------------------------------------------------------
  const targets = (): number[] => (app.checked.length ? [...app.checked] : app.selectedMessageId != null ? [app.selectedMessageId] : []);

  async function act(name: 'archive' | 'trash' | 'flag' | 'toggleRead') {
    const ids = targets();
    if (!ids.length) return say(t('mail.terminal.error.noSelection'), 'error');
    if (app.checked.length) {
      if (name === 'toggleRead') {
        const anyUnread = ids.some((id) => !(app.messages.find((m) => m.id === id)?.flags.includes(FLAG_SEEN) ?? true));
        await app.bulk(anyUnread ? 'read' : 'unread');
      } else await app.bulk(name);
      return;
    }
    const id = ids[0];
    if (name === 'archive' || name === 'trash') await app.moveToRole(id, name);
    else if (name === 'flag') await app.toggleFlag(id);
    else await app.toggleRead(id);
  }

  function openMessage() {
    if (!app.messages.length) return;
    if (app.selectedMessageId == null) listGo(0);
    pane = 'reader';
  }

  function focusLine(m: Exclude<Mode, 'normal'>, prefill = '') {
    mode = m;
    line = prefill;
    historyPos = -1;
    queueMicrotask(() => {
      inputEl?.focus();
      const l = inputEl?.value.length ?? 0;
      inputEl?.setSelectionRange(l, l);
    });
  }
  function leaveLine() {
    mode = 'normal';
    line = '';
    inputEl?.blur();
  }

  function perform(a: Action) {
    const n = a.count;
    switch (a.name) {
      case 'down':
        return move(1, n);
      case 'up':
        return move(-1, n);
      case 'halfDown':
        return move(1, 'half');
      case 'halfUp':
        return move(-1, 'half');
      case 'pageDown':
        return move(1, 'page');
      case 'pageUp':
        return move(-1, 'page');
      case 'top':
        return pane === 'reader' ? readerScroll('top') : pane === 'folders' ? folderGo(a.hasCount ? n - 1 : 0, 1) : listGo(a.hasCount ? n - 1 : 0);
      case 'bottom':
        if (pane === 'reader') return readerScroll('bottom');
        if (pane === 'folders') return folderGo(a.hasCount ? n - 1 : entries.length - 1, -1);
        return listGo(a.hasCount ? n - 1 : app.messages.length - 1);
      case 'paneLeft':
        pane = pane === 'reader' ? 'list' : 'folders';
        return;
      case 'paneRight':
        if (pane === 'folders') {
          void chooseEntry(cursor).then(() => (pane = 'list'));
        } else if (pane === 'list') openMessage();
        return;
      case 'cyclePane':
      case 'cyclePaneBack': {
        const order: Pane[] = message ? ['folders', 'list', 'reader'] : ['folders', 'list'];
        const i = order.indexOf(pane);
        pane = order[(i + (a.name === 'cyclePane' ? 1 : order.length - 1)) % order.length];
        return;
      }
      case 'open':
        if (pane === 'folders') void chooseEntry(cursor).then(() => (pane = 'list'));
        else if (pane === 'list') openMessage();
        return;
      case 'back':
        if (output.length) output = [];
        else if (pane !== 'list') pane = 'list';
        else say(t('mail.terminal.quitHint'));
        return;
      case 'command':
        return focusLine('command');
      case 'search':
        return focusLine('search', app.search);
      case 'help':
        helpOpen = true;
        return;
      case 'refresh':
        void app.refreshNow();
        return say(t('mail.terminal.refreshing'));
      case 'compose':
        return app.openCompose();
      case 'reply':
        return void (message ? app.replyTo('reply') : say(t('mail.terminal.error.noSelection'), 'error'));
      case 'replyAll':
        return void (message ? app.replyTo('replyAll') : say(t('mail.terminal.error.noSelection'), 'error'));
      case 'forward':
        return void (message ? app.replyTo('forward') : say(t('mail.terminal.error.noSelection'), 'error'));
      case 'archive':
      case 'trash':
      case 'flag':
      case 'toggleRead':
        return void act(a.name);
      case 'move':
        return focusLine('command', 'move ');
      case 'mark': {
        const id = app.selectedMessageId;
        if (id == null) return;
        app.toggleChecked(id);
        return listGo(listIndex() + 1);
      }
      case 'unmarkAll':
        app.checked = [];
        return;
      case 'nextUnread':
        return nextUnread(1);
      case 'prevUnread':
        return nextUnread(-1);
      case 'nextMessage':
        return listGo(listIndex() + 1);
      case 'prevMessage':
        return listGo(listIndex() - 1);
      case 'goto':
        return void gotoRole(a.arg ?? 'inbox');
    }
  }

  // ---- keyboard ------------------------------------------------------------------------
  function onKey(e: KeyboardEvent) {
    if (document.querySelector('[aria-modal="true"]') || app.paletteOpen) return; // dialogs (composer ...) keep their keys
    if (e.target === inputEl) return; // the command line has its own handler
    if (isEditable(e.target)) return;
    const mod = e.ctrlKey || e.metaKey;
    if (helpOpen) {
      if (e.key === 'Escape' || e.key === 'q' || e.key === '?') helpOpen = false;
      if (!mod) {
        e.preventDefault();
        e.stopPropagation();
      }
      return;
    }
    const r = parser.feed({ key: e.key, ctrl: e.ctrlKey, shift: e.shiftKey, alt: e.altKey, meta: e.metaKey });
    clearTimeout(seqTimer);
    pending = parser.pending;
    if (r === 'pending') {
      seqTimer = setTimeout(() => {
        parser.reset();
        pending = '';
      }, 1500);
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    // Browser shortcuts (Ctrl+C, Ctrl+R, F5, Alt+Left ...) stay the browser's; plain keys belong to this view only.
    if (r === null) {
      if (!mod && !e.altKey && e.key.length === 1) {
        e.preventDefault();
        e.stopPropagation();
      }
      return;
    }
    e.preventDefault();
    e.stopPropagation();
    perform(r);
  }

  onMount(() => {
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      clearTimeout(seqTimer);
    };
  });

  // ---- command and search line ---------------------------------------------------------
  const commandNames = [...new Set(specs.flatMap((s) => [s.name, ...s.aliases]))].sort();

  function complete() {
    if (mode !== 'command' || line.includes(' ')) return;
    const hits = commandNames.filter((c) => c.startsWith(line));
    if (hits.length === 1) line = hits[0] + ' ';
    else if (hits.length > 1) {
      let p = hits[0];
      for (const h of hits) while (!h.startsWith(p)) p = p.slice(0, -1);
      line = p;
      say(hits.join('  '));
    }
  }

  async function submit() {
    const text = line.trim();
    if (mode === 'search') {
      leaveLine();
      await app.setSearch(text);
      if (app.messages.length) listGo(0);
      return;
    }
    const cmd = text.replace(/^:/, '');
    leaveLine();
    if (!cmd || busy) return;
    history = [cmd, ...history.filter((h) => h !== cmd)].slice(0, 50);
    historyPos = -1;
    busy = true;
    const echo: OutLine = { kind: 'text', text: `:${cleanLine(cmd)}` };
    try {
      output = [echo, ...(await runCommand(cmd, app))];
    } catch (e) {
      output = [echo, { kind: 'error', text: errorText(e) }];
    } finally {
      busy = false;
    }
  }

  function onInputKey(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      e.preventDefault();
      void submit();
    } else if (e.key === 'Escape' || (e.ctrlKey && e.key === 'c')) {
      e.preventDefault();
      e.stopPropagation();
      leaveLine();
    } else if (e.key === 'Tab') {
      e.preventDefault();
      complete();
    } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown' || (e.ctrlKey && (e.key === 'p' || e.key === 'n'))) {
      e.preventDefault();
      if (mode !== 'command') return;
      const up = e.key === 'ArrowUp' || e.key === 'p';
      historyPos = Math.max(-1, Math.min(history.length - 1, historyPos + (up ? 1 : -1)));
      line = historyPos < 0 ? '' : history[historyPos];
    } else if (e.key === 'Backspace' && line === '') {
      leaveLine(); // like vim: Backspace on an empty line leaves it
    }
  }

  const HELP: { title: string; rows: [string, string][] }[] = [
    {
      title: 'move',
      rows: [
        ['j k  ↓ ↑', 'down'],
        ['5j  12G', 'count'],
        ['gg  G', 'topBottom'],
        ['Ctrl-d  Ctrl-u', 'half'],
        ['Ctrl-f  Ctrl-b  Space', 'page'],
        ['h  l  Tab', 'panes'],
        ['Enter  o', 'open'],
        ['Esc  q', 'back'],
      ],
    },
    {
      title: 'mail',
      rows: [
        ['c', 'compose'],
        ['r  R  f', 'reply'],
        ['dd  #', 'trash'],
        ['e', 'archive'],
        ['s', 'flag'],
        ['u', 'read'],
        ['x  X', 'mark'],
        ['m', 'move'],
        ['n  N', 'unread'],
        ['J  K', 'nextMessage'],
      ],
    },
    {
      title: 'go',
      rows: [
        ['gi  gs  gd', 'gotoA'],
        ['ga  gt  gj', 'gotoB'],
        ['gr  Ctrl-l', 'refresh'],
        ['/', 'search'],
        [':', 'command'],
        ['?', 'help'],
        [':q', 'quit'],
      ],
    },
  ];
</script>

<div class="term" role="application" aria-label={t('mail.terminal.appLabel')}>
  <header class="status">
    <b>TuxMail</b>
    <span class="scope">{scopeLabel}</span>
    {#if app.search}<span class="chip">/{app.search}</span>{/if}
    <span class="grow"></span>
    <span>{t('mail.terminal.counts', { total: app.messagesTotal, unread: unreadHere })}</span>
    {#if app.checked.length}<span class="chip">{t('mail.terminal.markedCount', { count: app.checked.length })}</span>{/if}
  </header>

  <div class="panes">
    <nav class="pane folders" class:focus={pane === 'folders'} bind:this={folderEl} aria-label={t('mail.terminal.foldersLabel')}>
      {#each entries as e, i (e.key)}
        {#if e.sel}
          <button type="button" class="entry" class:cur={pane === 'folders' && i === cursor} class:active={i === currentEntry} style:padding-left="{8 + e.depth * 12}px" onclick={() => void chooseEntry(i).then(() => (pane = 'list'))}>
            <span class="lbl">{e.label}</span>{#if e.unread}<span class="num">{e.unread}</span>{/if}
          </button>
        {:else}
          <div class="heading">{e.label}</div>
        {/if}
      {/each}
    </nav>

    <div
      class="pane list"
      class:focus={pane === 'list'}
      bind:this={listEl}
      onscroll={() => {
        const el = listEl;
        if (el && el.scrollTop + el.clientHeight > el.scrollHeight - 80) void app.loadMoreMessages();
      }}
      role="listbox"
      tabindex="-1"
      aria-label={t('mail.terminal.listLabel')}
      aria-activedescendant={app.selectedMessageId != null ? `term-msg-${app.selectedMessageId}` : undefined}
      data-list="messages"
    >
      {#each app.messages as m (m.id)}
        <div
          id="term-msg-{m.id}"
          class="row"
          class:selected={m.id === app.selectedMessageId}
          class:unread={!m.flags.includes(FLAG_SEEN)}
          role="option"
          aria-selected={m.id === app.selectedMessageId}
          tabindex="-1"
          onclick={() => {
            void app.selectMessage(m.id);
            pane = 'list';
          }}
          ondblclick={() => (pane = 'reader')}
          onkeydown={() => {}}
        >
          <span class="marks" aria-hidden="true">{marks(m)}</span><span class="text">{rowText(m)}</span>
        </div>
      {:else}
        <p class="empty">{app.search ? t('mail.list.noResults') : t('mail.list.empty')}</p>
      {/each}
    </div>

    <div class="pane reader" class:focus={pane === 'reader'} aria-label={t('mail.terminal.readerLabel')} role="region">
      {#if message}
        <div class="body" bind:this={bodyEl} data-term-body>
          <pre class="head">{t('mail.terminal.from')}: {message.from.map((a) => cleanLine(a.name ? `${a.name} <${a.email}>` : a.email)).join(', ')}
{t('mail.terminal.to')}: {message.to.map((a) => cleanLine(a.email)).join(', ')}
{t('mail.terminal.date')}: {message.date}
{t('mail.terminal.subject')}: {cleanLine(message.subject)}{#each message.attachments as a (a.id)}{'\n'}{t('mail.terminal.attachment')}: {cleanLine(a.filename)}{/each}</pre>
          <pre class="text-body">{body}</pre>
        </div>
      {:else}
        <p class="empty">{t('mail.terminal.nothingOpen')}</p>
      {/if}
    </div>
  </div>

  {#if output.length}
    <div class="output" role="log" aria-label={t('mail.terminal.outputLabel')}>
      {#each output as o, i (i)}
        {#if o.kind === 'row'}
          <button class="out-row" onclick={() => void app.selectMessage(o.id)}>{o.text}</button>
        {:else}
          <div class:err={o.kind === 'error'} role={o.kind === 'error' ? 'alert' : undefined}>{o.text}</div>
        {/if}
      {/each}
    </div>
  {/if}

  <form
    class="cmd"
    class:active={mode !== 'normal'}
    onsubmit={(e) => {
      e.preventDefault();
      void submit();
    }}
  >
    {#if mode === 'normal'}
      <span class="mode">{pane === 'folders' ? t('mail.terminal.paneFolders') : pane === 'reader' ? t('mail.terminal.paneReader') : t('mail.terminal.paneList')}</span>
      {#if pending}<span class="pending">{pending}</span>{/if}
      <span class="hint">{t('mail.terminal.statusHint')}</span>
    {:else}
      <label class="prompt" for="term-cmd">{mode === 'search' ? '/' : ':'}</label>
    {/if}
    <input
      id="term-cmd"
      class:hidden={mode === 'normal'}
      bind:this={inputEl}
      bind:value={line}
      onkeydown={onInputKey}
      onblur={() => {
        if (mode !== 'normal') leaveLine();
      }}
      type="text"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      enterkeyhint="send"
      aria-label={mode === 'search' ? t('mail.terminal.searchLabel') : t('mail.terminal.commandLabel')}
      tabindex={mode === 'normal' ? -1 : 0}
      {...inputAttrs}
    />
  </form>

  {#if helpOpen}
    <div class="help" role="dialog" aria-label={t('mail.terminal.help.title')}>
      <h2>{t('mail.terminal.help.title')}</h2>
      <div class="cols">
        {#each HELP as sec (sec.title)}
          <section>
            <h3>{t(`mail.terminal.help.sec.${sec.title}` as 'mail.terminal.help.sec.move')}</h3>
            {#each sec.rows as [keys, what] (keys)}
              <div class="hrow"><kbd>{keys}</kbd><span>{t(`mail.terminal.help.${what}` as 'mail.terminal.help.down')}</span></div>
            {/each}
          </section>
        {/each}
      </div>
      <p class="hint">{t('mail.terminal.help.close')}</p>
    </div>
  {/if}
</div>

<style>
  .term {
    position: fixed;
    inset: 0;
    z-index: 70;
    display: flex;
    flex-direction: column;
    font-family: var(--tm-font-mono, ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace);
    font-size: 13px;
    --t-bg: #0f1419;
    --t-fg: #d6dbe0;
    --t-dim: #7d8793;
    --t-line: #2a323c;
    --t-hi: #1f2a37;
    --t-accent: #4fb3ff;
    --t-sel: #2563a8;
    --t-err: #ff7b72;
    background: var(--t-bg);
    color: var(--t-fg);
  }
  .status {
    display: flex;
    gap: 14px;
    align-items: center;
    padding: 2px 10px;
    background: var(--t-accent);
    color: #04111f;
    white-space: nowrap;
    overflow: hidden;
  }
  .status .grow,
  .grow {
    flex: 1;
  }
  .chip {
    background: #04111f;
    color: var(--t-accent);
    padding: 0 6px;
    border-radius: 3px;
  }
  .panes {
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .pane {
    min-width: 0;
    overflow: auto;
    border-right: 1px solid var(--t-line);
    outline: none;
  }
  .pane.focus {
    box-shadow: inset 0 2px 0 var(--t-accent);
  }
  .folders {
    flex: 0 0 24ch;
    padding-top: 2px;
  }
  .list {
    flex: 1 1 50%;
  }
  .reader {
    flex: 1 1 50%;
    border-right: none;
    display: flex;
    flex-direction: column;
  }
  .heading {
    padding: 6px 8px 2px;
    color: var(--t-dim);
    text-transform: uppercase;
    font-size: 11px;
  }
  .entry {
    display: flex;
    width: 100%;
    gap: 8px;
    justify-content: space-between;
    background: none;
    border: none;
    color: inherit;
    font: inherit;
    text-align: left;
    padding-top: 2px;
    padding-bottom: 2px;
    padding-right: 8px;
    cursor: pointer;
  }
  .entry .lbl {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .entry .num {
    color: var(--t-accent);
  }
  .entry.active {
    color: var(--t-accent);
  }
  .entry.cur {
    background: var(--t-sel);
    color: #fff;
  }
  .row {
    display: flex;
    white-space: pre;
    padding: 1px 8px;
    min-height: 24px;
    align-items: center;
    cursor: pointer;
  }
  .row .text {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .row .marks {
    color: var(--t-dim);
    margin-right: 1ch;
  }
  .row.unread {
    font-weight: 700;
  }
  .row.selected {
    background: var(--t-hi);
    box-shadow: inset 3px 0 0 var(--t-accent);
  }
  .list.focus .row.selected {
    background: var(--t-sel);
    color: #fff;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 8px 12px;
  }
  pre {
    margin: 0;
    font: inherit;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .head {
    color: var(--t-dim);
    margin-bottom: 12px;
  }
  .empty {
    padding: 10px 12px;
    margin: 0;
    color: var(--t-dim);
  }
  .output {
    max-height: 30%;
    overflow-y: auto;
    border-top: 1px solid var(--t-line);
    padding: 4px 10px;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    white-space: pre-wrap;
  }
  .out-row {
    font: inherit;
    background: none;
    border: none;
    color: inherit;
    text-decoration: underline;
    text-align: left;
    padding: 0;
    cursor: pointer;
  }
  .err {
    color: var(--t-err);
  }
  .cmd {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 26px;
    padding: 0 10px;
    border-top: 1px solid var(--t-line);
    background: #0b0f13;
  }
  .cmd.active {
    box-shadow: inset 0 0 0 1px var(--t-accent);
  }
  .mode {
    background: var(--t-accent);
    color: #04111f;
    padding: 0 8px;
    font-weight: 700;
  }
  .pending {
    color: var(--t-accent);
    font-weight: 700;
  }
  .hint {
    color: var(--t-dim);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .prompt {
    font-weight: 700;
    color: var(--t-accent);
  }
  .cmd input {
    flex: 1;
    min-width: 0;
    font: inherit;
    border: none;
    outline: none;
    background: none;
    color: inherit;
  }
  .cmd input.hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    opacity: 0;
    pointer-events: none;
  }
  .help {
    position: absolute;
    inset: 5% 4%;
    overflow: auto;
    background: #0b0f13;
    border: 1px solid var(--t-accent);
    padding: 12px 20px;
    box-shadow: 0 10px 40px #000a;
  }
  .help h2 {
    margin: 0 0 8px;
    font-size: 15px;
    color: var(--t-accent);
  }
  .help h3 {
    margin: 10px 0 4px;
    font-size: 12px;
    color: var(--t-dim);
    text-transform: uppercase;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(380px, 1fr));
    gap: 0 28px;
  }
  .hrow {
    display: flex;
    gap: 12px;
  }
  .hrow kbd {
    flex: 0 0 18ch;
    font: inherit;
    color: var(--t-accent);
  }
  @media (max-width: 899px) {
    .folders {
      display: none;
    }
  }
</style>
