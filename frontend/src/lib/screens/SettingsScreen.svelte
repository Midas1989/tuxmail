<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Sun from '@lucide/svelte/icons/sun';
  import Moon from '@lucide/svelte/icons/moon';
  import Monitor from '@lucide/svelte/icons/monitor';
  import { app, prefs } from '../state';
  import type { Density, Theme } from '../state/prefs.svelte';
  import { t } from '../i18n/index.svelte';
  import { messengerChannel, type Account } from '../types';
  import ChannelIcon from '../components/common/ChannelIcon.svelte';
  import AccountWizard from '../components/settings/AccountWizard.svelte';
  import NotificationSettings from '../components/settings/NotificationSettings.svelte';
  import RegionSettings from '../components/settings/RegionSettings.svelte';
  import ChatSettings from '../components/settings/ChatSettings.svelte';
  import SnippetSettings from '../components/settings/SnippetSettings.svelte';
  import AiSettingsCard from '../components/settings/AiSettingsCard.svelte';
  import logoUrl from '$branding/tuxmail-logo.png';
  import ServerConnectWizard from '../components/settings/ServerConnectWizard.svelte';
  import Server from '@lucide/svelte/icons/server';
  import ScreenShell from './ScreenShell.svelte';
  import SignatureSettings from '../components/settings/SignatureSettings.svelte';
  import TagSettings from '../components/settings/TagSettings.svelte';
  import WorkflowList from '../components/workflow/WorkflowList.svelte';
  import MessengerSettings from '../components/settings/MessengerSettings.svelte';
  import MessengerWizard from '../components/settings/MessengerWizard.svelte';
  import MessageCircle from '@lucide/svelte/icons/message-circle';

  let messengerWizardOpen = $state(false);
  import SieveSettings from '../components/settings/SieveSettings.svelte';
  import VacationSettings from '../components/settings/VacationSettings.svelte';
  import RemoteContentSettings from '../components/settings/RemoteContentSettings.svelte';
  import FolderSettings from '../components/settings/FolderSettings.svelte';
  import ImportExportSettings from '../components/settings/ImportExportSettings.svelte';
  import BackupSettings from '../components/settings/BackupSettings.svelte';
  import CloudAccountsSettings from '../components/settings/CloudAccountsSettings.svelte'; // OAuth/Cloud accounts
  import ComfortSettingsCard from '../components/comfort/ComfortSettingsCard.svelte'; // spam, categories, spell, merge, PDF
  import SmimeSettingsCard from '../components/comfort/SmimeSettingsCard.svelte';
  import TeamSettings from '../components/team/TeamSettings.svelte'; // team mailboxes (server mode)
  import AccountEditDialog from '../components/settings/AccountEditDialog.svelte'; // account edit
  import ChevronRight from '@lucide/svelte/icons/chevron-right';

  /** Account whose edit dialog is open ("Konto bearbeiten"). */
  let editAccountId = $state<number | null>(null);

  // Deep links from the tree / palette ("Manage tags", "Workflows").
  $effect(() => {
    const id = app.settingsAnchor;
    if (!id) return;
    requestAnimationFrame(() => {
      document.getElementById(id)?.scrollIntoView({ block: 'start' });
      app.settingsAnchor = null;
    });
  });

  let wizardOpen = $state(false);
  let connectOpen = $state(false);

  function server(a: Account): string {
    if (messengerChannel(a.kind)) return a.extra?.address ?? '';
    const i = a.incoming;
    return i ? `${i.host}:${i.port} · ${t(`settings.tls.${i.tls}`)}` : '';
  }

  const themes: { id: Theme; icon: typeof Sun }[] = [
    { id: 'light', icon: Sun },
    { id: 'dark', icon: Moon },
    { id: 'system', icon: Monitor },
  ];
  const densities: Density[] = ['compact', 'normal', 'comfortable'];

  const shortcuts: [string, string][] = $derived([
    ['Ctrl+K', t('settings.shortcut.palette')],
    ['Ctrl+Shift+V', t('settings.shortcut.toggleView')],
    ['J / K', t('settings.shortcut.nextPrev')],
    ['Enter', t('settings.shortcut.open')],
    ['Esc', t('settings.shortcut.close')],
    ['C', t('settings.shortcut.compose')],
    ['R / Shift+R / F', t('settings.shortcut.reply')],
    ['E / Entf', t('settings.shortcut.archiveDelete')],
    ['S', t('settings.shortcut.flag')],
    ['T', t('settings.shortcut.tag')],
    ['/', t('settings.shortcut.search')],
    ['Ctrl+Enter', t('settings.shortcut.send')],
  ]);
</script>

