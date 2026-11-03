<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import SendHorizontal from '@lucide/svelte/icons/send-horizontal';
  import Paperclip from '@lucide/svelte/icons/paperclip';
  import Maximize2 from '@lucide/svelte/icons/maximize-2';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import { app } from '../../state';
  import type { ChannelOption } from '../../state';
  import { textToHtml } from '../../state/app.svelte';
  import { t } from '../../i18n/index.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';

  let text = $state('');
  let sending = $state(false);
  let selectedKey = $state<string | null>(null);
  let textarea = $state<HTMLTextAreaElement>();

  const options = $derived(app.selectedConversationId != null ? app.channelOptions() : []);
  // Reset the channel choice when the conversation changes (default: last used channel).
  $effect(() => {
    void app.selectedConversationId;
    selectedKey = null;
    text = '';
  });
  const option = $derived<ChannelOption | undefined>(options.find((o) => o.key === selectedKey) ?? options[0]);

  function autosize() {
    if (!textarea) return;
    textarea.style.height = 'auto';
    textarea.style.height = Math.min(textarea.scrollHeight, 180) + 'px';
  }

  async function send() {
    if (!option || !text.trim() || sending) return;
    sending = true;
    const body = text;
    try {
      text = '';
      queueMicrotask(autosize);
      await app.sendChat(body, option);
    } catch (err) {
      text = body;
      app.toast(t('compose.sendFailed', { error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
    } finally {
      sending = false;
      textarea?.focus();
    }
  }

  async function expand() {
    const lastMail = [...app.items].reverse().find((i) => i.messageId != null);
    const html = text ? `<p>${textToHtml(text)}</p>` : '';
    if (lastMail?.messageId != null) {
      const original = await app.api.getMessage(lastMail.messageId);
      const init = app.replyInit(original, app.conversation?.isGroup ? 'replyAll' : 'reply', html);
      if (option?.channel === 'email' && option.identityId) init.identityId = option.identityId;
      app.openCompose(init);
    } else {
      const p = app.person(app.conversation?.participantIds[0] ?? -1);
      const email = p?.handles.find((h) => h.kind === 'email')?.value;
      app.openCompose({ mode: 'new', to: email ? [{ name: p?.displayName, email }] : [], bodyHtml: html, identityId: option?.identityId });
    }
    text = '';
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      void send();
    }
  }
</script>

<div class="composer" role="group" aria-label={t('chat.composerLabel')}>
  <div class="channel">
    {#if option}<ChannelIcon channel={option.channel} size={15} />{/if}
    <select class="sel" value={option?.key} onchange={(e) => (selectedKey = (e.currentTarget as HTMLSelectElement).value)} aria-label={t('chat.channel')} title={t('chat.channel')}>
      {#each options as o (o.key)}
        <option value={o.key}>{t(`channel.${o.channel}`)} · {o.label}</option>
      {/each}
    </select>
    <ChevronDown size={14} class="chev-down" aria-hidden="true" />
  </div>
  <div class="field">
    <textarea
      bind:this={textarea}
      bind:value={text}
      rows="1"
      placeholder={option?.channel === 'email' ? t('chat.placeholderMail') : t('chat.placeholder')}
      aria-label={t('chat.placeholder')}
      oninput={autosize}
      {onkeydown}
      data-chat-input
    ></textarea>
    <div class="actions">
      <button class="icon-btn" title={t('compose.attach')} aria-label={t('compose.attach')} onclick={expand}><Paperclip size={17} /></button>
      <button class="icon-btn" title={t('chat.expand')} aria-label={t('chat.expand')} onclick={expand}><Maximize2 size={16} /></button>
      <button class="send" disabled={!text.trim() || sending || !option} onclick={send} aria-label={t('action.send')} title={t('action.send') + ' (Enter)'}>
        <SendHorizontal size={17} />
      </button>
    </div>
  </div>
  {#if option?.channel === 'email'}
    <p class="hint">{t('chat.mailHint')}</p>
  {/if}
</div>

<style>
  .composer {
    padding: 10px 14px 12px;
    border-top: 1px solid var(--tm-line);
    background: var(--tm-surface);
  }
  .channel {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 6px;
    padding: 0 8px;
    height: 26px;
    border-radius: 99px;
    background: var(--tm-surface-2);
    border: 1px solid var(--tm-line);
    max-width: 100%;
  }
  .sel {
    appearance: none;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 12.5px;
    padding-right: 16px;
    max-width: 320px;
    text-overflow: ellipsis;
  }
  .sel:focus {
    outline: none;
  }
  .channel:focus-within {
    border-color: var(--tm-accent);
  }
  .channel :global(.chev-down) {
    position: absolute;
    right: 8px;
    pointer-events: none;
    color: var(--tm-muted);
  }
  .field {
    display: flex;
    align-items: flex-end;
    gap: 6px;
    padding: 6px 6px 6px 14px;
    border-radius: 18px;
    border: 1px solid var(--tm-line-strong);
    background: var(--tm-surface);
    transition: border-color var(--tm-dur);
  }
  .field:focus-within {
    border-color: var(--tm-accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-accent) 18%, transparent);
  }
  textarea {
    flex: 1;
    resize: none;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 14px;
    line-height: 1.4;
    padding: 6px 0;
    max-height: 180px;
    outline: none;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 2px;
  }
  .send {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    border-radius: 99px;
    border: none;
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
    transition:
      opacity var(--tm-dur),
      transform var(--tm-dur);
  }
  .send:disabled {
    opacity: 0.4;
    cursor: default;
  }
  .send:not(:disabled):active {
    transform: scale(0.94);
  }
  .hint {
    margin: 6px 4px 0;
    font-size: 11.5px;
    color: var(--tm-faint);
  }
</style>
