<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Files (KONZEPT 12): provider switcher, breadcrumb browser with upload,
  download, new folder, rename, delete and share links, Vaultwarden Sends and
  "My shares" with expiry and revoke.
-->
<script lang="ts">
  import { connection } from '../state/connection.svelte';
  import Upload from '@lucide/svelte/icons/upload';
  import FolderPlus from '@lucide/svelte/icons/folder-plus';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Download from '@lucide/svelte/icons/download';
  import Share2 from '@lucide/svelte/icons/share-2';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Trash from '@lucide/svelte/icons/trash';
  import Plus from '@lucide/svelte/icons/plus';
  import Link from '@lucide/svelte/icons/link';
  import FileText from '@lucide/svelte/icons/file-text';
  import LogOut from '@lucide/svelte/icons/log-out';
  import { app, cloud } from '../state';
  import { joinPath, parentPath } from '../state/cloud.svelte';
  import { formatSize, t } from '../i18n/index.svelte';
  import type { CloudAccount, CloudAccountProvider, CloudEntry, CloudQuota } from '../types';
  import ScreenShell from './ScreenShell.svelte';
  import Modal from '../components/common/Modal.svelte';
  import ConfirmDialog from '../components/common/ConfirmDialog.svelte';
  import CloudBrowser from '../components/cloud/CloudBrowser.svelte';
  import CloudConnectForm, { CLOUD_PROVIDERS } from '../components/cloud/CloudConnectForm.svelte';
  import CloudShareDialog from '../components/cloud/CloudShareDialog.svelte';
  import CloudSharesList from '../components/cloud/CloudSharesList.svelte';
  import ProviderIcon from '../components/cloud/ProviderIcon.svelte';

  const TAB_KEY = 'tuxmail.files.tab.v1';
  function savedTab(): number | 'shares' | null {
    try {
      const v = localStorage.getItem(TAB_KEY);
      return v === 'shares' ? 'shares' : v ? Number(v) : null;
    } catch {
      return null;
    }
  }

  let tab = $state<number | 'shares' | null>(savedTab());
  let path = $state('/');
  let reload = $state(0);
  let sharesReload = $state(0);
  let connecting = $state<CloudAccountProvider | null>(null);
  let sharing = $state<{ account: CloudAccount; entry?: CloudEntry; file?: File } | null>(null);
  let deleting = $state<CloudEntry | null>(null);
  let removing = $state<CloudAccount | null>(null);
  let quota = $state<CloudQuota | null>(null);
  let uploading = $state(0);
  let fileInput = $state<HTMLInputElement>();
  let sendInput = $state<HTMLInputElement>();
  let dragOver = $state(false);

  $effect(() => {
    void cloud.load();
  });

  // Pick a valid tab once the accounts are known.
  $effect(() => {
    if (!cloud.loaded) return;
    if (tab === 'shares') return;
    if (tab == null || !cloud.account(tab)) tab = cloud.accounts[0]?.id ?? null;
  });

  $effect(() => {
    try {
      if (tab != null) localStorage.setItem(TAB_KEY, String(tab));
    } catch {
      /* ignore */
    }
  });

  const account = $derived(typeof tab === 'number' ? cloud.account(tab) : undefined);

  $effect(() => {
    const acc = account;
    void reload;
    quota = null;
    if (acc?.capabilities.files) {
      app.api
        .cloudQuota(acc.id)
        .then((q) => (quota = q))
        .catch(() => (quota = null));
    }
  });

  function select(next: number | 'shares') {
    if (next !== tab) path = '/';
    tab = next;
  }

  async function uploadFiles(files: FileList | File[] | null) {
    if (!files || !account) return;
    const acc = account;
    for (const f of files) {
      uploading++;
      try {
        await app.api.uploadCloudFile(acc.id, path, f, f.name);
        app.toast(t('files.uploaded', { name: f.name }));
      } catch (err) {
        app.toast(t('files.uploadFailed', { name: f.name, error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
      } finally {
        uploading--;
      }
    }
    reload++;
  }

  async function newFolder() {
    if (!account) return;
    const name = prompt(t('files.newFolderName'));
    if (!name?.trim()) return;
    try {
      await app.api.createCloudFolder(account.id, joinPath(path, name.trim()));
      reload++;
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  async function download(e: CloudEntry) {
    if (!account) return;
    try {
      const blob = await app.api.downloadCloudFile(account.id, e.path);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = e.name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 30_000);
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  async function rename(e: CloudEntry) {
    if (!account) return;
    const name = prompt(t('files.renamePrompt'), e.name);
    if (!name?.trim() || name === e.name) return;
    try {
      await app.api.moveCloudFile(account.id, e.path, joinPath(parentPath(e.path), name.trim()));
      reload++;
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  async function remove(e: CloudEntry) {
    deleting = null;
    if (!account) return;
    try {
      await app.api.deleteCloudFile(account.id, e.path);
      app.toast(t('files.deleted', { name: e.name }));
      reload++;
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  async function removeAccount(acc: CloudAccount) {
    removing = null;
    try {
      await app.api.deleteCloudAccount(acc.id);
      await cloud.load();
      tab = cloud.accounts[0]?.id ?? null;
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  function connected(acc: CloudAccount) {
    connecting = null;
    void cloud.load().then(() => select(acc.id));
  }

  const isSpaceRoot = $derived(account?.provider === 'opencloud' && path === '/');
  const quotaText = $derived(
    quota && quota.used >= 0 ? (quota.available >= 0 ? t('files.quota', { used: formatSize(quota.used), total: formatSize(quota.used + quota.available) }) : t('files.quotaUnlimited', { used: formatSize(quota.used) })) : '',
  );
  const quotaPct = $derived(quota && quota.used >= 0 && quota.available > 0 ? Math.min(100, (quota.used / (quota.used + quota.available)) * 100) : 0);
</script>

<ScreenShell title={t('nav.files')} subtitle={t('files.subtitle')}>
  {#snippet actions()}
    <button class="btn" onclick={() => (connecting = 'nextcloud')}><Plus size={15} /> {t('files.addAccount')}</button>
  {/snippet}

  {#if connection.offline}
    <p class="offline-note" role="status" data-testid="files-offline">{t('connection.filesOffline')}</p>
  {:else if cloud.loaded && cloud.accounts.length === 0}
    <p class="lead">{t('files.noAccounts')}</p>
    <div class="grid">
      {#each CLOUD_PROVIDERS as p (p)}
        <article class="card">
          <ProviderIcon provider={p} size={22} />
          <h2>{t(`cloud.provider.${p}`)}</h2>
          <p>{t(`files.${p}Desc`)}</p>
          <button class="btn btn-primary" onclick={() => (connecting = p)}>{t('files.connect')}</button>
        </article>
      {/each}
    </div>
  {:else}
    <div class="tabs" role="tablist" aria-label={t('files.providers')}>
      {#each cloud.accounts as a (a.id)}
        <button role="tab" aria-selected={tab === a.id} class="tab" class:on={tab === a.id} onclick={() => select(a.id)} title={a.server}>
          <ProviderIcon provider={a.provider} />
          <span>{a.displayName}</span>
        </button>
      {/each}
      <button role="tab" aria-selected={tab === 'shares'} class="tab" class:on={tab === 'shares'} onclick={() => select('shares')}>
        <Link size={16} />
        <span>{t('files.myShares')}</span>
      </button>
    </div>

    {#if tab === 'shares'}
      <CloudSharesList reloadKey={sharesReload} />
    {:else if account?.capabilities.files}
      {@const acc = account}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="pane"
        class:drag={dragOver}
        ondragover={(e) => {
          e.preventDefault();
          dragOver = !isSpaceRoot;
        }}
        ondragleave={() => (dragOver = false)}
        ondrop={(e) => {
          e.preventDefault();
          dragOver = false;
          if (!isSpaceRoot) void uploadFiles(e.dataTransfer?.files ?? null);
        }}
      >
        <div class="toolbar">
          <button class="btn btn-primary" disabled={isSpaceRoot} onclick={() => fileInput?.click()}><Upload size={15} /> {uploading ? t('files.uploading', { count: uploading }) : t('files.upload')}</button>
          <input type="file" multiple hidden bind:this={fileInput} onchange={(e) => uploadFiles((e.currentTarget as HTMLInputElement).files)} />
          <button class="btn" disabled={isSpaceRoot} onclick={newFolder}><FolderPlus size={15} /> {t('files.newFolder')}</button>
          <button class="icon-btn" onclick={() => reload++} aria-label={t('files.refresh')} title={t('files.refresh')}><RefreshCw size={16} /></button>
          <span class="spacer"></span>
          {#if quotaText}
            <span class="quota" title={quotaText}>
              <span class="bar"><span style:width="{quotaPct}%"></span></span>
              {quotaText}
            </span>
          {/if}
          <button class="icon-btn" onclick={() => (removing = acc)} aria-label={t('files.removeAccount')} title={t('files.removeAccount')}><LogOut size={16} /></button>
        </div>
        {#key acc.id}
          <CloudBrowser accountId={acc.id} bind:path reloadKey={reload} onfile={download}>
            {#snippet actions(e)}
              <span class="row-acts">
                {#if !e.dir}
                  <button class="icon-btn" onclick={() => download(e)} aria-label={t('files.downloadFile', { name: e.name })} title={t('files.download')}><Download size={15} /></button>
                {/if}
                {#if acc.capabilities.share && !(isSpaceRoot && e.dir)}
                  <button class="icon-btn" onclick={() => (sharing = { account: acc, entry: e })} aria-label={t('files.shareFile', { name: e.name })} title={t('files.share')}><Share2 size={15} /></button>
                {/if}
                {#if !isSpaceRoot}
                  <button class="icon-btn" onclick={() => rename(e)} aria-label={t('files.renameFile', { name: e.name })} title={t('files.rename')}><Pencil size={15} /></button>
                  <button class="icon-btn danger" onclick={() => (deleting = e)} aria-label={t('files.deleteFile', { name: e.name })} title={t('common.action.delete')}><Trash size={15} /></button>
                {/if}
              </span>
            {/snippet}
          </CloudBrowser>
        {/key}
        {#if acc.dav}
          <details class="dav">
            <summary>{t('files.dav.title')}</summary>
            <p>{t('files.dav.hint')}</p>
            <dl>
              <dt>{t('files.dav.caldav')}</dt>
              <dd><code>{acc.dav.caldav}</code></dd>
              <dt>{t('files.dav.carddav')}</dt>
              <dd><code>{acc.dav.carddav}</code></dd>
            </dl>
          </details>
        {/if}
      </div>
    {:else if account?.capabilities.send}
      {@const acc = account}
      <div class="send">
        <div class="send-card">
          <ProviderIcon provider="vaultwarden" size={28} />
          <div>
            <h2>{t('files.send.title')}</h2>
            <p>{t('files.send.hint')}</p>
          </div>
        </div>
        <div class="toolbar">
          <button class="btn btn-primary" onclick={() => sendInput?.click()}><Upload size={15} /> {t('files.send.file')}</button>
          <input
            type="file"
            hidden
            bind:this={sendInput}
            onchange={(e) => {
              const f = (e.currentTarget as HTMLInputElement).files?.[0];
              if (f) sharing = { account: acc, file: f };
              (e.currentTarget as HTMLInputElement).value = '';
            }}
          />
          <button class="btn" onclick={() => (sharing = { account: acc })}><FileText size={15} /> {t('files.send.text')}</button>
          <span class="spacer"></span>
          <button class="icon-btn" onclick={() => (removing = acc)} aria-label={t('files.removeAccount')} title={t('files.removeAccount')}><LogOut size={16} /></button>
        </div>
        <CloudSharesList reloadKey={sharesReload} accountId={acc.id} />
      </div>
    {/if}
  {/if}
</ScreenShell>

{#if connecting}
  <Modal title={t('cloud.connect.title')} onclose={() => (connecting = null)} width="560px">
    <CloudConnectForm provider={connecting} ondone={connected} oncancel={() => (connecting = null)} />
  </Modal>
{/if}
{#if sharing}
  <CloudShareDialog account={sharing.account} entry={sharing.entry} file={sharing.file} onclose={() => (sharing = null)} oncreated={() => sharesReload++} />
{/if}
{#if deleting}
  {@const d = deleting}
  <ConfirmDialog
    title={t('common.action.delete')}
    message={t('files.deleteConfirm', { name: d.name })}
    confirmLabel={t('common.action.delete')}
    danger
    onconfirm={() => remove(d)}
    oncancel={() => (deleting = null)}
  />
{/if}
{#if removing}
  {@const r = removing}
  <ConfirmDialog
    title={t('files.removeAccount')}
    message={t('files.removeConfirm', { name: r.displayName })}
    confirmLabel={t('files.removeAccount')}
    danger
    onconfirm={() => removeAccount(r)}
    oncancel={() => (removing = null)}
  />
{/if}

<style>
  .offline-note {
    margin: 16px;
    color: var(--tm-muted);
  }
  .lead {
    margin: 0 0 14px;
    color: var(--tm-muted);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: 14px;
  }
  .card {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 6px;
    padding: 18px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .card h2 {
    margin: 6px 0 0;
    font-size: 15px;
  }
  .card p {
    margin: 0 0 8px;
    font-size: 13px;
    color: var(--tm-muted);
    flex: 1;
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 14px;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 7px 12px;
    border-radius: 999px;
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    color: var(--tm-fg);
    font-size: 13px;
    max-width: 280px;
  }
  .tab span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tab.on {
    border-color: var(--tm-accent);
    background: var(--tm-accent-soft);
    font-weight: 600;
  }
  .pane,
  .send {
    display: flex;
    flex-direction: column;
    gap: 12px;
    border-radius: var(--tm-radius);
  }
  .pane.drag {
    outline: 2px dashed var(--tm-accent);
    outline-offset: 6px;
  }
  .toolbar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .spacer {
    flex: 1;
  }
  .quota {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .quota .bar {
    width: 90px;
    height: 6px;
    border-radius: 3px;
    background: var(--tm-surface-3);
    overflow: hidden;
  }
  .quota .bar span {
    display: block;
    height: 100%;
    background: var(--tm-accent);
  }
  .row-acts {
    display: inline-flex;
    gap: 2px;
  }
  .danger {
    color: var(--tm-danger);
  }
  .dav {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .dav summary {
    cursor: pointer;
    font-weight: 600;
  }
  .dav dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 12px;
  }
  .dav dd {
    margin: 0;
    word-break: break-all;
  }
  .send-card {
    display: flex;
    gap: 14px;
    align-items: center;
    padding: 16px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .send-card h2 {
    margin: 0;
    font-size: 15px;
  }
  .send-card p {
    margin: 2px 0 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
</style>
