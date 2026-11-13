<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Empty state of the cloud picker and the "Save to cloud" dialog: instead of only saying that no
  cloud account exists, connect Nextcloud (or OpenCloud / WebDAV) right here, without leaving the
  composer or the message. The dialog around it switches to the file browser as soon as the new
  account is loaded (CloudConnectForm reloads the cloud state).
-->
<script lang="ts">
  import { t } from '../../i18n/index.svelte';
  import type { CloudAccountProvider } from '../../types';
  import CloudConnectForm from './CloudConnectForm.svelte';
  import ProviderIcon from './ProviderIcon.svelte';

  const STORAGE: CloudAccountProvider[] = ['nextcloud', 'opencloud', 'webdav'];
  let provider = $state<CloudAccountProvider | null>(null);
</script>

{#if provider}
  <CloudConnectForm {provider} ondone={() => (provider = null)} oncancel={() => (provider = null)} />
{:else}
  <p class="empty">{t('cloud.picker.noAccounts')}</p>
  <div class="btns">
    {#each STORAGE as p (p)}
      <button type="button" class="btn" class:btn-primary={p === 'nextcloud'} onclick={() => (provider = p)}><ProviderIcon provider={p} /> {t(`cloud.provider.${p}`)}</button>
    {/each}
  </div>
{/if}

<style>
  .empty {
    margin: 0 0 12px;
    color: var(--tm-muted);
  }
  .btns {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
</style>
