<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Bell from '@lucide/svelte/icons/bell';
  import Video from '@lucide/svelte/icons/video';
  import X from '@lucide/svelte/icons/x';
  import { app } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import type { CalendarEvent } from '../../types';
  import { safeHref } from '../../safeurl';

  // Reminders (VALARM) pushed by the core as calendar.reminder events, with
  // snooze, dismiss and a "Join" button for online meetings (KONZEPT 9.1).
  interface Shown {
    id: string;
    trigger: string;
    event: CalendarEvent;
  }
  let shown = $state<Shown[]>([]);

  $effect(() => {
    return app.api.subscribe((e) => {
      if (e.type !== 'calendar.reminder') return;
      void app.api.getEvent(e.itemId).then((event) => {
        if (shown.some((s) => s.id === e.itemId && s.trigger === e.state)) return;
        shown = [...shown, { id: e.itemId, trigger: e.state, event }];
        try {
          if (typeof Notification !== 'undefined' && Notification.permission === 'granted' && document.hidden) {
            new Notification(event.title, { body: event.allDay ? fmt().date(event.start + 'T12:00:00Z') : fmt().dateTime(event.start), tag: 'rem-' + e.itemId });
          }
        } catch {
          /* notifications unavailable */
        }
      });
    });
  });

  async function snooze(s: Shown, minutes: number) {
    shown = shown.filter((x) => x !== s);
    await app.api.snoozeReminder(s.id, s.trigger, minutes);
  }
</script>

{#if shown.length}
  <div class="rem" role="region" aria-label={t('calendar.reminder.title')}>
    {#each shown as s (s.id + s.trigger)}
      <div class="card" role="alert">
        <span class="ic"><Bell size={16} /></span>
        <div class="txt">
          <strong>{s.event.title}</strong>
          <span>{s.event.allDay ? fmt().date(s.event.start + 'T12:00:00Z') : `${fmt().relative(s.event.start)} · ${fmt().time(s.event.start)}`}{#if s.event.location}&nbsp;· {s.event.location}{/if}</span>
          <div class="acts">
            {#if safeHref(s.event.meetingUrl)}<a class="btn btn-primary" href={safeHref(s.event.meetingUrl)} target="_blank" rel="noopener noreferrer"><Video size={14} /> {t('calendar.event.join')}</a>{/if}
            <button class="btn" onclick={() => snooze(s, 5)}>{t('calendar.reminder.snooze', { n: 5 })}</button>
            <button class="btn" onclick={() => snooze(s, 15)}>{t('calendar.reminder.snooze', { n: 15 })}</button>
          </div>
        </div>
        <button class="icon-btn" aria-label={t('calendar.reminder.dismiss')} onclick={() => snooze(s, 0)}><X size={16} /></button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .rem {
    position: fixed;
    right: calc(16px + var(--tm-safe-right));
    bottom: calc(16px + var(--tm-safe-bottom));
    z-index: 200;
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: min(380px, calc(100vw - 32px));
  }
  .card {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 12px;
    border-radius: var(--tm-radius);
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
  }
  .ic {
    width: 30px;
    height: 30px;
    border-radius: 8px;
    display: grid;
    place-items: center;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    flex: none;
  }
  .txt {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 13px;
  }
  .txt span {
    color: var(--tm-muted);
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 6px;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .rem {
      bottom: 72px;
    }
  }
</style>
