<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Cloud from '@lucide/svelte/icons/cloud';
  import Lock from '@lucide/svelte/icons/lock';
  import FolderOpen from '@lucide/svelte/icons/folder-open';
  import { t } from '../i18n/index.svelte';
  import ScreenShell from './ScreenShell.svelte';

  const providers = [
    { id: 'nextcloud', name: 'Nextcloud', icon: Cloud, desc: 'files.nextcloudDesc' },
    { id: 'opencloud', name: 'OpenCloud', icon: Cloud, desc: 'files.opencloudDesc' },
    { id: 'vaultwarden', name: 'Vaultwarden Send', icon: Lock, desc: 'files.vaultwardenDesc' },
    { id: 'webdav', name: 'WebDAV / lokal', icon: FolderOpen, desc: 'files.webdavDesc' },
  ] as const;
</script>

<ScreenShell title={t('nav.files')} subtitle={t('files.placeholder')}>
  <div class="grid">
    {#each providers as p (p.id)}
      {@const Icon = p.icon}
      <article class="card">
        <span class="ic"><Icon size={22} /></span>
        <h2>{p.name}</h2>
        <p>{t(p.desc)}</p>
        <button class="btn" disabled title={t('common.soon')}>{t('files.connect')}</button>
      </article>
    {/each}
  </div>
</ScreenShell>

<style>
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
  .ic {
    display: inline-flex;
    padding: 10px;
    border-radius: 12px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
  }
  h2 {
    margin: 6px 0 0;
    font-size: 15px;
  }
  p {
    margin: 0 0 8px;
    font-size: 13px;
    color: var(--tm-muted);
    flex: 1;
  }
</style>
