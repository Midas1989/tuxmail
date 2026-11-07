<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  /** Initial values for a new event. */
  export interface EventDraft {
    day: string;
    startMin: number;
    endMin: number;
    allDay: boolean;
    title?: string;
    calendarId?: number;
  }

  /** Random Jitsi room on the public instance (KONZEPT 9.1). */
  export function jitsiRoom(title: string): string {
    const slug = title
      .toLowerCase()
      .normalize('NFD')
      .replace(/[̀-ͯ]/g, '')
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-|-$/g, '')
      .slice(0, 24);
    const rnd = Array.from(crypto.getRandomValues(new Uint8Array(6)), (b) => b.toString(36).padStart(2, '0')).join('');
    return `https://meet.jit.si/${slug ? slug + '-' : ''}${rnd}`;
  }
</script>

<script lang="ts">
  import MeetingLinkButton from '../comfort/MeetingLinkButton.svelte'; // Jitsi / Nextcloud Talk / Teams
  import Trash from '@lucide/svelte/icons/trash-2';
  import Video from '@lucide/svelte/icons/video';
  import MapPin from '@lucide/svelte/icons/map-pin';
  import X from '@lucide/svelte/icons/x';
  import Plus from '@lucide/svelte/icons/plus';
  import Users from '@lucide/svelte/icons/users';
  import Copy from '@lucide/svelte/icons/copy';
  import { app } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import { tk } from '../../calendar/i18n';
  import type { AddressSuggestion, Attendee, CalendarEvent, EventInput, FreeBusy, Invitation, PimCalendar, RecurrenceRule, RecurrenceScope } from '../../types';
  import { addDays, dayKeyIn, diffDays, minutesIn, zonedInstant } from '../../calendar/datemath';
  import Modal from '../common/Modal.svelte';
  import RecurrenceEditor from './RecurrenceEditor.svelte';
  import ScopeDialog from './ScopeDialog.svelte';
  import InvitationCard from './InvitationCard.svelte';
  import { safeHref } from '../../safeurl';

  interface Props {
    event: CalendarEvent | null;
    draft?: EventDraft;
    calendars: PimCalendar[];
    tz: string;
    weekStart: number;
    onclose: () => void;
    onsaved?: () => void;
  }
  let { event, draft, calendars, tz, weekStart, onclose, onsaved }: Props = $props();

  const hm = (m: number) => `${String(Math.floor(m / 60) % 24).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
  const toMin = (s: string) => {
    const [h, m] = s.split(':').map(Number);
    return (h || 0) * 60 + (m || 0);
  };

  const writable = $derived(calendars.filter((c) => !c.readOnly && c.components.includes('VEVENT')));
  const readOnly = $derived(!!event?.readOnly);

  // ---- form state ---------------------------------------------------------
  const init = (() => {
    if (event) {
      const s = new Date(event.start);
      const e = new Date(event.end);
      return {
        title: event.title,
        calendarId: event.calendarId,
        allDay: event.allDay,
        startDay: event.allDay ? event.start : dayKeyIn(s, tz),
        startTime: event.allDay ? '09:00' : hm(minutesIn(s, tz)),
        endDay: event.allDay ? addDays(event.end, -1) : dayKeyIn(new Date(e.getTime() - 1), tz),
        endTime: event.allDay ? '10:00' : hm(minutesIn(e, tz)) === '00:00' ? '24:00' : hm(minutesIn(e, tz)),
        location: event.location ?? '',
        meetingUrl: event.meetingUrl ?? '',
        description: event.description ?? '',
        recurrence: event.recurrence ?? null,
        attendees: event.attendees.filter((a) => a.email !== event.organizer?.email || !event.isOrganizer),
        alarms: event.alarms.map((a) => a.minutesBefore),
        busy: event.busy,
        categories: event.categories.join(', '),
      };
    }
    const d = draft ?? { day: dayKeyIn(new Date(), tz), startMin: 9 * 60, endMin: 10 * 60, allDay: false };
    return {
      title: d.title ?? '',
      calendarId: d.calendarId ?? calendars.find((c) => c.isDefault && !c.readOnly)?.id ?? calendars.find((c) => !c.readOnly)?.id ?? 0,
      allDay: d.allDay,
      startDay: d.day,
      startTime: hm(d.startMin),
      endDay: d.endMin >= 24 * 60 ? addDays(d.day, Math.floor(d.endMin / 1440)) : d.day,
      endTime: hm(d.endMin % 1440),
      location: '',
      meetingUrl: '',
      description: '',
      recurrence: null as RecurrenceRule | null,
      attendees: [] as Attendee[],
      alarms: [15],
      busy: true,
      categories: '',
    };
  })();

  let title = $state(init.title);
  let calendarId = $state(init.calendarId);
  let allDay = $state(init.allDay);
  let startDay = $state(init.startDay);
  let startTime = $state(init.startTime);
  let endDay = $state(init.endDay);
  let endTime = $state(init.endTime);
  let location = $state(init.location);
  let meetingUrl = $state(init.meetingUrl);
  let description = $state(init.description);
  let recurrence = $state<RecurrenceRule | null>(init.recurrence);
  let attendees = $state<Attendee[]>(init.attendees);
  let alarms = $state<number[]>(init.alarms);
  let busy = $state(init.busy);
  let categories = $state(init.categories);
  let notifyAttendees = $state(true);
  let saving = $state(false);
  let error = $state('');
  let scopeFor = $state<'edit' | 'delete' | null>(null);
  let invitation = $state<Invitation | null>(null);

  $effect(() => {
    if (event?.kind === 'invitation' && event.invitationId) {
      void app.api.listInvitations().then((l) => (invitation = l.find((i) => i.id === event.invitationId) ?? null));
    }
  });

  // Keep the end after the start when the start moves.
  let lastStart = `${init.startDay} ${init.startTime}`;
  $effect(() => {
    const cur = `${startDay} ${startTime}`;
    if (cur === lastStart) return;
    const [pd, pt] = lastStart.split(' ');
    const delta = diffDays(pd, startDay) * 1440 + (toMin(startTime) - toMin(pt));
    lastStart = cur;
    const end = zonedInstant(endDay, toMin(endTime), tz).getTime() + delta * 60_000;
    const e = new Date(end);
    endDay = dayKeyIn(new Date(end - 1), tz);
    endTime = hm(minutesIn(e, tz)) === '00:00' && endDay !== dayKeyIn(e, tz) ? '24:00' : hm(minutesIn(e, tz));
  });

  // ---- attendees ------------------------------------------------------------
  let attendeeQuery = $state('');
  let suggestions = $state<AddressSuggestion[]>([]);
  let fb = $state<Record<string, FreeBusy>>({});
  let suggestTimer: ReturnType<typeof setTimeout> | undefined;

  $effect(() => {
    const q = attendeeQuery.trim();
    clearTimeout(suggestTimer);
    if (q.length < 2) {
      suggestions = [];
      return;
    }
    suggestTimer = setTimeout(() => {
      void app.api.autocompleteAddresses(q, 6).then((s) => (suggestions = s.filter((x) => !attendees.some((a) => a.email === x.email))));
    }, 150);
  });

  function addAttendee(a: { email: string; name?: string }) {
    const email = a.email.trim().toLowerCase();
    if (!email.includes('@') || attendees.some((x) => x.email === email)) return;
    attendees = [...attendees, { email, name: a.name || undefined, role: 'REQ-PARTICIPANT', partStat: 'NEEDS-ACTION', rsvp: true }];
    attendeeQuery = '';
    suggestions = [];
  }

  const range = $derived(times());
  $effect(() => {
    const emails = attendees.map((a) => a.email);
    if (!emails.length || !range) return;
    const from = new Date(range.start.getTime() - 4 * 3_600_000).toISOString();
    const to = new Date(range.end.getTime() + 4 * 3_600_000).toISOString();
    void app.api
      .getFreeBusy({ attendees: emails, from, to })
      .then((list) => (fb = Object.fromEntries(list.map((x) => [x.email, x]))))
      .catch(() => {});
  });

  function availability(email: string): 'busy' | 'free' | 'unknown' {
    const f = fb[email];
    if (!f || f.source === 'unknown' || !range) return 'unknown';
    return f.busy.some((b) => Date.parse(b.start) < range.end.getTime() && Date.parse(b.end) > range.start.getTime()) ? 'busy' : 'free';
  }

  function times(): { start: Date; end: Date } | null {
    if (!startDay) return null;
    if (allDay) return { start: zonedInstant(startDay, 0, tz), end: zonedInstant(addDays(endDay || startDay, 1), 0, tz) };
    return { start: zonedInstant(startDay, toMin(startTime), tz), end: zonedInstant(endDay || startDay, toMin(endTime), tz) };
  }

  // ---- save -----------------------------------------------------------------
  function input(): EventInput | null {
    if (!title.trim()) {
      error = t('calendar.event.titleRequired');
      return null;
    }
    const r = times();
    if (!r || r.end.getTime() <= r.start.getTime()) {
      error = t('calendar.event.endBeforeStart');
      return null;
    }
    return {
      calendarId,
      title: title.trim(),
      description: description || undefined,
      location: location || undefined,
      meetingUrl: meetingUrl || undefined,
      start: allDay ? startDay : r.start.toISOString(),
      end: allDay ? addDays(endDay || startDay, 1) : r.end.toISOString(),
      allDay,
      timeZone: tz,
      recurrence: recurrence ?? (event?.recurring ? { freq: '' } : null),
      attendees,
      alarms: alarms.map((m) => ({ minutesBefore: m })),
      busy,
      categories: categories
        .split(',')
        .map((c) => c.trim())
        .filter(Boolean),
      notifyAttendees,
    };
  }

  async function save(scope?: RecurrenceScope) {
    error = '';
    const inp = input();
    if (!inp) return;
    if (event?.recurring && event.recurrenceId && !scope) {
      scopeFor = 'edit';
      return;
    }
    scopeFor = null;
    saving = true;
    try {
      if (event) await app.api.updateEvent(event.id, inp, scope ?? 'all');
      else await app.api.createEvent(inp);
      onsaved?.();
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  async function remove(scope?: RecurrenceScope) {
    if (!event) return;
    if (event.recurring && event.recurrenceId && !scope) {
      scopeFor = 'delete';
      return;
    }
    scopeFor = null;
    try {
      await app.api.deleteEvent(event.id, scope ?? 'all');
      app.toast(t('calendar.event.deleted'));
      onsaved?.();
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function resolve(keep: boolean) {
    if (!event) return;
    await app.api.resolveEventConflict(event.objectId, keep);
    onsaved?.();
    onclose();
  }

  const ALARM_CHOICES = [0, 5, 10, 15, 30, 60, 120, 1440, 2880, 10080];
  function alarmLabel(m: number): string {
    if (m === 0) return t('calendar.event.alarmAtStart');
    if (m % 1440 === 0) return t('calendar.event.alarmDays', { n: m / 1440 });
    if (m % 60 === 0) return t('calendar.event.alarmHours', { n: m / 60 });
    return t('calendar.event.alarmMinutes', { n: m });
  }
  const mapUrl = $derived(location && !/^https?:/i.test(location) ? `https://www.openstreetmap.org/search?query=${encodeURIComponent(location)}` : '');
  const heading = $derived(event ? (readOnly ? event.title : t('calendar.event.editTitle')) : t('calendar.event.newTitle'));
