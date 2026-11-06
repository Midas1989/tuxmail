<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Account entry at the bottom of the rail (web client against `tuxmail serve`):
  who is signed in, "Edit profile", "Sign out". The desktop shell has no server
  session and the native app has #/native-settings, so neither shows it.
  Phones have no room in the bottom bar: there the same actions are the first
  entry of the settings list (My account).
-->
<script lang="ts">
  import UserRound from '@lucide/svelte/icons/user-round';
  import UserPen from '@lucide/svelte/icons/user-pen';
  import LogOut from '@lucide/svelte/icons/log-out';
  import Avatar from './common/Avatar.svelte';
  import ContextMenu, { type MenuItem } from './common/ContextMenu.svelte';
  import { app, security } from '../state';
  import { signOut } from '../accountActions';
  import { t } from '../i18n/index.svelte';

  let anchor = $state<{ x: number; y: number } | null>(null);

  const user = $derived(security.me?.user ?? null);
  const name = $derived(user ? user.displayName || user.username : '');
  // The second line must add something: the address, or else the login name.
  const detail = $derived(user ? user.email || (user.displayName ? user.username : '') : '');
  const label = $derived(name ? `${t('settings.pages.profile.menu')}: ${name}` : t('settings.pages.profile.menu'));

  const items: MenuItem[] = $derived([
    { id: 'profile', label: t('settings.pages.profile.edit'), icon: UserPen, run: () => app.openSettings('profile') },
    { id: 'signout', label: t('settings.pages.session.signOut'), icon: LogOut, run: signOut },
  ]);

  function toggle(e: MouseEvent) {
    if (anchor) {
      anchor = null;
      return;
    }
    // Beside the rail; ContextMenu opens it upwards from here, because the entry sits at the bottom.
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    anchor = { x: r.right + 8, y: r.bottom };
  }
</script>

{#if security.available}
  <div class="account">
    <button class="trigger" aria-haspopup="menu" aria-expanded={!!anchor} aria-label={label} title={label} data-testid="account-menu" onclick={toggle}>
      {#if user}
        <Avatar {name} seed={user.username} size={30} />
      {:else}
        <UserRound size={22} strokeWidth={1.8} aria-hidden="true" />
      {/if}
    </button>
  </div>
  {#if anchor}
    <ContextMenu x={anchor.x} y={anchor.y} {items} label={t('settings.pages.profile.menu')} width={260} onclose={() => (anchor = null)} header={user ? who : undefined} />
  {/if}
{/if}

{#snippet who()}
  <div class="who">
    <span class="name" title={name}>{name}</span>
    {#if detail}<span class="detail" title={detail}>{detail}</span>{/if}
  </div>
{/snippet}

<style>
  .trigger {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 64px;
    min-height: 44px;
    border: none;
    border-radius: 12px;
    background: transparent;
    color: var(--tm-rail-fg);
    transition: background var(--tm-dur);
  }
  .trigger:hover,
  .trigger[aria-expanded='true'] {
    background: rgb(255 255 255 / 0.08);
    color: var(--tm-rail-active);
  }
  .who {
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 4px 8px;
    min-width: 0;
  }
  .name,
  .detail {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .name {
    font-size: 13.5px;
    font-weight: 600;
    color: var(--tm-fg);
  }
  .detail {
    font-size: 12px;
    color: var(--tm-muted);
  }
  /* Phones reach this through the settings list; the bottom bar is full. */
  @media (max-width: 639px) {
    .account {
      display: none;
    }
  }
</style>
