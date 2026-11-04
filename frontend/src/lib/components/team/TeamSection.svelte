<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Team mailboxes in the folder tree (variant "tree", inside the tree <ul>)
     and as a strip above the chat list (variant "strip"), KONZEPT 8.14. -->
<script lang="ts">
  import Users from '@lucide/svelte/icons/users';
  import UserCheck from '@lucide/svelte/icons/user-check';
  import CircleDashed from '@lucide/svelte/icons/circle-dashed';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import Inbox from '@lucide/svelte/icons/inbox';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import AtSign from '@lucide/svelte/icons/at-sign';
  import { team } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { TeamMailbox, TeamView } from '../../api/team';

  interface Props {
    variant?: 'tree' | 'strip';
  }
  let { variant = 'tree' }: Props = $props();

  const views: { id: TeamView; icon: typeof Inbox; key: 'team.view.all' | 'team.view.mine' | 'team.view.unassigned' | 'team.view.done' }[] = [
    { id: '', icon: Inbox, key: 'team.view.all' },
    { id: 'mine', icon: UserCheck, key: 'team.view.mine' },
    { id: 'unassigned', icon: CircleDashed, key: 'team.view.unassigned' },
    { id: 'done', icon: CircleCheck, key: 'team.view.done' },
  ];

  function count(mb: TeamMailbox, v: TeamView): number {
    if (v === 'mine') return mb.mine;
    if (v === 'unassigned') return mb.unassigned;
    if (v === '') return mb.open;
    return 0;
  }
</script>

{#if team.available}
  {#if variant === 'tree'}
    <li role="none" class="section">
      <span class="section-title">{t('team.section')}</span>
    </li>
    {#if team.activeId != null}
      <li role="none">
        <button role="treeitem" aria-selected="false" tabindex="-1" class="row back" onclick={() => team.leave()}>
          <span class="twisty"></span>
          <ArrowLeft size={15} aria-hidden="true" />
          <span class="name">{t('team.back')}</span>
        </button>
      </li>
    {/if}
    {#each team.mailboxes as mb (mb.id)}
      {@const open = team.activeId === mb.id}
      <li role="none">
        <button role="treeitem" aria-selected={open && team.view === ''} aria-expanded={open} tabindex="-1" class="row" class:on={open} onclick={() => team.enter(mb.id)} title={mb.address}>
          <span class="twisty"></span>
          <Users size={16} aria-hidden="true" />
          <span class="name">{mb.name}</span>
          {#if mb.mentions > 0}<span class="mention" title={t('team.mentions', { count: mb.mentions })}><AtSign size={11} />{mb.mentions}</span>{/if}
          {#if mb.unassigned > 0}<span class="count" title={t('team.view.unassigned')}>{mb.unassigned}</span>{/if}
        </button>
        {#if open}
          <ul role="group">
            {#each views.slice(1) as v (v.id)}
              {@const Icon = v.icon}
              <li role="none">
                <button role="treeitem" aria-selected={team.view === v.id} tabindex="-1" class="row" style:--depth={1} onclick={() => team.enter(mb.id, v.id)}>
                  <span class="twisty"></span>
                  <Icon size={15} aria-hidden="true" />
                  <span class="name">{t(v.key)}</span>
                  {#if count(mb, v.id) > 0}<span class="count">{count(mb, v.id)}</span>{/if}
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </li>
    {/each}
  {:else}
    <div class="strip" role="toolbar" aria-label={t('team.section')}>
      <button class="chip" class:on={team.activeId == null} onclick={() => team.activeId != null && team.leave()}>{t('team.personal')}</button>
      {#each team.mailboxes as mb (mb.id)}
        <button class="chip" class:on={team.activeId === mb.id} onclick={() => team.enter(mb.id, team.activeId === mb.id ? team.view : '')}>
          <Users size={13} aria-hidden="true" />{mb.name}
          {#if mb.unassigned > 0}<span class="n">{mb.unassigned}</span>{/if}
        </button>
      {/each}
    </div>
    {#if team.active}
      <div class="strip views" role="radiogroup" aria-label={t('team.viewsLabel')}>
        {#each views as v (v.id)}
          <button class="pill" role="radio" aria-checked={team.view === v.id} onclick={() => team.enter(team.activeId!, v.id)}>{t(v.key)}</button>
        {/each}
      </div>
    {/if}
  {/if}
{/if}

<style>
  .section {
    margin-top: 10px;
  }
  .section-title {
    display: block;
    padding: 6px 8px 4px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--tm-faint);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    height: var(--tm-tree-h, 30px);
    padding: 0 8px 0 calc(8px + var(--depth, 0) * 14px);
    border: 0;
    border-radius: var(--tm-radius-sm);
    background: none;
    color: var(--tm-fg);
    font: inherit;
    font-size: 13.5px;
    text-align: left;
    cursor: pointer;
  }
  .row:hover {
    background: var(--tm-surface-2);
  }
  .row[aria-selected='true'],
  .row.on {
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
  }
  .row.back {
    color: var(--tm-muted);
  }
  .twisty {
    width: 4px;
  }
  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    font-size: 12px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .mention {
    display: inline-flex;
    align-items: center;
    gap: 1px;
    padding: 0 5px;
    border-radius: 9px;
    background: var(--tm-warn);
    color: #1f1500;
    font-size: 11px;
    font-weight: 700;
  }
  .strip {
    display: flex;
    gap: 6px;
    padding: 2px 12px 8px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .chip,
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    flex: none;
    height: 28px;
    padding: 0 11px;
    border: 1px solid var(--tm-line);
    border-radius: 14px;
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    font-size: 12.5px;
    cursor: pointer;
  }
  .chip.on,
  .pill[aria-checked='true'] {
    border-color: transparent;
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .n {
    padding: 0 5px;
    border-radius: 8px;
    background: color-mix(in srgb, currentColor 18%, transparent);
    font-size: 11px;
    font-weight: 700;
  }
  .views .pill {
    height: 24px;
    font-size: 12px;
  }
</style>
