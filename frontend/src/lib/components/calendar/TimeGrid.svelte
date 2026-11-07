<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Repeat from '@lucide/svelte/icons/repeat';
  import Video from '@lucide/svelte/icons/video';
  import Cake from '@lucide/svelte/icons/cake';
  import { fmt, t } from '../../i18n/index.svelte';
  import type { CalendarEvent } from '../../types';
  import { addDays, dayKeyIn, diffDays, eventDays, isAllDayRow, layoutDay, minutesIn, moveTo, segmentOn, shiftEvent, snap, zonedInstant, type DayKey } from '../../calendar/datemath';

  interface Props {
    days: DayKey[];
    events: CalendarEvent[];
    colors: Record<number, string>;
    tz: string;
    secondaryTz?: string;
    today: DayKey;
    onopen: (e: CalendarEvent) => void;
    oncreate: (day: DayKey, startMin: number, endMin: number, allDay: boolean) => void;
    onmove: (e: CalendarEvent, start: string, end: string) => void;
  }
  let { days, events, colors, tz, secondaryTz, today, onopen, oncreate, onmove }: Props = $props();

  const HOUR = 48;
  const hours = Array.from({ length: 24 }, (_, i) => i);
  let scroller = $state<HTMLDivElement>();
  let cols = $state<HTMLDivElement>();
  let nowMin = $state(0);

  $effect(() => {
    nowMin = minutesIn(new Date(), tz);
    const id = setInterval(() => (nowMin = minutesIn(new Date(), tz)), 60_000);
    return () => clearInterval(id);
  });
  $effect(() => {
    // Scroll to 7:00 (or one hour before now) when the view opens.
    const now = minutesIn(new Date(), tz);
    if (scroller) scroller.scrollTop = (now >= 8 * 60 && now <= 20 * 60 ? now - 90 : 7 * 60) * (HOUR / 60);
  });

  const allDay = $derived(events.filter((e) => isAllDayRow(e, tz)));
  const timed = $derived(events.filter((e) => !isAllDayRow(e, tz)));

  /** All-day row: one lane per overlapping chip, spanning visible days. */
  const allDayLanes = $derived.by(() => {
    const first = days[0];
    const last = days[days.length - 1];
    const lanes: { e: CalendarEvent; from: number; span: number; lane: number }[] = [];
    const used: number[][] = [];
    const sorted = [...allDay].sort((a, b) => eventDays(a, tz).first.localeCompare(eventDays(b, tz).first));
    for (const e of sorted) {
      const { first: s, endExcl } = eventDays(e, tz);
      const from = Math.max(0, diffDays(first, s));
      const to = Math.min(days.length, diffDays(first, endExcl));
      if (to <= 0 || s > last) continue;
      let lane = 0;
      while ((used[lane] ?? []).some((c) => c >= from && c < to)) lane++;
      used[lane] = [...(used[lane] ?? []), ...Array.from({ length: to - from }, (_, i) => from + i)];
      lanes.push({ e, from, span: to - from, lane });
    }
    return lanes;
  });
  const laneCount = $derived(Math.max(1, ...allDayLanes.map((l) => l.lane + 1)));

  const placedByDay = $derived(
    days.map((d) =>
      layoutDay(
        timed
          .filter((e) => eventDays(e, tz).first <= d && eventDays(e, tz).endExcl > d)
          .map((e) => ({ item: e, ...segmentOn(e, d, tz) })),
      ),
    ),
  );

  function hourLabel(h: number, zone: string): string {
    return fmt().time(zonedInstant(days[0], h * 60, tz), zone);
  }

  function color(e: CalendarEvent): string {
    return e.color || colors[e.calendarId] || '#64748b';
  }

  // ---- pointer interaction -------------------------------------------------
  type Drag =
    | { kind: 'move' | 'resize'; e: CalendarEvent; day: number; x: number; y: number; dMin: number; dDay: number; moved: boolean }
    | { kind: 'create'; day: number; startMin: number; endMin: number };
  let drag = $state<Drag | null>(null);

  function colWidth(): number {
    return cols ? cols.clientWidth / days.length : 100;
  }

  function minuteAt(ev: PointerEvent, col: HTMLElement): number {
    const r = col.getBoundingClientRect();
    return Math.max(0, Math.min(24 * 60, ((ev.clientY - r.top) / HOUR) * 60));
  }

  function startEventDrag(ev: PointerEvent, e: CalendarEvent, di: number, kind: 'move' | 'resize') {
    if (ev.button !== 0) return;
    ev.stopPropagation();
    if (e.readOnly) {
      (ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId);
      drag = { kind: 'move', e, day: di, x: ev.clientX, y: ev.clientY, dMin: 0, dDay: 0, moved: false };
      return;
    }
    (ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId);
    drag = { kind, e, day: di, x: ev.clientX, y: ev.clientY, dMin: 0, dDay: 0, moved: false };
  }

  function onEventMove(ev: PointerEvent) {
    if (!drag || drag.kind === 'create') return;
    const dy = ev.clientY - drag.y;
    const dx = ev.clientX - drag.x;
    if (!drag.moved && Math.abs(dy) < 4 && Math.abs(dx) < 4) return;
    if (drag.e.readOnly) return;
    drag.moved = true;
    drag.dMin = snap((dy / HOUR) * 60);
    drag.dDay = drag.kind === 'move' ? Math.round(dx / colWidth()) : 0;
  }

  function onEventUp(e: CalendarEvent) {
    const d = drag;
    drag = null;
    if (!d || d.kind === 'create') return;
    if (!d.moved) {
      onopen(e);
      return;
    }
    if (d.kind === 'move') {
      const startDay = dayKeyIn(new Date(e.start), tz);
      const m = moveTo(e, addDays(startDay, d.dDay), minutesIn(new Date(e.start), tz) + d.dMin, tz);
      onmove(e, m.start, m.end);
    } else {
      const end = new Date(new Date(e.end).getTime() + d.dMin * 60_000);
      if (end.getTime() - new Date(e.start).getTime() >= 15 * 60_000) onmove(e, e.start, end.toISOString());
    }
  }

  function startCreate(ev: PointerEvent, di: number) {
    if (ev.button !== 0) return;
    const col = ev.currentTarget as HTMLElement;
    col.setPointerCapture(ev.pointerId);
    const m = Math.floor(minuteAt(ev, col) / 30) * 30;
    drag = { kind: 'create', day: di, startMin: m, endMin: m + 30 };
  }
  function moveCreate(ev: PointerEvent) {
    if (drag?.kind !== 'create') return;
    const m = snap(minuteAt(ev, ev.currentTarget as HTMLElement), 15);
    drag.endMin = Math.max(drag.startMin + 15, m);
  }
  function endCreate() {
    if (drag?.kind !== 'create') return;
    const d = drag;
    drag = null;
    const end = d.endMin - d.startMin <= 30 ? d.startMin + 60 : d.endMin;
    oncreate(days[d.day], d.startMin, end, false);
  }

  function preview(p: { item: CalendarEvent; start: number; end: number }, di: number) {
    if (!drag || drag.kind === 'create' || drag.e.id !== p.item.id || !drag.moved) return { top: p.start, height: p.end - p.start, shift: 0 };
    if (drag.kind === 'resize') return { top: p.start, height: Math.max(15, p.end - p.start + drag.dMin), shift: 0 };
    void di;
    return { top: p.start + drag.dMin, height: p.end - p.start, shift: drag.dDay };
  }

  function allDayDrop(ev: PointerEvent, e: CalendarEvent) {
    const d = drag;
    drag = null;
    if (!d || d.kind === 'create') return;
    if (!d.moved) return onopen(e);
    if (d.dDay) {
      const s = shiftEvent(e, d.dDay, 0);
      onmove(e, s.start, s.end);
    }
    void ev;
  }

  function onAllDayMove(ev: PointerEvent) {
    if (!drag || drag.kind === 'create' || drag.e.readOnly) return;
    const dx = ev.clientX - drag.x;
    if (!drag.moved && Math.abs(dx) < 4) return;
    drag.moved = true;
    drag.dDay = Math.round(dx / colWidth());
  }

  function timeRange(e: CalendarEvent): string {
    return `${fmt().time(e.start)} – ${fmt().time(e.end)}`;
  }
