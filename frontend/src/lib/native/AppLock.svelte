<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2026 Midas Wollinger and TuxMail contributors -->
<!-- Full-screen lock overlay; covers the app until BiometricPrompt succeeds. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Lock from '@lucide/svelte/icons/lock';
  import penguinUrl from '$branding/tuxmail-penguin.png';
  import { t } from '../i18n/index.svelte';

  interface Props {
    /** Runs the system prompt; resolves true when unlocked. */
    unlock: () => Promise<boolean>;
    onunlocked: () => void;
  }
  let { unlock, onunlocked }: Props = $props();
  let failed = $state(false);
  let busy = $state(false);

  async function run() {
    if (busy) return;
    busy = true;
    failed = false;
    try {
      if (await unlock()) onunlocked();
      else failed = true;
    } finally {
      busy = false;
    }
  }

  onMount(() => {
    void run();
  });
</script>

<div class="lock" role="dialog" aria-modal="true" aria-labelledby="lock-title">
  <img src={penguinUrl} alt="" width="88" height="112" />
  <h1 id="lock-title">{t('native.lock.title')}</h1>
  {#if failed}<p class="error" role="alert">{t('native.lock.failed')}</p>{/if}
  <button class="btn btn-primary" onclick={() => void run()} disabled={busy}><Lock size={16} />{t('native.lock.unlock')}</button>
</div>

<style>
  .lock {
    position: fixed;
    inset: 0;
    z-index: 10000;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 16px;
    background: var(--tm-bg);
    padding: env(safe-area-inset-top) 16px env(safe-area-inset-bottom);
  }
  h1 {
    margin: 0;
    font-size: 20px;
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
  }
  .btn {
    height: 44px;
    padding: 0 20px;
    font-size: 14px;
  }
</style>
