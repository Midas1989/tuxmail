<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  "Mail-Fenster": the full mail in movable, resizable in-app windows (Wails
  v2 has only one native window). Several can be open; each has its own
  toolbar (reply, reply all, forward, print, source, new browser window in
  web/PWA mode, close). Opened by double-click / Shift+Enter on a list row
  or bubble (app.openMailWindow).
-->
<script lang="ts">
  import Reply from '@lucide/svelte/icons/reply';
  import ReplyAll from '@lucide/svelte/icons/reply-all';
  import Forward from '@lucide/svelte/icons/forward';
  import Printer from '@lucide/svelte/icons/printer';
  import FileCode from '@lucide/svelte/icons/file-code';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import X from '@lucide/svelte/icons/x';
  import { app, prefs } from '../../state';
  import type { MailWindow } from '../../state/app.svelte';
  import { formatFullDate, t } from '../../i18n/index.svelte';
  import type { Address } from '../../types';
  import MailFrame, { buildSrcdoc, escapeHtml } from './MailFrame.svelte';
  import AttachmentCard from '../common/AttachmentCard.svelte';
  import { isWails } from '../../platform';
  import { proxiedRemoteHtml } from '../../remote';

  let sources = $state<Record<number, string | null>>({});
  let remote = $state<Record<number, boolean>>({});
  /** Proxied HTML per window after "load images". */
  let remoteHtml = $state<Record<number, string | null>>({});

  async function loadRemote(w: MailWindow) {
    const html = await proxiedRemoteHtml(app.api, w.messageId);
    remoteHtml = { ...remoteHtml, [w.key]: html };
    remote = { ...remote, [w.key]: true };
  }

  const fmtList = (l: Address[]) => l.map((a) => (a.name ? `${a.name} <${a.email}>` : a.email)).join(', ');

  function drag(e: PointerEvent, w: MailWindow, mode: 'move' | 'resize') {
    if (e.button !== 0 || (mode === 'move' && (e.target as HTMLElement).closest('button'))) return;
    e.preventDefault();
    app.focusMailWindow(w.key);
    const sx = e.clientX;
    const sy = e.clientY;
    const start = { x: w.x, y: w.y, w: w.w, h: w.h };
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    const onmove = (ev: PointerEvent) => {
      const dx = ev.clientX - sx;
      const dy = ev.clientY - sy;
      if (mode === 'move') {
        app.moveMailWindow(w.key, { x: Math.min(Math.max(start.x + dx, -start.w + 120), innerWidth - 80), y: Math.min(Math.max(start.y + dy, 0), innerHeight - 40) });
      } else {
        app.moveMailWindow(w.key, { w: Math.max(360, start.w + dx), h: Math.max(260, start.h + dy) });
      }
    };
    const onup = () => {
      el.removeEventListener('pointermove', onmove);
      el.removeEventListener('pointerup', onup);
    };
    el.addEventListener('pointermove', onmove);
    el.addEventListener('pointerup', onup);
  }

  async function toggleSource(w: MailWindow) {
    if (sources[w.key] != null) {
      sources = { ...sources, [w.key]: null };
      return;
    }
    try {
      sources = { ...sources, [w.key]: await app.api.messageSource(w.messageId) };
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  /** Print the mail only: a temporary frame without scripts (allow-modals for the print dialog). */
  function print(w: MailWindow) {
    const m = w.message;
    if (!m) return;
    const f = document.createElement('iframe');
    f.setAttribute('sandbox', 'allow-same-origin allow-modals');
    f.style.cssText = 'position:fixed;width:0;height:0;border:0;right:0;bottom:0';
    const head = `<h2 style="font:600 16px system-ui;margin:0 0 6px">${escapeHtml(m.subject)}</h2><p style="font:12px system-ui;color:#555;margin:0 0 14px">${escapeHtml(fmtList(m.from))} · ${escapeHtml(formatFullDate(m.date))}</p>`;
    const html = remoteHtml[w.key] ?? m.html;
    f.srcdoc = buildSrcdoc({ html: html ? head + html : undefined, text: m.html ? undefined : m.text, allowRemote: !!remote[w.key], dark: false });
    f.onload = () => {
      try {
        f.contentWindow?.print();
      } finally {
        setTimeout(() => f.remove(), 1000);
      }
    };
    document.body.appendChild(f);
  }

  function newBrowserWindow(w: MailWindow) {
    window.open(`${location.pathname}#/mail/${w.messageId}`, `tuxmail-mail-${w.messageId}`, 'popup,width=900,height=760');
    app.closeMailWindow(w.key);
  }

  function onkeydown(e: KeyboardEvent, w: MailWindow) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      app.closeMailWindow(w.key);
    }
  }
</script>

