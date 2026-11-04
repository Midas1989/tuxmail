<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- "Original-E-Mail anzeigen": the complete mail (headers, quotes, signature) in an overlay. -->
<script lang="ts">
  import Rows3 from '@lucide/svelte/icons/rows-3';
  import { app, prefs } from '../../state';
  import { formatFullDate, t } from '../../i18n/index.svelte';
  import type { Address, MessageDetail } from '../../types';
  import Modal from '../common/Modal.svelte';
  import AttachmentCard from '../common/AttachmentCard.svelte';
  import MailFrame from '../mail/MailFrame.svelte';
  import { proxiedRemoteHtml } from '../../remote';

  interface Props {
    message: MessageDetail;
  }
  let { message: m }: Props = $props();
  let allowRemote = $state(false);
  let remoteHtml = $state<string | null>(null);
  async function loadRemote() {
    remoteHtml = await proxiedRemoteHtml(app.api, m.id);
    allowRemote = true;
  }

  const fmtList = (l: Address[]) => l.map((a) => (a.name ? `${a.name} <${a.email}>` : a.email)).join(', ');
  const close = () => (app.originalMessage = null);
</script>

<Modal title={m.subject || t('mail.list.noSubject')} onclose={close} width="860px">
  <dl class="hdr">
    <dt>{t('compose.from')}</dt>
    <dd>{fmtList(m.from)}</dd>
    <dt>{t('mail.reader.to')}</dt>
    <dd>{fmtList(m.to)}</dd>
    {#if m.cc.length}<dt>{t('mail.reader.cc')}</dt><dd>{fmtList(m.cc)}</dd>{/if}
    <dt>{t('chat.original.date')}</dt>
    <dd>{formatFullDate(m.date)}</dd>
  </dl>
  {#if m.hasRemoteContent && !allowRemote}
    <div class="notice">
      <span>{t('mail.reader.remoteBlocked')}</span>
      <button class="btn" onclick={loadRemote}>{t('mail.reader.loadRemote')}</button>
    </div>
  {/if}
  {#if m.attachments.length}
    <div class="atts">
      {#each m.attachments as a (a.id)}<AttachmentCard attachment={a} compact />{/each}
    </div>
  {/if}
  <div class="frame" class:html={!!m.html}>
    <MailFrame html={remoteHtml ?? m.html} text={m.text} {allowRemote} dark={prefs.effectiveTheme === 'dark'} title={t('mail.reader.bodyTitle', { subject: m.subject })} />
  </div>
  {#snippet footer()}
    <button
      class="btn"
      onclick={async () => {
        const id = m.id;
        close();
        await app.revealMessage(id);
      }}><Rows3 size={15} /> {t('chat.openInClassic')}</button
    >
    <button class="btn btn-primary" onclick={close}>{t('common.action.close')}</button>
  {/snippet}
</Modal>

<style>
  .hdr {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 3px 14px;
    margin: 0 0 12px;
    font-size: 13px;
  }
  dt {
    color: var(--tm-muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .notice {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 10px;
    padding: 6px 6px 6px 12px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--tm-warn) 12%, var(--tm-surface));
    font-size: 13px;
  }
  .notice span {
    flex: 1;
  }
  .atts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 10px;
  }
  .frame {
    height: 52vh;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    overflow: hidden;
  }
  .frame.html {
    background: #fff;
  }
</style>
