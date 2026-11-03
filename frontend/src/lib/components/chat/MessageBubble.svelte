<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  One message bubble. Mail and messenger messages look the same: no
  subject/From/To; quoted history and signatures are hidden (reachable via
  "Show original e-mail"). Newsletters/HTML mails render as a compact card.
-->
<script lang="ts">
  import Check from '@lucide/svelte/icons/check';
  import CheckCheck from '@lucide/svelte/icons/check-check';
  import Clock from '@lucide/svelte/icons/clock';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import Forward from '@lucide/svelte/icons/forward';
  import Ellipsis from '@lucide/svelte/icons/ellipsis';
  import Newspaper from '@lucide/svelte/icons/newspaper';
  import type { ConversationItem } from '../../types';
  import { fmt, formatTime, t } from '../../i18n/index.svelte';
  import { highlight, isEmojiOnly, linkify } from '../../richtext';
  import { hueFor } from '../../conversation';
  import { longpress } from '../../actions';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import AttachmentCard from '../common/AttachmentCard.svelte';
  import Avatar from '../common/Avatar.svelte';
  import VoicePlayer from './VoicePlayer.svelte';
  import Star from '@lucide/svelte/icons/star';
  import Pin from '@lucide/svelte/icons/pin';
  import CalendarClock from '@lucide/svelte/icons/calendar-clock';
  import Languages from '@lucide/svelte/icons/languages';
  import { prefs } from '../../state';

  interface Props {
    item: ConversationItem;
    groupStart: boolean;
    groupEnd: boolean;
    channelChanged: boolean;
    showReplyQuote: boolean;
    isGroupChat: boolean;
    focused: boolean;
    query: string;
    activeHit: boolean;
    onmenu: (item: ConversationItem, x: number, y: number) => void;
    onreact: (item: ConversationItem, emoji: string) => void;
    onjump: (itemId: string) => void;
    onoriginal: (item: ConversationItem) => void;
    onimage: (item: ConversationItem, index: number) => void;
    onfocusitem: (id: string) => void;
    translation?: { text: string; sourceLang: string; showOriginal: boolean } | null;
    ontoggletranslation?: (item: ConversationItem) => void;
    onslot?: (item: ConversationItem, start: string) => void;
  }
  let { item, groupStart, groupEnd, channelChanged, showReplyQuote, isGroupChat, focused, query, activeHit, onmenu, onreact, onjump, onoriginal, onimage, onfocusitem, translation = null, ontoggletranslation, onslot }: Props = $props();
  let sigOpen = $state(false);
  const sigShown = $derived(prefs.chatSignatures === 'shown' || sigOpen);

  const images = $derived(item.attachments.filter((a) => a.mimeType.startsWith('image/') && a.previewUrl));
  const voices = $derived(item.attachments.filter((a) => a.mimeType.startsWith('audio/')));
  const files = $derived(item.attachments.filter((a) => !images.includes(a) && !voices.includes(a)));
  const segments = $derived(highlight(linkify(item.body), query));
  const bigEmoji = $derived(!item.attachments.length && !item.replyTo && isEmojiOnly(item.body));
  const hasHidden = $derived(item.channel === 'email' && !!item.quoted);
  const senderHue = $derived(hueFor(item.senderPersonId));
  const label = $derived(`${item.isOwn ? t('chat.you') : item.senderName}, ${formatTime(item.sentAt)}, ${t(`common.channel.${item.channel}`)}`);
  const gallery = $derived(images.slice(0, 4));

  function openMenuFromButton(e: MouseEvent) {
    e.stopPropagation();
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    onmenu(item, item.isOwn ? r.right - 230 : r.left, r.bottom + 4);
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) {
      e.preventDefault();
      const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
      onmenu(item, r.left + 20, r.bottom);
    }
  }
</script>

