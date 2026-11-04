<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Composer: pick files from Nextcloud/OpenCloud/WebDAV (KONZEPT 12.1 #1) -
  attach them (downloaded into uploads) or insert share links.
-->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { app, cloud } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { CloudEntry, CloudImportResult, CloudShare } from '../../types';
  import Modal from '../common/Modal.svelte';
  import CloudBrowser from './CloudBrowser.svelte';

  interface Props {
    onattach: (files: CloudImportResult[]) => void;
    onlinks?: (shares: CloudShare[]) => void;
    onclose: () => void;
  }
  let { onattach, onlinks, onclose }: Props = $props();

  let accountId = $state<number>(cloud.storageAccounts[0]?.id ?? 0);
  let path = $state('/');
  let selected = $state<CloudEntry[]>([]);
  let busy = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    void cloud.ensureLoaded().then(() => {
      if (!accountId && cloud.storageAccounts.length) accountId = cloud.storageAccounts[0].id;
    });
  });

  const account = $derived(cloud.account(accountId));

  function switchAccount(id: number) {
    accountId = id;
    path = '/';
    selected = [];
  }

  async function attach() {
    busy = true;
    error = null;
    try {
      const out: CloudImportResult[] = [];
      for (const e of selected) out.push(await app.api.importCloudFile(accountId, e.path));
      onattach(out);
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function links() {
    busy = true;
    error = null;
    try {
      const out: CloudShare[] = [];
      for (const e of selected) out.push(await app.api.createCloudShare({ accountId, path: e.path, generatePassword: true }));
      onlinks?.(out);
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={t('cloud.picker.title')} {onclose} width="720px">
  {#if cloud.storageAccounts.length === 0}
    <p class="empty">{t('cloud.picker.noAccounts')}</p>
  {:else}
    <div class="pick">
      {#if cloud.storageAccounts.length > 1}
        <label class="acc">
          <span class="sr-only">{t('cloud.picker.account')}</span>
          <select class="input" value={accountId} onchange={(e) => switchAccount(Number((e.currentTarget as HTMLSelectElement).value))}>
            {#each cloud.storageAccounts as a (a.id)}<option value={a.id}>{a.displayName}</option>{/each}
          </select>
        </label>
      {/if}
      {#if accountId}
        {#key accountId}
          <div class="scroll"><CloudBrowser {accountId} bind:path bind:selected mode="pick" compact /></div>
        {/key}
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </div>
  {/if}
  {#snippet footer()}
    <span class="sel">{t('cloud.picker.selected', { count: selected.length })}</span>
    <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
    {#if onlinks && account?.capabilities.share}
      <button class="btn" disabled={busy || selected.length === 0} onclick={links}>{t('cloud.picker.insertLinks')}</button>
    {/if}
    <button class="btn btn-primary" disabled={busy || selected.length === 0} onclick={attach}>
      {#if busy}<LoaderCircle size={15} class="spin" />{/if}
      {t('cloud.picker.attach')}
    </button>
  {/snippet}
</Modal>

<style>
  .pick {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .scroll {
    max-height: 52vh;
    overflow: auto;
  }
  .sel {
    margin-right: auto;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .empty,
  .error {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .error {
    color: var(--tm-danger);
  }
</style>
