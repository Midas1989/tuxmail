<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Terminal view: mail as monospaced text with the command line of
  `tuxmail term`. It is an ordinary part of the page (no terminal emulator, no
  shell, no PTY); commands go through lib/terminal/exec.ts to the normal API.
  Mail text is only ever rendered as text, never as HTML.
-->
<script lang="ts">
  import { app } from '../../state';
  import { FLAG_FLAGGED, FLAG_SEEN } from '../../types';
  import { t } from '../../i18n/index.svelte';
  import { TERM_INPUT_ATTR } from '../../keyboard';
  import { ParseError, bodyText, cleanLine } from '../../terminal/parse';
  import { TermError, rowText, runCommand, type OutLine } from '../../terminal/exec';

  let line = $state('');
  let output = $state<OutLine[]>([]);
  let busy = $state(false);
  let history: string[] = [];
  let historyPos = -1;
  let listEl = $state<HTMLElement>();
  let panesEl = $state<HTMLElement>();

  // Width of the list in percent of the view; adjustable by dragging or with the arrow keys on the divider.
  const SPLIT_KEY = 'tuxmail.term.listPct';
  const clampPct = (n: number) => Math.max(25, Math.min(75, Math.round(n)));
  function loadPct(): number {
    try {
      const v = Number(localStorage.getItem(SPLIT_KEY));
      return v ? clampPct(v) : 50;
    } catch {
      return 50;
    }
  }
  let listPct = $state(loadPct());
  function setPct(n: number) {
    listPct = clampPct(n);
    try {
      localStorage.setItem(SPLIT_KEY, String(listPct));
    } catch {
      /* private mode: keep the value for this session */
    }
  }
  function dragSplit(e: PointerEvent) {
    const box = panesEl?.getBoundingClientRect();
    if (!box || !box.width) return;
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture?.(e.pointerId);
    const move = (ev: PointerEvent) => setPct(((ev.clientX - box.left) / box.width) * 100);
    const up = () => {
      el.removeEventListener('pointermove', move);
      el.removeEventListener('pointerup', up);
    };
    el.addEventListener('pointermove', move);
    el.addEventListener('pointerup', up);
  }
  function splitKey(e: KeyboardEvent) {
    const d = e.key === 'ArrowLeft' ? -5 : e.key === 'ArrowRight' ? 5 : 0;
    if (!d) return;
    e.preventDefault();
    e.stopPropagation();
    setPct(listPct + d);
  }
  let inputEl = $state<HTMLInputElement>();

  const message = $derived(app.message);
  const body = $derived(message ? bodyText(message.text, message.html) : '');
  const inputAttrs = { [TERM_INPUT_ATTR]: '' };

  $effect(() => {
    void app.listScrollTick;
    const id = app.selectedMessageId;
    if (id == null || !listEl) return;
    listEl.querySelector(`#term-msg-${id}`)?.scrollIntoView?.({ block: 'nearest' });
  });

  function errorText(e: unknown): string {
    if (e instanceof ParseError) return t(`mail.terminal.error.${e.code}`, { detail: cleanLine(e.detail) });
    if (e instanceof TermError) return t(`mail.terminal.error.${e.key}`, { detail: e.detail });
    return cleanLine(e instanceof Error ? e.message : String(e));
  }

  async function submit() {
    const cmd = line.trim().replace(/^:/, '');
    if (!cmd || busy) return;
    history = [cmd, ...history.filter((h) => h !== cmd)].slice(0, 50);
    historyPos = -1;
    line = '';
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
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      line = '';
      inputEl?.blur();
      listEl?.focus();
    } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      e.preventDefault();
      historyPos = Math.max(-1, Math.min(history.length - 1, historyPos + (e.key === 'ArrowUp' ? 1 : -1)));
      line = historyPos < 0 ? '' : history[historyPos];
    }
  }

  function moreOnScroll() {
    const el = listEl;
    if (el && el.scrollTop + el.clientHeight > el.scrollHeight - 80) void app.loadMoreMessages();
  }

  function marks(flags: string[], attachments: boolean): string {
    return (flags.includes(FLAG_SEEN) ? ' ' : '*') + (flags.includes(FLAG_FLAGGED) ? '!' : ' ') + (attachments ? '@' : ' ');
  }
</script>

