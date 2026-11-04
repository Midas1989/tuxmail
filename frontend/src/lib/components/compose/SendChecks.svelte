<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Send-check findings (KONZEPT 8.13) returned by a blocked send: the user
  can go back to the message or confirm the warnings ("send anyway").
  Blocking findings (severity "block") cannot be confirmed here.
-->
<script lang="ts" module>
  import type { SendFinding } from '../../types';

  /** Text of a finding in the given locale (falls back to English). */
  export function findingText(f: SendFinding, locale: string): string {
    return f.localized?.[locale] ?? f.localized?.[locale.split('-')[0]] ?? f.message;
  }

  /** Checks to confirm for "send anyway" (everything except blocking findings). */
  export function confirmable(findings: SendFinding[]): string[] | null {
    if (findings.some((f) => f.severity === 'block')) return null;
    return [...new Set(findings.map((f) => f.check))];
  }
</script>

<script lang="ts">
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import OctagonX from '@lucide/svelte/icons/octagon-x';
  import Info from '@lucide/svelte/icons/info';
  import { i18n, t } from '../../i18n/index.svelte';

  interface Props {
    findings: SendFinding[];
    onconfirm: (checks: string[]) => void;
    oncancel: () => void;
    compact?: boolean;
  }
  let { findings, onconfirm, oncancel, compact = false }: Props = $props();
  const checks = $derived(confirmable(findings));
</script>

<div class="checks" class:compact role="alert" aria-label={t('compose.checks.title')}>
  <p class="title"><TriangleAlert size={16} aria-hidden="true" /> {t('compose.checks.title')}</p>
  <ul>
    {#each findings as f, i (f.check + i)}
      <li class="sev-{f.severity}" data-check={f.check}>
        {#if f.severity === 'block'}<OctagonX size={14} aria-hidden="true" />{:else if f.severity === 'warn'}<TriangleAlert size={14} aria-hidden="true" />{:else}<Info size={14} aria-hidden="true" />{/if}
        <span>{findingText(f, i18n.locale)}</span>
      </li>
    {/each}
  </ul>
  <div class="actions">
    <button class="btn btn-ghost" onclick={oncancel}>{t('compose.checks.edit')}</button>
    {#if checks}<button class="btn btn-primary" onclick={() => onconfirm(checks)}>{t('compose.checks.sendAnyway')}</button>{/if}
  </div>
</div>

<style>
  .checks {
    border: 1px solid color-mix(in srgb, var(--tm-warn) 45%, var(--tm-line));
    background: color-mix(in srgb, var(--tm-warn) 12%, var(--tm-surface));
    border-radius: 10px;
    padding: 10px 12px;
    font-size: 13px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .compact {
    margin: 0 12px 8px;
  }
  .title {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-weight: 600;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  li {
    display: flex;
    gap: 6px;
    align-items: flex-start;
  }
  li :global(svg) {
    flex: none;
    margin-top: 2px;
  }
  .sev-block {
    color: var(--tm-danger);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
