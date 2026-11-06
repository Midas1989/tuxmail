<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> Administration (web client, administrators only; the page is
  hidden for everyone else, see settings/catalog.ts "admin"): users, invites,
  groups, the backup key and the audit log of the server. The server checks the admin role on
  every call; hiding the page is a convenience, not the protection.
-->
<script lang="ts">
  import { tick } from 'svelte';
  import { admin, app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import AdminUsers from './AdminUsers.svelte';
  import AdminInvites from './AdminInvites.svelte';
  import AdminGroups from './AdminGroups.svelte';
  import AdminBackup from './AdminBackup.svelte';
  import AdminAudit from './AdminAudit.svelte';
  import { ADMIN_TABS, type AdminTab } from '../../admin/tabs';

  let tab = $state<AdminTab>('users');
  let tabEls = $state<Record<string, HTMLButtonElement>>({});

  const client = $derived(admin.client);
  const me = $derived(admin.me);
  const notify = (text: string, kind: 'info' | 'error' = 'info') => app.toast(text, undefined, kind);

  async function onkeydown(e: KeyboardEvent) {
    const i = ADMIN_TABS.indexOf(tab);
    let next = -1;
    if (e.key === 'ArrowRight') next = (i + 1) % ADMIN_TABS.length;
    else if (e.key === 'ArrowLeft') next = (i - 1 + ADMIN_TABS.length) % ADMIN_TABS.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = ADMIN_TABS.length - 1;
    else return;
    e.preventDefault();
    tab = ADMIN_TABS[next];
    await tick();
    tabEls[tab]?.focus();
  }
</script>

<section class="card adm" id="set-admin" aria-labelledby="set-admin-h">
  <h2 id="set-admin-h">{t('admin.title')}</h2>
  <p class="intro">{t('admin.intro')}</p>

  {#if client && me && admin.isAdmin}
    {#if me.mfaSetupRequired}
      <p class="notice" role="alert" data-testid="admin-mfa">{t('admin.errors.mfa_setup_required')}</p>
    {:else}
      <div class="tabs" role="tablist" aria-label={t('admin.tabs.label')} tabindex="-1" {onkeydown}>
        {#each ADMIN_TABS as id (id)}
          <button
            bind:this={tabEls[id]}
            role="tab"
            id="admin-tab-{id}"
            class="tab"
            aria-selected={tab === id}
            aria-controls="admin-panel-{id}"
            tabindex={tab === id ? 0 : -1}
            onclick={() => (tab = id)}>{t(`admin.tabs.${id}`)}</button
          >
        {/each}
      </div>
      <div class="panel" role="tabpanel" id="admin-panel-{tab}" aria-labelledby="admin-tab-{tab}" tabindex="-1">
        {#key tab}
          {#if tab === 'users'}<AdminUsers {client} {me} config={admin.config} {notify} />
          {:else if tab === 'invites'}<AdminInvites {client} {me} config={admin.config} {notify} />
          {:else if tab === 'groups'}<AdminGroups {client} {me} config={admin.config} {notify} />
          {:else if tab === 'backup'}<AdminBackup {client} {me} config={admin.config} {notify} />
          {:else}<AdminAudit {client} {me} config={admin.config} {notify} />{/if}
        {/key}
      </div>
    {/if}
  {/if}
</section>

<style>
  .tabs {
    display: flex;
    gap: 2px;
    margin-bottom: 14px;
    border-bottom: 1px solid var(--tm-line);
    overflow-x: auto;
  }
  .tab {
    flex: none;
    height: 36px;
    padding: 0 14px;
    border: none;
    border-bottom: 2px solid transparent;
    background: none;
    color: var(--tm-muted);
    font: inherit;
    font-size: 13.5px;
    font-weight: 500;
    cursor: pointer;
  }
  .tab:hover {
    color: var(--tm-fg);
  }
  .tab[aria-selected='true'] {
    color: var(--tm-accent-text);
    border-bottom-color: var(--tm-accent);
    font-weight: 600;
  }
  .tab:focus-visible {
    outline: 2px solid var(--tm-focus);
    outline-offset: -2px;
  }
  .panel {
    outline: none;
  }
  .notice {
    margin: 0;
    padding: 10px 12px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-warn) 14%, transparent);
    font-size: 13.5px;
  }
</style>
