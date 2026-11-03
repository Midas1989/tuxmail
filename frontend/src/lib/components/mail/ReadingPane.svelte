<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Reply from '@lucide/svelte/icons/reply';
  import ReplyAll from '@lucide/svelte/icons/reply-all';
  import Forward from '@lucide/svelte/icons/forward';
  import Archive from '@lucide/svelte/icons/archive';
  import Trash from '@lucide/svelte/icons/trash';
  import Star from '@lucide/svelte/icons/star';
  import Mail from '@lucide/svelte/icons/mail';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import ImageOff from '@lucide/svelte/icons/image-off';
  import MailOpen from '@lucide/svelte/icons/mail-open';
  import Tags from '@lucide/svelte/icons/tags';
  import FolderKanban from '@lucide/svelte/icons/folder-kanban';
  import TagChips from '../common/TagChips.svelte';
  import { app, prefs } from '../../state';
  import { FLAG_FLAGGED, type Address } from '../../types';
  import { formatFullDate, t } from '../../i18n/index.svelte';
  import Avatar from '../common/Avatar.svelte';
  import AttachmentCard from '../common/AttachmentCard.svelte';
  import MailFrame from './MailFrame.svelte';
  import MoveMenu from './MoveMenu.svelte';

  let allowRemote = $state(false);
  let showAllRecipients = $state(false);

  $effect(() => {
    void app.message?.id;
    allowRemote = false;
    showAllRecipients = false;
  });

  const m = $derived(app.message);
  const flagged = $derived(!!m?.flags.includes(FLAG_FLAGGED));
  const acc = $derived(m ? app.account(m.accountId) : undefined);

  function fmt(list: Address[]): string {
    return list.map((a) => (a.name ? `${a.name} <${a.email}>` : a.email)).join(', ');
  }
  const rcptCount = $derived(m ? m.to.length + m.cc.length : 0);
</script>

