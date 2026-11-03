<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2026 Midas Wollinger and TuxMail contributors -->
<!--
  App settings of the Android app (server profile, app lock, push), shown as
  overlay for the route #/native-settings. The regular settings screen can
  link here; all native calls go through the controller passed in.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import X from '@lucide/svelte/icons/x';
  import LogOut from '@lucide/svelte/icons/log-out';
  import Server from '@lucide/svelte/icons/server';
  import { t } from '../i18n/index.svelte';
  import { GRACE_CHOICES, type LockSettings } from './applock';
  import type { PushInfo } from './plugins';
  import type { NativeController, PushStatus } from './status';


  interface Props {
    ctl: NativeController;
    onclose: () => void;
  }
  let { ctl, onclose }: Props = $props();

  let lock = $state<LockSettings>({ enabled: false, graceSeconds: 0 });
  let bioOk = $state(true);
  let push = $state<PushInfo | null>(null);
  let status = $state<PushStatus>({ state: 'idle' });
  let distributor = $state('');
  let version = $state('');
  let busy = $state(false);

  onMount(() => {
    status = ctl.pushStatus();
    void (async () => {
      [lock, bioOk, push, version] = await Promise.all([ctl.lockSettings(), ctl.biometricAvailable(), ctl.pushInfo().catch(() => null), ctl.appVersion().catch(() => '')]);
      distributor = push?.distributor ?? push?.distributors[0] ?? '';
    })();
  });

  async function setLock(next: LockSettings) {
    lock = next;
    await ctl.saveLockSettings(next);
  }

  async function enablePush() {
    busy = true;
    try {
      status = await ctl.enablePush(distributor || undefined);
    } finally {
      busy = false;
    }
  }
</script>

<div class="sheet" role="dialog" aria-modal="true" aria-labelledby="ns-title">
  <header>
    <h1 id="ns-title">{t('native.settings.title')}</h1>
    <button class="icon-btn" aria-label={t('common.action.close')} title={t('common.action.close')} onclick={onclose}><X size={18} /></button>
  </header>

  <section>
    <h2>{t('native.settings.server')}</h2>
    <p class="muted">{t('native.settings.signedInAs', { server: ctl.serverLabel, user: ctl.userLabel })}</p>
    <div class="col">
      <button class="btn" onclick={() => void ctl.switchServer()}><Server size={16} />{t('native.settings.switchServer')}</button>
      <button class="btn danger" onclick={() => void ctl.signOut()}><LogOut size={16} />{t('native.settings.signOut')}</button>
    </div>
  </section>

  <section>
    <h2>{t('native.settings.appLock')}</h2>
    {#if bioOk}
      <label class="toggle">
        <input type="checkbox" checked={lock.enabled} onchange={(e) => void setLock({ ...lock, enabled: (e.currentTarget as HTMLInputElement).checked })} />
        <span>{t('native.settings.appLockHint')}</span>
      </label>
      {#if lock.enabled}
        <label class="label" for="ns-grace">{t('native.settings.lockAfter')}</label>
        <select id="ns-grace" class="input" value={String(lock.graceSeconds)} onchange={(e) => void setLock({ ...lock, graceSeconds: Number((e.currentTarget as HTMLSelectElement).value) })}>
          {#each GRACE_CHOICES as g (g)}
            <option value={String(g)}>{g === 0 ? t('native.settings.lockImmediately') : t('native.settings.lockAfterMinutes', { count: g / 60 })}</option>
          {/each}
        </select>
      {/if}
    {:else}
      <p class="muted">{t('native.settings.appLockUnavailable')}</p>
    {/if}
  </section>

  <section>
    <h2>{t('native.settings.push')}</h2>
    {#if push}
      <p class="muted">{push.provider === 'fcm' ? t('native.settings.pushProviderFcm') : t('native.settings.pushProviderUnifiedPush')}</p>
      {#if push.provider === 'fcm' && !push.configured}
        <p class="warn">{t('native.settings.pushNotConfigured')}</p>
      {:else if push.provider === 'unifiedpush' && push.distributors.length === 0}
        <p class="warn">{t('native.settings.pushNoDistributor')}</p>
      {:else}
        {#if push.provider === 'unifiedpush' && push.distributors.length > 1}
          <label class="label" for="ns-dist">{t('native.settings.pushDistributor')}</label>
          <select id="ns-dist" class="input" bind:value={distributor}>
            {#each push.distributors as d (d)}<option value={d}>{d}</option>{/each}
          </select>
        {/if}
        {#if status.state === 'active'}
          <p class="ok">{t('native.settings.pushActive')}</p>
        {:else if status.state === 'denied'}
          <p class="warn">{t('native.settings.pushDenied')}</p>
        {:else if status.state === 'server-unsupported'}
          <p class="warn">{t('native.settings.pushServerUnsupported')}</p>
        {:else if status.state === 'failed'}
          <p class="warn">{t('native.settings.pushFailed', { message: status.message ?? '' })}</p>
        {/if}
        <button class="btn btn-primary" disabled={busy} onclick={() => void enablePush()}>{t('native.settings.pushEnable')}</button>
      {/if}
    {/if}
  </section>

  {#if version}<p class="muted small">{t('native.settings.version', { version })}</p>{/if}
</div>

<style>
  .sheet {
    position: fixed;
    inset: 0;
    z-index: 900;
    overflow-y: auto;
    background: var(--tm-bg);
    padding: calc(12px + env(safe-area-inset-top)) 16px calc(24px + env(safe-area-inset-bottom));
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h1 {
    margin: 0;
    font-size: 20px;
  }
  h2 {
    margin: 0 0 8px;
    font-size: 14px;
  }
  section {
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 640px;
  }
  .col {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .btn {
    height: 44px;
    justify-content: center;
    font-size: 14px;
  }
  .danger {
    color: var(--tm-danger);
  }
  .muted {
    margin: 0;
    color: var(--tm-muted);
  }
  .small {
    font-size: 12px;
  }
  .warn {
    margin: 0;
    color: var(--tm-warn);
  }
  .ok {
    margin: 0;
    color: var(--tm-success);
  }
  .toggle {
    display: flex;
    gap: 10px;
    align-items: flex-start;
  }
  .toggle input {
    width: 20px;
    height: 20px;
    margin-top: 2px;
  }
  .input {
    height: 44px;
  }
</style>
