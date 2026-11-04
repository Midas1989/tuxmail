<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- "My shares": all created links with expiry, source and revoke (KONZEPT 12.2 #4). -->
<script lang="ts" module>
  import type { CloudShare } from '../../types';

  export type ShareFilter = 'active' | 'all';

  export function filterShares(list: CloudShare[], f: ShareFilter): CloudShare[] {
    return f === 'all' ? list : list.filter((s) => s.active);
  }

  /** Days until expiry (negative = expired), null if none. */
  export function daysLeft(s: CloudShare, now = Date.now()): number | null {
    if (!s.expiresAt) return null;
    return Math.ceil((Date.parse(s.expiresAt) - now) / 86400_000);
  }
</script>

<script lang="ts">
  import Copy from '@lucide/svelte/icons/copy';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import Ban from '@lucide/svelte/icons/ban';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import { app, cloud } from '../../state';
  import { formatDay, formatSize, t } from '../../i18n/index.svelte';
  import { openExternal } from '../../platform';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import ProviderIcon from './ProviderIcon.svelte';

  interface Props {
    reloadKey?: number;
    /** Only links of this account (e.g. the Vaultwarden tab). */
    accountId?: number;
  }
  let { reloadKey = 0, accountId }: Props = $props();

  let shares = $state<CloudShare[]>([]);
  let filter = $state<ShareFilter>('active');
  let loading = $state(true);
  let error = $state<string | null>(null);
  let revoking = $state<CloudShare | null>(null);

  $effect(() => {
    void reloadKey;
    void load();
  });

  async function load() {
    loading = true;
    try {
      shares = await app.api.listCloudShares();
      error = null;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  const visible = $derived(filterShares(accountId == null ? shares : shares.filter((s) => s.accountId === accountId), filter));

  async function copy(text: string, msg: string) {
    try {
      await navigator.clipboard.writeText(text);
      app.toast(msg);
    } catch {
      app.toast(t('compose.passwordCopyFailed'), undefined, 'error');
    }
  }

  async function copyPassword(s: CloudShare) {
    try {
      const { password } = await app.api.cloudSharePassword(s.id);
      await copy(password, t('cloud.share.passwordCopied'));
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  async function revoke(s: CloudShare) {
    revoking = null;
    try {
      const r = await app.api.revokeCloudShare(s.id);
      shares = shares.map((x) => (x.id === r.id ? r : x));
      app.toast(t('cloud.shares.revoked', { name: s.fileName }));
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  function status(s: CloudShare): { cls: string; label: string } {
    if (s.revokedAt) return { cls: 'revoked', label: t('cloud.shares.statusRevoked') };
    if (!s.active) return { cls: 'expired', label: t('cloud.shares.statusExpired') };
    const d = daysLeft(s);
    if (d !== null && d <= 2) return { cls: 'soon', label: t('cloud.shares.expiresSoon', { count: Math.max(d, 0) }) };
    return { cls: 'active', label: t('cloud.shares.statusActive') };
  }
</script>

<div class="shares">
  <div class="bar">
    <div class="seg" role="radiogroup" aria-label={t('cloud.shares.filter')}>
      <button role="radio" aria-checked={filter === 'active'} class:on={filter === 'active'} onclick={() => (filter = 'active')}>{t('cloud.shares.filterActive')}</button>
      <button role="radio" aria-checked={filter === 'all'} class:on={filter === 'all'} onclick={() => (filter = 'all')}>{t('cloud.shares.filterAll')}</button>
    </div>
    <span class="count">{t('cloud.shares.count', { count: visible.length })}</span>
  </div>
  {#if error}
    <p class="empty error" role="alert">{error}</p>
  {:else if !loading && visible.length === 0}
    <p class="empty">{t('cloud.shares.empty')}</p>
  {:else}
    <ul class="list">
      {#each visible as s (s.id)}
        {@const st = status(s)}
        {@const acc = cloud.account(s.accountId)}
        <li class:inactive={!s.active}>
          <ProviderIcon provider={s.provider} size={20} />
          <div class="main">
            <span class="name" title={s.remotePath ?? s.fileName}>{s.fileName}</span>
            <span class="meta">
              {[
                acc?.displayName ?? t(`cloud.provider.${s.provider}`),
                t(`cloud.shares.source.${s.source}`),
                s.size ? formatSize(s.size) : '',
                t('cloud.shares.createdAt', { date: formatDay(s.createdAt) }),
                s.revokedAt
                  ? t('cloud.shares.revokedAt', { date: formatDay(s.revokedAt) })
                  : s.expiresAt
                    ? t('cloud.shares.expiresAt', { date: formatDay(s.expiresAt) })
                    : '',
              ]
                .filter(Boolean)
                .join(' · ')}
            </span>
          </div>
          <span class="chip {st.cls}">{st.label}</span>
          <div class="acts">
            {#if s.active}
              <button class="icon-btn" onclick={() => copy(s.url, t('cloud.share.linkCopied'))} aria-label={t('cloud.share.copyLink')} title={t('cloud.share.copyLink')}><Copy size={16} /></button>
              {#if s.hasPassword}
                <button class="icon-btn" onclick={() => copyPassword(s)} aria-label={t('cloud.share.copyPassword')} title={t('cloud.share.copyPassword')}><KeyRound size={16} /></button>
              {/if}
              {#if s.provider !== 'vaultwarden'}
                <button class="icon-btn" onclick={() => openExternal(s.url)} aria-label={t('cloud.shares.open')} title={t('cloud.shares.open')}><ExternalLink size={16} /></button>
              {/if}
              <button class="icon-btn danger" onclick={() => (revoking = s)} aria-label={t('cloud.shares.revoke')} title={t('cloud.shares.revoke')}><Ban size={16} /></button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

{#if revoking}
  {@const r = revoking}
  <ConfirmDialog
    title={t('cloud.shares.revoke')}
    message={t('cloud.shares.revokeConfirm', { name: r.fileName })}
    confirmLabel={t('cloud.shares.revoke')}
    danger
    onconfirm={() => revoke(r)}
    oncancel={() => (revoking = null)}
  />
{/if}

<style>
  .shares {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .seg {
    display: inline-flex;
    padding: 2px;
    border-radius: 999px;
    background: var(--tm-surface-2);
  }
  .seg button {
    padding: 5px 12px;
    border-radius: 999px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .seg button.on {
    background: var(--tm-surface);
    color: var(--tm-fg);
    box-shadow: var(--tm-shadow);
  }
  .count {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface);
    overflow: hidden;
  }
  li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--tm-line);
  }
  li:last-child {
    border-bottom: 0;
  }
  li.inactive .name {
    color: var(--tm-muted);
    text-decoration: line-through;
  }
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .name {
    font-weight: 600;
    font-size: 13.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .chip {
    flex: none;
    font-size: 11.5px;
    font-weight: 600;
    padding: 3px 9px;
    border-radius: 999px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
  }
  .chip.active {
    background: color-mix(in srgb, var(--tm-success) 14%, transparent);
    color: var(--tm-success);
  }
  .chip.soon {
    background: color-mix(in srgb, var(--tm-warn) 16%, transparent);
    color: var(--tm-warn);
  }
  .chip.revoked {
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    color: var(--tm-danger);
  }
  .acts {
    display: flex;
    gap: 2px;
    flex: none;
    min-width: 32px;
  }
  .danger {
    color: var(--tm-danger);
  }
  .empty {
    margin: 0;
    padding: 28px 12px;
    text-align: center;
    color: var(--tm-muted);
    border: 1px dashed var(--tm-line-strong);
    border-radius: var(--tm-radius);
    font-size: 13px;
  }
  .error {
    color: var(--tm-danger);
  }
  @media (max-width: 639px) {
    li {
      flex-wrap: wrap;
    }
    .chip {
      order: 3;
    }
  }
</style>
