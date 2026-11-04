<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Reading pane toolbar additions: Spam / Kein Spam, print view, archive PDF. -->
<script lang="ts">
  import ShieldAlert from '@lucide/svelte/icons/shield-alert';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import Printer from '@lucide/svelte/icons/printer';
  import FileDown from '@lucide/svelte/icons/file-down';
  import { app, comfort } from '../../state';
  import { i18n, t } from '../../i18n/index.svelte';

  interface Props {
    messageId: number;
    conversationId?: number;
  }
  let { messageId, conversationId }: Props = $props();

  const isSpam = $derived(comfort.insights[messageId]?.spam.level === 'spam');

  async function toggleSpam() {
    const spam = !isSpam;
    await comfort.api.markSpam([messageId], spam, true);
    await comfort.loadInsights(messageId, true);
    app.toast(spam ? t('comfort.spam.markedSpam') : t('comfort.spam.markedHam'));
  }

  function print(whole: boolean) {
    const url = comfort.api.printUrl(whole && conversationId ? { conversationId, lang: i18n.locale } : { messageId, lang: i18n.locale });
    const w = window.open(url, '_blank', 'noopener');
    if (!w) app.toast(t('comfort.print.popupBlocked'), undefined, 'error');
  }

  function pdf() {
    window.open(comfort.api.pdfUrl(messageId, true), '_blank', 'noopener');
  }
</script>

<button class="icon-btn" title={isSpam ? t('comfort.spam.notSpam') : t('comfort.spam.spam')} aria-label={isSpam ? t('comfort.spam.notSpam') : t('comfort.spam.spam')} aria-pressed={isSpam} onclick={toggleSpam}>
  {#if isSpam}<ShieldCheck size={17} />{:else}<ShieldAlert size={17} />{/if}
</button>
<button class="icon-btn" title={t('comfort.print.print')} aria-label={t('comfort.print.print')} onclick={(e) => print(e.shiftKey)}><Printer size={17} /></button>
<button class="icon-btn" title={t('comfort.print.pdf')} aria-label={t('comfort.print.pdf')} onclick={pdf}><FileDown size={17} /></button>
