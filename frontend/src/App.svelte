<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { app, connection, prefs, security, team } from './lib/state';
  import { startRouter } from './lib/router';
  import { parseSettingsHash } from './lib/settings/catalog';
  import { installShortcuts } from './lib/keyboard';
  import { t } from './lib/i18n/index.svelte';
  import ActivityBar from './lib/components/ActivityBar.svelte';
  import CommandPalette from './lib/components/CommandPalette.svelte';
  import Composer from './lib/components/compose/Composer.svelte';
  import Toasts from './lib/components/common/Toasts.svelte';
  import MailScreen from './lib/screens/MailScreen.svelte';
  import CalendarScreen from './lib/screens/CalendarScreen.svelte';
  import ContactsScreen from './lib/screens/ContactsScreen.svelte';
  import FilesScreen from './lib/screens/FilesScreen.svelte';
  import SettingsScreen from './lib/screens/SettingsScreen.svelte';
  import SearchScreen from './lib/screens/SearchScreen.svelte';
  import ProjectsScreen from './lib/screens/ProjectsScreen.svelte';
  import OrganizeDialogs from './lib/components/organize/OrganizeDialogs.svelte';
  import IosInstallHint from './lib/components/IosInstallHint.svelte';
  import Reminders from './lib/components/calendar/Reminders.svelte';
  import VaultUnlock from './lib/components/settings/VaultUnlock.svelte';
  import MfaSetupDialog from './lib/components/security/MfaSetupDialog.svelte';
  import { registerServiceWorker } from './lib/pwa';
  import CloudDialogs from './lib/components/cloud/CloudDialogs.svelte';
  import TitleBar from './lib/components/TitleBar.svelte';
  import MailWindows from './lib/components/mail/MailWindows.svelte';
  import { desktopWindow } from './lib/native/window.svelte';
  import ConnectionBar from './lib/components/common/ConnectionBar.svelte';
  import ServerConnectWizard from './lib/components/settings/ServerConnectWizard.svelte';

  // Theme + density tokens on <html>.
  $effect(() => {
    prefs.apply(document.documentElement);
  });

  onMount(() => {
    const stopRouter = startRouter((r, hash) => {
      app.route = r;
      if (r === 'settings') app.settingsLoc = parseSettingsHash(hash);
    });
    const stopKeys = installShortcuts(app);
    void desktopWindow.init();
    const stopSecurity = security.start(); // web client: forced two-factor set-up for administrators
    const stopConnection = connection.start(); // desktop app: connection to a TuxMail server
    connection.onAttention = () => app.toast(t('connection.attention'), { label: t('connection.attentionOpen'), run: () => (location.hash = '#/settings/accounts/server') });
    connection.onSettingsSynced = () => {
      void prefs.connect(app.api);
      void app.reloadAfterReconnect();
    };
    void app.init().then(() => {
      // PWA shortcuts: #/mail?compose=1, #/mail?view=chat
      const q = new URLSearchParams(location.hash.split('?')[1] ?? '');
      const v = q.get('view');
      if (v === 'chat' || v === 'hybrid') void app.setViewMode(v);
      if (q.get('compose')) app.openCompose();
      void team.init(); // team mailboxes (server mode)
    });
    // Browser/PWA only (never inside Wails); notification clicks navigate here.
    void registerServiceWorker((url) => {
      const u = new URL(url, location.href);
      if (u.origin === location.origin) location.hash = u.hash || '#/mail';
    });
    return () => {
      stopRouter();
      stopKeys();
      stopSecurity();
      stopConnection();
      app.destroy();
    };
  });
</script>

<a class="skip" href="#main">{t('common.a11y.skipToContent')}</a>
{#if desktopWindow.custom}
  <TitleBar />
{/if}
<ConnectionBar />
<div class="app" class:barred={connection.unreachable} class:framed={desktopWindow.custom} class:chat-open={app.route === 'mail' && !app.messageListView && app.selectedConversationId != null}>
  <ActivityBar />
  <main id="main" class="main" tabindex="-1">
    {#if app.loadError}
      <div class="fatal" role="alert">
        <h1>{t('errors.loadTitle')}</h1>
        <p>{app.loadError}</p>
        <button class="btn btn-primary" onclick={() => location.reload()}>{t('errors.retry')}</button>
      </div>
    {:else if app.route === 'mail'}
      <MailScreen />
    {:else if app.route === 'search'}
      <SearchScreen />
    {:else if app.route === 'projects'}
      <ProjectsScreen />
    {:else if app.route === 'calendar'}
      <CalendarScreen />
    {:else if app.route === 'contacts'}
      <ContactsScreen />
    {:else if app.route === 'files'}
      <FilesScreen />
    {:else if app.route === 'settings'}
      <SettingsScreen />
    {/if}
  </main>
</div>

{#if connection.wizard}
  <ServerConnectWizard purpose={connection.wizard} onclose={() => (connection.wizard = null)} />
{/if}
{#if app.vault?.locked}
  <VaultUnlock />
{/if}
{#if security.mfaSetupRequired}
  <MfaSetupDialog />
{/if}
{#if app.paletteOpen}
  <CommandPalette />
{/if}
{#if app.composer}
  <Composer init={app.composer} onclose={() => (app.composer = null)} />
{/if}
<MailWindows />
<OrganizeDialogs />
<CloudDialogs />
<Toasts />
{#if app.ready}<Reminders />{/if}
<IosInstallHint />

<style>
  .app {
    display: flex;
    height: 100%;
    min-height: 0;
    overflow: hidden;
  }
  /* Below the custom title bar of the frameless desktop window (34 px). */
  .app.barred {
    height: calc(100% - 34px);
  }
  .app.framed.barred {
    height: calc(100% - 68px);
  }
  .app.framed:not(.barred) {
    height: calc(100% - 34px);
  }
  .main {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    overflow: hidden;
    outline: none;
  }
  .skip {
    position: absolute;
    left: -999px;
    top: 8px;
    z-index: 100;
    padding: 8px 12px;
    border-radius: 8px;
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .skip:focus {
    left: 8px;
  }
  .fatal {
    margin: auto;
    text-align: center;
    max-width: 420px;
    padding: 24px;
  }
  .fatal p {
    color: var(--tm-muted);
  }
  @media (max-width: 639px) {
    .app {
      padding-bottom: calc(var(--tm-bar-h) + var(--tm-safe-bottom));
    }
    /* Phones: an open chat is full screen, like messenger apps. */
    .app.chat-open {
      padding-bottom: 0;
    }
    .app.chat-open :global(.rail) {
      display: none;
    }
  }
</style>