<section class="pane" aria-label={t('mail.reader.label')}>
  {#if m}
    <div class="toolbar" role="toolbar" aria-label={t('mail.reader.actions')}>
      <button class="icon-btn back" aria-label={t('common.action.back')} onclick={() => app.selectMessage(null)}><ArrowLeft size={18} /></button>
      <button class="btn btn-ghost" onclick={() => app.replyTo('reply')} title="R"><Reply size={16} /> <span class="hide-sm">{t('common.action.reply')}</span></button>
      <button class="btn btn-ghost" onclick={() => app.replyTo('replyAll')} title="Shift+R"><ReplyAll size={16} /> <span class="hide-sm">{t('common.action.replyAll')}</span></button>
      <button class="btn btn-ghost" onclick={() => app.replyTo('forward')} title="F"><Forward size={16} /> <span class="hide-sm">{t('common.action.forward')}</span></button>
      <span class="sep"></span>
      <button class="icon-btn" title={t('common.action.archive')} aria-label={t('common.action.archive')} onclick={() => app.moveToRole(m.id, 'archive')}><Archive size={17} /></button>
      <button class="icon-btn" title={t('common.action.delete')} aria-label={t('common.action.delete')} onclick={() => app.moveToRole(m.id, 'trash')}><Trash size={17} /></button>
      <MoveMenu messageId={m.id} accountId={m.accountId} currentFolderIds={m.folderIds} />
      <button class="icon-btn" title={t('common.action.flag')} aria-label={t('common.action.flag')} aria-pressed={flagged} onclick={() => app.toggleFlag(m.id)}><Star size={17} class={flagged ? 'starred' : ''} /></button>
      <button class="icon-btn" title={t('common.action.markUnread')} aria-label={t('common.action.markUnread')} onclick={() => app.toggleRead(m.id)}><Mail size={17} /></button>
      <button class="icon-btn" title={t('organize.tags.menu') + ' (T)'} aria-label={t('organize.tags.menu')} onclick={() => app.openTagPicker([{ kind: 'message', id: m.id }], m.tagIds ?? [])}><Tags size={17} /></button>
      <button class="icon-btn" title={t('organize.project.menu')} aria-label={t('organize.project.menu')} onclick={() => app.openProjectPicker([{ kind: 'message', id: m.id }])}><FolderKanban size={17} /></button>
      <span class="grow"></span>
      <button class="btn btn-ghost conv" onclick={() => app.setViewMode('chat')} title={t('mail.view.showAsConversation') + ' (Ctrl+Shift+V)'}>
        <MessagesSquare size={16} /> <span class="hide-sm">{t('mail.view.showAsConversation')}</span>
      </button>
    </div>

    <div class="scroll">
      <header class="head">
        <h2 class="subject">{m.subject || t('mail.list.noSubject')}</h2>
        {#if m.tagIds?.length || m.projectIds?.length}
          <div class="org">
            <TagChips ids={m.tagIds} max={6} onclick={(id) => app.select({ kind: 'tag', tagId: id })} />
            {#each m.projectIds ?? [] as pid (pid)}
              {@const p = app.project(pid)}
              {#if p}<button class="proj" style:--c={p.color} onclick={() => app.openProject(pid)}><FolderKanban size={11} aria-hidden="true" /> {p.name}</button>{/if}
            {/each}
          </div>
        {/if}
        <div class="sender">
          <Avatar name={m.from[0]?.name || m.from[0]?.email || '?'} seed={m.from[0]?.email} size={40} />
          <div class="who">
            <div class="l1">
              <span class="name">{m.from[0]?.name || m.from[0]?.email}</span>
              <span class="email">&lt;{m.from[0]?.email}&gt;</span>
            </div>
            <div class="l2">
              {#if showAllRecipients || rcptCount <= 2}
                <span>{t('mail.reader.to')}: {fmt(m.to)}</span>
                {#if m.cc.length}<span> · {t('mail.reader.cc')}: {fmt(m.cc)}</span>{/if}
              {:else}
                <button class="linkish" onclick={() => (showAllRecipients = true)}>{t('mail.reader.toMany', { name: m.to[0]?.name || m.to[0]?.email || '', count: rcptCount - 1 })}</button>
              {/if}
            </div>
          </div>
          <div class="meta">
            <time datetime={m.date}>{formatFullDate(m.date)}</time>
            {#if acc}<span class="acc"><span class="dot" style:background={acc.color}></span>{acc.displayName}</span>{/if}
          </div>
        </div>
      </header>

      {#if m.hasRemoteContent && !allowRemote}
        <div class="notice" role="note">
          <ImageOff size={16} aria-hidden="true" />
          <span>{t('mail.reader.remoteBlocked')}</span>
          <button class="btn" onclick={() => (allowRemote = true)}>{t('mail.reader.loadRemote')}</button>
        </div>
      {/if}

      {#if m.attachments.length}
        <div class="atts" aria-label={t('mail.reader.attachments', { count: m.attachments.length })}>
          {#each m.attachments as a (a.id)}
            <AttachmentCard attachment={a} />
          {/each}
        </div>
      {/if}

      <div class="body" class:html={!!m.html}>
        <MailFrame html={m.html} text={m.text} {allowRemote} dark={prefs.effectiveTheme === 'dark'} title={t('mail.reader.bodyTitle', { subject: m.subject })} />
      </div>
    </div>
  {:else}
    <div class="empty">
      <MailOpen size={44} strokeWidth={1.2} aria-hidden="true" />
      <p class="e1">{t('mail.reader.empty')}</p>
      <p class="e2"><span class="kbd">J</span> / <span class="kbd">K</span> {t('mail.reader.emptyHint')}</p>
    </div>
  {/if}
</section>

<style>
  .pane {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background: var(--tm-surface);
  }
  .toolbar {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--tm-line);
    min-height: 52px;
    box-sizing: border-box;
  }
  .toolbar .btn {
    height: 32px;
    padding: 0 10px;
  }
  .sep {
    width: 1px;
    height: 20px;
    background: var(--tm-line);
    margin: 0 6px;
  }
  .grow {
    flex: 1;
  }
  .conv {
    color: var(--tm-accent-text);
  }
  .back {
    display: none;
  }
  :global(.starred) {
    color: #f59e0b;
    fill: #f59e0b;
  }
  .scroll {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .head {
    padding: 18px 24px 12px;
  }
  .subject {
    margin: 0 0 14px;
    font-size: 20px;
    font-weight: 650;
    letter-spacing: -0.015em;
    line-height: 1.3;
  }
  .sender {
    display: flex;
    gap: 12px;
    align-items: flex-start;
  }
  .who {
    flex: 1;
    min-width: 0;
  }
  .l1 {
    display: flex;
    gap: 6px;
    align-items: baseline;
    flex-wrap: wrap;
  }
  .name {
    font-weight: 600;
  }
  .email {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .l2 {
    font-size: 12.5px;
    color: var(--tm-muted);
    margin-top: 2px;
  }
  .linkish {
    border: none;
    background: none;
    padding: 0;
    color: var(--tm-muted);
    font: inherit;
    text-decoration: underline dotted;
  }
  .meta {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 4px;
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
  }
  .acc {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 9px;
  }
  .notice {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 24px 10px;
    padding: 8px 8px 8px 12px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--tm-warn) 12%, var(--tm-surface));
    color: var(--tm-fg);
    font-size: 13px;
  }
  .notice span {
    flex: 1;
  }
  .notice .btn {
    height: 28px;
  }
  .atts {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    padding: 0 24px 12px;
  }
  .body {
    flex: 1;
    min-height: 240px;
    margin: 0 12px 12px;
    border-top: 1px solid var(--tm-line);
  }
  .body.html {
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    overflow: hidden;
    background: #fff;
  }
  .empty {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: var(--tm-faint);
    text-align: center;
    padding: 24px;
  }
  .e1 {
    margin: 12px 0 4px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .e2 {
    margin: 0;
    font-size: 13px;
  }
  @media (max-width: 1439px) {
    .hide-sm {
      display: none;
    }
  }
  @media (max-width: 899px) {
    .back {
      display: inline-flex;
    }
    .head {
      padding: 14px 16px 10px;
    }
  }
  .org {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    margin: -4px 0 10px;
  }
  .proj {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 1px 8px;
    border: 1px solid color-mix(in srgb, var(--c) 40%, transparent);
    border-radius: 6px;
    background: transparent;
    color: color-mix(in srgb, var(--c) 70%, var(--tm-fg));
    font: inherit;
    font-size: 11px;
    font-weight: 600;
    line-height: 16px;
    cursor: pointer;
  }
</style>
