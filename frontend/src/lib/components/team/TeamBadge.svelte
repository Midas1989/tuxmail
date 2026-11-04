<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Assignee/status marker of a team conversation in the chat and hybrid lists. -->
<script lang="ts">
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import Hourglass from '@lucide/svelte/icons/hourglass';
  import MessageSquareLock from '@lucide/svelte/icons/message-square-lock';
  import { team } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { TeamAssignment } from '../../api/team';

  interface Props {
    conversation: object;
  }
  let { conversation }: Props = $props();
  const a = $derived((conversation as { team?: TeamAssignment }).team);
  const initials = $derived(
    (a?.assigneeName ?? '')
      .split(/\s+/)
      .filter(Boolean)
      .map((w) => w[0])
      .slice(0, 2)
      .join('')
      .toUpperCase(),
  );
  const overdue = $derived(!!a?.dueAt && a.status !== 'done' && new Date(a.dueAt).getTime() < Date.now());
</script>

{#if team.activeId != null && a}
  <span class="tb" class:overdue>
    {#if a.status === 'done'}<CircleCheck size={13} class="done" aria-label={t('team.status.done')} />{:else if a.status === 'waiting'}<Hourglass size={12} aria-label={t('team.status.waiting')} />{/if}
    {#if a.priority === 'urgent' || a.priority === 'high'}<span class="prio" title={t(`team.priority.${a.priority}`)}>!</span>{/if}
    {#if a.comments > 0}<span class="notes" title={t('team.comments.count', { count: a.comments })}><MessageSquareLock size={12} />{a.comments}</span>{/if}
    {#if a.assigneeId}
      <span class="who" class:me={a.assigneeId === team.myId} title={`${t('team.assignee')}: ${a.assigneeName ?? ''}`}>{initials || '?'}</span>
    {:else if a.status !== 'done'}
      <span class="who free" title={t('team.unassigned')}>–</span>
    {/if}
  </span>
{/if}

<style>
  .tb {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex: none;
    color: var(--tm-muted);
    font-size: 11px;
  }
  .tb :global(.done) {
    color: var(--tm-success);
  }
  .who {
    display: inline-grid;
    place-items: center;
    min-width: 20px;
    height: 18px;
    padding: 0 4px;
    border-radius: 9px;
    background: var(--tm-surface-3);
    color: var(--tm-fg);
    font-weight: 700;
    font-size: 10px;
  }
  .who.me {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .who.free {
    background: none;
    border: 1px dashed var(--tm-line-strong);
    color: var(--tm-faint);
  }
  .notes {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    color: color-mix(in srgb, var(--tm-warn) 80%, var(--tm-fg));
  }
  .prio {
    color: var(--tm-danger);
    font-weight: 800;
    font-size: 12px;
  }
  .overdue .who {
    outline: 2px solid var(--tm-danger);
  }
</style>
