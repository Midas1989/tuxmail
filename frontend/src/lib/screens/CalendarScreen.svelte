<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import { fmt, t } from '../i18n/index.svelte';
  import { prefs } from '../state';
  import ScreenShell from './ScreenShell.svelte';

  // Placeholder: static week grid. Real data comes from CalendarService (KONZEPT 9).
  let offset = $state(0);
  const days = $derived.by(() => {
    const now = new Date();
    const first = fmt().weekStart();
    const start = new Date(now);
    start.setDate(now.getDate() - ((now.getDay() - first + 7) % 7) + offset * 7);
    return Array.from({ length: 7 }, (_, i) => {
      const d = new Date(start);
      d.setDate(start.getDate() + i);
      return d;
    });
  });
  const secondary = $derived(prefs.region.secondaryTimeZone);
  function hourLabel(day: Date, h: number, tz?: string): string {
    const d = new Date(day);
    d.setHours(h, 0, 0, 0);
    return fmt().time(d, tz);
  }
  const hours = Array.from({ length: 11 }, (_, i) => 8 + i);
  const today = new Date().toDateString();
  // Demo events tied to the mock mails; `day` = weekday with Monday = 0.
  const events = [
    { day: 3, start: 10, len: 1, title: 'Teammeeting', color: '#9333ea' },
    { day: 1, start: 8, len: 1.5, title: 'Bodenplatte – Wagner Bau', color: '#2563eb' },
    { day: 5, start: 16, len: 3, title: 'Grillfest TSV', color: '#16a34a' },
  ];
</script>

<ScreenShell title={t('nav.calendar')} subtitle={t('calendar.placeholder')}>
  {#snippet actions()}
    <button class="icon-btn" aria-label={t('calendar.prev')} onclick={() => offset--}><ChevronLeft size={18} /></button>
    <button class="btn" onclick={() => (offset = 0)}>{t('common.date.today')}</button>
    <button class="icon-btn" aria-label={t('calendar.next')} onclick={() => offset++}><ChevronRight size={18} /></button>
  {/snippet}
  <div class="week" role="grid" aria-label={t('nav.calendar')}>
    <div class="corner"></div>
    {#each days as d (d.toISOString())}
      <div class="dh" class:today={d.toDateString() === today} role="columnheader">
        <span class="wd">{fmt().pattern(d, 'ddd')}</span>
        <span class="dn">{d.getDate()}</span>
      </div>
    {/each}
    {#each hours as h (h)}
      <div class="hr">{hourLabel(days[0], h)}{#if secondary}<small>{hourLabel(days[0], h, secondary)}</small>{/if}</div>
      {#each days as d (d.toISOString() + h)}
        <div class="cell" role="gridcell">
          {#if offset === 0}
            {#each events.filter((e) => e.day === (d.getDay() + 6) % 7 && Math.floor(e.start) === h) as e (e.title)}
              <div class="ev" style:--c={e.color} style:height="calc({e.len} * 100% - 4px)">{e.title}</div>
            {/each}
          {/if}
        </div>
      {/each}
    {/each}
  </div>
</ScreenShell>

<style>
  .week {
    display: grid;
    grid-template-columns: 56px repeat(7, minmax(90px, 1fr));
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
    overflow: auto;
  }
  .dh {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 8px 0;
    border-bottom: 1px solid var(--tm-line);
    border-left: 1px solid var(--tm-line);
  }
  .corner {
    border-bottom: 1px solid var(--tm-line);
  }
  .wd {
    font-size: 11.5px;
    color: var(--tm-muted);
    text-transform: uppercase;
  }
  .dn {
    font-size: 18px;
    font-weight: 600;
    width: 32px;
    height: 32px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 99px;
  }
  .dh.today .dn {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .hr small {
    display: block;
    color: var(--tm-faint);
    opacity: 0.8;
  }
  .hr {
    font-size: 11px;
    color: var(--tm-faint);
    text-align: right;
    padding: 2px 8px 0 0;
    height: 48px;
  }
  .cell {
    position: relative;
    border-left: 1px solid var(--tm-line);
    border-top: 1px solid var(--tm-line);
  }
  .ev {
    position: absolute;
    inset: 2px 4px auto;
    z-index: 1;
    padding: 4px 6px;
    border-radius: 6px;
    font-size: 12px;
    font-weight: 500;
    color: var(--tm-fg);
    background: color-mix(in srgb, var(--c) 18%, var(--tm-surface));
    border-left: 3px solid var(--c);
    overflow: hidden;
  }
</style>
