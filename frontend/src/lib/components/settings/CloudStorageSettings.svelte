<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Cloud & Dateien: connected cloud accounts (Nextcloud, OpenCloud,
  WebDAV, Vaultwarden Send) with connect/disconnect, and all share links
  with expiry and revoke. Browsing files stays in the Files screen.
-->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import LogOut from '@lucide/svelte/icons/log-out';
  import FolderOpen from '@lucide/svelte/icons/folder-open';
  import { app, cloud } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { CloudAccount, CloudAccountProvider } from '../../types';
  import Modal from '../common/Modal.svelte';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import CloudConnectForm, { CLOUD_PROVIDERS } from '../cloud/CloudConnectForm.svelte';
  import CloudSharesList from '../cloud/CloudSharesList.svelte';
  import ProviderIcon from '../cloud/ProviderIcon.svelte';

  interface Props {
    part: 'storage' | 'shares';
  }
  let { part }: Props = $props();

  let connecting = $state<CloudAccountProvider | null>(null);
  let removing = $state<CloudAccount | null>(null);

  $effect(() => {
    if (part === 'storage') void cloud.load();
  });

  async function removeAccount(acc: CloudAccount) {
    removing = null;
    try {
      await app.api.deleteCloudAccount(acc.id);
      await cloud.load();
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  function connected() {
    connecting = null;
    void cloud.load();
  }
</script>

{#if part === 'storage'}
  <section class="card" id="set-cloud" aria-labelledby="set-cloud-h">
    <div class="card-head">
      <h2 id="set-cloud-h">{t('settings.pages.cloud.sections.storage')}</h2>
      <button class="btn" onclick={() => app.navigate('files')}><FolderOpen size={14} /> {t('settings.pages.cloud.openFiles')}</button>
    </div>
    <p class="intro">{t('settings.pages.cloud.intro')}</p>
    {#if cloud.accounts.length}
      <ul class="rows">
        {#each cloud.accounts as a (a.id)}
          <li>
            <ProviderIcon provider={a.provider} size={18} />
            <span class="acc">
              <span class="nm">{a.displayName}</span>
              <span class="sub">{t(`cloud.provider.${a.provider}`)} · {a.server}{#if a.uploadDir}{' · '}{t('settings.pages.cloud.uploadDir', { dir: a.uploadDir })}{/if}</span>
            </span>
            <button class="icon-btn" onclick={() => (removing = a)} aria-label={t('files.removeAccount')} title={t('files.removeAccount')}><LogOut size={16} /></button>
          </li>
        {/each}
      </ul>
    {:else if cloud.loaded}
      <p class="sub">{t('settings.pages.cloud.empty')}</p>
    {/if}
    <div class="connect">
      {#each CLOUD_PROVIDERS as p (p)}
        <button class="btn" onclick={() => (connecting = p)}><Plus size={14} /> <ProviderIcon provider={p} size={15} /> {t(`cloud.provider.${p}`)}</button>
      {/each}
    </div>
  </section>
{:else}
  <section class="card" id="set-shares" aria-labelledby="set-shares-h">
    <h2 id="set-shares-h">{t('settings.pages.cloud.sections.shares')}</h2>
    <CloudSharesList />
  </section>
{/if}

{#if connecting}
  <Modal title={t('cloud.connect.title')} onclose={() => (connecting = null)} width="560px">
    <CloudConnectForm provider={connecting} ondone={connected} oncancel={() => (connecting = null)} />
  </Modal>
{/if}
{#if removing}
  {@const r = removing}
  <ConfirmDialog title={t('files.removeAccount')} message={t('files.removeConfirm', { name: r.displayName })} confirmLabel={t('files.removeAccount')} danger onconfirm={() => removeAccount(r)} oncancel={() => (removing = null)} />
{/if}

<style>
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
  }
  .card-head h2 {
    margin: 0;
  }
  .rows {
    list-style: none;
    margin: 0 0 12px;
    padding: 0;
  }
  .rows li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 0;
    border-top: 1px solid var(--tm-line);
  }
  .acc {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .nm {
    font-weight: 600;
  }
  .sub {
    margin: 0 0 12px;
    font-size: 12.5px;
    color: var(--tm-muted);
    overflow-wrap: anywhere;
  }
  .acc .sub {
    margin: 0;
  }
  .connect {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
</style>