<ScreenShell title={t('nav.settings')}>
  <div class="cols">
    <section class="card" aria-labelledby="set-accounts">
      <div class="card-head">
        <h2 id="set-accounts">{t('settings.accounts')}</h2>
        <span class="acc-actions">
          <button class="btn" onclick={() => (messengerWizardOpen = true)}><MessageCircle size={15} /> {t('messenger.add')}</button>
          <button class="btn btn-primary" onclick={() => (wizardOpen = true)}><Plus size={15} /> {t('settings.addAccount')}</button>
        </span>
      </div>
      <ul class="accounts">
        {#each app.accounts as a (a.id)}
          <li>
            <button class="acc-row" aria-label={t('settings.accountEdit.open', { name: a.displayName })} onclick={() => (editAccountId = a.id)}>
            <span class="dot" style:background={a.color}></span>
            <div class="acc">
              <span class="name">
                {a.displayName}
                {#if messengerChannel(a.kind)}<ChannelIcon channel={messengerChannel(a.kind)!} size={13} />{/if}
              </span>
              <span class="sub">{a.extra?.address} · {server(a)}</span>
            </div>
            <span class="kind">{t(`settings.protocol.${a.kind}` as 'settings.protocol.imap')}</span>
            <span class="state" class:on={a.enabled}>{a.enabled ? t('settings.active') : t('settings.inactive')}</span>
            <ChevronRight size={15} class="chev" />
            </button>
          </li>
        {/each}
      </ul>
    </section>

    <FolderSettings />

    <section class="card" aria-labelledby="set-server">
      <h2 id="set-server">{t('onboarding.connect.cardTitle')}</h2>
      <p class="about">{t('onboarding.connect.cardText')}</p>
      <button class="btn btn-primary" onclick={() => (connectOpen = true)}><Server size={15} /> {t('onboarding.connect.title')}</button>
    </section>

    <TeamSettings />

    <MessengerSettings />

    <NotificationSettings />

    <RegionSettings />

    <ChatSettings />

    <SignatureSettings />

    <SnippetSettings />

    <AiSettingsCard />

    <CloudAccountsSettings />

    <ComfortSettingsCard />

    <SmimeSettingsCard />

    <TagSettings />

    <section class="card wide-card" id="set-workflows" aria-labelledby="set-workflows-h">
      <h2 id="set-workflows-h">{t('settings.workflows.title')}</h2>
      <p class="about">{t('settings.workflows.intro')}</p>
      <WorkflowList />
    </section>

    <VacationSettings />

    <RemoteContentSettings />

    <SieveSettings />

    <ImportExportSettings />

    <BackupSettings />

    <section class="card" aria-labelledby="set-look">
      <h2 id="set-look">{t('settings.appearance')}</h2>
      <div class="field">
        <span class="label" id="lbl-theme">{t('settings.theme')}</span>
        <div class="seg" role="radiogroup" aria-labelledby="lbl-theme">
          {#each themes as th (th.id)}
            {@const Icon = th.icon}
            <button role="radio" aria-checked={prefs.theme === th.id} onclick={() => ((prefs.theme = th.id), prefs.save())}><Icon size={14} /> {t(`settings.themes.${th.id}`)}</button>
          {/each}
        </div>
      </div>
      <div class="field">
        <span class="label" id="lbl-density">{t('settings.density')}</span>
        <div class="seg" role="radiogroup" aria-labelledby="lbl-density">
          {#each densities as d (d)}
            <button role="radio" aria-checked={prefs.density === d} onclick={() => ((prefs.density = d), prefs.save())}>{t(`settings.densities.${d}`)}</button>
          {/each}
        </div>
      </div>
      <div class="field">
        <span class="label" id="lbl-view">{t('settings.defaultView')}</span>
        <div class="seg" role="radiogroup" aria-labelledby="lbl-view">
          <button role="radio" aria-checked={prefs.viewMode === 'classic'} onclick={() => app.setViewMode('classic')}>{t('mail.view.classic')}</button>
          <button role="radio" aria-checked={prefs.viewMode === 'hybrid'} onclick={() => app.setViewMode('hybrid')}>{t('mail.view.hybrid')}</button>
          <button role="radio" aria-checked={prefs.viewMode === 'chat'} onclick={() => app.setViewMode('chat')}>{t('mail.view.chat')}</button>
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
      <img class="logo" src={logoUrl} alt="TuxMail" width="160" height="160" />
      <p class="about">{t('settings.aboutText')}</p>
      <p class="about">{t('settings.transport', { kind: app.api.kind })}</p>
    </section>
  </div>
</ScreenShell>

{#if wizardOpen}
  <AccountWizard onclose={() => (wizardOpen = false)} />
{/if}
{#if messengerWizardOpen}
  <MessengerWizard onclose={() => (messengerWizardOpen = false)} />
{/if}
{#if connectOpen}
  <ServerConnectWizard onclose={() => (connectOpen = false)} />
{/if}
{#if editAccountId !== null}
  <AccountEditDialog accountId={editAccountId} onclose={() => (editAccountId = null)} />
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
  .wide-card {
    grid-column: 1 / -1;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 8px;
  }
  .acc-actions {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 6px;
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
    border-top: 1px solid var(--tm-line);
  }
  .acc-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 10px 4px;
    background: none;
    border: none;
    font: inherit;
    color: inherit;
    text-align: left;
    cursor: pointer;
    border-radius: 8px;
  }
  .acc-row:hover,
  .acc-row:focus-visible {
    background: var(--tm-surface-2, var(--tm-surface-3));
  }
  .acc-row :global(.chev) {
    color: var(--tm-faint);
    flex: none;
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
  .logo {
    display: block;
    width: 160px;
    height: auto;
    border-radius: 16px;
    margin: 0 0 10px;
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
