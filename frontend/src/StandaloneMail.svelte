<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  #/mail/<id>: one mail on its own page ("In neuem Fenster öffnen" in
  web/PWA mode; the desktop uses the in-app mail windows instead).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { app, prefs } from './lib/state';
  import { formatFullDate, t } from './lib/i18n/index.svelte';
  import type { Address, MessageDetail } from './lib/types';
  import MailFrame from './lib/components/mail/MailFrame.svelte';
  import AttachmentCard from './lib/components/common/AttachmentCard.svelte';

  let { id }: { id: number } = $props();
  let m = $state<MessageDetail | null>(null);
  let error = $state('');
  let allowRemote = $state(false);
  const fmtList = (l: Address[]) => l.map((a) => (a.name ? `${a.name} <${a.email}>` : a.email)).join(', ');

  $effect(() => {
    prefs.apply(document.documentElement);
  });
  onMount(() => {
    void prefs
      .connect(app.api)
      .catch(() => undefined)
      .then(() => app.api.getMessage(id))
      .then((d) => {
        m = d;
        document.title = d.subject || 'TuxMail';
      })
      .catch((err) => (error = err instanceof Error ? err.message : String(err)));
  });
</script>

<div class="page">
  {#if m}
    <header>
      <h1>{m.subject || t('mail.list.noSubject')}</h1>
      <dl>
        <dt>{t('compose.from')}</dt>
        <dd>{fmtList(m.from)}</dd>
        <dt>{t('mail.reader.to')}</dt>
        <dd>{fmtList(m.to)}</dd>
        <dt>{t('chat.original.date')}</dt>
        <dd>{formatFullDate(m.date)}</dd>
      </dl>
      {#if m.hasRemoteContent && !allowRemote}
        <button class="btn" onclick={() => (allowRemote = true)}>{t('mail.reader.loadRemote')}</button>
      {/if}
      {#if m.attachments.length}
        <div class="atts">{#each m.attachments as a (a.id)}<AttachmentCard attachment={a} compact />{/each}</div>
      {/if}
    </header>
    <div class="body"><MailFrame html={m.html} text={m.text} {allowRemote} dark={prefs.effectiveTheme === 'dark'} title={m.subject} /></div>
  {:else if error}
    <p class="err" role="alert">{error}</p>
  {:else}
    <p class="muted">{t('mail.window.loading')}</p>
  {/if}
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    height: 100%;
    background: var(--tm-bg);
  }
  header {
    padding: 14px 20px;
    border-bottom: 1px solid var(--tm-line);
    background: var(--tm-surface);
  }
  h1 {
    margin: 0 0 8px;
    font-size: 17px;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 2px 12px;
    margin: 0 0 8px;
    font-size: 13px;
  }
  dt {
    color: var(--tm-muted);
  }
  dd {
    margin: 0;
  }
  .atts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .body {
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .body > :global(*) {
    flex: 1;
  }
  .err,
  .muted {
    padding: 20px;
  }
  .err {
    color: var(--tm-danger);
  }
</style>
