<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { fmt, t } from '../../i18n/index.svelte';
  import { tk } from '../../calendar/i18n';
  import type { RecurrenceRule } from '../../types';
  import { WEEKDAYS, describeRule, nthWeekday, presetOf, ruleForPreset, type RecurrencePreset, type WeekdayCode } from '../../calendar/recurrence';
  import { addDays, startOfWeek, zonedInstant, type DayKey } from '../../calendar/datemath';

  interface Props {
    value: RecurrenceRule | null;
    start: DayKey;
    tz: string;
    weekStart: number;
  }
  let { value = $bindable(), start, tz, weekStart }: Props = $props();

  const preset = $derived(presetOf(value, start));
  let custom = $state(false);
  const showCustom = $derived(custom || preset === 'custom');

  const weekdayName = (c: WeekdayCode) => {
    const base = startOfWeek(start, 0); // a Sunday
    return fmt().pattern(zonedInstant(addDays(base, WEEKDAYS.indexOf(c)), 720, tz), 'dddd');
  };
  const orderedDays = $derived(Array.from({ length: 7 }, (_, i) => WEEKDAYS[(weekStart + i) % 7]));
  const summary = $derived(describeRule(value, tk, weekdayName, (k) => fmt().date(zonedInstant(k, 720, tz))));
  const nth = $derived(nthWeekday(start));
  const endMode = $derived(value?.count ? 'count' : value?.until ? 'until' : 'never');

  function choose(p: RecurrencePreset) {
    custom = p === 'custom';
    value = p === 'custom' ? (value ?? ruleForPreset('weekly', start)) : ruleForPreset(p, start);
  }

  function patch(p: Partial<RecurrenceRule>) {
    value = { ...(value ?? { freq: 'WEEKLY' }), ...p };
  }

  function toggleDay(c: WeekdayCode) {
    const days = new Set(value?.byDay ?? []);
    if (days.has(c)) days.delete(c);
    else days.add(c);
    patch({ byDay: WEEKDAYS.filter((d) => days.has(d)) });
  }

  function setEnd(mode: string) {
    if (mode === 'never') patch({ count: undefined, until: undefined });
    else if (mode === 'count') patch({ count: value?.count || 10, until: undefined });
    else patch({ count: undefined, until: value?.until || addDays(start, 90) });
  }
</script>

<div class="re">
  <label class="label" for="rec-preset">{t('calendar.recurrence.label')}</label>
  <select id="rec-preset" class="input" value={showCustom ? 'custom' : preset} onchange={(e) => choose((e.currentTarget as HTMLSelectElement).value as RecurrencePreset)}>
    <option value="none">{t('calendar.recurrence.preset.none')}</option>
    <option value="daily">{t('calendar.recurrence.preset.daily')}</option>
    <option value="weekdays">{t('calendar.recurrence.preset.weekdays')}</option>
    <option value="weekly">{t('calendar.recurrence.preset.weekly', { day: weekdayName(nth.code) })}</option>
    <option value="biweekly">{t('calendar.recurrence.preset.biweekly', { day: weekdayName(nth.code) })}</option>
    <option value="monthly">{t('calendar.recurrence.preset.monthly', { n: Number(start.slice(8, 10)) })}</option>
    <option value="monthlyNth">{tk(nth.last && nth.n >= 4 ? 'calendar.recurrence.lastWeekday' : 'calendar.recurrence.nthWeekday', { n: nth.n, day: weekdayName(nth.code) })}</option>
    <option value="yearly">{t('calendar.recurrence.preset.yearly')}</option>
    <option value="custom">{t('calendar.recurrence.preset.custom')}</option>
  </select>
  {#if value && showCustom}
    <div class="custom">
      <div class="row">
        <span>{t('calendar.recurrence.interval')}</span>
        <input class="input num" type="number" min="1" max="99" value={value.interval ?? 1} aria-label={t('calendar.recurrence.interval')} oninput={(e) => patch({ interval: Math.max(1, Number((e.currentTarget as HTMLInputElement).value)) })} />
        <select class="input" value={value.freq} aria-label={t('calendar.recurrence.unitLabel')} onchange={(e) => patch({ freq: (e.currentTarget as HTMLSelectElement).value, byDay: undefined, byMonthDay: undefined })}>
          <option value="DAILY">{t('calendar.recurrence.unit.day')}</option>
          <option value="WEEKLY">{t('calendar.recurrence.unit.week')}</option>
          <option value="MONTHLY">{t('calendar.recurrence.unit.month')}</option>
          <option value="YEARLY">{t('calendar.recurrence.unit.year')}</option>
        </select>
      </div>
      {#if value.freq === 'WEEKLY'}
        <div class="days" role="group" aria-label={t('calendar.recurrence.weekdays')}>
          {#each orderedDays as c (c)}
            <button type="button" class="dbtn" aria-pressed={value.byDay?.includes(c) ?? false} onclick={() => toggleDay(c)} title={weekdayName(c)}>{weekdayName(c).slice(0, 2)}</button>
          {/each}
        </div>
      {/if}
      <div class="row">
        <span>{t('calendar.recurrence.end')}</span>
        <select class="input" value={endMode} aria-label={t('calendar.recurrence.end')} onchange={(e) => setEnd((e.currentTarget as HTMLSelectElement).value)}>
          <option value="never">{t('calendar.recurrence.endNever')}</option>
          <option value="count">{t('calendar.recurrence.endCount')}</option>
          <option value="until">{t('calendar.recurrence.endUntil')}</option>
        </select>
        {#if endMode === 'count'}
          <input class="input num" type="number" min="1" max="999" value={value.count} aria-label={t('calendar.recurrence.occurrences')} oninput={(e) => patch({ count: Math.max(1, Number((e.currentTarget as HTMLInputElement).value)) })} />
          <span>{t('calendar.recurrence.occurrences')}</span>
        {:else if endMode === 'until'}
          <input class="input" type="date" value={value.until} aria-label={t('calendar.recurrence.endUntil')} oninput={(e) => patch({ until: (e.currentTarget as HTMLInputElement).value })} />
        {/if}
      </div>
    </div>
  {/if}
  {#if value}<p class="sum">{summary}</p>{/if}
</div>

<style>
  .re {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .custom {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
  }
  .row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    font-size: 13px;
  }
  .row .input {
    width: auto;
  }
  .row .num {
    width: 70px;
  }
  .days {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .dbtn {
    width: 34px;
    height: 30px;
    border-radius: 99px;
    border: 1px solid var(--tm-line-strong);
    background: var(--tm-surface);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
    color: var(--tm-fg);
  }
  .dbtn[aria-pressed='true'] {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
    border-color: var(--tm-accent);
  }
  .sum {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
</style>
