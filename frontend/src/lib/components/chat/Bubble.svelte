<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Check from '@lucide/svelte/icons/check';
  import CheckCheck from '@lucide/svelte/icons/check-check';
  import Clock from '@lucide/svelte/icons/clock';
  import Forward from '@lucide/svelte/icons/forward';
  import TextQuote from '@lucide/svelte/icons/text-quote';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import type { ConversationItem } from '../../types';
  import { formatTime, t } from '../../i18n/index.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import AttachmentCard from '../common/AttachmentCard.svelte';
  import Avatar from '../common/Avatar.svelte';

  interface Props {
    item: ConversationItem;
    groupStart: boolean;
    groupEnd: boolean;
    showSender: boolean;
    showSubject: boolean;
    focused: boolean;
    onfocusitem: (id: string) => void;
    onopenoriginal: (item: ConversationItem) => void;
  }
  let { item, groupStart, groupEnd, showSender, showSubject, focused, onfocusitem, onopenoriginal }: Props = $props();

  let showQuote = $state(false);
  const label = $derived(`${item.isOwn ? t('chat.you') : item.senderName}, ${formatTime(item.sentAt)}, ${t(`common.channel.${item.channel}`)}`);
</script>

<div class="row" class:own={item.isOwn} class:start={groupStart} class:end={groupEnd}>
  {#if !item.isOwn}
    <div class="av">
      {#if groupEnd}<Avatar name={item.senderName} seed={item.senderName} size={30} />{/if}
    </div>
  {/if}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
  <article
    id="item-{item.id}"
    class="bubble ch-{item.channel}"
    class:focused
    aria-label={label}
    tabindex={focused ? 0 : -1}
    onclick={() => onfocusitem(item.id)}
    onkeydown={(e) => e.key === 'Enter' && onfocusitem(item.id)}
    onfocus={() => onfocusitem(item.id)}
  >
    {#if showSender && groupStart && !item.isOwn}
      <div class="sender">{item.senderName}</div>
    {/if}
    {#if item.channel === 'email' && showSubject && item.subject}
      <div class="subject">
        {#if item.relation === 'forward'}<Forward size={12} aria-label={t('chat.forwarded')} />{/if}
        {item.subject}
      </div>
    {/if}
    {#if item.body}
      <div class="text">{item.body}</div>
    {:else if item.relation === 'forward'}
      <div class="text muted">{t('chat.forwardedNoText')}</div>
    {/if}
    {#if item.attachments.length}
      <div class="atts">
        {#each item.attachments as a (a.id)}
          <AttachmentCard attachment={a} compact onown={item.isOwn} />
        {/each}
      </div>
    {/if}
    {#if item.quoted}
      <button class="quote-toggle" aria-expanded={showQuote} onclick={(e) => (e.stopPropagation(), (showQuote = !showQuote))}>
        <TextQuote size={13} aria-hidden="true" />
        {showQuote ? t('chat.hideQuoted') : t('chat.showQuoted')}
      </button>
      {#if showQuote}
        <pre class="quoted">{item.quoted}</pre>
      {/if}
    {/if}
    {#if item.reactions?.length}
      <div class="reactions">
        {#each item.reactions as r (r.emoji)}<span class="reaction">{r.emoji}{#if r.count > 1}<small>{r.count}</small>{/if}</span>{/each}
      </div>
    {/if}
    <div class="meta">
      <ChannelIcon channel={item.channel} size={12} />
      {#if item.channel === 'email' && item.messageId != null}
        <button class="orig" onclick={(e) => (e.stopPropagation(), onopenoriginal(item))} title={t('chat.openOriginal')}>
          <ExternalLink size={11} aria-hidden="true" /><span class="sr-only">{t('chat.openOriginal')}</span>
        </button>
      {/if}
      <time datetime={item.sentAt}>{formatTime(item.sentAt)}</time>
      {#if item.isOwn && item.status}
        <span class="status status-{item.status}" title={t(`chat.status.${item.status}`)}>
          {#if item.status === 'pending'}<Clock size={12} />{:else if item.status === 'sent'}<Check size={13} />{:else}<CheckCheck size={13} />{/if}
          <span class="sr-only">{t(`chat.status.${item.status}`)}</span>
        </span>
      {/if}
    </div>
  </article>
</div>

<style>
  .row {
    display: flex;
    align-items: flex-end;
    gap: 8px;
    padding: 1px 0;
  }
  .row.start {
    margin-top: 8px;
  }
  .row.own {
    justify-content: flex-end;
  }
  .av {
    width: 30px;
    flex: none;
  }
  .bubble {
    position: relative;
    max-width: min(560px, 78%);
    padding: 7px 11px 5px;
    border-radius: 16px;
    background: var(--tm-bubble-other);
    color: var(--tm-bubble-other-fg);
    box-shadow: var(--tm-shadow);
    transition:
      box-shadow var(--tm-dur),
      transform var(--tm-dur);
    outline: none;
    animation: pop 160ms ease-out;
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
  }
  .row:not(.own).start .bubble {
    border-top-left-radius: 16px;
  }
  .row:not(.own):not(.end) .bubble {
    border-bottom-left-radius: 6px;
  }
  .row:not(.own):not(.start) .bubble {
    border-top-left-radius: 6px;
  }
  .own .bubble {
    background: var(--tm-bubble-own);
    color: var(--tm-bubble-own-fg);
  }
  .row.own:not(.end) .bubble {
    border-bottom-right-radius: 6px;
  }
  .row.own:not(.start) .bubble {
    border-top-right-radius: 6px;
  }
  .bubble.focused {
    box-shadow:
      0 0 0 2px var(--tm-surface),
      0 0 0 4px var(--tm-focus);
  }
  .sender {
    font-size: 12px;
    font-weight: 600;
    color: var(--tm-accent-text);
    margin-bottom: 1px;
  }
  .subject {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 11.5px;
    font-weight: 600;
    opacity: 0.75;
    margin-bottom: 2px;
  }
  .text {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: 14px;
    line-height: 1.42;
  }
  .text.muted {
    font-style: italic;
    opacity: 0.75;
  }
  .atts {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 6px 0 2px;
  }
  .quote-toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-top: 4px;
    padding: 2px 8px;
    border-radius: 99px;
    border: 1px solid color-mix(in srgb, currentColor 25%, transparent);
    background: transparent;
    color: inherit;
    font-size: 11.5px;
    opacity: 0.8;
  }
  .quote-toggle:hover {
    opacity: 1;
  }
  .quoted {
    margin: 6px 0 2px;
    padding: 6px 0 2px 10px;
    border-left: 3px solid color-mix(in srgb, currentColor 30%, transparent);
    white-space: pre-wrap;
    font: inherit;
    font-size: 12.5px;
    opacity: 0.78;
    max-height: 320px;
    overflow: auto;
  }
  .reactions {
    position: absolute;
    bottom: -12px;
    left: 10px;
    display: flex;
    gap: 2px;
  }
  .own .reactions {
    left: auto;
    right: 10px;
  }
  .reaction {
    font-size: 12px;
    padding: 0 5px;
    border-radius: 99px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    color: var(--tm-fg);
  }
  .reaction small {
    margin-left: 2px;
    font-size: 10px;
  }
  .meta {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 5px;
    margin-top: 2px;
    font-size: 11px;
    opacity: 0.7;
  }
  .own .meta :global(.ch) {
    color: inherit;
  }
  .orig {
    display: inline-flex;
    border: none;
    background: transparent;
    color: inherit;
    padding: 1px;
    border-radius: 4px;
  }
  .orig:hover {
    background: color-mix(in srgb, currentColor 15%, transparent);
  }
  .status {
    display: inline-flex;
  }
  .status-read {
    color: #a5f3fc;
  }
  .row:has(.reactions) {
    margin-bottom: 12px;
  }
</style>