</script>

<Modal title={heading} {onclose} width="640px">
  {#if event?.kind === 'invitation'}
    {#if invitation}<InvitationCard {invitation} onchange={() => onsaved?.()} />{/if}
  {:else if readOnly}
    <dl class="ro">
      <dt>{t('calendar.event.when')}</dt>
      <dd>{event!.allDay ? fmt().dateLong(event!.start + 'T12:00:00Z') : `${fmt().dateTime(event!.start)} – ${fmt().time(event!.end)}`}</dd>
      {#if event!.birthYear}<dt>{t('calendar.birthday')}</dt><dd>{t('calendar.age', { n: Number(event!.start.slice(0, 4)) - event!.birthYear })}</dd>{/if}
      {#if event!.location}<dt>{t('calendar.event.location')}</dt><dd>{event!.location}</dd>{/if}
      {#if event!.description}<dt>{t('calendar.event.description')}</dt><dd class="pre">{event!.description}</dd>{/if}
      <dt>{t('calendar.event.calendar')}</dt>
      <dd>{calendars.find((c) => c.id === event!.calendarId)?.name ?? ''} · {t('calendar.readOnly')}</dd>
    </dl>
  {:else}
    {#if event?.conflict}
      <div class="conflict">
        <span>{t('calendar.event.conflict')}</span>
        <button class="btn" onclick={() => resolve(true)}>{t('calendar.event.keepCopy')}</button>
        <button class="btn btn-ghost" onclick={() => resolve(false)}>{t('calendar.event.discardCopy')}</button>
      </div>
    {/if}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <input class="input big" bind:value={title} placeholder={t('calendar.event.titlePlaceholder')} aria-label={t('calendar.event.title')} data-autofocus />
      <div class="grid2">
        <label>
          <span class="label">{t('calendar.event.calendar')}</span>
          <select class="input" bind:value={calendarId}>
            {#each writable as c (c.id)}<option value={c.id}>{c.name}{c.accountName ? ` (${c.accountName})` : ''}</option>{/each}
          </select>
        </label>
        <label class="check"><input type="checkbox" bind:checked={allDay} /> {t('calendar.allDay')}</label>
      </div>
      <div class="when">
        <label><span class="label">{t('calendar.event.start')}</span><input class="input" type="date" bind:value={startDay} required /></label>
        {#if !allDay}<label><span class="label sr-only">{t('calendar.event.startTime')}</span><input class="input" type="time" step="900" bind:value={startTime} aria-label={t('calendar.event.startTime')} /></label>{/if}
        <span class="dash">–</span>
        <label><span class="label">{t('calendar.event.end')}</span><input class="input" type="date" bind:value={endDay} required /></label>
        {#if !allDay}<label><span class="label sr-only">{t('calendar.event.endTime')}</span><input class="input" type="time" step="900" bind:value={endTime} aria-label={t('calendar.event.endTime')} /></label>{/if}
      </div>
      {#if !allDay}<p class="tz">{t('calendar.event.timeZone', { tz })}</p>{/if}
      <RecurrenceEditor bind:value={recurrence} start={startDay} {tz} {weekStart} />
      <label>
        <span class="label">{t('calendar.event.location')}</span>
        <span class="with-btn">
          <input class="input" bind:value={location} placeholder={t('calendar.event.locationPlaceholder')} />
          {#if mapUrl}<a class="icon-btn" href={mapUrl} target="_blank" rel="noopener noreferrer" title={t('calendar.event.openMap')} aria-label={t('calendar.event.openMap')}><MapPin size={16} /></a>{/if}
        </span>
      </label>
      <label>
        <span class="label">{t('calendar.event.meetingUrl')}</span>
        <span class="with-btn">
          <input class="input" type="url" bind:value={meetingUrl} placeholder="https://" />
          {#if meetingUrl}
            <a class="icon-btn" href={safeHref(meetingUrl)} target="_blank" rel="noopener noreferrer" title={t('calendar.event.join')} aria-label={t('calendar.event.join')}><Video size={16} /></a>
            <button type="button" class="icon-btn" title={t('calendar.event.copyLink')} aria-label={t('calendar.event.copyLink')} onclick={() => navigator.clipboard?.writeText(meetingUrl)}><Copy size={15} /></button>
          {:else}
            <MeetingLinkButton
              {title}
              participants={attendees.map((a) => a.email)}
              fallback={() => jitsiRoom(title)}
              onlink={(l) => {
                meetingUrl = l.url;
                if (!location) location = l.url;
                description = description ? `${description}\n\n${l.description}` : l.description;
              }}
            />
          {/if}
        </span>
      </label>
      <div class="att">
        <span class="label"><Users size={13} /> {t('calendar.event.attendees')}</span>
        {#if attendees.length}
          <ul>
            {#each attendees as a, i (a.email)}
              {@const av = availability(a.email)}
              <li>
                <span class="dot" data-av={av} title={tk(`calendar.event.fb.${av}`)}></span>
                <span class="an">{a.name || a.email}{#if a.name}<small>{a.email}</small>{/if}</span>
                <span class="ps" data-ps={a.partStat}>{tk(`calendar.partstat.${a.partStat || 'NEEDS-ACTION'}`)}</span>
                <label class="opt"><input type="checkbox" checked={a.role === 'OPT-PARTICIPANT'} onchange={(e) => (attendees[i] = { ...a, role: (e.currentTarget as HTMLInputElement).checked ? 'OPT-PARTICIPANT' : 'REQ-PARTICIPANT' })} /> {t('calendar.event.optional')}</label>
                <button type="button" class="icon-btn sm" aria-label={t('calendar.event.removeAttendee', { name: a.name || a.email })} onclick={() => (attendees = attendees.filter((x) => x !== a))}><X size={14} /></button>
              </li>
            {/each}
          </ul>
        {/if}
        <div class="addatt">
          <input
            class="input"
            bind:value={attendeeQuery}
            placeholder={t('calendar.event.addAttendee')}
            aria-label={t('calendar.event.addAttendee')}
            onkeydown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                if (suggestions[0]) addAttendee(suggestions[0]);
                else addAttendee({ email: attendeeQuery });
              }
            }}
          />
          {#if suggestions.length}
            <ul class="sugg" role="listbox">
              {#each suggestions as s (s.email)}
                <li role="option" aria-selected="false">
                  <button type="button" onclick={() => addAttendee(s)}><strong>{s.name || s.email}</strong> <small>{s.email}</small></button>
                </li>
              {/each}
            </ul>
          {/if}
        </div>
        {#if attendees.length}
          <label class="check"><input type="checkbox" bind:checked={notifyAttendees} /> {t('calendar.event.notify')}</label>
        {/if}
      </div>
      <div class="alarms">
        <span class="label">{t('calendar.event.alarms')}</span>
        <div class="chips">
          {#each alarms as m, i (i)}
            <span class="chip">{alarmLabel(m)}<button type="button" aria-label={t('calendar.event.removeAlarm')} onclick={() => (alarms = alarms.filter((_, j) => j !== i))}><X size={12} /></button></span>
          {/each}
          <select
            class="input sm"
            aria-label={t('calendar.event.addAlarm')}
            value=""
            onchange={(e) => {
              const v = (e.currentTarget as HTMLSelectElement).value;
              if (v !== '') alarms = [...new Set([...alarms, Number(v)])].sort((a, b) => a - b);
              (e.currentTarget as HTMLSelectElement).value = '';
            }}
          >
            <option value="">{t('calendar.event.addAlarm')}</option>
            {#each ALARM_CHOICES.filter((c) => !alarms.includes(c)) as c (c)}<option value={c}>{alarmLabel(c)}</option>{/each}
          </select>
        </div>
      </div>
      <div class="grid2">
        <label>
          <span class="label">{t('calendar.event.showAs')}</span>
          <select class="input" bind:value={busy}>
            <option value={true}>{t('calendar.event.busy')}</option>
            <option value={false}>{t('calendar.event.free')}</option>
          </select>
        </label>
        <label><span class="label">{t('calendar.event.categories')}</span><input class="input" bind:value={categories} /></label>
      </div>
      <label><span class="label">{t('calendar.event.description')}</span><textarea class="input" rows="4" bind:value={description}></textarea></label>
      {#if error}<p class="err" role="alert">{error}</p>{/if}
      <button type="submit" hidden aria-hidden="true"></button>
    </form>
  {/if}
  {#snippet footer()}
    {#if event && !readOnly && event.kind === 'event'}
      <button class="btn btn-ghost danger" onclick={() => remove()}><Trash size={15} /> {t('common.action.delete')}</button>
    {/if}
    <span class="sp"></span>
    <button class="btn" onclick={onclose}>{readOnly || event?.kind === 'invitation' ? t('common.action.close') : t('common.action.cancel')}</button>
    {#if !readOnly && event?.kind !== 'invitation'}
      <button class="btn btn-primary" disabled={saving || !writable.length} onclick={() => save()}><Plus size={15} /> {t('common.action.save')}</button>
    {/if}
  {/snippet}
</Modal>

{#if scopeFor}
  <ScopeDialog mode={scopeFor} onclose={() => (scopeFor = null)} onchoose={(s) => (scopeFor === 'edit' ? save(s) : remove(s))} />
{/if}

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .big {
    font-size: 16px;
    font-weight: 600;
    height: 40px;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
    align-items: end;
  }
  .check {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;
  }
  .when {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: flex-end;
  }
  .when label {
    display: flex;
    flex-direction: column;
  }
  .dash {
    padding-bottom: 8px;
    color: var(--tm-muted);
  }
  .tz {
    margin: -6px 0 0;
    font-size: 11.5px;
    color: var(--tm-faint);
  }
  .with-btn {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .with-btn .input {
    flex: 1;
  }
  .att > .label {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .att ul {
    list-style: none;
    margin: 0 0 6px;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .att li {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .dot {
    width: 9px;
    height: 9px;
    border-radius: 99px;
    background: var(--tm-line-strong);
    flex: none;
  }
  .dot[data-av='busy'] {
    background: var(--tm-danger);
  }
  .dot[data-av='free'] {
    background: var(--tm-success);
  }
  .an {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .an small {
    color: var(--tm-faint);
  }
  .ps {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .ps[data-ps='ACCEPTED'] {
    color: var(--tm-success);
  }
  .ps[data-ps='DECLINED'] {
    color: var(--tm-danger);
  }
  .opt {
    font-size: 11.5px;
    color: var(--tm-muted);
    display: flex;
    gap: 3px;
    align-items: center;
  }
  .sm {
    width: 26px;
    height: 26px;
  }
  .addatt {
    position: relative;
  }
  .sugg {
    position: absolute;
    z-index: 10;
    left: 0;
    right: 0;
    top: 100%;
    margin-top: 2px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    box-shadow: var(--tm-shadow-lg);
    padding: 4px !important;
  }
  .sugg button {
    width: 100%;
    text-align: left;
    border: none;
    background: none;
    font: inherit;
    padding: 6px 8px;
    border-radius: 6px;
    cursor: pointer;
    color: var(--tm-fg);
  }
  .sugg button:hover {
    background: var(--tm-accent-soft);
  }
  .sugg small {
    color: var(--tm-muted);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 28px;
    padding: 0 4px 0 10px;
    border-radius: 99px;
    background: var(--tm-accent-soft);
    font-size: 12.5px;
  }
  .chip button {
    border: none;
    background: none;
    display: grid;
    place-items: center;
    cursor: pointer;
    color: var(--tm-muted);
  }
  select.sm {
    width: auto;
    height: 28px;
  }
  .err {
    color: var(--tm-danger);
    margin: 0;
    font-size: 13px;
  }
  .sp {
    flex: 1;
  }
  .danger {
    color: var(--tm-danger);
  }
  .conflict {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 8px 10px;
    margin-bottom: 10px;
    border-radius: var(--tm-radius);
    background: color-mix(in srgb, var(--tm-warn) 12%, var(--tm-surface));
    font-size: 13px;
  }
  .conflict span {
    flex: 1;
  }
  .ro {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 14px;
    margin: 0;
    font-size: 13.5px;
  }
  .ro dt {
    color: var(--tm-muted);
  }
  .ro dd {
    margin: 0;
  }
  .pre {
    white-space: pre-wrap;
  }
  @media (max-width: 639px) {
    .grid2 {
      grid-template-columns: 1fr;
    }
  }

  @media (max-width: 639px) {
    .with-btn {
      flex-wrap: wrap;
    }
    .with-btn .input {
      flex: 1 1 100%;
    }
  }
</style>
