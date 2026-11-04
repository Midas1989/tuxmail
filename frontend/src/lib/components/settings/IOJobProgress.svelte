<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Progress, cancel and summary of an import/export/backup/restore job (polls Api.getIOJob). -->
<script lang="ts">
  import X from '@lucide/svelte/icons/x';
  import Download from '@lucide/svelte/icons/download';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { app } from '../../state';
  import { t, type MessageKey } from '../../i18n/index.svelte';
  import type { IOJob } from '../../types';

  let { job, onfinished }: { job: IOJob; onfinished?: (job: IOJob) => void } = $props();

  let current = $state<IOJob | null>(null);
  const view = $derived(current ?? job);
  const pct = $derived(view.total > 0 ? Math.min(100, Math.round((view.done / view.total) * 100)) : view.status === 'running' ? 0 : 100);

  $effect(() => {
    const id = job.id;
    let stopped = false;
    let notified = false;
    async function poll() {
      while (!stopped) {
        try {
          const j = await app.api.getIOJob(id);
          current = j;
          if (j.status !== 'running') {
            if (!notified) {
              notified = true;
              onfinished?.(j);
            }
            return;
          }
        } catch {
          return;
        }
        await new Promise((r) => setTimeout(r, 400));
      }
    }
    poll();
    return () => {
      stopped = true;
    };
  });

  function summaryKey(k: string): MessageKey {
    return `importExport.summary.${k}` as MessageKey;
  }
  const knownSummary = ['messages', 'folders', 'contacts', 'events', 'accounts', 'duplicates', 'blobs', 'secrets', 'nonMailItems'];
</script>

<div class="job" aria-live="polite">
  <div class="head">
    <span class="title">{t(`importExport.jobKind.${view.kind}`)} · {t(`importExport.status.${view.status}`)}</span>
    {#if view.status === 'running'}
      <button class="btn btn-ghost" onclick={() => app.api.cancelIOJob(view.id)}><X size={14} /> {t('importExport.cancel')}</button>
    {/if}
  </div>
  <div class="bar" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct} aria-label={t('importExport.progress')}>
    <div class="fill" class:done={view.status === 'done'} class:failed={view.status === 'failed'} style:width="{pct}%"></div>
  </div>
  <p class="counts">
    {t('importExport.counts', { done: view.done, total: view.total || '?', skipped: view.skipped, failed: view.failed })}
    {#if view.current}<span class="cur">· {view.current}</span>{/if}
  </p>

  {#if view.status === 'done'}
    <p class="ok"><CircleCheck size={15} /> {t('importExport.finished')}</p>
    {#if view.summary}
      <ul class="summary">
        {#each Object.entries(view.summary) as [k, v] (k)}
          {#if knownSummary.includes(k)}<li><strong>{v}</strong> {t(summaryKey(k))}</li>{/if}
        {/each}
      </ul>
    {/if}
    {#if view.download}
      <a class="btn btn-primary" href={app.api.ioDownloadUrl(view.download)} download={view.downloadName}><Download size={14} /> {t('importExport.download', { name: view.downloadName ?? '' })}</a>
    {/if}
  {:else if view.status === 'failed'}
    <p class="err"><TriangleAlert size={15} /> {view.error}</p>
  {:else if view.status === 'canceled' && view.resumable}
    <p class="hint">{t('importExport.resumeHint')}</p>
  {/if}

  {#if view.problems?.length}
    <details>
      <summary>{t('importExport.problems', { count: view.problems.length })}</summary>
      <ul class="problems">
        {#each view.problems.slice(0, 50) as p, i (i)}
          <li><span class="item">{p.item}</span> {p.error}</li>
        {/each}
      </ul>
    </details>
  {/if}
</div>

<style>
  .job {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 14px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-bg);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .title {
    font-weight: 600;
    font-size: 13.5px;
  }
  .bar {
    height: 8px;
    border-radius: 999px;
    background: var(--tm-line);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--tm-accent);
    transition: width 0.3s;
  }
  .fill.done {
    background: var(--tm-success);
  }
  .fill.failed {
    background: var(--tm-danger);
  }
  .counts,
  .hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .cur {
    font-family: var(--tm-mono);
  }
  .ok,
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: 13px;
  }
  .ok {
    color: var(--tm-success);
  }
  .err {
    color: var(--tm-danger);
  }
  .summary {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 14px;
    margin: 0;
    padding: 0;
    list-style: none;
    font-size: 13px;
  }
  .problems {
    margin: 6px 0 0;
    padding-left: 18px;
    font-size: 12px;
    color: var(--tm-muted);
    max-height: 160px;
    overflow: auto;
  }
  .item {
    font-weight: 600;
    color: var(--tm-fg);
  }
  details summary {
    cursor: pointer;
    font-size: 12.5px;
  }
  a.btn {
    align-self: flex-start;
    text-decoration: none;
  }
</style>
