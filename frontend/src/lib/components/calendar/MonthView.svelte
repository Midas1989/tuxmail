<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Cake from '@lucide/svelte/icons/cake';
  import { fmt, t } from '../../i18n/index.svelte';
  import { phone } from '../../phone.svelte';
  import type { CalendarEvent } from '../../types';
  import { diffDays, isoWeek, onDay, shiftEvent, zonedInstant, type DayKey } from '../../calendar/datemath';

  interface Props {
    days: DayKey[];
    month: number;
    events: CalendarEvent[];
    colors: Record<number, string>;
    tz: string;
    today: DayKey;
    onopen: (e: CalendarEvent) => void;
    oncreate: (day: DayKey) => void;
    onmove: (e: CalendarEvent, start: string, end: string) => void;
    onday: (day: DayKey) => void;
  }
  let { days, month, events, colors, tz, today, onopen, oncreate, onmove, onday }: Props = $props();

  const MAX = 3;
  const weeks = $derived(Array.from({ length: days.length / 7 }, (_, i) => days.slice(i * 7, i * 7 + 7)));
  const byDay = $derived(Object.fromEntries(days.map((d) => [d, events.filter((e) => onDay(e, d, tz))])));

  let dragged = $state<{ e: CalendarEvent; from: DayKey } | null>(null);
  let over = $state<DayKey | null>(null);
  // Phones: the grid shows dots; the tapped day lists its events underneath.
  let picked = $state<DayKey | null>(null);
  const selected = $derived(picked && days.includes(picked) ? picked : days.includes(today) ? today : (days.find((d) => Number(d.slice(5, 7)) === month) ?? days[0]));
  const selList = $derived(byDay[selected] ?? []);

  function drop(d: DayKey) {
    const g = dragged;
    dragged = null;
    over = null;
    if (!g || g.from === d || g.e.readOnly) return;
    const s = shiftEvent(g.e, diffDays(g.from, d), 0);
    onmove(g.e, s.start, s.end);
  }
</script>