{#each app.mailWindows as w (w.key)}
  {@const m = w.message}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="mw"
    tabindex="-1"
    role="dialog"
    aria-label={m?.subject ?? t('mail.window.loading')}
    style:left="{w.x}px"
    style:top="{w.y}px"
    style:width="{w.w}px"
    style:height="{w.h}px"
    style:z-index={80 + w.z}
    onpointerdown={() => app.focusMailWindow(w.key)}
    onkeydown={(e) => onkeydown(e, w)}
    data-testid="mail-window"
  >
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <header class="bar" onpointerdown={(e) => drag(e, w, 'move')}>
      <span class="title">{m?.subject || t('mail.list.noSubject')}</span>
      <div class="tools">
        {#if m}
          <button class="icon-btn" title={t('common.action.reply')} aria-label={t('common.action.reply')} onclick={() => app.replyToMessage(m, 'reply')}><Reply size={16} /></button>
          <button class="icon-btn" title={t('common.action.replyAll')} aria-label={t('common.action.replyAll')} onclick={() => app.replyToMessage(m, 'replyAll')}><ReplyAll size={16} /></button>
          <button class="icon-btn" title={t('common.action.forward')} aria-label={t('common.action.forward')} onclick={() => app.replyToMessage(m, 'forward')}><Forward size={16} /></button>
          <button class="icon-btn" title={t('mail.window.print')} aria-label={t('mail.window.print')} onclick={() => print(w)}><Printer size={16} /></button>
          <button class="icon-btn" title={t('mail.window.source')} aria-label={t('mail.window.source')} aria-pressed={sources[w.key] != null} onclick={() => toggleSource(w)}><FileCode size={16} /></button>
          {#if !isWails()}
            <button class="icon-btn" title={t('mail.window.newWindow')} aria-label={t('mail.window.newWindow')} onclick={() => newBrowserWindow(w)}><ExternalLink size={16} /></button>
          {/if}
        {/if}
        <button class="icon-btn" title={t('common.action.close')} aria-label={t('common.action.close')} onclick={() => app.closeMailWindow(w.key)}><X size={16} /></button>
      </div>
    </header>
    {#if m}
      <dl class="hdr">
        <dt>{t('compose.from')}</dt>
        <dd>{fmtList(m.from)}</dd>
        <dt>{t('mail.reader.to')}</dt>
        <dd>{fmtList(m.to)}</dd>
        {#if m.cc.length}<dt>{t('mail.reader.cc')}</dt><dd>{fmtList(m.cc)}</dd>{/if}
        <dt>{t('chat.original.date')}</dt>
        <dd>{formatFullDate(m.date)}</dd>
      </dl>
      {#if m.hasRemoteContent && !remote[w.key]}
        <div class="notice">
          <span>{t('mail.reader.remoteBlocked')}</span>
          <button class="btn" onclick={() => loadRemote(w)}>{t('mail.reader.loadRemote')}</button>
        </div>
      {/if}
      {#if m.attachments.length}
        <div class="atts">
          {#each m.attachments as a (a.id)}<AttachmentCard attachment={a} compact />{/each}
        </div>
      {/if}
      <div class="body">
        {#if sources[w.key] != null}
          <pre class="src">{sources[w.key]}</pre>
        {:else}
          <MailFrame html={remoteHtml[w.key] ?? m.html} text={m.text} allowRemote={!!remote[w.key]} dark={prefs.effectiveTheme === 'dark'} title={t('mail.reader.bodyTitle', { subject: m.subject })} />
        {/if}
      </div>
    {:else}
      <p class="loading">{t('mail.window.loading')}</p>
    {/if}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="grip" onpointerdown={(e) => drag(e, w, 'resize')} title={t('mail.window.resize')}></div>
  </div>
{/each}

<style>
  .mw {
    position: fixed;
    display: flex;
    flex-direction: column;
    min-width: 320px;
    min-height: 240px;
    max-width: 100vw;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: 12px;
    box-shadow: 0 18px 48px rgb(10 14 22 / 0.28), var(--tm-shadow-lg);
    overflow: hidden;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 40px;
    flex: none;
    padding: 0 6px 0 14px;
    background: var(--tm-surface-2);
    border-bottom: 1px solid var(--tm-line);
    cursor: move;
    user-select: none;
    touch-action: none;
  }
  .title {
    flex: 1;
    min-width: 0;
    font-weight: 600;
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tools {
    display: flex;
    gap: 2px;
    cursor: default;
  }
  .hdr {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 2px 12px;
    margin: 0;
    padding: 10px 14px;
    font-size: 12.5px;
    border-bottom: 1px solid var(--tm-line);
  }
  .hdr dt {
    color: var(--tm-muted);
  }
  .hdr dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .notice {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 6px 14px;
    font-size: 12.5px;
    background: color-mix(in srgb, var(--tm-warn) 10%, transparent);
  }
  .atts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 8px 14px;
  }
  .body {
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .body > :global(*) {
    flex: 1;
  }
  .src {
    margin: 0;
    padding: 12px 14px;
    overflow: auto;
    font: 12px/1.45 var(--tm-mono);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .loading {
    padding: 20px;
    color: var(--tm-muted);
  }
  .grip {
    position: absolute;
    right: 0;
    bottom: 0;
    width: 18px;
    height: 18px;
    cursor: nwse-resize;
    touch-action: none;
    background: linear-gradient(135deg, transparent 50%, var(--tm-line) 50%, var(--tm-line) 60%, transparent 60%, transparent 70%, var(--tm-line) 70%, var(--tm-line) 80%, transparent 80%);
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .mw {
      left: 0 !important;
      top: 0 !important;
      width: 100vw !important;
      height: 100% !important;
      border-radius: 0;
    }
    .grip {
      display: none;
    }
  }
</style>