<div class="row" class:own={item.isOwn} class:start={groupStart} class:end={groupEnd} class:big={bigEmoji}>
  {#if !item.isOwn && isGroupChat}
    <div class="av">
      {#if groupEnd}<Avatar name={item.senderName} seed={item.senderPersonId} size={30} />{/if}
    </div>
  {/if}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
  <article
    id="item-{item.id}"
    class="bubble"
    class:card={item.display === 'card'}
    class:focused
    class:hit={activeHit}
    class:emoji={bigEmoji}
    class:scheduled={!!item.scheduledFor}
    aria-label={label}
    tabindex={focused ? 0 : -1}
    onfocus={() => onfocusitem(item.id)}
    onclick={() => onfocusitem(item.id)}
    {onkeydown}
    use:longpress={(x, y) => onmenu(item, x, y)}
  >
    {#if isGroupChat && groupStart && !item.isOwn}
      <div class="sender" style:--h={senderHue}>{item.senderName}</div>
    {/if}
    {#if item.scheduledFor}
      <div class="sched-label"><Clock size={12} aria-hidden="true" /> {t('chat.schedule.for', { when: fmt().dateTime(item.scheduledFor) })}</div>
    {/if}
    {#if item.relation === 'forward'}
      <div class="fwd"><Forward size={12} aria-hidden="true" /> {t('chat.forwarded')}</div>
    {/if}
    {#if showReplyQuote && item.replyTo}
      {@const r = item.replyTo}
      <button class="quote" style:--h={hueFor(r.senderName)} onclick={(e) => (e.stopPropagation(), onjump(r.itemId))} title={t('chat.jumpToReply')}>
        <span class="qname">{r.senderName}</span>
        <span class="qtext">{r.excerpt}</span>
      </button>
    {/if}

    {#if item.display === 'card' && item.card}
      <div class="card-body">
        <span class="card-ic"><Newspaper size={18} aria-hidden="true" /></span>
        <div class="card-text">
          <strong>{item.card.title}</strong>
          <span>{item.card.excerpt}</span>
        </div>
      </div>
      <button class="card-open" onclick={(e) => (e.stopPropagation(), onoriginal(item))}>{t('chat.openCard')}</button>
    {:else}
      {#if gallery.length}
        <div class="gallery n{Math.min(gallery.length, 4)}">
          {#each gallery as img, i (img.id)}
            <button class="thumb" onclick={(e) => (e.stopPropagation(), onimage(item, i))} aria-label={t('chat.media.openImage', { name: img.filename })}>
              <img src={img.previewUrl} alt={img.filename} loading="lazy" />
              {#if i === 3 && images.length > 4}<span class="more-n">+{images.length - 4}</span>{/if}
            </button>
          {/each}
        </div>
      {/if}
      {#each voices as v (v.id)}
        <VoicePlayer attachment={v} own={item.isOwn} />
      {/each}
      {#if files.length}
        <div class="files">
          {#each files as f (f.id)}
            <AttachmentCard attachment={f} compact onown={item.isOwn} />
          {/each}
        </div>
      {/if}
      {#if item.body}
        <div class="text">{#each segments as seg, i (i)}{#if seg.href}<a href={seg.href} target="_blank" rel="noopener noreferrer" class:hit={seg.hit} onclick={(e) => e.stopPropagation()}>{seg.text}</a>{:else if seg.hit}<mark>{seg.text}</mark>{:else}{seg.text}{/if}{/each}{#if hasHidden}<button class="hidden-more" onclick={(e) => (e.stopPropagation(), onoriginal(item))} title={t('chat.menu.original')} aria-label={t('chat.menu.original')}><Ellipsis size={14} /></button>{/if}</div>
      {:else if item.relation === 'forward' && !item.attachments.length}
        <div class="text muted">{t('chat.forwardedNoText')}</div>
      {/if}
      {#if item.meeting}
        {@const mt = item.meeting}
        <div class="meeting">
          <div class="mt-head"><CalendarClock size={16} aria-hidden="true" /> <strong>{mt.title}</strong> <small>{t('chat.meeting.minutes', { count: mt.durationMin })}</small></div>
          <div class="slots" role="group" aria-label={t('chat.meeting.slots')}>
            {#each mt.slots as sl (sl.start)}
              <button
                class="slot"
                class:chosen={mt.chosen === sl.start}
                disabled={mt.status === 'booked'}
                onclick={(e) => (e.stopPropagation(), onslot?.(item, sl.start))}
                title={mt.status === 'open' ? t('chat.meeting.pickDemo') : ''}>{fmt().full(sl.start)}</button
              >
            {/each}
          </div>
          <small class="mt-status">{mt.status === 'booked' ? t('chat.meeting.bookedShort') : t('chat.meeting.waiting')}</small>
        </div>
      {/if}
      {#if item.signature}
        {#if sigShown}
          <pre class="signature">{item.signature}</pre>
        {/if}
        {#if prefs.chatSignatures === 'collapsed'}
          <button class="sig-toggle" aria-expanded={sigOpen} onclick={(e) => (e.stopPropagation(), (sigOpen = !sigOpen))}>
            {t('chat.signature.label')} <ChevronDown size={11} class={sigOpen ? 'rot' : ''} />
          </button>
        {/if}
      {/if}
      {#if translation}
        <div class="translation">
          <span class="tr-label"><Languages size={12} aria-hidden="true" /> {t('chat.ai.translated', { lang: translation.sourceLang.toUpperCase() })}</span>
          {#if !translation.showOriginal}<span class="tr-text">{translation.text}</span>{/if}
          <button class="tr-toggle" onclick={(e) => (e.stopPropagation(), ontoggletranslation?.(item))}>{translation.showOriginal ? t('chat.ai.showTranslation') : t('chat.ai.showOriginal')}</button>
        </div>
      {/if}
      {#if item.linkPreview}
        {@const lp = item.linkPreview}
        <a class="link-preview" href={lp.url} target="_blank" rel="noopener noreferrer" onclick={(e) => e.stopPropagation()}>
          {#if lp.siteName}<span class="lp-site">{lp.siteName}</span>{/if}
          <span class="lp-title">{lp.title}</span>
          {#if lp.description}<span class="lp-desc">{lp.description}</span>{/if}
        </a>
      {/if}
    {/if}

    <span class="meta">
      {#if item.pinned}<Pin size={11} aria-label={t('chat.pin.pinned')} />{/if}
      {#if item.starred}<Star size={11} class="starred-ic" aria-label={t('chat.starred.starred')} />{/if}
      {#if channelChanged}<ChannelIcon channel={item.channel} size={11} />{/if}
      <time datetime={item.sentAt}>{formatTime(item.sentAt)}</time>
      {#if item.isOwn}
        {@const st = item.status ?? 'sent'}
        <span class="tick tick-{st}" title={t(`chat.status.${st}`)}>
          {#if st === 'pending'}<Clock size={12} />{:else if st === 'failed'}<CircleAlert size={12} />{:else if st === 'sent'}<Check size={14} />{:else}<CheckCheck size={14} />{/if}
          <span class="sr-only">{t(`chat.status.${st}`)}</span>
        </span>
      {/if}
    </span>

    <button class="menu-btn" aria-label={t('chat.messageMenu')} tabindex="-1" onclick={openMenuFromButton}><ChevronDown size={16} /></button>

    {#if item.reactions?.length}
      <div class="reactions">
        {#each item.reactions as r (r.emoji)}
          <button class="reaction" class:mine={r.mine} onclick={(e) => (e.stopPropagation(), onreact(item, r.emoji))} aria-pressed={!!r.mine} aria-label={t('chat.reactionCount', { emoji: r.emoji, count: r.count })}>
            {r.emoji}{#if r.count > 1}<small>{r.count}</small>{/if}
          </button>
        {/each}
      </div>
    {/if}
  </article>
</div>

<style>
  .row {
    display: flex;
    align-items: flex-end;
    gap: 6px;
    padding: 1px 0;
  }
  .row.start {
    margin-top: 6px;
  }
  .row.own {
    justify-content: flex-end;
  }
  .row:has(.reactions) {
    margin-bottom: 14px;
  }
  .av {
    width: 30px;
    flex: none;
  }
  .bubble {
    position: relative;
    max-width: min(520px, 76%);
    min-width: 72px;
    padding: 6px 9px 7px 10px;
    border-radius: 10px;
    background: var(--tm-bubble-other);
    color: var(--tm-bubble-other-fg);
    box-shadow: 0 1px 1px rgb(16 24 40 / 0.08);
    outline: none;
    animation: pop 140ms ease-out;
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(3px);
    }
  }
  .row:not(.own).start .bubble {
    border-top-left-radius: 3px;
  }
  .row.own.start .bubble {
    border-top-right-radius: 3px;
  }
  .own .bubble {
    background: var(--tm-bubble-own);
    color: var(--tm-bubble-own-fg);
  }
  .bubble.focused {
    box-shadow:
      0 0 0 2px var(--tm-chat-bg),
      0 0 0 4px var(--tm-focus);
  }
  .bubble.hit {
    box-shadow:
      0 0 0 2px var(--tm-chat-bg),
      0 0 0 4px #f59e0b;
  }
  .bubble.emoji {
    background: transparent;
    box-shadow: none;
    padding: 0 4px;
  }
  .bubble.emoji .text {
    font-size: 40px;
    line-height: 1.15;
  }
  .bubble.emoji .meta {
    color: var(--tm-muted);
  }
  .sender {
    font-size: 12.5px;
    font-weight: 650;
    color: hsl(var(--h) 60% 42%);
    margin-bottom: 1px;
  }
  :global([data-theme='dark']) .sender {
    color: hsl(var(--h) 70% 70%);
  }
  .fwd {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 11.5px;
    font-style: italic;
    opacity: 0.7;
    margin-bottom: 2px;
  }
  .quote {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    width: 100%;
    margin: 2px 0 4px;
    padding: 4px 8px;
    border: none;
    border-left: 3px solid hsl(var(--h) 60% 50%);
    border-radius: 6px;
    background: color-mix(in srgb, currentColor 8%, transparent);
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .qname {
    font-size: 12px;
    font-weight: 650;
    color: hsl(var(--h) 60% 45%);
  }
  .own .qname {
    color: inherit;
  }
  .qtext {
    font-size: 12.5px;
    opacity: 0.8;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .text {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: 14.5px;
    line-height: 1.38;
  }
  /* reserve room for the time in the last line */
  .text::after {
    content: '';
    display: inline-block;
    width: 78px;
  }
  .own .text::after {
    width: 98px;
  }
  .text a {
    color: inherit;
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .text.muted {
    font-style: italic;
    opacity: 0.75;
  }
  mark,
  a.hit {
    background: #fde68a;
    color: #1c2b45;
    border-radius: 2px;
  }
  .hidden-more {
    display: inline-flex;
    vertical-align: middle;
    margin-left: 6px;
    padding: 0 5px;
    border: none;
    border-radius: 99px;
    background: color-mix(in srgb, currentColor 12%, transparent);
    color: inherit;
    opacity: 0.7;
  }
  .hidden-more:hover {
    opacity: 1;
  }
  .meta {
    float: right;
    position: relative;
    top: 6px;
    margin: -14px -2px 0 8px;
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: 11px;
    opacity: 0.7;
    white-space: nowrap;
  }
  .card .meta,
  .bubble:not(:has(.text)) .meta {
    float: none;
    position: static;
    margin: 4px 0 0;
    display: flex;
    justify-content: flex-end;
  }
  .tick {
    display: inline-flex;
  }
  .tick-read {
    color: #7cc4ff;
    opacity: 1;
  }
  .row:not(.own) .tick-read {
    color: #3b9be6;
  }
  .gallery {
    display: grid;
    gap: 3px;
    margin: -2px -5px 4px -6px;
    border-radius: 8px;
    overflow: hidden;
    width: 300px;
    max-width: 100%;
  }
  .gallery.n2,
  .gallery.n3,
  .gallery.n4 {
    grid-template-columns: 1fr 1fr;
  }
  .gallery.n3 .thumb:first-child {
    grid-column: span 2;
  }
  .thumb {
    position: relative;
    padding: 0;
    border: none;
    background: var(--tm-surface-3);
    cursor: zoom-in;
    aspect-ratio: 4 / 3;
  }
  .gallery.n1 .thumb {
    aspect-ratio: auto;
  }
  .thumb img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .more-n {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgb(0 0 0 / 0.45);
    color: #fff;
    font-size: 22px;
    font-weight: 700;
  }
  .files {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 2px 0 4px;
  }
  .link-preview {
    display: flex;
    flex-direction: column;
    gap: 1px;
    margin-top: 6px;
    padding: 6px 8px;
    border-radius: 6px;
    border-left: 3px solid var(--tm-brand);
    background: color-mix(in srgb, currentColor 7%, transparent);
    color: inherit;
    text-decoration: none;
  }
  .lp-site {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    opacity: 0.65;
  }
  .lp-title {
    font-size: 13.5px;
    font-weight: 650;
  }
  .lp-desc {
    font-size: 12.5px;
    opacity: 0.8;
  }
  .card {
    width: min(420px, 100%);
  }
  .card-body {
    display: flex;
    gap: 10px;
  }
  .card-ic {
    flex: none;
    width: 34px;
    height: 34px;
    border-radius: 8px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: color-mix(in srgb, var(--tm-brand) 18%, transparent);
    color: #b45309;
  }
  .card-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .card-text strong {
    font-size: 14px;
  }
  .card-text span {
    font-size: 12.5px;
    opacity: 0.75;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .card-open {
    margin-top: 6px;
    width: 100%;
    height: 30px;
    border: none;
    border-top: 1px solid color-mix(in srgb, currentColor 12%, transparent);
    background: transparent;
    color: var(--tm-accent-text);
    font: inherit;
    font-size: 13px;
    font-weight: 600;
  }
  .own .card-open {
    color: inherit;
  }
  .bubble.scheduled {
    opacity: 0.62;
    border: 1px dashed color-mix(in srgb, currentColor 45%, transparent);
  }
  .sched-label {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 11.5px;
    font-weight: 600;
    margin-bottom: 2px;
  }
  :global(.starred-ic) {
    color: #f59e0b;
    fill: #f59e0b;
  }
  .signature {
    margin: 6px 0 0;
    padding-top: 4px;
    border-top: 1px dashed color-mix(in srgb, currentColor 25%, transparent);
    font: inherit;
    font-size: 12px;
    white-space: pre-wrap;
    opacity: 0.75;
  }
  .sig-toggle {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    margin-top: 2px;
    padding: 0 4px;
    border: none;
    border-radius: 4px;
    background: transparent;
    color: inherit;
    font: inherit;
    font-size: 10.5px;
    opacity: 0.5;
  }
  .sig-toggle:hover {
    opacity: 0.9;
  }
  .sig-toggle :global(.rot) {
    transform: rotate(180deg);
  }
  .translation {
    margin-top: 6px;
    padding: 6px 8px;
    border-radius: 6px;
    background: color-mix(in srgb, currentColor 7%, transparent);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .tr-label {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 11px;
    opacity: 0.7;
  }
  .tr-text {
    font-size: 14px;
  }
  .tr-toggle {
    align-self: flex-start;
    border: none;
    background: transparent;
    color: inherit;
    font: inherit;
    font-size: 11.5px;
    text-decoration: underline;
    padding: 0;
    opacity: 0.8;
  }
  .meeting {
    margin-top: 4px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .mt-head {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .mt-head small {
    opacity: 0.7;
  }
  .slots {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .slot {
    padding: 4px 10px;
    border-radius: 99px;
    border: 1px solid color-mix(in srgb, currentColor 35%, transparent);
    background: color-mix(in srgb, currentColor 6%, transparent);
    color: inherit;
    font: inherit;
    font-size: 12px;
  }
  .slot:not(:disabled):hover {
    background: color-mix(in srgb, currentColor 16%, transparent);
  }
  .slot:disabled {
    opacity: 0.5;
  }
  .slot.chosen {
    opacity: 1;
    background: var(--tm-brand);
    border-color: var(--tm-brand);
    color: var(--tm-brand-navy);
    font-weight: 700;
  }
  .mt-status {
    font-size: 11px;
    opacity: 0.7;
  }
  .menu-btn {
    position: absolute;
    top: 3px;
    right: 3px;
    display: inline-flex;
    padding: 1px;
    border: none;
    border-radius: 99px;
    background: inherit;
    color: inherit;
    opacity: 0;
    transition: opacity var(--tm-dur);
    box-shadow: -6px 0 8px -2px var(--tm-bubble-other);
  }
  .own .menu-btn {
    box-shadow: -6px 0 8px -2px var(--tm-bubble-own);
  }
  .bubble:hover .menu-btn,
  .bubble:focus-within .menu-btn {
    opacity: 0.85;
  }
  @media (hover: none) {
    .menu-btn {
      display: none;
    }
  }
  .reactions {
    position: absolute;
    bottom: -16px;
    left: 8px;
    display: flex;
    gap: 3px;
  }
  .own .reactions {
    left: auto;
    right: 8px;
  }
  .reaction {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    height: 22px;
    padding: 0 6px;
    border-radius: 99px;
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    color: var(--tm-fg);
    font-size: 13px;
    box-shadow: var(--tm-shadow);
  }
  .reaction.mine {
    border-color: var(--tm-brand);
    background: color-mix(in srgb, var(--tm-brand) 14%, var(--tm-surface));
  }
  .reaction small {
    font-size: 11px;
    color: var(--tm-muted);
  }
</style>
