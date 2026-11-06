<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Sicherheit & Datenschutz: master-password vault (portable
  desktop), app lock and app settings of the Android app (#/native-settings)
  and links to the privacy defaults that live with their features.
-->
<script lang="ts">
  import Lock from '@lucide/svelte/icons/lock';
  import LockOpen from '@lucide/svelte/icons/lock-open';
  import Smartphone from '@lucide/svelte/icons/smartphone';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import LogOut from '@lucide/svelte/icons/log-out';
  import { app, security } from '../../state';
  import { isNativeShell } from '../../platform';
  import { signOutWeb } from '../../session';
  import PasskeyCard from './PasskeyCard.svelte';
  import { t, type MessageKey } from '../../i18n/index.svelte';
  import type { SettingsCategoryId } from '../../settings/catalog';
  import TwoFactorCard from './TwoFactorCard.svelte';
  import PasswordSessionsCard from './PasswordSessionsCard.svelte';

  interface Props {
    part: 'vault' | 'applock' | 'app' | 'privacy';
  }
  let { part }: Props = $props();

  const links: { label: MessageKey; category: SettingsCategoryId; section: string }[] = [
    { label: 'settings.pages.privacy.remote', category: 'mail', section: 'remote' },
    { label: 'settings.pages.privacy.notifications', category: 'notifications', section: 'notifications' },
    { label: 'settings.pages.privacy.ai', category: 'ai', section: 'assistant' },
  ];

  // Web client served by `tuxmail serve` (cookie session): the only place to end the session from the SPA (NG-07).
  // The native app has its own sign-out (#/native-settings); the desktop shell has no server session.
  const canSignOut = app.api.kind === 'http' && !isNativeShell();

  async function signOut() {
    try {
      await signOutWeb({ api: app.api });
    } catch (err) {
      app.toast(t('settings.pages.session.failed', { error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
    }
  }

  function openNative() {
    location.hash = '#/native-settings';
  }
</script>

{#if part === 'vault' && app.vault}
  {@const v = app.vault}
  <section class="card" id="set-vault" aria-labelledby="set-vault-h">
    <h2 id="set-vault-h">{t('settings.pages.security.sections.vault')}</h2>
    <p class="intro">{t('settings.pages.vault.intro')}</p>
    <p class="state" class:on={!v.locked && v.initialized}>
      {#if v.locked}<Lock size={14} />{:else}<LockOpen size={14} />{/if}
      {!v.initialized ? t('settings.pages.vault.stateNew') : v.locked ? t('settings.pages.vault.stateLocked') : t('settings.pages.vault.stateUnlocked')}
    </p>
  </section>
{:else if part === 'applock' || part === 'app'}
  <section class="card" id={part === 'app' ? 'set-native' : 'set-applock'} aria-labelledby="set-{part}-h">
    <h2 id="set-{part}-h">{part === 'app' ? t('native.settings.title') : t('native.settings.appLock')}</h2>
    <p class="intro">{part === 'app' ? t('settings.pages.app.intro') : t('settings.pages.applock.intro')}</p>
    <button class="btn" onclick={openNative}><Smartphone size={14} /> {part === 'app' ? t('settings.pages.app.open') : t('settings.pages.applock.open')}</button>
  </section>
{:else if part === 'privacy'}
  <section class="card" id="set-privacy" aria-labelledby="set-privacy-h">
    <h2 id="set-privacy-h">{t('settings.pages.security.sections.privacy')}</h2>
    <p class="intro">{t('settings.pages.privacy.intro')}</p>
    <ul class="links">
      {#each links as l (l.label)}
        <li>
          <button class="link-row" onclick={() => app.openSettings(l.category, l.section)}>
            <span>{t(l.label)}</span>
            <span class="where">{t(`settings.nav.${l.category}` as MessageKey)}</span>
            <ChevronRight size={15} />
          </button>
        </li>
      {/each}
    </ul>
  </section>
  {#if security.available}
    <TwoFactorCard />
    <PasskeyCard />
    <PasswordSessionsCard />
  {/if}
  {#if canSignOut}
    <section class="card" id="set-session" aria-labelledby="set-session-h">
      <h2 id="set-session-h">{t('settings.pages.session.title')}</h2>
      <p class="intro">{t('settings.pages.session.intro')}</p>
      <button class="btn" onclick={() => void signOut()}><LogOut size={14} /> {t('settings.pages.session.signOut')}</button>
    </section>
  {/if}
{/if}

<style>
  .state {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    padding: 3px 10px;
    border-radius: 99px;
    font-size: 12.5px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
  }
  .state.on {
    background: color-mix(in srgb, var(--tm-success) 16%, var(--tm-surface));
    color: var(--tm-success);
  }
  .links {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .links li {
    border-top: 1px solid var(--tm-line);
  }
  .link-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 10px 4px;
    background: none;
    border: none;
    border-radius: 8px;
    font: inherit;
    font-size: 13.5px;
    color: inherit;
    text-align: left;
    cursor: pointer;
  }
  .link-row span:first-child {
    flex: 1;
    min-width: 0;
  }
  .link-row:hover,
  .link-row:focus-visible {
    background: var(--tm-surface-2);
  }
  .where {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
  }
</style>
