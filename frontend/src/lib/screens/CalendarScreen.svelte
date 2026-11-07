<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { needsOnline } from '../state/connection.svelte';
  import { t as tOffline } from '../i18n/index.svelte';
  const notOffline = () => tOffline('connection.notAvailableOffline');
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import Plus from '@lucide/svelte/icons/plus';
  import PanelLeft from '@lucide/svelte/icons/panel-left';
  import Sparkles from '@lucide/svelte/icons/sparkles';
  import { app, prefs } from '../state';
  import { fmt, t } from '../i18n/index.svelte';
  import { tk } from '../calendar/i18n';
  import { phone } from '../phone.svelte';
  import type { CalendarEvent, EventInput, PimCalendar, RecurrenceScope } from '../types';
  import { dayKeyIn, rangeOf, stepAnchor, viewDays, zonedInstant, type CalendarView, type DayKey } from '../calendar/datemath';
  import { parseQuick } from '../calendar/quickparse';
  import TimeGrid from '../components/calendar/TimeGrid.svelte';
  import MonthView from '../components/calendar/MonthView.svelte';
  import AgendaView from '../components/calendar/AgendaView.svelte';
  import MiniMonth from '../components/calendar/MiniMonth.svelte';
  import CalendarSidebar from '../components/calendar/CalendarSidebar.svelte';
  import EventDialog, { type EventDraft } from '../components/calendar/EventDialog.svelte';
  import ScopeDialog from '../components/calendar/ScopeDialog.svelte';
  import TasksView from '../components/calendar/TasksView.svelte';

  type Mode = CalendarView | 'tasks';
  const MODES: Mode[] = ['day', 'workweek', 'week', 'month', 'agenda', 'tasks'];
  const STORE = 'tuxmail.calendar.view';
  const narrow = typeof matchMedia !== 'undefined' && matchMedia('(max-width: 639px), (pointer: coarse) and (max-height: 500px)').matches;

  function initialMode(): Mode {
    try {
      const v = localStorage.getItem(STORE) as Mode | null;
      if (v && MODES.includes(v)) return v;
    } catch {
      /* private mode */
    }
    return narrow ? 'agenda' : 'week';
  }

  const tz = $derived(fmt().tz);
  const weekStart = $derived(fmt().weekStart());
  const secondary = $derived(prefs.region.secondaryTimeZone || undefined);
  let mode = $state<Mode>(initialMode());
  let today = $state(dayKeyIn(new Date(), fmt().tz));
  let anchor = $state<DayKey>(dayKeyIn(new Date(), fmt().tz));
  let calendars = $state<PimCalendar[]>([]);
  let events = $state<CalendarEvent[]>([]);
  let loading = $state(false);
  let sidebarOpen = $state(!narrow);
  let quick = $state('');
  let dialog = $state<{ event: CalendarEvent | null; draft?: EventDraft } | null>(null);
  let pendingMove = $state<{ e: CalendarEvent; start: string; end: string } | null>(null);
  let reloadKey = $state(0);

  $effect(() => {
    try {
      localStorage.setItem(STORE, mode);
    } catch {
      /* ignore */
    }
  });

  // Phones show three days instead of seven narrow columns; the stored choice stays "week".
  const view = $derived<CalendarView>(mode === 'tasks' ? 'week' : phone.current && (mode === 'week' || mode === 'workweek') ? 'threeday' : mode);
  const days = $derived(viewDays(view, anchor, weekStart));
  const colors = $derived(Object.fromEntries(calendars.map((c) => [c.id, c.color])));
  const marked = $derived(new Set(events.map((e) => (e.allDay ? e.start : dayKeyIn(new Date(e.start), tz)))));

  const title = $derived.by(() => {
    const mid = zonedInstant(anchor, 720, tz);
    // Phones: short titles that fit next to the navigation buttons ("Sa., 7. Nov.", "7.–9. Nov.", "Nov. 2026").
    if (mode === 'day') return phone.current ? fmt().pattern(mid, 'ddd, D. MMM') : fmt().dateLong(mid);
    if (mode === 'month' || mode === 'tasks') return fmt().pattern(mid, phone.current ? 'MMM YYYY' : 'MMMM YYYY');
    const a = zonedInstant(days[0], 720, tz);
    const b = zonedInstant(days[days.length - 1], 720, tz);
    if (phone.current) return days[0].slice(0, 7) === days[days.length - 1].slice(0, 7) ? `${fmt().pattern(a, 'D')}.–${fmt().pattern(b, 'D. MMM')}` : `${fmt().pattern(a, 'D. MMM')} – ${fmt().pattern(b, 'D. MMM')}`;
    return `${fmt().dateShort(a)} – ${fmt().date(b)}`;
  });

  async function loadCalendars() {
    calendars = await app.api.listCalendars();
  }

  let seq = 0;
  async function loadEvents() {
    if (mode === 'tasks') return;
    const my = ++seq;
    loading = true;
    try {
      const r = rangeOf(days, tz);
      const list = await app.api.listEvents({ from: r.from.toISOString(), to: r.to.toISOString() });
      if (my === seq) events = list;
    } finally {
      if (my === seq) loading = false;
    }
  }

  $effect(() => {
    void loadCalendars();
    const off = app.api.subscribe((e) => {
      if (e.type === 'calendar.changed' || e.type === 'calendar.invitation' || e.type === 'contacts.changed') {
        void loadCalendars();
        void loadEvents();
        reloadKey++;
      }
    });
    const tick = setInterval(() => (today = dayKeyIn(new Date(), tz)), 60_000);
    return () => {
      off();
      clearInterval(tick);
    };
  });

  $effect(() => {
    void days;
    void mode;
    void loadEvents();
  });

  function go(dir: number) {
    anchor = stepAnchor(view, anchor, dir);
  }

  // Phones: swipe left/right moves the shown range (not in the task list or while a dialog is open).
  let swipe: { x: number; y: number } | null = null;
  function swipeStart(e: TouchEvent) {
    const p = e.touches[0];
    swipe = e.touches.length === 1 && mode !== 'tasks' && !dialog ? { x: p.clientX, y: p.clientY } : null;
  }
  function swipeEnd(e: TouchEvent) {
    const s = swipe;
    swipe = null;
    if (!s || !phone.current) return;
    const p = e.changedTouches[0];
    const dx = p.clientX - s.x;
    const dy = p.clientY - s.y;
    if (Math.abs(dx) > 60 && Math.abs(dx) > 2 * Math.abs(dy)) go(dx < 0 ? 1 : -1);
  }

  function onkey(e: KeyboardEvent) {
    const tgt = e.target as HTMLElement;
    if (dialog || tgt.closest('input,textarea,select,[contenteditable]')) return;
    const keys: Record<string, Mode> = { d: 'day', w: 'week', x: 'workweek', m: 'month', a: 'agenda', t: 'tasks' };
    if (keys[e.key]) mode = keys[e.key];
    else if (e.key === 'ArrowLeft' || e.key === 'j') go(-1);
    else if (e.key === 'ArrowRight' || e.key === 'k') go(1);
    else if (e.key === 'n') openNew();
  }

  function openNew(draft?: Partial<EventDraft>) {
    const now = new Date();
    const m = Math.ceil((now.getHours() * 60 + now.getMinutes()) / 30) * 30;
    dialog = { event: null, draft: { day: draft?.day ?? anchor, startMin: draft?.startMin ?? m, endMin: draft?.endMin ?? m + 60, allDay: draft?.allDay ?? false, title: draft?.title } };
  }

  function oncreate(day: DayKey, startMin: number, endMin: number, allDay: boolean) {
    openNew({ day, startMin, endMin, allDay });
  }

  async function open(e: CalendarEvent) {
    dialog = { event: e };
  }

  function toInput(e: CalendarEvent, start: string, end: string): EventInput {
    return {
      calendarId: e.calendarId,
      title: e.title,
      description: e.description,
      location: e.location,
      url: e.url,
      meetingUrl: e.meetingUrl,
      start,
      end,
      allDay: e.allDay,
      timeZone: e.timeZone || tz,
      recurrence: null,
      attendees: e.attendees,
      alarms: e.alarms,
      busy: e.busy,
      categories: e.categories,
    };
  }

  async function doMove(e: CalendarEvent, start: string, end: string, scope: RecurrenceScope) {
    pendingMove = null;
    const before = { start: e.start, end: e.end };
    // Optimistic update.
    events = events.map((x) => (x.id === e.id ? { ...x, start, end } : x));
    try {
      const saved = await app.api.updateEvent(e.id, toInput(e, start, end), scope);
      app.toast(t('calendar.moved', { title: e.title }), {
        label: t('common.action.undo'),
        run: () => void app.api.updateEvent(saved.id, toInput(saved, before.start, before.end), scope).then(loadEvents),
      });
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
    await loadEvents();
  }

  function onmove(e: CalendarEvent, start: string, end: string) {
    if (e.readOnly) return;
    if (e.recurring && e.recurrenceId) pendingMove = { e, start, end };
    else void doMove(e, start, end, 'all');
  }

  async function quickAdd() {
    const text = quick.trim();
    if (!text) return;
    const r = parseQuick(text, today);
    if (!r.matched || !r.title) {
      openNew({ title: text, day: anchor });
      quick = '';
      return;
    }
    const start = r.allDay ? r.day : zonedInstant(r.day, r.startMin ?? 9 * 60, tz).toISOString();
    const end = r.allDay ? stepAnchor('day', r.day, 1) : new Date(new Date(start).getTime() + r.durationMin * 60_000).toISOString();
    try {
      const ev = await app.api.createEvent({ calendarId: 0, title: r.title, start, end, allDay: r.allDay, timeZone: tz, alarms: [{ minutesBefore: 15 }] });
      app.toast(t('calendar.created', { title: ev.title, when: r.allDay ? fmt().date(zonedInstant(r.day, 720, tz)) : fmt().dateTime(ev.start) }), {
        label: t('common.action.edit'),
        run: () => (dialog = { event: ev }),
      });
      anchor = r.day;
      quick = '';
      await loadEvents();
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  const quickPreview = $derived.by(() => {
    if (!quick.trim()) return '';
    const r = parseQuick(quick, today);
    if (!r.matched) return '';
    const when = r.allDay ? fmt().date(zonedInstant(r.day, 720, tz)) : fmt().dateTime(zonedInstant(r.day, r.startMin ?? 540, tz));
    return `${r.title || '…'} · ${when}${r.allDay ? '' : ' · ' + t('calendar.minutes', { n: r.durationMin })}`;
  });
</script>

<svelte:window onkeydown={onkey} />

<section class="cal" aria-labelledby="cal-title">
  <header>
    <button class="icon-btn" aria-label={t('calendar.toggleSidebar')} aria-expanded={sidebarOpen} onclick={() => (sidebarOpen = !sidebarOpen)}><PanelLeft size={18} /></button>
    <h1 id="cal-title">{mode === 'tasks' ? t('tasks.title') : title}</h1>
    {#if mode !== 'tasks'}
      <div class="nav">
        <button class="icon-btn" aria-label={t('calendar.prev')} onclick={() => go(-1)}><ChevronLeft size={18} /></button>
        <button class="btn" onclick={() => (anchor = today)}>{t('common.date.today')}</button>
        <button class="icon-btn" aria-label={t('calendar.next')} onclick={() => go(1)}><ChevronRight size={18} /></button>
      </div>
    {/if}
    <span class="sp"></span>
    {#if mode !== 'tasks'}
      <form
        class="quick"
        onsubmit={(e) => {
          e.preventDefault();
          void quickAdd();
        }}
      >
        <Sparkles size={15} />
        <input bind:value={quick} placeholder={t('calendar.quickPlaceholder')} aria-label={t('calendar.quickLabel')} />
        {#if quickPreview}<span class="qp" aria-live="polite">{quickPreview}</span>{/if}
      </form>
    {/if}
    <div class="seg views" role="radiogroup" aria-label={t('calendar.viewLabel')}>
      {#each MODES as m (m)}
        <button role="radio" aria-checked={mode === m} class:hide-narrow={m === 'workweek'} onclick={() => (mode = m)}>{tk(`calendar.view.${m === 'week' && phone.current ? 'threeday' : m}`)}</button>
      {/each}
    </div>
    {#if mode !== 'tasks'}
      <button use:needsOnline={notOffline} class="btn btn-primary" onclick={() => openNew()}><Plus size={15} /> <span class="lbl">{t('calendar.newEvent')}</span></button>
    {/if}
  </header>
  <div class="layout">
    {#if sidebarOpen}
      <aside>
        <MiniMonth selected={anchor} {today} {weekStart} {tz} {marked} onselect={(d) => (anchor = d)} />
        <CalendarSidebar
          {calendars}
          onchanged={() => {
            void loadCalendars();
            void loadEvents();
            reloadKey++;
          }}
        />
      </aside>
    {/if}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="main" class:loading ontouchstart={swipeStart} ontouchend={swipeEnd}>
      {#if mode === 'tasks'}
        <TasksView {calendars} {tz} {weekStart} {reloadKey} />
      {:else if mode === 'month'}
        <MonthView {days} month={Number(anchor.slice(5, 7))} {events} {colors} {tz} {today} onopen={open} oncreate={(d) => oncreate(d, 9 * 60, 10 * 60, false)} {onmove} onday={(d) => ((anchor = d), (mode = 'day'))} />
      {:else if mode === 'agenda'}
        <AgendaView {days} {events} {colors} {tz} {today} onopen={open} />
      {:else}
        <TimeGrid {days} {events} {colors} {tz} secondaryTz={secondary} {today} onopen={open} {oncreate} {onmove} />
      {/if}
    </div>
  </div>
</section>

{#if dialog}
  <EventDialog event={dialog.event} draft={dialog.draft} {calendars} {tz} {weekStart} onclose={() => (dialog = null)} onsaved={loadEvents} />
{/if}
{#if pendingMove}
  <ScopeDialog mode="edit" onclose={() => ((pendingMove = null), void loadEvents())} onchoose={(s) => pendingMove && doMove(pendingMove.e, pendingMove.start, pendingMove.end, s)} />
{/if}

<style>
  .cal {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    background: var(--tm-bg);
  }
  header {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 20px;
    background: var(--tm-surface);
    border-bottom: 1px solid var(--tm-line);
    flex-wrap: wrap;
  }
  h1 {
    margin: 0;
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.015em;
    text-transform: capitalize;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .nav {
    display: flex;
    gap: 4px;
    align-items: center;
  }
  .sp {
    flex: 1;
  }
  .quick {
    display: flex;
    align-items: center;
    gap: 6px;
    position: relative;
    height: 34px;
    padding: 0 10px;
    border: 1px solid var(--tm-line);
    border-radius: 99px;
    background: var(--tm-surface-2, var(--tm-bg));
    color: var(--tm-muted);
    width: min(320px, 32vw);
  }
  .quick input {
    flex: 1;
    min-width: 0;
    border: none;
    outline: none;
    background: none;
    font: inherit;
    font-size: 13px;
    color: var(--tm-fg);
  }
  .qp {
    position: absolute;
    top: calc(100% + 4px);
    left: 10px;
    right: 0;
    z-index: 20;
    font-size: 12px;
    padding: 4px 8px;
    border-radius: 6px;
    background: var(--tm-fg);
    color: var(--tm-surface);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .layout {
    flex: 1;
    min-height: 0;
    display: flex;
  }
  aside {
    width: 248px;
    flex: none;
    padding: 12px 12px 24px;
    overflow-y: auto;
    border-right: 1px solid var(--tm-line);
    background: var(--tm-sidebar);
  }
  .main {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: 14px 16px 16px;
    transition: opacity var(--tm-dur);
  }
  .main.loading {
    opacity: 0.85;
  }
  @media (max-width: 1099px) {
    .quick {
      order: 10;
      width: 100%;
    }
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    header {
      padding: 10px 12px;
      gap: 6px;
    }
    /* The title gets its own line so it is always readable in full. */
    h1 {
      font-size: 16px;
      flex: 1 1 100%;
      order: -1;
      white-space: nowrap;
    }
    .sp {
      display: none;
    }
    .views {
      order: 11;
      width: 100%;
      overflow-x: auto;
    }
    .hide-narrow {
      display: none !important;
    }
    .lbl {
      display: none;
    }
    aside {
      position: absolute;
      z-index: 30;
      top: 0;
      bottom: 0;
      left: 0;
      width: 82vw;
      box-shadow: var(--tm-shadow-lg);
    }
    .layout {
      position: relative;
    }
    .main {
      padding: 8px;
    }
  }
</style>
