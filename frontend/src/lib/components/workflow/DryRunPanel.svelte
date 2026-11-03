<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Dry run: which sample items match and which actions would run (no side effects). -->
<script lang="ts">
  import X from '@lucide/svelte/icons/x';
  import FlaskConical from '@lucide/svelte/icons/flask-conical';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import CircleDashed from '@lucide/svelte/icons/circle-dashed';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import type { WorkflowDryRunResult } from '../../types';
  import { formatListDate, t } from '../../i18n/index.svelte';

  interface Props {
    result: WorkflowDryRunResult;
    onclose?: () => void;
  }
  let { result, onclose }: Props = $props();
</script>

<section class="dry" aria-label={t('workflow.dryRun')}>
  <header>
    <FlaskConical size={15} aria-hidden="true" />
    <strong>{t('workflow.dry.title')}</strong>
    <span class="sum">{t('workflow.dry.summary', { matched: result.matched, checked: result.checked })}</span>
    <span class="grow"></span>
    {#if onclose}<button class="icon-btn" aria-label={t('common.action.close')} onclick={onclose}><X size={15} /></button>{/if}
  </header>
  <ul>
    {#each result.samples as s (s.item.kind + s.item.id)}
      <li class:hit={s.matched}>
        <div class="item">
          {#if s.matched}<CircleCheck size={14} class="ok" />{:else}<CircleDashed size={14} class="no" />{/if}
          <span class="title">{s.item.title}</span>
          {#if s.item.from}<span class="from">{s.item.from}</span>{/if}
          <time datetime={s.item.date}>{formatListDate(s.item.date)}</time>
        </div>
        {#if s.planned.length}
          <ol class="plan">
            {#each s.planned as p, i (i)}
              <li>
                <span class="act">{t(`workflow.action.${p.type}` as 'workflow.action.move')}</span>
                <span class="params">{Object.values(p.params ?? {}).join(' · ') || p.summary}</span>
                {#if p.external}<span class="ext"><TriangleAlert size={11} /> {t('workflow.external')}</span>{/if}
              </li>
            {/each}
          </ol>
        {/if}
      </li>
    {/each}
  </ul>
</section>

<style>
  .dry {
    border: 1px solid var(--tm-line);
    border-radius: 12px;
    background: var(--tm-surface);
    overflow: hidden;
  }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    background: var(--tm-surface-2);
    font-size: 13px;
  }
  .sum {
    color: var(--tm-muted);
    font-size: 12.5px;
  }
  .grow {
    flex: 1;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 280px;
    overflow-y: auto;
  }
  ul > li {
    padding: 7px 12px;
    border-top: 1px solid var(--tm-line);
  }
  ul > li:not(.hit) {
    opacity: 0.6;
  }
  .item {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    min-width: 0;
  }
  .item :global(.ok) {
    color: var(--tm-success);
    flex: none;
  }
  .item :global(.no) {
    color: var(--tm-faint);
    flex: none;
  }
  .title {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .from {
    color: var(--tm-muted);
    font-size: 12px;
    white-space: nowrap;
  }
  time {
    margin-left: auto;
    font-size: 11.5px;
    color: var(--tm-muted);
    flex: none;
  }
  .plan {
    margin: 4px 0 0 22px;
    padding: 0;
    list-style: decimal;
    font-size: 12.5px;
  }
  .plan li {
    padding: 1px 0;
  }
  .act {
    font-weight: 650;
    color: color-mix(in srgb, var(--tm-success) 70%, var(--tm-fg));
  }
  .params {
    margin-left: 6px;
    color: var(--tm-muted);
    font-family: var(--tm-mono);
    font-size: 11.5px;
  }
  .ext {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    margin-left: 6px;
    font-size: 10.5px;
    color: var(--tm-warn);
  }
</style>
