<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> account -> folders: which folder is Sent, Drafts, Trash, Junk,
  Archive, Templates (detected by the server, overridable), behaviour
  (sent copies, trash, archive) and the sent/drafts folder per identity.
-->
<script lang="ts">
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
  import FolderPlus from '@lucide/svelte/icons/folder-plus';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { ArchiveMode, AssignableRole, FolderBehaviour, FolderConfig, FolderRoleEntry, TrashMode } from '../../types';
  import { badgeOf, folderOptions, folderSettingsTarget, missingRoles } from '../../folderconfig';

  interface Props {
    /** Fixed account (account edit dialog): no account picker, no card frame. */
    fixedAccountId?: number;
  }
  let { fixedAccountId }: Props = $props();
  const embedded = $derived(!!fixedAccountId);

  let accountId = $state(folderSettingsTarget.accountId || app.mailAccounts[0]?.id || 0);
  folderSettingsTarget.accountId = 0;
  $effect.pre(() => {
    if (fixedAccountId) accountId = fixedAccountId;
  });
  let cfg = $state<FolderConfig | null>(null);
  let busy = $state(false);

  const options = $derived(folderOptions(app.folders[accountId] ?? []));
  const missing = $derived(cfg ? missingRoles(cfg) : []);
  const year = new Date().getFullYear();
  const month = String(new Date().getMonth() + 1).padStart(2, '0');

  async function load(id: number) {
    if (!id) return;
    try {
      cfg = await app.api.getFolderConfig(id);
    } catch (err) {
      cfg = null;
      app.toast(String((err as Error).message ?? err), undefined, 'error');
    }
  }
  $effect(() => {
    void load(accountId);
  });

  async function apply(p: Promise<FolderConfig>, toast = true) {
    busy = true;
    try {
      cfg = await p;
      await app.reloadFolders(accountId);
      if (toast) app.toast(t('settings.folders.saved'));
    } catch (err) {
      app.toast(String((err as Error).message ?? err), undefined, 'error');
    } finally {
      busy = false;
    }
  }

  const roleLabel = (r: AssignableRole) => t(`mail.folder.role.${r}`);
  const folderName = (id?: number) => (id ? (options.find((o) => o.id === id)?.label ?? '') : '');

  function setRole(e: FolderRoleEntry, value: string) {
    void apply(app.api.setFolderRole(accountId, e.role, Number(value) || 0));
  }
  function createFolder(r: AssignableRole) {
    void apply(app.api.createRoleFolder(accountId, r, t(`settings.folders.defaultName.${r}`)));
  }
  function saveBehaviour(patch: Partial<FolderBehaviour>) {
    if (!cfg) return;
    void apply(app.api.saveFolderBehaviour(accountId, { ...cfg.behaviour, ...patch }));
  }
  function setSaveSent(mode: 'auto' | 'on' | 'off') {
    saveBehaviour({ saveSentAuto: mode === 'auto', saveSent: mode === 'on' });
  }
  const saveSentMode = $derived(cfg ? (cfg.behaviour.saveSentAuto ? 'auto' : cfg.behaviour.saveSent ? 'on' : 'off') : 'auto');
</script>

