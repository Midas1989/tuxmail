<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import { fmt, t } from '../../i18n/index.svelte';
  import { addMonths, monthGrid, zonedInstant, type DayKey } from '../../calendar/datemath';

  interface Props {
    selected: DayKey;
    today: DayKey;
    weekStart: number;
    tz: string;
    marked?: Set<DayKey>;
    onselect: (d: DayKey) => void;
  }
  let { selected, today, weekStart, tz, marked = new Set(), onselect }: Props = $props();

  let shown = $state<DayKey>('');
  $effect(() => {
    shown = selected;
  });
  const grid = $derived(monthGrid(shown || selected, weekStart));
  const month = $derived(Number((shown || selected).slice(5, 7)));
  const label = $derived(fmt().pattern(zonedInstant((shown || selected).slice(0, 8) + '15', 720, tz), 'MMMM YYYY'));
</script>

<div class="mm">
  <div class="hd">
    <span class="lb">{label}</span>
    <button class="icon-btn sm" aria-label={t('calendar.prevMonth')} onclick={() => (shown = addMonths(shown || selected, -1))}><ChevronLeft size={15} /></button>
    <button class="icon-btn sm" aria-label={t('calendar.nextMonth')} onclick={() => (shown = addMonths(shown || selected, 1))}><ChevronRight size={15} /></button>
  </div>
  <div class="grid">
    {#each grid.slice(0, 7) as d (d)}<span class="wd">{fmt().pattern(zonedInstant(d, 720, tz), 'ddd').slice(0, 2)}</span>{/each}
    {#each grid as d (d)}
      <button
        class="d"
        class:other={Number(d.slice(5, 7)) !== month}
        class:today={d === today}
        class:sel={d === selected}
        class:mark={marked.has(d)}
        aria-label={fmt().dateLong(zonedInstant(d, 720, tz))}
        aria-pressed={d === selected}
        onclick={() => onselect(d)}>{Number(d.slice(8, 10))}</button
      >
    {/each}
  </div>
</div>

<style>
  .mm {
    padding: 4px 2px;
  }
  .hd {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-bottom: 4px;
  }
  .lb {
    flex: 1;
    font-weight: 600;
    font-size: 13px;
    padding-left: 6px;
    text-transform: capitalize;
  }
  .sm {
    width: 26px;
    height: 26px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(7, 1fr);
    gap: 1px;
    text-align: center;
  }
  .wd {
    font-size: 10.5px;
    color: var(--tm-faint);
    text-transform: uppercase;
    padding: 2px 0;
  }
  .d {
    position: relative;
    border: none;
    background: none;
    font: inherit;
    font-size: 12px;
    height: 26px;
    border-radius: 99px;
    color: var(--tm-fg);
    cursor: pointer;
  }
  .d:hover {
    background: var(--tm-accent-soft);
  }
  .d.other {
    color: var(--tm-faint);
  }
  .d.today {
    font-weight: 700;
    color: var(--tm-accent-text);
    box-shadow: inset 0 0 0 1px var(--tm-accent);
  }
  .d.sel {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .d.mark::after {
    content: '';
    position: absolute;
    left: 50%;
    bottom: 2px;
    width: 4px;
    height: 4px;
    margin-left: -2px;
    border-radius: 99px;
    background: currentColor;
    opacity: 0.6;
  }
</style>