<div class="mv">
  <div class="wh">
    <span class="wk"></span>
    {#each weeks[0] as d (d)}<span>{fmt().pattern(zonedInstant(d, 720, tz), 'ddd')}</span>{/each}
  </div>
  {#each weeks as w (w[0])}
    <div class="week">
      <span class="wk" title={t('calendar.weekNumber', { n: isoWeek(w[0]) })}>{isoWeek(w[0])}</span>
      {#each w as d (d)}
        {@const list = byDay[d] ?? []}
        <div
          class="day"
          class:other={Number(d.slice(5, 7)) !== month}
          class:today={d === today}
          class:over={over === d}
          role="gridcell"
          tabindex="-1"
          aria-label={fmt().dateLong(zonedInstant(d, 720, tz))}
          ondblclick={() => oncreate(d)}
          ondragover={(ev) => {
            ev.preventDefault();
            over = d;
          }}
          ondragleave={() => over === d && (over = null)}
          ondrop={(ev) => {
            ev.preventDefault();
            drop(d);
          }}
        >
          {#if phone.current}
            <button class="cell" class:sel={d === selected} aria-pressed={d === selected} aria-label="{fmt().dateLong(zonedInstant(d, 720, tz))}{list.length ? ', ' + list.length : ''}" onclick={() => (picked = d)}>
              <span class="num">{Number(d.slice(8, 10))}</span>
              <span class="dots" aria-hidden="true">{#each list.slice(0, 4) as e (e.id)}<i style:--c={e.color || colors[e.calendarId] || '#64748b'}></i>{/each}</span>
            </button>
          {:else}
          <button class="num" onclick={() => onday(d)}>{Number(d.slice(8, 10))}</button>
          {#each list.slice(0, list.length > MAX ? MAX - 1 : MAX) as e (e.id)}
            <button
              class="item"
              class:timed={!e.allDay}
              class:inv={e.kind === 'invitation'}
              style:--c={e.color || colors[e.calendarId] || '#64748b'}
              draggable={!e.readOnly}
              ondragstart={() => (dragged = { e, from: d })}
              ondragend={() => (dragged = null)}
              onclick={() => onopen(e)}
              title={e.title}
            >
              {#if e.kind === 'birthday'}<Cake size={11} />{/if}
              {#if !e.allDay}<span class="tm">{fmt().time(e.start)}</span>{/if}
              <span class="tt">{e.title}</span>
            </button>
          {/each}
          {#if list.length > MAX}
            <button class="more" onclick={() => onday(d)}>{t('calendar.more', { n: list.length - MAX + 1 })}</button>
          {/if}
          {/if}
        </div>
      {/each}
    </div>
  {/each}
  {#if phone.current}
    <section class="dl" aria-label={fmt().dateLong(zonedInstant(selected, 720, tz))}>
      <header>
        <h3>{fmt().dateLong(zonedInstant(selected, 720, tz))}</h3>
        <button class="btn btn-ghost" onclick={() => onday(selected)}>{t('calendar.view.day')}</button>
        <button class="btn btn-primary" onclick={() => oncreate(selected)}>{t('calendar.newEvent')}</button>
      </header>
      {#each selList as e (e.id)}
        <button class="li" style:--c={e.color || colors[e.calendarId] || '#64748b'} onclick={() => onopen(e)}>
          <span class="when">{e.allDay ? t('calendar.allDay') : fmt().time(e.start)}</span>
          <span class="ti">{e.title}</span>
        </button>
      {:else}
        <p class="none">{t('calendar.noEvents')}</p>
      {/each}
    </section>
  {/if}
</div>

<style>
  .mv {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
    overflow: hidden;
  }
  .wh,
  .week {
    display: grid;
    grid-template-columns: 28px repeat(7, minmax(0, 1fr));
  }
  .wh {
    border-bottom: 1px solid var(--tm-line);
    font-size: 11.5px;
    text-transform: uppercase;
    color: var(--tm-muted);
    text-align: center;
    padding: 6px 0;
  }
  .week {
    flex: 1;
    min-height: 92px;
    border-bottom: 1px solid var(--tm-line);
  }
  .week:last-child {
    border-bottom: none;
  }
  .wk {
    font-size: 10.5px;
    color: var(--tm-faint);
    text-align: center;
    padding-top: 6px;
  }
  .day {
    border-left: 1px solid var(--tm-line);
    padding: 2px 3px 4px;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    overflow: hidden;
    outline: none;
  }
  .day.other {
    background: var(--tm-surface-2, #f7f9fc);
  }
  .day.other .num {
    color: var(--tm-faint);
  }
  .day.over {
    background: var(--tm-accent-soft);
  }
  .num {
    align-self: flex-end;
    border: none;
    background: none;
    font: inherit;
    font-size: 12.5px;
    font-weight: 600;
    min-width: 24px;
    height: 24px;
    border-radius: 99px;
    color: var(--tm-fg);
    cursor: pointer;
  }
  .day.today .num {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .item {
    display: flex;
    align-items: center;
    gap: 4px;
    border: none;
    border-radius: 4px;
    padding: 1px 5px;
    font: inherit;
    font-size: 11.5px;
    text-align: left;
    color: var(--tm-fg);
    background: color-mix(in srgb, var(--c) 20%, var(--tm-surface));
    cursor: pointer;
    min-width: 0;
  }
  .item.timed {
    background: transparent;
  }
  .item.timed::before {
    content: '';
    flex: none;
    width: 7px;
    height: 7px;
    border-radius: 99px;
    background: var(--c);
  }
  .item.inv {
    outline: 1px dashed var(--c);
  }
  .item:hover {
    background: color-mix(in srgb, var(--c) 28%, var(--tm-surface));
  }
  .tm {
    color: var(--tm-muted);
    flex: none;
  }
  .tt {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .more {
    border: none;
    background: none;
    font: inherit;
    font-size: 11px;
    color: var(--tm-muted);
    text-align: left;
    padding: 0 5px;
    cursor: pointer;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .wk {
      display: none;
    }
    .wh,
    .week {
      grid-template-columns: repeat(7, minmax(0, 1fr));
    }
    .week {
      min-height: 64px;
    }
    .tm,
    .tt {
      display: none;
    }
    .item {
      height: 6px;
      padding: 0;
    }
    .item.timed::before {
      width: 6px;
      height: 6px;
    }
    .mv {
      overflow-y: auto;
    }
    .week {
      flex: none;
      min-height: 52px;
    }
    .day {
      padding: 0;
    }
    .cell {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 3px;
      width: 100%;
      height: 100%;
      min-height: 52px;
      padding: 4px 0;
      border: none;
      background: none;
      font: inherit;
      color: inherit;
    }
    .cell.sel {
      background: var(--tm-accent-soft);
    }
    .cell .num {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-width: 28px;
      height: 28px;
      font-size: 14px;
    }
    .day.today .cell .num {
      background: var(--tm-accent);
      color: var(--tm-accent-fg);
    }
    .day.other .cell .num {
      color: var(--tm-faint);
    }
    .dots {
      display: flex;
      gap: 3px;
      height: 6px;
    }
    .dots i {
      width: 6px;
      height: 6px;
      border-radius: 99px;
      background: var(--c);
    }
    .dl {
      border-top: 1px solid var(--tm-line);
      padding: 8px 12px 12px;
      display: flex;
      flex-direction: column;
      gap: 4px;
    }
    .dl header {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }
    .dl h3 {
      flex: 1 1 100%;
      margin: 0;
      font-size: 14px;
    }
    .li {
      display: flex;
      align-items: center;
      gap: 10px;
      min-height: 48px;
      padding: 0 10px;
      border: none;
      border-left: 4px solid var(--c);
      border-radius: 6px;
      background: color-mix(in srgb, var(--c) 12%, var(--tm-surface));
      color: var(--tm-fg);
      font: inherit;
      text-align: left;
    }
    .when {
      flex: none;
      min-width: 48px;
      color: var(--tm-muted);
    }
    .ti {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .none {
      margin: 4px 0;
      color: var(--tm-muted);
    }
  }
</style>