<div class="term" class:has-message={message != null}>
  <div class="panes" bind:this={panesEl} style:--term-list="{listPct}%">
    <div
      class="list"
      bind:this={listEl}
      onscroll={moreOnScroll}
      role="listbox"
      tabindex="0"
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
          onclick={() => app.selectMessage(m.id)}
          onkeydown={() => {}}
        >
          <span class="marks" aria-hidden="true">{marks(m.flags, m.hasAttachments)}</span><span class="text">{rowText(m)}</span>
        </div>
      {:else}
        <p class="empty">{app.search ? t('mail.list.noResults') : t('mail.list.empty')}</p>
      {/each}
    </div>
    <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions: the divider is an ARIA separator with keys -->
    <div
      class="split"
      role="separator"
      tabindex="0"
      aria-orientation="vertical"
      aria-label={t('mail.terminal.splitLabel')}
      aria-valuemin="25"
      aria-valuemax="75"
      aria-valuenow={listPct}
      onpointerdown={dragSplit}
      onkeydown={splitKey}
    ></div>
    <div class="reader" aria-label={t('mail.terminal.readerLabel')} role="region">
      {#if message}
        <div class="actions" role="toolbar" aria-label={t('mail.terminal.actionsLabel')}>
          <button class="back" onclick={() => app.selectMessage(null)}>{t('common.action.back')}</button>
          <button onclick={() => app.replyTo('reply')}>{t('common.action.reply')} <kbd>r</kbd></button>
          <button onclick={() => app.toggleFlag(message.id)}>{t('mail.terminal.flag')} <kbd>s</kbd></button>
          <button onclick={() => app.toggleRead(message.id)}>{t('mail.terminal.unread')} <kbd>u</kbd></button>
          <button onclick={() => app.moveToRole(message.id, 'archive')}>{t('mail.terminal.archive')} <kbd>e</kbd></button>
          <button onclick={() => app.moveToRole(message.id, 'trash')}>{t('mail.terminal.trash')} <kbd>#</kbd></button>
        </div>
        <!-- svelte-ignore a11y_no_noninteractive_tabindex: a scroll area must be reachable by keyboard -->
        <div class="body" tabindex="0" data-term-body>
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
          <button class="out-row" onclick={() => app.selectMessage(o.id)}>{o.text}</button>
        {:else}
          <div class:err={o.kind === 'error'} role={o.kind === 'error' ? 'alert' : undefined}>{o.text}</div>
        {/if}
      {/each}
      <button class="close" onclick={() => (output = [])} aria-label={t('common.action.close')}>×</button>
    </div>
  {/if}
  <form
    class="cmd"
    onsubmit={(e) => {
      e.preventDefault();
      void submit();
    }}
  >
    <label class="prompt" for="term-cmd" title={t('mail.terminal.commandLabel')}>:</label>
    <input
      id="term-cmd"
      bind:this={inputEl}
      bind:value={line}
      onkeydown={onInputKey}
      type="text"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      enterkeyhint="send"
      aria-label={t('mail.terminal.commandLabel')}
      placeholder={t('mail.terminal.placeholder')}
      {...inputAttrs}
    />
  </form>
</div>

<style>
  .term {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    font-family: var(--tm-font-mono, ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace);
    font-size: 13px;
    background: var(--tm-surface);
    color: var(--tm-fg);
  }
  .panes {
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .list {
    flex: 0 0 var(--term-list, 50%);
    min-width: 0;
    overflow-y: auto;
    outline-offset: -2px;
  }
  .split {
    flex: 0 0 6px;
    cursor: col-resize;
    background: var(--tm-line);
    touch-action: none;
  }
  .split:hover,
  .split:focus-visible {
    background: var(--tm-accent);
  }
  .row {
    display: flex;
    white-space: pre;
    padding: 2px 8px;
    cursor: pointer;
    min-height: 24px;
    align-items: center;
  }
  .row .text {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .row.unread {
    font-weight: 700;
  }
  .row.selected {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .reader {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding: 4px 8px;
    border-bottom: 1px solid var(--tm-line);
  }
  .actions button,
  .out-row {
    font: inherit;
    background: none;
    border: 1px solid var(--tm-line-strong);
    border-radius: 4px;
    color: inherit;
    padding: 2px 8px;
    min-height: 28px;
    cursor: pointer;
  }
  .actions .back {
    display: none;
  }
  kbd {
    font: inherit;
    color: var(--tm-muted);
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 8px;
    outline-offset: -2px;
  }
  pre {
    margin: 0;
    font: inherit;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .head {
    color: var(--tm-muted);
    margin-bottom: 12px;
  }
  .empty {
    padding: 12px;
    margin: 0;
    color: var(--tm-muted);
  }
  .output {
    position: relative;
    max-height: 30%;
    overflow-y: auto;
    border-top: 1px solid var(--tm-line);
    padding: 4px 28px 4px 8px;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    white-space: pre-wrap;
  }
  .out-row {
    border: none;
    padding: 0 2px;
    min-height: 22px;
    text-align: left;
    text-decoration: underline;
  }
  .err {
    color: var(--tm-danger);
  }
  .close {
    position: absolute;
    top: 2px;
    right: 4px;
    font: inherit;
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
    min-width: 24px;
    min-height: 24px;
  }
  .cmd {
    display: flex;
    align-items: center;
    gap: 6px;
    border-top: 1px solid var(--tm-line-strong);
    padding: 4px 8px;
  }
  .prompt {
    font-weight: 700;
  }
  .cmd input {
    flex: 1;
    min-width: 0;
    font: inherit;
    border: none;
    outline: none;
    background: none;
    color: inherit;
    min-height: 28px;
  }
  .cmd:focus-within {
    box-shadow: inset 0 0 0 2px var(--tm-accent);
  }
  @media (max-width: 899px) {
    /* Phones and small windows: list or message, like the classic view. */
    .split {
      display: none;
    }
    .list {
      flex: 1;
    }
    .reader {
      display: none;
    }
    .has-message .list {
      display: none;
    }
    .has-message .reader {
      display: flex;
    }
    .actions .back {
      display: inline-block;
    }
    .cmd input {
      font-size: 16px; /* no zoom on focus in iOS Safari */
    }
  }
</style>
