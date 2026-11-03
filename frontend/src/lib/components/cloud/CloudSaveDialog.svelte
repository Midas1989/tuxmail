<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  "Save to Nextcloud/OpenCloud..." for attachments of mails and chats with a
  target folder picker (KONZEPT 12.1 #2). Opened through cloud.openSave().
-->
<script lang="ts">
  import FolderPlus from '@lucide/svelte/icons/folder-plus';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { app, cloud } from '../../state';
  import { joinPath, type CloudSaveSource } from '../../state/cloud.svelte';
  import { t } from '../../i18n/index.svelte';
  import Modal from '../common/Modal.svelte';
  import CloudBrowser from './CloudBrowser.svelte';

  interface Props {
    source: CloudSaveSource;
    onclose: () => void;
  }
  let { source, onclose }: Props = $props();

  const LAST_KEY = 'tuxmail.cloud.lastSave.v1';
  function last(): { accountId: number; path: string } | null {
    try {
      return JSON.parse(localStorage.getItem(LAST_KEY) ?? 'null');
    } catch {
      return null;
    }
  }
  const remembered = last();
  const firstAccount = remembered && cloud.account(remembered.accountId)?.capabilities.files ? remembered.accountId : (cloud.storageAccounts[0]?.id ?? 0);
  let accountId = $state<number>(firstAccount);
  let path = $state(remembered?.accountId === firstAccount ? remembered.path : '/');
  // svelte-ignore state_referenced_locally
  let filename = $state(source.filename);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let reload = $state(0);

  $effect(() => {
    void cloud.ensureLoaded().then(() => {
      if (!accountId && cloud.storageAccounts.length) accountId = cloud.storageAccounts[0].id;
    });
  });

  async function newFolder() {
    const name = prompt(t('files.newFolderName'));
    if (!name?.trim()) return;
    try {
      const e = await app.api.createCloudFolder(accountId, joinPath(path, name.trim()));
      path = e.path;
      reload++;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function save() {
    busy = true;
    error = null;
    try {
      const e = await app.api.saveAttachmentToCloud({
        accountId,
        dir: path,
        messageId: source.messageId,
        index: source.index,
        uploadToken: source.uploadToken,
        filename: filename.trim() || undefined,
      });
      try {
        localStorage.setItem(LAST_KEY, JSON.stringify({ accountId, path }));
      } catch {
        /* ignore */
      }
      app.toast(t('cloud.save.saved', { path: e.path }));
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={t('cloud.save.title', { name: source.filename })} {onclose} width="640px">
  {#if cloud.storageAccounts.length === 0}
    <p class="muted">{t('cloud.picker.noAccounts')}</p>
  {:else}
    <div class="body">
      <div class="row">
        {#if cloud.storageAccounts.length > 1}
          <select class="input" value={accountId} aria-label={t('cloud.picker.account')} onchange={(e) => ((accountId = Number((e.currentTarget as HTMLSelectElement).value)), (path = '/'))}>
            {#each cloud.storageAccounts as a (a.id)}<option value={a.id}>{a.displayName}</option>{/each}
          </select>
        {/if}
        <button class="btn" onclick={newFolder}><FolderPlus size={15} /> {t('files.newFolder')}</button>
      </div>
      {#key accountId}
        <div class="scroll"><CloudBrowser {accountId} bind:path mode="folders" compact reloadKey={reload} /></div>
      {/key}
      <label>
        <span class="label">{t('cloud.save.filename')}</span>
        <input class="input" bind:value={filename} />
      </label>
      <p class="muted">{t('cloud.save.target', { path })}</p>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </div>
  {/if}
  {#snippet footer()}
    <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" disabled={busy || !accountId} onclick={save} data-autofocus>
      {#if busy}<LoaderCircle size={15} class="spin" />{/if}
      {t('cloud.save.action')}
    </button>
  {/snippet}
</Modal>

<style>
  .body {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .row select {
    flex: 1;
  }
  .scroll {
    max-height: 40vh;
    overflow: auto;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .label {
    font-size: 12.5px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .muted {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .error {
    margin: 0;
    font-size: 13px;
    color: var(--tm-danger);
  }
</style>
