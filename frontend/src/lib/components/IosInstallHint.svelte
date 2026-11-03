<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- iOS delivers Web Push only to installed PWAs: explain "Zum Home-Bildschirm". -->
<script lang="ts">
  import { fly } from 'svelte/transition';
  import Share from '@lucide/svelte/icons/share';
  import SquarePlus from '@lucide/svelte/icons/square-plus';
  import X from '@lucide/svelte/icons/x';
  import { currentIosEnv, shouldShowIosInstallHint } from '../platform';
  import { t } from '../i18n/index.svelte';

  const KEY = 'tuxmail.iosHintDismissed';
  function dismissedBefore(): boolean {
    try {
      return localStorage.getItem(KEY) === '1';
    } catch {
      return false;
    }
  }

  let visible = $state(typeof navigator !== 'undefined' && shouldShowIosInstallHint(currentIosEnv(dismissedBefore())));

  function dismiss() {
    visible = false;
    try {
      localStorage.setItem(KEY, '1');
    } catch {
      /* ignore */
    }
  }
</script>

{#if visible}
  <aside class="hint" aria-label={t('ios.title')} transition:fly={{ y: 40, duration: 200 }}>
    <img src="./icons/icon-192.png" alt="" width="40" height="40" />
    <div class="text">
      <strong>{t('ios.title')}</strong>
      <span>
        {t('ios.step1')} <Share size={14} aria-label={t('ios.shareIcon')} />
        {t('ios.step2')} <SquarePlus size={14} aria-hidden="true" /> <b>{t('ios.addToHome')}</b>.
      </span>
      <small>{t('ios.why')}</small>
    </div>
    <button class="icon-btn" aria-label={t('action.close')} onclick={dismiss}><X size={16} /></button>
  </aside>
{/if}

<style>
  .hint {
    position: fixed;
    left: 12px;
    right: 12px;
    bottom: calc(72px + env(safe-area-inset-bottom));
    z-index: 45;
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 12px 8px 12px 12px;
    border-radius: 14px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
    max-width: 520px;
    margin: 0 auto;
  }
  img {
    border-radius: 10px;
    flex: none;
  }
  .text {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 13.5px;
  }
  .text span {
    line-height: 1.6;
  }
  .text :global(svg) {
    vertical-align: -2px;
    color: var(--tm-accent-text);
  }
  small {
    color: var(--tm-muted);
    font-size: 12px;
  }
</style>
