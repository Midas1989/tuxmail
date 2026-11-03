<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Sun from '@lucide/svelte/icons/sun';
  import Moon from '@lucide/svelte/icons/moon';
  import Monitor from '@lucide/svelte/icons/monitor';
  import { app, prefs } from '../state';
  import type { Density, Theme } from '../state/prefs.svelte';
  import { t } from '../i18n/index.svelte';
  import type { Account } from '../types';
  import ChannelIcon from '../components/common/ChannelIcon.svelte';
  import AccountWizard from '../components/settings/AccountWizard.svelte';
  import NotificationSettings from '../components/settings/NotificationSettings.svelte';
  import ServerConnectWizard from '../components/settings/ServerConnectWizard.svelte';
  import Server from '@lucide/svelte/icons/server';
  import ScreenShell from './ScreenShell.svelte';

  let wizardOpen = $state(false);
  let connectOpen = $state(false);

  function server(a: Account): string {
    if (a.kind === 'whatsapp' || a.kind === 'signal') return a.extra?.address ?? '';
    const i = a.incoming;
    return i ? `${i.host}:${i.port} · ${t(`tls.${i.tls}`)}` : '';
  }

  const themes: { id: Theme; icon: typeof Sun }[] = [
    { id: 'light', icon: Sun },
    { id: 'dark', icon: Moon },
    { id: 'system', icon: Monitor },
  ];
  const densities: Density[] = ['compact', 'normal', 'comfortable'];

  const shortcuts: [string, string][] = $derived([
    ['Ctrl+K', t('shortcut.palette')],
    ['Ctrl+Shift+V', t('shortcut.toggleView')],
    ['J / K', t('shortcut.nextPrev')],
    ['Enter', t('shortcut.open')],
    ['Esc', t('shortcut.close')],
    ['C', t('shortcut.compose')],
    ['R / Shift+R / F', t('shortcut.reply')],
    ['E / Entf', t('shortcut.archiveDelete')],
    ['S', t('shortcut.flag')],
    ['/', t('shortcut.search')],
    ['Ctrl+Enter', t('shortcut.send')],
  ]);
</script>

<ScreenShell title={t('nav.settings')}>
  <div class="cols">
    <section class="card" aria-labelledby="set-accounts">
      <div class="card-head">
        <h2 id="set-accounts">{t('settings.accounts')}</h2>
        <button class="btn btn-primary" onclick={() => (wizardOpen = true)}><Plus size={15} /> {t('settings.addAccount')}</button>
      </div>
      <ul class="accounts">
        {#each app.accounts as a (a.id)}
          <li>
            <span class="dot" style:background={a.color}></span>
            <div class="acc">
              <span class="name">
                {a.displayName}
                {#if a.kind === 'whatsapp' || a.kind === 'signal'}<ChannelIcon channel={a.kind} size={13} />{/if}
              </span>
              <span class="sub">{a.extra?.address} · {server(a)}</span>
            </div>
            <span class="kind">{t(`protocol.${a.kind}` as 'protocol.imap')}</span>
            <span class="state" class:on={a.enabled}>{a.enabled ? t('settings.active') : t('settings.inactive')}</span>
          </li>
        {/each}
      </ul>
    </section>

    <section class="card" aria-labelledby="set-server">
      <h2 id="set-server">{t('connect.cardTitle')}</h2>
      <p class="about">{t('connect.cardText')}</p>
      <button class="btn btn-primary" onclick={() => (connectOpen = true)}><Server size={15} /> {t('connect.title')}</button>
    </section>

    <NotificationSettings />

    <section class="card" aria-labelledby="set-look">
      <h2 id="set-look">{t('settings.appearance')}</h2>
      <div class="field">
        <span class="label" id="lbl-theme">{t('settings.theme')}</span>
        <div class="seg" role="radiogroup" aria-labelledby="lbl-theme">
          {#each themes as th (th.id)}
            {@const Icon = th.icon}
            <button role="radio" aria-checked={prefs.theme === th.id} onclick={() => ((prefs.theme = th.id), prefs.save())}><Icon size={14} /> {t(`theme.${th.id}`)}</button>
          {/each}
        </div>
      </div>
      <div class="field">
        <span class="label" id="lbl-density">{t('settings.density')}</span>
        <div class="seg" role="radiogroup" aria-labelledby="lbl-density">
          {#each densities as d (d)}
            <button role="radio" aria-checked={prefs.density === d} onclick={() => ((prefs.density = d), prefs.save())}>{t(`density.${d}`)}</button>
          {/each}
        </div>
      </div>
      <div class="field">
        <label class="label" for="set-lang">{t('settings.language')}</label>
        <select id="set-lang" class="input narrow" value={prefs.locale} onchange={(e) => prefs.setLocale((e.currentTarget as HTMLSelectElement).value as 'de' | 'en')}>
          <option value="de">Deutsch</option>
          <option value="en">English</option>
        </select>
      </div>
      <div class="field">
        <span class="label" id="lbl-view">{t('settings.defaultView')}</span>
        <div class="seg" role="radiogroup" aria-labelledby="lbl-view">
          <button role="radio" aria-checked={prefs.viewMode === 'classic'} onclick={() => app.setViewMode('classic')}>{t('view.classic')}</button>
          <button role="radio" aria-checked={prefs.viewMode === 'chat'} onclick={() => app.setViewMode('chat')}>{t('view.chat')}</button>
        </div>
      </div>
    </section>

    <section class="card" aria-labelledby="set-keys">
      <h2 id="set-keys">{t('settings.shortcuts')}</h2>
      <dl class="keys">
        {#each shortcuts as [k, d] (k)}
          <dt><span class="kbd">{k}</span></dt>
          <dd>{d}</dd>
        {/each}
      </dl>
    </section>

    <section class="card" aria-labelledby="set-about">
      <h2 id="set-about">{t('settings.about')}</h2>
      <p class="about">{t('settings.aboutText')}</p>
      <p class="about">{t('settings.transport', { kind: app.api.kind })}</p>
    </section>
  </div>
</ScreenShell>

{#if wizardOpen}
  <AccountWizard onclose={() => (wizardOpen = false)} />
{/if}
{#if connectOpen}
  <ServerConnectWizard onclose={() => (connectOpen = false)} />
{/if}

<style>
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(380px, 1fr));
    gap: 16px;
    max-width: 1200px;
  }
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
    gap: 8px;
    margin-bottom: 8px;
  }
  h2 {
    margin: 0 0 10px;
    font-size: 15px;
    font-weight: 650;
  }
  .card-head h2 {
    margin: 0;
  }
  .accounts {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .accounts li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 0;
    border-top: 1px solid var(--tm-line);
  }
  .dot {
    width: 10px;
    height: 10px;
    border-radius: 99px;
    flex: none;
  }
  .acc {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .name {
    display: inline-flex;
    gap: 6px;
    align-items: center;
    font-weight: 600;
  }
  .sub {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .kind {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .state {
    font-size: 11px;
    padding: 1px 8px;
    border-radius: 99px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
  }
  .state.on {
    background: color-mix(in srgb, var(--tm-success) 16%, var(--tm-surface));
    color: var(--tm-success);
  }
  .field {
    margin-bottom: 14px;
  }
  .narrow {
    width: 200px;
  }
  .keys {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 8px 16px;
    margin: 0;
    font-size: 13px;
  }
  .keys dd {
    margin: 0;
    color: var(--tm-muted);
  }
  .about {
    margin: 0 0 10px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  @media (max-width: 639px) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
</style>