</script>

<div class="tg" style:--days={days.length} style:--hour="{HOUR}px" class:sec={!!secondaryTz}>
  <div class="head">
    <div class="gutter">
      {#if secondaryTz}<span class="tzl">{secondaryTz.split('/').pop()?.replace('_', ' ')}</span>{/if}
    </div>
    {#each days as d (d)}
      {@const date = zonedInstant(d, 12 * 60, tz)}
      <div class="dh" class:today={d === today} role="columnheader">
        <span class="wd">{fmt().pattern(date, 'ddd')}</span>
        <span class="dn">{Number(d.slice(8, 10))}</span>
      </div>
    {/each}
  </div>
  <div class="allday" style:--lanes={laneCount}>
    <div class="gutter lbl">{t('calendar.allDay')}</div>
    <div class="adcols" role="presentation">
      {#each days as d, i (d)}
        <button class="adcell" style:grid-column={i + 1} aria-label={t('calendar.newAllDay', { date: fmt().date(zonedInstant(d, 720, tz)) })} ondblclick={() => oncreate(d, 0, 0, true)}></button>
      {/each}
      {#each allDayLanes as l (l.e.id)}
        {@const shift = drag && drag.kind !== 'create' && drag.e.id === l.e.id ? drag.dDay : 0}
        <button
          class="chip"
          data-touch-compact
          class:inv={l.e.kind === 'invitation'}
          style:--c={color(l.e)}
          style:grid-column="{Math.max(1, l.from + 1 + shift)} / span {l.span}"
          style:grid-row={l.lane + 1}
          title={l.e.title}
          onpointerdown={(ev) => startEventDrag(ev, l.e, l.from, 'move')}
          onpointermove={onAllDayMove}
          onpointerup={(ev) => allDayDrop(ev, l.e)}
        >
          {#if l.e.kind === 'birthday'}<Cake size={12} />{/if}
          <span>{l.e.title}{#if l.e.birthYear}&nbsp;({Number(today.slice(0, 4)) - l.e.birthYear}){/if}</span>
        </button>
      {/each}
    </div>
  </div>
  <div class="body" bind:this={scroller}>
    <div class="hours">
      {#each hours as h (h)}
        <div class="hr">
          {#if h > 0}<span>{hourLabel(h, tz)}</span>{#if secondaryTz}<small>{hourLabel(h, secondaryTz)}</small>{/if}{/if}
        </div>
      {/each}
    </div>
    <div class="cols" bind:this={cols}>
      {#each days as d, di (d)}
        <div
          class="col"
          class:today={d === today}
          role="gridcell"
          tabindex="-1"
          aria-label={fmt().dateLong(zonedInstant(d, 720, tz))}
          onpointerdown={(ev) => startCreate(ev, di)}
          onpointermove={moveCreate}
          onpointerup={endCreate}
        >
          {#each hours as h (h)}<div class="slot"></div>{/each}
          {#if drag?.kind === 'create' && drag.day === di}
            <div class="ghost" style:top="{(drag.startMin / 60) * HOUR}px" style:height="{((drag.endMin - drag.startMin) / 60) * HOUR}px">
              {fmt().time(zonedInstant(d, drag.startMin, tz))} – {fmt().time(zonedInstant(d, drag.endMin, tz))}
            </div>
          {/if}
          {#each placedByDay[di] as p (p.item.id)}
            {@const pv = preview(p, di)}
            {@const e = p.item}
            <div
              class="ev"
              data-touch-compact
              class:inv={e.kind === 'invitation'}
              class:tent={e.myPartStat === 'TENTATIVE' || e.myPartStat === 'NEEDS-ACTION'}
              class:declined={e.myPartStat === 'DECLINED' || e.status === 'CANCELLED'}
              class:free={!e.busy}
              class:dragging={drag && drag.kind !== 'create' && drag.e.id === e.id && drag.moved}
              class:short={p.end - p.start < 40}
              style:--c={color(e)}
              style:top="{(pv.top / 60) * HOUR}px"
              style:height="{Math.max(18, (pv.height / 60) * HOUR - 2)}px"
              style:left="calc({(p.col / p.cols) * 100}% + {pv.shift * 100}%)"
              style:width="calc({100 / p.cols}% - 4px)"
              role="button"
              tabindex="0"
              aria-label="{e.title}, {timeRange(e)}"
              onpointerdown={(ev) => startEventDrag(ev, e, di, 'move')}
              onpointermove={onEventMove}
              onpointerup={() => onEventUp(e)}
              onkeydown={(ev) => (ev.key === 'Enter' || ev.key === ' ') && onopen(e)}
            >
              <span class="et">{e.title}</span>
              <span class="em">
                {#if e.recurring}<Repeat size={10} />{/if}
                {#if e.meetingUrl}<Video size={10} />{/if}
                {timeRange(e)}{#if e.location}&nbsp;· {e.location}{/if}
              </span>
              {#if !e.readOnly}
                <span class="rs" role="presentation" onpointerdown={(ev) => startEventDrag(ev, e, di, 'resize')} onpointermove={onEventMove} onpointerup={() => onEventUp(e)}></span>
              {/if}
            </div>
          {/each}
          {#if d === today}
            <div class="now" style:top="{(nowMin / 60) * HOUR}px"></div>
          {/if}
        </div>
      {/each}
    </div>
  </div>
</div>

<style>
  .tg {
    --gutter: 56px;
    display: flex;
    flex-direction: column;
    min-height: 0;
    flex: 1;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
    overflow: hidden;
    user-select: none;
  }
  .tg.sec {
    --gutter: 96px;
  }
  .head,
  .allday {
    display: grid;
    grid-template-columns: var(--gutter) repeat(var(--days), minmax(0, 1fr));
    border-bottom: 1px solid var(--tm-line);
    padding-right: 10px;
  }
  .gutter {
    font-size: 11px;
    color: var(--tm-faint);
    display: flex;
    align-items: flex-end;
    justify-content: flex-end;
    padding: 0 8px 4px 0;
  }
  .tzl {
    font-size: 10.5px;
  }
  .lbl {
    align-items: center;
    padding: 0 6px 0 0;
    font-size: 10px;
    text-align: right;
    line-height: 1.1;
    overflow: hidden;
    word-break: break-word;
  }
  .dh {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 8px 0;
    border-left: 1px solid var(--tm-line);
  }
  .wd {
    font-size: 11.5px;
    color: var(--tm-muted);
    text-transform: uppercase;
  }
  .dn {
    font-size: 17px;
    font-weight: 600;
    min-width: 30px;
    height: 30px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 99px;
  }
  .dh.today .dn {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .adcols {
    grid-column: 2 / -1;
    display: grid;
    grid-template-columns: repeat(var(--days), minmax(0, 1fr));
    grid-template-rows: repeat(var(--lanes), 24px);
    min-height: 28px;
    padding: 2px 0;
    position: relative;
  }
  .adcell {
    grid-row: 1 / -1;
    border: none;
    border-left: 1px solid var(--tm-line);
    background: transparent;
    padding: 0;
    min-height: 24px;
  }
  .chip {
    z-index: 1;
    margin: 1px 3px;
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 0 6px;
    border: none;
    border-radius: 5px;
    font: inherit;
    font-size: 12px;
    font-weight: 500;
    color: var(--tm-fg);
    background: color-mix(in srgb, var(--c) 22%, var(--tm-surface));
    border-left: 3px solid var(--c);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
    cursor: pointer;
    text-align: left;
  }
  .chip span {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    display: grid;
    grid-template-columns: var(--gutter) 1fr;
  }
  .hr {
    height: var(--hour);
    font-size: 11px;
    color: var(--tm-faint);
    text-align: right;
    padding-right: 8px;
    position: relative;
  }
  .hr span,
  .hr small {
    position: relative;
    top: -7px;
  }
  .hr small {
    display: block;
    opacity: 0.75;
    top: -9px;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(var(--days), minmax(0, 1fr));
    position: relative;
  }
  .col {
    position: relative;
    border-left: 1px solid var(--tm-line);
    outline: none;
  }
  .col.today {
    background: color-mix(in srgb, var(--tm-accent) 3%, transparent);
  }
  .slot {
    height: var(--hour);
    border-top: 1px solid var(--tm-line);
  }
  .slot:first-child {
    border-top: none;
  }
  .ev {
    position: absolute;
    z-index: 2;
    margin-left: 2px;
    padding: 2px 6px;
    border-radius: 6px;
    font-size: 12px;
    line-height: 1.25;
    color: var(--tm-fg);
    background: color-mix(in srgb, var(--c) 18%, var(--tm-surface));
    border-left: 3px solid var(--c);
    overflow: hidden;
    cursor: pointer;
    display: flex;
    flex-direction: column;
    touch-action: none;
  }
  .ev:hover {
    background: color-mix(in srgb, var(--c) 26%, var(--tm-surface));
  }
  .ev.short {
    flex-direction: row;
    gap: 6px;
    align-items: baseline;
  }
  .ev.tent {
    background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--c) 18%, var(--tm-surface)) 0 6px, var(--tm-surface) 6px 10px);
  }
  .ev.inv,
  .chip.inv {
    border: 1px dashed var(--c);
    border-left-width: 3px;
  }
  .ev.declined .et {
    text-decoration: line-through;
    opacity: 0.7;
  }
  .ev.free {
    background: var(--tm-surface);
    border: 1px solid color-mix(in srgb, var(--c) 45%, transparent);
    border-left: 3px solid var(--c);
  }
  .ev.dragging {
    opacity: 0.85;
    box-shadow: var(--tm-shadow-lg);
    z-index: 5;
  }
  .et {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .em {
    font-size: 11px;
    color: var(--tm-muted);
    display: inline-flex;
    align-items: center;
    gap: 3px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .rs {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 7px;
    cursor: ns-resize;
  }
  .ghost {
    position: absolute;
    left: 2px;
    right: 4px;
    z-index: 3;
    border-radius: 6px;
    background: color-mix(in srgb, var(--tm-accent) 20%, transparent);
    border: 1px solid var(--tm-accent);
    font-size: 11.5px;
    padding: 2px 6px;
    color: var(--tm-accent-text);
    pointer-events: none;
  }
  .now {
    position: absolute;
    left: 0;
    right: 0;
    height: 2px;
    background: var(--tm-danger);
    z-index: 4;
    pointer-events: none;
  }
  .now::before {
    content: '';
    position: absolute;
    left: -5px;
    top: -4px;
    width: 10px;
    height: 10px;
    border-radius: 99px;
    background: var(--tm-danger);
  }
  @media (max-width: 639px) {
    .tg {
      --gutter: 44px;
      border-radius: var(--tm-radius);
    }
    .tg.sec {
      --gutter: 44px;
    }
    .hr small,
    .tzl {
      display: none;
    }
    .lbl {
      font-size: 0;
    }
    .em {
      display: none;
    }
  }
</style>
