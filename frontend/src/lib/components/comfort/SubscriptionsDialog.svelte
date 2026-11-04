<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- "Abonnements": detected newsletters with frequency, unsubscribe, archive all, archive automatically. -->
<script lang="ts">
  import Newspaper from '@lucide/svelte/icons/newspaper';
  import { app, comfort } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import Modal from '../common/Modal.svelte';
  import type { Subscription } from '../../api/comfort';

  interface Props {
    onclose: () => void;
  }
  let { onclose }: Props = $props();

  let subs = $state<Subscription[] | null>(null);
  let busy = $state<string | null>(null);
  let filter = $state('');

  async function load() {
    subs = await comfort.api.listSubscriptions();
  }
  $effect(() => {
    void load();
  });

  const shown = $derived((subs ?? []).filter((s) => !filter || `${s.name ?? ''} ${s.sender} ${s.key}`.toLowerCase().includes(filter.toLowerCase())));

  async function act(s: Subscription, what: 'unsubscribe' | 'archive' | 'auto') {
    busy = s.key + what;
    try {
      if (what === 'unsubscribe') {
        const r = await comfort.api.unsubscribe(s.key);
        if (r.done) app.toast(t('comfort.newsletter.unsubscribed'));
        else if (r.openUrl) window.open(r.openUrl, '_blank', 'noopener,noreferrer');
        else app.toast(t('comfort.newsletter.failed', { error: r.error ?? '' }), undefined, 'error');
      } else if (what === 'archive') {
        app.toast(t('comfort.newsletter.archived', { count: await comfort.api.archiveSubscription(s.key) }));
      } else {
        await comfort.api.autoArchiveSubscription(s.key);
        app.toast(t('comfort.newsletter.autoArchiveCreated'));
      }
      await load();
    } catch (e) {
      app.toast(String(e instanceof Error ? e.message : e), undefined, 'error');
    } finally {
      busy = null;
    }
  }
</script>

<Modal title={t('comfort.subscriptions.title')} {onclose} width="880px">
  <p class="intro">{t('comfort.subscriptions.intro')}</p>
  <input class="input" type="search" bind:value={filter} placeholder={t('comfort.subscriptions.filter')} aria-label={t('comfort.subscriptions.filter')} />
  {#if subs === null}
    <p class="muted">{t('comfort.loading')}</p>
  {:else if !shown.length}
    <p class="muted"><Newspaper size={16} aria-hidden="true" /> {t('comfort.subscriptions.empty')}</p>
  {:else}
    <table>
      <thead>
        <tr>
          <th>{t('comfort.subscriptions.sender')}</th>
          <th class="num">{t('comfort.subscriptions.frequency')}</th>
          <th>{t('comfort.subscriptions.lastMail')}</th>
          <th class="num">{t('comfort.subscriptions.inInbox')}</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#each shown as s (s.key)}
          <tr class:off={!!s.unsubscribed}>
            <td>
              <div class="name">{s.name || s.sender}</div>
              <div class="sub">{s.sender}{s.key !== s.sender ? ` · ${s.key}` : ''}</div>
            </td>
            <td class="num">{t('comfort.subscriptions.perMonth', { n: s.perMonth })}</td>
            <td>{fmt().date(s.lastMail)}</td>
            <td class="num">{s.inInbox}</td>
            <td><div class="acts">
              {#if s.unsubscribed}
                <span class="badge">{t('comfort.subscriptions.unsubscribedBadge')}</span>
              {:else}
                <button class="btn sm" disabled={busy !== null || !s.unsubscribe.method} onclick={() => act(s, 'unsubscribe')}>
                  {s.unsubscribe.oneClick ? t('comfort.subscriptions.oneClick') : t('comfort.newsletter.unsubscribe')}
                </button>
              {/if}
              <button class="btn btn-ghost sm" disabled={busy !== null || !s.inInbox} onclick={() => act(s, 'archive')}>{t('comfort.newsletter.archiveAll')}</button>
              {#if s.autoArchive}
                <span class="badge ok">{t('comfort.subscriptions.autoArchiveOn')}</span>
              {:else}
                <button class="btn btn-ghost sm" disabled={busy !== null} onclick={() => act(s, 'auto')}>{t('comfort.newsletter.autoArchive')}</button>
              {/if}
            </div></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</Modal>

<style>
  .intro,
  .muted {
    color: var(--tm-muted);
    font-size: 13px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 12px;
    font-size: 13px;
  }
  th {
    text-align: left;
    font-weight: 600;
    color: var(--tm-muted);
    font-size: 12px;
    padding: 6px 8px;
    border-bottom: 1px solid var(--tm-line);
  }
  td {
    padding: 8px;
    border-bottom: 1px solid var(--tm-line);
    vertical-align: middle;
  }
  .num {
    text-align: right;
    white-space: nowrap;
  }
  .name {
    font-weight: 600;
  }
  .sub {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .acts {
    display: flex;
    gap: 6px;
    justify-content: flex-end;
    flex-wrap: wrap;
  }
  .sm {
    height: 28px;
    padding: 0 9px;
    font-size: 12.5px;
  }
  tr.off .name {
    text-decoration: line-through;
    color: var(--tm-muted);
  }
  .badge.ok {
    background: color-mix(in srgb, #16a34a 16%, transparent);
    color: #15803d;
  }
</style>
