<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  What waits for the connection (desktop app and Android app in server mode):
  mails and noted actions, with what to do when one needs a decision.
-->
<script lang="ts">
  import type { ConnectionState } from '../../state/connection.svelte';
  import { app } from '../../state';
  import { openOutboxDraft, outboxReasons } from '../../state/outboxDraft';
  import { t } from '../../i18n/index.svelte';
  import type { OutboxEntry } from '../../types';

  interface Props {
    conn: ConnectionState;
    busy?: boolean;
    run: (fn: () => Promise<void>) => void;
  }
  let { conn, busy = false, run }: Props = $props();

  function label(o: OutboxEntry): string {
    if (o.kind === 'send') return o.title || t('connection.outbox.noSubject');
    const what = o.action ? t(`connection.outbox.action.${o.action}`) : t('connection.outbox.mark');
    return o.title ? `${what}: ${o.title}` : what;
  }

  function conflictText(o: OutboxEntry): string {
    return o.error === 'gone' ? t('connection.outbox.conflictGone') : t('connection.outbox.conflictRefused');
  }
</script>

<ul class="outbox" data-testid="outbox">
  {#each conn.status.outbox as o (o.id)}
    <li>
      <span class="what">
        <b>{label(o)}</b>
        {#if o.recipients}<small>{o.recipients}</small>{/if}
        {#if o.state === 'failed'}<small class="bad">{t('connection.outbox.failed', { error: o.error ?? '' })}</small>{/if}
        {#if o.state === 'in_doubt'}<small class="bad">{t('connection.outbox.inDoubt')}</small>{/if}
        {#if o.state === 'conflict'}<small class="bad" data-testid="outbox-conflict">{conflictText(o)}</small>{/if}
        {#if o.state === 'blocked'}
          <small class="bad">{t('connection.outbox.blocked')}</small>
          {#each outboxReasons(o) as r, i (i)}<small class="bad">{r}</small>{/each}
        {/if}
      </span>
      <span class="acts">
        {#if o.state !== 'pending' && o.kind === 'send'}
          <button class="btn btn-ghost" disabled={busy} onclick={() => run(() => openOutboxDraft(conn, app, o))}>{t('connection.outbox.asDraft')}</button>
        {/if}
        {#if o.state !== 'pending' && o.state !== 'conflict'}
          <button class="btn btn-ghost" disabled={busy} onclick={() => run(() => conn.outboxRetry(o.id))}>{o.state === 'failed' ? t('connection.outbox.retry') : t('connection.outbox.sendAgain')}</button>
        {/if}
        <button class="btn btn-ghost" disabled={busy} onclick={() => run(() => conn.outboxDiscard(o.id))}>{o.state === 'conflict' ? t('connection.outbox.understood') : t('connection.outbox.discard')}</button>
      </span>
    </li>
  {/each}
</ul>

<style>
  .outbox {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .outbox li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
  }
  .what {
    flex: 1 1 12rem;
    min-width: 0;
    display: flex;
    flex-direction: column;
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .what small {
    color: var(--tm-muted);
    overflow-wrap: anywhere;
  }
  .what small.bad {
    color: var(--tm-danger);
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
</style>
