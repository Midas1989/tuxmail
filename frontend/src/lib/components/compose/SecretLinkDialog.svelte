<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  One-time secret link (lib/secretlink.ts, server internal/server/secretlinks.go): the text is
  encrypted in this browser, the key only lives in the link. The dialog says plainly that this is
  for everyday secrets and that very secret passwords belong into Bitwarden Send or the sharing
  function of a password manager.
-->
<script lang="ts">
  import ShieldAlert from '@lucide/svelte/icons/shield-alert';
  import Copy from '@lucide/svelte/icons/copy';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { app } from '../../state';
  import { ApiError } from '../../api';
  import { t, formatFullDate } from '../../i18n/index.svelte';
  import { encryptSecret, MAX_SECRET_BYTES, SECRET_TTL_SECONDS, SECRET_VIEW_CHOICES, secretBytes, secretLinkUrl } from '../../secretlink';
  import Modal from '../common/Modal.svelte';

  interface Props {
    /** Puts the sentence with the link into the message. */
    oninsert?: (line: string, url: string) => void;
    onclose: () => void;
  }
  let { oninsert, onclose }: Props = $props();

  let text = $state('');
  let ttl = $state<keyof typeof SECRET_TTL_SECONDS>('day');
  let views = $state<number>(1);
  let busy = $state(false);
  let error = $state('');
  let result = $state<{ url: string; expiresAt: string } | null>(null);
  let copied = $state(false);

  const tooLong = $derived(secretBytes(text) > MAX_SECRET_BYTES);
  // Web Crypto only exists on secure connections (HTTPS, localhost).
  const secure = typeof crypto !== 'undefined' && !!crypto.subtle;

  async function create() {
    if (!text || tooLong || busy) return;
    busy = true;
    error = '';
    try {
      const enc = await encryptSecret(text);
      const link = await app.api.createSecretLink({ ciphertext: enc.ciphertext, ttlSeconds: SECRET_TTL_SECONDS[ttl], maxViews: views });
      if (!link.url) throw new Error('no link address');
      result = { url: secretLinkUrl(link.url, enc.key), expiresAt: link.expiresAt };
      text = ''; // the clear text does not stay in the dialog
    } catch (e) {
      error = e instanceof ApiError && e.code === 'quota_exceeded' ? t('compose.secret.quota') : e instanceof ApiError && e.code === 'unsupported' ? t('compose.secret.unavailable') : t('compose.secret.failed');
    } finally {
      busy = false;
    }
  }

  async function copy() {
    if (!result) return;
    try {
      await navigator.clipboard.writeText(result.url);
      copied = true;
      app.toast(t('compose.secret.copied'));
    } catch {
      /* the field is selectable */
    }
  }

  function insert() {
    if (!result) return;
    oninsert?.(t('compose.secret.line', { date: formatFullDate(result.expiresAt), link: result.url }), result.url);
    onclose();
  }
</script>

<Modal title={t('compose.secret.title')} {onclose} width="520px">
  <p class="intro">{t('compose.secret.intro')}</p>
  <div class="warn" role="note">
    <ShieldAlert size={18} aria-hidden="true" />
    <div>
      <b>{t('compose.secret.warnTitle')}</b>
      <p>{t('compose.secret.warn')}</p>
    </div>
  </div>

  {#if !result}
    <label class="field">
      <span class="label">{t('compose.secret.text')}</span>
      <textarea class="input" rows="4" bind:value={text} placeholder={t('compose.secret.textPlaceholder')} autocomplete="off" spellcheck="false" data-autofocus></textarea>
    </label>
    {#if tooLong}<p class="error" role="alert">{t('compose.secret.tooLong')}</p>{/if}
    {#if !secure}<p class="error" role="alert">{t('compose.secret.needsHttps')}</p>{/if}
    <div class="row">
      <label class="field">
        <span class="label">{t('compose.secret.expires')}</span>
        <select class="input" bind:value={ttl}>
          <option value="hour">{t('compose.secret.hour')}</option>
          <option value="day">{t('compose.secret.day')}</option>
          <option value="week">{t('compose.secret.week')}</option>
        </select>
      </label>
      <label class="field">
        <span class="label">{t('compose.secret.views')}</span>
        <select class="input" bind:value={views}>
          {#each SECRET_VIEW_CHOICES as n (n)}<option value={n}>{n === 1 ? t('compose.secret.once') : t('compose.secret.times', { count: n })}</option>{/each}
        </select>
      </label>
    </div>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  {:else}
    <label class="field">
      <span class="label">{t('compose.secret.link')}</span>
      <input class="input mono" readonly value={result.url} onfocus={(e) => e.currentTarget.select()} />
    </label>
    <p class="hint">{t('compose.secret.afterHint')}</p>
  {/if}

  {#snippet footer()}
    {#if !result}
      <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={!text || tooLong || busy || !secure} onclick={create}>
        {#if busy}<LoaderCircle size={15} class="spin" /> {t('compose.secret.creating')}{:else}{t('compose.secret.create')}{/if}
      </button>
    {:else}
      <button class="btn btn-primary" onclick={copy}><Copy size={14} /> {copied ? t('compose.secret.copied') : t('compose.secret.copy')}</button>
      {#if oninsert}<button class="btn" onclick={insert}>{t('compose.secret.insert')}</button>{:else}<button class="btn" onclick={onclose}>{t('compose.secret.done')}</button>{/if}
    {/if}
  {/snippet}
</Modal>

<style>
  .intro {
    margin: 0 0 10px;
    font-size: 13.5px;
    color: var(--tm-muted);
  }
  .warn {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 12px;
    margin-bottom: 14px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--tm-warn, #d97706) 12%, transparent);
    border: 1px solid color-mix(in srgb, var(--tm-warn, #d97706) 35%, transparent);
    font-size: 13px;
  }
  .warn p {
    margin: 2px 0 0;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin-bottom: 10px;
    flex: 1;
  }
  .row {
    display: flex;
    gap: 12px;
  }
  textarea {
    resize: vertical;
    font-family: var(--tm-mono, monospace);
  }
  .mono {
    font-family: var(--tm-mono, monospace);
    font-size: 12.5px;
  }
  .error {
    margin: 0 0 8px;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
</style>