<section class="card wide-card" class:embedded id={embedded ? undefined : 'set-folders'} aria-labelledby="set-folders-h">
  <div class="card-head">
    <h2 id="set-folders-h">{t('settings.folders.title')}</h2>
    {#if app.mailAccounts.length && !embedded}
      <select class="input acc" aria-label={t('settings.folders.account')} bind:value={accountId}>
        {#each app.mailAccounts as a (a.id)}<option value={a.id}>{a.displayName}</option>{/each}
      </select>
    {/if}
  </div>
  <p class="hint">{t('settings.folders.intro')}</p>

  {#if !app.mailAccounts.length}
    <p class="hint">{t('settings.folders.noAccounts')}</p>
  {:else if cfg}
    <div class="table" role="table" aria-label={t('settings.folders.title')}>
      <div class="row head" role="row">
        <span role="columnheader">{t('settings.folders.role')}</span>
        <span role="columnheader">{t('settings.folders.folder')}</span>
        <span role="columnheader">{t('settings.folders.source')}</span>
      </div>
      {#each cfg.roles as e (e.role)}
        {@const badge = badgeOf(e)}
        <div class="row" role="row">
          <span class="role" role="cell">{roleLabel(e.role)}</span>
          <span role="cell">
            <select class="input" aria-label={roleLabel(e.role)} value={e.overridden && !e.unresolved ? String(e.folderId ?? '') : ''} disabled={busy} onchange={(ev) => setRole(e, (ev.currentTarget as HTMLSelectElement).value)}>
              <option value="">{e.serverFolderId ? folderName(e.serverFolderId) : t('settings.folders.none')}</option>
              {#each options as o (o.id)}<option value={String(o.id)}>{o.label}</option>{/each}
            </select>
          </span>
          <span role="cell" class="src">
            <span class="badge {badge}">{t(`settings.folders.badge.${badge}`)}</span>
            {#if badge === 'none' && cfg.canCreate}
              <button class="btn btn-ghost small" disabled={busy} onclick={() => createFolder(e.role)}><FolderPlus size={13} /> {t('settings.folders.createNamed', { name: t(`settings.folders.defaultName.${e.role}`) })}</button>
            {/if}
          </span>
        </div>
        {#if badge === 'unresolved'}
          <p class="hint warn">{t('settings.folders.unresolvedHint', { folder: e.overrideRemoteId ?? '' })}</p>
        {/if}
      {/each}
    </div>
    <div class="actions">
      <button class="btn" disabled={busy} onclick={() => void apply(app.api.resetFolderRoles(accountId))}><RotateCcw size={14} /> {t('settings.folders.detect')}</button>
      {#if missing.length && cfg.canCreate}
        <span class="hint">{missing.map(roleLabel).join(', ')}: {t('settings.folders.badge.none')}</span>
      {/if}
    </div>

    <h3>{t('settings.folders.behaviour')}</h3>
    <div class="field">
      <span class="label" id="fs-sent">{t('settings.folders.saveSent')}</span>
      <div class="seg" role="radiogroup" aria-labelledby="fs-sent">
        {#each ['auto', 'on', 'off'] as const as m (m)}
          <button role="radio" aria-checked={saveSentMode === m} disabled={busy} onclick={() => setSaveSent(m)}>{t(`settings.folders.saveSentMode.${m}`)}</button>
        {/each}
      </div>
      <small>{cfg.behaviour.providerSavesSent ? t('settings.folders.saveSentProvider') : t('settings.folders.saveSentLocal')}</small>
    </div>
    <div class="field">
      <label class="label" for="fs-trash">{t('settings.folders.trash')}</label>
      <select id="fs-trash" class="input" value={cfg.behaviour.trashMode} disabled={busy} onchange={(ev) => saveBehaviour({ trashMode: (ev.currentTarget as HTMLSelectElement).value as TrashMode })}>
        {#each ['move', 'mark', 'delete'] as const as m (m)}<option value={m}>{t(`settings.folders.trashMode.${m}`)}</option>{/each}
      </select>
    </div>
    <div class="field">
      <label class="label" for="fs-archive">{t('settings.folders.archive')}</label>
      <select id="fs-archive" class="input" value={cfg.behaviour.archiveMode} disabled={busy} onchange={(ev) => saveBehaviour({ archiveMode: (ev.currentTarget as HTMLSelectElement).value as ArchiveMode })}>
        <option value="single">{t('settings.folders.archiveMode.single')}</option>
        <option value="yearly">{t('settings.folders.archiveMode.yearly', { example: `${t('settings.folders.defaultName.archive')}/${year}` })}</option>
        <option value="monthly">{t('settings.folders.archiveMode.monthly', { example: `${t('settings.folders.defaultName.archive')}/${year}/${year}-${month}` })}</option>
      </select>
    </div>

    {#if cfg.identities.length}
      <h3>{t('settings.folders.identities')}</h3>
      <div class="table" role="table" aria-label={t('settings.folders.identities')}>
        {#each cfg.identities as i (i.identityId)}
          <div class="row" role="row">
            <span class="role" role="cell">{i.email}</span>
            <span role="cell">
              <select class="input" aria-label={`${t('settings.folders.identitySent')}: ${i.email}`} value={String(i.sentFolderId ?? '')} disabled={busy} onchange={(ev) => void apply(app.api.setIdentityFolders(i.identityId, Number((ev.currentTarget as HTMLSelectElement).value) || 0, i.draftsFolderId ?? 0))}>
                <option value="">{t('settings.folders.identitySent')}: {t('settings.folders.accountDefault')}</option>
                {#each options as o (o.id)}<option value={String(o.id)}>{o.label}</option>{/each}
              </select>
            </span>
            <span role="cell">
              <select class="input" aria-label={`${t('settings.folders.identityDrafts')}: ${i.email}`} value={String(i.draftsFolderId ?? '')} disabled={busy} onchange={(ev) => void apply(app.api.setIdentityFolders(i.identityId, i.sentFolderId ?? 0, Number((ev.currentTarget as HTMLSelectElement).value) || 0))}>
                <option value="">{t('settings.folders.identityDrafts')}: {t('settings.folders.accountDefault')}</option>
                {#each options as o (o.id)}<option value={String(o.id)}>{o.label}</option>{/each}
              </select>
            </span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .card.embedded {
    padding: 0;
    border: none;
    background: none;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 6px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  h3 {
    margin: 18px 0 4px;
    font-size: 13px;
    font-weight: 650;
  }
  .hint {
    margin: 0 0 8px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .hint.warn {
    color: var(--tm-danger, #b45309);
    margin: -2px 0 4px;
  }
  .acc {
    max-width: 240px;
    height: 30px;
    font-size: 12.5px;
  }
  .table {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .row {
    display: grid;
    grid-template-columns: 0.8fr 1.4fr 1.2fr;
    gap: 8px;
    align-items: center;
  }
  .row.head {
    font-size: 11.5px;
    color: var(--tm-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .role {
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .row .input {
    height: 30px;
    font-size: 12.5px;
    width: 100%;
  }
  .src {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .badge {
    font-size: 11.5px;
    padding: 2px 8px;
    border-radius: 999px;
    border: 1px solid var(--tm-line);
    color: var(--tm-muted);
    white-space: nowrap;
  }
  .badge.user {
    color: var(--tm-accent);
    border-color: var(--tm-accent);
  }
  .badge.none,
  .badge.unresolved {
    color: var(--tm-danger, #b45309);
    border-color: currentColor;
  }
  .btn.small {
    height: 26px;
    font-size: 12px;
    padding: 0 8px;
  }
  .actions {
    display: flex;
    gap: 10px;
    align-items: center;
    margin-top: 12px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin-top: 12px;
  }
  .field .input {
    max-width: 360px;
    height: 32px;
    font-size: 13px;
  }
  small {
    color: var(--tm-muted);
    font-size: 12px;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
