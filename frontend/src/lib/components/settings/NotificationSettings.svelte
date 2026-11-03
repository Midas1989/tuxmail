<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Bell from '@lucide/svelte/icons/bell';
  import BellOff from '@lucide/svelte/icons/bell-off';
  import Send from '@lucide/svelte/icons/send';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { currentPushSubscription, disablePush, enablePush, pushSupported } from '../../pwa';
  import { currentIosEnv, isIos, isWails } from '../../platform';
  import type { Channel, NotificationSettings } from '../../types';
  import ChannelIcon from '../common/ChannelIcon.svelte';

  type PushState = 'loading' | 'desktop' | 'unsupported' | 'ios-install' | 'off' | 'denied' | 'on';

  let settings = $state<NotificationSettings | null>(null);
  let pushState = $state<PushState>('loading');
  let busy = $state(false);
  let saved = $state(false);

  async function refreshPushState() {
    if (isWails()) return (pushState = 'desktop');
    if (!pushSupported()) {
      const env = currentIosEnv(false);
      return (pushState = isIos(env) && !env.standalone && !env.displayStandalone ? 'ios-install' : 'unsupported');
    }
    if (Notification.permission === 'denied') return (pushState = 'denied');
    pushState = (await currentPushSubscription()) ? 'on' : 'off';
  }

  onMount(() => {
    void app.api.getNotificationSettings().then((s) => (settings = s));
    void refreshPushState();
  });

  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  function save() {
    if (!settings) return;
    saved = false;
    if (saveTimer) clearTimeout(saveTimer);
    const snapshot = $state.snapshot(settings) as NotificationSettings;
    saveTimer = setTimeout(async () => {
      try {
        settings = await app.api.saveNotificationSettings(snapshot);
        saved = true;
      } catch (err) {
        app.toast(String(err instanceof Error ? err.message : err), undefined, 'error');
      }
    }, 400);
  }

  async function togglePush() {
    busy = true;
    try {
      if (pushState === 'on') {
        await disablePush(app.api);
      } else {
        const r = await enablePush(app.api);
        if (r === 'denied') app.toast(t('notify.denied'), undefined, 'error');
        if (r === 'no-sw') app.toast(t('notify.noServiceWorker'), undefined, 'error');
      }
    } catch (err) {
      app.toast(t('notify.failed', { error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
    } finally {
      busy = false;
      await refreshPushState();
    }
  }

  async function test() {
    try {
      if (!isWails() && typeof Notification !== 'undefined' && Notification.permission === 'default') await Notification.requestPermission();
      await app.api.sendTestNotification();
      app.toast(t('notify.testSent'));
    } catch (err) {
      app.toast(t('notify.failed', { error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
    }
  }

  const channels: Channel[] = ['email', 'whatsapp', 'signal'];
</script>

<section class="card" aria-labelledby="set-notify">
  <div class="card-head">
    <h2 id="set-notify">{t('notify.title')}</h2>
    {#if saved}<span class="saved" role="status">{t('notify.saved')}</span>{/if}
  </div>

  <div class="push">
    <span class="ic" class:on={pushState === 'on'}>{#if pushState === 'on'}<Bell size={18} />{:else}<BellOff size={18} />{/if}</span>
    <div class="ptext">
      <strong>{t(`notify.state.${pushState}`)}</strong>
      <small>{t(`notify.stateHint.${pushState}`)}</small>
    </div>
    {#if pushState === 'on' || pushState === 'off'}
      <button class="btn" class:btn-primary={pushState === 'off'} disabled={busy} onclick={togglePush}>
        {pushState === 'on' ? t('notify.disable') : t('notify.enable')}
      </button>
    {/if}
  </div>

  {#if settings}
    <fieldset>
      <legend class="label">{t('notify.privacy')}</legend>
      <label class="radio"><input type="radio" name="privacy" value="full" bind:group={settings.privacy} onchange={save} /> {t('notify.privacyFull')}</label>
      <label class="radio"><input type="radio" name="privacy" value="generic" bind:group={settings.privacy} onchange={save} /> {t('notify.privacyGeneric')}</label>
    </fieldset>

    <fieldset>
      <legend class="label">{t('notify.quietHours')}</legend>
      <div class="row">
        <label class="check"><input type="checkbox" bind:checked={settings.quietHours.enabled} onchange={save} /> {t('notify.quietEnabled')}</label>
        <label class="time">{t('notify.from')} <input class="input" type="time" bind:value={settings.quietHours.start} onchange={save} disabled={!settings.quietHours.enabled} /></label>
        <label class="time">{t('notify.to')} <input class="input" type="time" bind:value={settings.quietHours.end} onchange={save} disabled={!settings.quietHours.enabled} /></label>
      </div>
    </fieldset>

    <fieldset>
      <legend class="label">{t('notify.channels')}</legend>
      <div class="row">
        {#each channels as ch (ch)}
          <label class="check"><input type="checkbox" bind:checked={settings.channels[ch]} onchange={save} /> <ChannelIcon channel={ch} size={14} label /></label>
        {/each}
      </div>
    </fieldset>

    <fieldset>
      <legend class="label">{t('notify.accounts')}</legend>
      {#each app.accounts as a (a.id)}
        <label class="check acc">
          <input
            type="checkbox"
            checked={settings.accounts[String(a.id)] !== false}
            onchange={(e) => {
              settings!.accounts[String(a.id)] = (e.currentTarget as HTMLInputElement).checked;
              save();
            }}
          />
          <span class="dot" style:background={a.color}></span>
          {a.displayName} <small>{a.extra?.address}</small>
        </label>
      {/each}
    </fieldset>
  {/if}

  <button class="btn" onclick={test}><Send size={14} /> {t('notify.test')}</button>
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  .saved {
    font-size: 12px;
    color: var(--tm-success);
  }
  .push {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border-radius: 12px;
    background: var(--tm-surface-2);
    margin-bottom: 14px;
  }
  .ic {
    display: inline-flex;
    padding: 8px;
    border-radius: 10px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
  }
  .ic.on {
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
  }
  .ptext {
    flex: 1;
    display: flex;
    flex-direction: column;
    font-size: 13.5px;
  }
  .ptext small {
    color: var(--tm-muted);
    font-size: 12px;
  }
  fieldset {
    border: none;
    padding: 0;
    margin: 0 0 14px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 18px;
    align-items: center;
  }
  .radio,
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
    padding: 3px 0;
  }
  .radio input,
  .check input {
    accent-color: var(--tm-accent);
  }
  .time {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .time .input {
    width: 110px;
    height: 30px;
  }
  .acc small {
    color: var(--tm-faint);
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 9px;
  }
</style>
