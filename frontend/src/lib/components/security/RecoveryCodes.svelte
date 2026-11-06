<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Recovery codes of two-factor authentication: shown ONCE (the server keeps
  only hashes). Copy / download buttons and a required "I have stored them"
  confirmation (`confirmed`, bindable) that the parent uses to enable its
  finish button.
-->
<script lang="ts">
  import Copy from '@lucide/svelte/icons/copy';
  import Download from '@lucide/svelte/icons/download';
  import Check from '@lucide/svelte/icons/check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { t } from '../../i18n/index.svelte';
  import { RECOVERY_FILENAME, recoveryCodesText } from '../../security';

  interface Props {
    codes: string[];
    confirmed?: boolean;
  }
  let { codes, confirmed = $bindable(false) }: Props = $props();

  let copied = $state(false);
  let copyFailed = $state(false);

  async function copy() {
    copyFailed = false;
    try {
      await navigator.clipboard.writeText(codes.join('\n'));
      copied = true;
      setTimeout(() => (copied = false), 3000);
    } catch {
      copyFailed = true;
    }
  }

  function download() {
    const text = recoveryCodesText(codes, t('security.codes.fileHeader'), t('security.codes.fileNote'));
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = RECOVERY_FILENAME;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 30_000);
  }
</script>

<div class="rc">
  <p class="warn"><TriangleAlert size={15} aria-hidden="true" /> <span>{t('security.codes.once')}</span></p>
  <ul class="codes" aria-label={t('security.codes.listLabel')} data-testid="recovery-codes">
    {#each codes as c (c)}
      <li><code>{c}</code></li>
    {/each}
  </ul>
  <div class="actions">
    <button type="button" class="btn" onclick={() => void copy()}>
      {#if copied}<Check size={14} />{:else}<Copy size={14} />{/if}
      {copied ? t('security.codes.copied') : t('security.codes.copy')}
    </button>
    <button type="button" class="btn" onclick={download}><Download size={14} /> {t('security.codes.download')}</button>
  </div>
  {#if copyFailed}<p class="error" role="alert">{t('security.codes.copyFailed')}</p>{/if}
  <label class="confirm">
    <input type="checkbox" bind:checked={confirmed} />
    <span>{t('security.codes.stored')}</span>
  </label>
</div>

<style>
  .rc {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .warn {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-warn) 12%, var(--tm-surface));
    color: var(--tm-fg);
    font-size: 13px;
  }
  .warn :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--tm-warn);
  }
  .codes {
    list-style: none;
    margin: 0;
    padding: 12px;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
    gap: 6px 12px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-sm);
    background: var(--tm-surface-2);
  }
  .codes code {
    font-family: var(--tm-mono);
    font-size: 14px;
    letter-spacing: 0.04em;
    user-select: all;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .confirm {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    font-size: 13.5px;
    cursor: pointer;
  }
  .confirm input {
    margin-top: 3px;
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
</style>
