<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Rows3 from '@lucide/svelte/icons/rows-3';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import { app } from '../../state';
  import { buildTimeline } from '../../conversation';
  import type { ConversationItem } from '../../types';
  import { fmt, formatDay, t } from '../../i18n/index.svelte';
  import Avatar from '../common/Avatar.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import Bubble from './Bubble.svelte';
  import ChatComposer from './ChatComposer.svelte';

  let scroller = $state<HTMLDivElement>();
  const timeline = $derived(buildTimeline(app.items, (iso) => fmt().dayKey(iso)));
  const conv = $derived(app.conversation);
  const multiParty = $derived(!!conv && (conv.isGroup || conv.participantIds.length > 1));

  const participantsLine = $derived.by(() => {
    if (!conv) return '';
    const names = conv.participantIds.map((id) => app.person(id)?.displayName).filter(Boolean);
    if (conv.kind === 'person_view') {
      const p = app.person(conv.participantIds[0]);
      return p?.handles.map((h) => h.value).join(' · ') ?? '';
    }
    return t('chat.with', { names: names.join(', ') });
  });

  // Scroll: focused bubble to the centre, otherwise stick to the newest item.
  $effect(() => {
    void app.timelineScrollTick;
    const focus = app.timelineScrollTarget;
    const el = scroller;
    if (!el) return;
    requestAnimationFrame(() => {
      const target = focus ? el.querySelector<HTMLElement>(`#item-${CSS.escape(focus)}`) : null;
      if (target) {
        target.scrollIntoView({ block: 'center' });
        // Do not steal focus from the composer while typing.
        if (!(document.activeElement as HTMLElement | null)?.closest('.composer')) target.focus({ preventScroll: true });
      } else {
        el.scrollTop = el.scrollHeight;
      }
    });
  });

  /** Show a subject line when it changes between consecutive mails. */
  function subjectChanged(i: number): boolean {
    const entry = timeline[i];
    if (entry.type !== 'item' || entry.item.channel !== 'email') return false;
    for (let j = i - 1; j >= 0; j--) {
      const e = timeline[j];
      if (e.type === 'item' && e.item.channel === 'email') {
        return norm(e.item.subject) !== norm(entry.item.subject);
      }
    }
    return true;
  }
  function norm(s?: string): string {
    return (s ?? '').replace(/^\s*((re|aw|fwd?|wg)\s*:\s*)+/i, '').trim();
  }

  async function openOriginal(item: ConversationItem) {
    app.focusItem(item.id);
    await app.setViewMode('classic');
  }
</script>

<section class="timeline" aria-label={t('chat.timelineLabel')}>
  {#if conv}
    <header class="head">
      <button class="icon-btn back" aria-label={t('common.action.back')} onclick={() => app.selectConversation(null)}><ArrowLeft size={18} /></button>
      <Avatar name={conv.title} seed={conv.kind === 'chat' && conv.isGroup ? conv.title : (app.person(conv.participantIds[0])?.handles[0]?.value ?? conv.title)} size={38} group={conv.isGroup && conv.kind === 'chat'} />
      <div class="who">
        <div class="title">
          <h2>{conv.title}</h2>
          {#each conv.channels as ch (ch)}<ChannelIcon channel={ch} size={14} />{/each}
        </div>
        <div class="sub">{participantsLine}</div>
      </div>
      <button class="btn btn-ghost" onclick={() => app.setViewMode('classic')} title={t('mail.view.classic') + ' (Ctrl+Shift+V)'}>
        <Rows3 size={16} /> <span class="hide-sm">{t('mail.view.classic')}</span>
      </button>
    </header>

    <div class="scroll" bind:this={scroller} role="log" aria-live="polite" aria-relevant="additions" tabindex="-1">
      <div class="inner">
        {#each timeline as entry, i (entry.key)}
          {#if entry.type === 'day'}
            <div class="day" role="separator"><span>{formatDay(entry.date)}</span></div>
          {:else}
            <Bubble
              item={entry.item}
              groupStart={entry.groupStart}
              groupEnd={entry.groupEnd}
              showSender={multiParty}
              showSubject={subjectChanged(i)}
              focused={entry.item.id === app.focusedItemId}
              onfocusitem={(id) => app.focusItem(id)}
              onopenoriginal={openOriginal}
            />
          {/if}
        {/each}
      </div>
    </div>

    <ChatComposer />
  {:else}
    <div class="empty">
      <MessagesSquare size={48} strokeWidth={1.2} aria-hidden="true" />
      <p class="e1">{t('chat.noSelection')}</p>
      <p class="e2">{t('chat.noSelectionHint')}</p>
    </div>
  {/if}
</section>

<style>
  .timeline {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background: var(--tm-chat-bg);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    min-height: 52px;
    box-sizing: border-box;
    border-bottom: 1px solid var(--tm-line);
    background: var(--tm-surface);
  }
  .back {
    display: none;
  }
  .who {
    flex: 1;
    min-width: 0;
  }
  .title {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .scroll {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    outline: none;
  }
  .inner {
    max-width: 860px;
    margin: 0 auto;
    padding: 12px 18px 20px;
  }
  .day {
    display: flex;
    justify-content: center;
    margin: 16px 0 6px;
    position: sticky;
    top: 6px;
    z-index: 1;
  }
  .day span {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--tm-muted);
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    padding: 2px 10px;
    border-radius: 99px;
    box-shadow: var(--tm-shadow);
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
  @media (max-width: 1279px) {
    .hide-sm {
      display: none;
    }
  }
  @media (max-width: 899px) {
    .back {
      display: inline-flex;
    }
    .inner {
      padding: 10px 10px 16px;
    }
  }
</style>
