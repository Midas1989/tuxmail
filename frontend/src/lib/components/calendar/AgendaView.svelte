<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Cake from '@lucide/svelte/icons/cake';
  import MapPin from '@lucide/svelte/icons/map-pin';
  import Repeat from '@lucide/svelte/icons/repeat';
  import Video from '@lucide/svelte/icons/video';
  import Mail from '@lucide/svelte/icons/mail';
  import { fmt, t } from '../../i18n/index.svelte';
  import type { CalendarEvent } from '../../types';
  import { onDay, zonedInstant, type DayKey } from '../../calendar/datemath';

  interface Props {
    days: DayKey[];
    events: CalendarEvent[];
    colors: Record<number, string>;
    tz: string;
    today: DayKey;
    onopen: (e: CalendarEvent) => void;
  }
  let { days, events, colors, tz, today, onopen }: Props = $props();

  const groups = $derived(days.map((d) => ({ d, list: events.filter((e) => onDay(e, d, tz)) })).filter((g) => g.list.length > 0));
</script>

<div class="ag">
  {#if groups.length === 0}
    <p class="empty">{t('calendar.noEvents')}</p>
  {/if}
  {#each groups as g (g.d)}
    <section>
      <h3 class:today={g.d === today}>{fmt().day(zonedInstant(g.d, 720, tz))}<small>{fmt().date(zonedInstant(g.d, 720, tz))}</small></h3>
      <ul>
        {#each g.list as e (e.id)}
          <li>
            <button class="row" style:--c={e.color || colors[e.calendarId] || '#64748b'} onclick={() => onopen(e)}>
              <span class="time">
                {#if e.allDay}{t('calendar.allDay')}{:else}{fmt().time(e.start)}<small>{fmt().time(e.end)}</small>{/if}
              </span>
              <span class="bar"></span>
              <span class="main">
                <span class="title">
                  {#if e.kind === 'birthday'}<Cake size={13} />{/if}
                  {#if e.kind === 'invitation'}<Mail size={13} />{/if}
                  {e.title}
                  {#if e.recurring}<Repeat size={12} />{/if}
                  {#if e.kind === 'invitation'}<span class="badge">{t('calendar.invitationBadge')}</span>{/if}
                </span>
                {#if e.location || e.meetingUrl}
                  <span class="meta">
                    {#if e.location}<MapPin size={12} /> {e.location}{/if}
                    {#if e.meetingUrl}<Video size={12} /> {t('calendar.event.onlineMeeting')}{/if}
                  </span>
                {/if}
              </span>
            </button>
          </li>
        {/each}
      </ul>
    </section>
  {/each}
</div>

<style>
  .ag {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .empty {
    color: var(--tm-muted);
    text-align: center;
    margin-top: 48px;
  }
  h3 {
    margin: 0 0 6px;
    font-size: 13.5px;
    font-weight: 650;
    display: flex;
    gap: 8px;
    align-items: baseline;
  }
  h3.today {
    color: var(--tm-accent-text);
  }
  h3 small {
    color: var(--tm-faint);
    font-weight: 500;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    overflow: hidden;
  }
  li + li {
    border-top: 1px solid var(--tm-line);
  }
  .row {
    width: 100%;
    display: flex;
    gap: 12px;
    align-items: center;
    padding: 10px 14px;
    border: none;
    background: none;
    font: inherit;
    color: var(--tm-fg);
    text-align: left;
    cursor: pointer;
  }
  .row:hover {
    background: var(--tm-accent-soft);
  }
  .time {
    width: 64px;
    flex: none;
    font-size: 12.5px;
    font-weight: 600;
    display: flex;
    flex-direction: column;
  }
  .time small {
    color: var(--tm-faint);
    font-weight: 500;
  }
  .bar {
    width: 4px;
    align-self: stretch;
    border-radius: 4px;
    background: var(--c);
  }
  .main {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .title {
    font-weight: 600;
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .meta {
    font-size: 12.5px;
    color: var(--tm-muted);
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
</style>
