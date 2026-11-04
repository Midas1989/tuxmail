// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Recurrence editor logic: presets, the structured rule behind them, a
// human-readable summary and a small expander (used by the mock transport
// and for previews; the core expands with rrule-go).

import type { RecurrenceRule } from '../types';
import { addDays, addMonths, diffDays, keyOf, parseKey, weekday, type DayKey } from './datemath';

export const WEEKDAYS = ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'] as const;
export type WeekdayCode = (typeof WEEKDAYS)[number];

export type RecurrencePreset = 'none' | 'daily' | 'weekdays' | 'weekly' | 'biweekly' | 'monthly' | 'monthlyNth' | 'yearly' | 'custom';

/** nth weekday of the month of k (1..5) and whether it is the last one. */
export function nthWeekday(k: DayKey): { n: number; last: boolean; code: WeekdayCode } {
  const { y, m, d } = parseKey(k);
  const n = Math.ceil(d / 7);
  const daysInMonth = new Date(Date.UTC(y, m, 0)).getUTCDate();
  return { n, last: d + 7 > daysInMonth, code: WEEKDAYS[weekday(k)] };
}

/** Rule for a preset, anchored at the start day. */
export function ruleForPreset(p: RecurrencePreset, start: DayKey): RecurrenceRule | null {
  const code = WEEKDAYS[weekday(start)];
  switch (p) {
    case 'none':
      return null;
    case 'daily':
      return { freq: 'DAILY' };
    case 'weekdays':
      return { freq: 'WEEKLY', byDay: ['MO', 'TU', 'WE', 'TH', 'FR'] };
    case 'weekly':
      return { freq: 'WEEKLY', byDay: [code] };
    case 'biweekly':
      return { freq: 'WEEKLY', interval: 2, byDay: [code] };
    case 'monthly':
      return { freq: 'MONTHLY', byMonthDay: [parseKey(start).d] };
    case 'monthlyNth': {
      const nth = nthWeekday(start);
      return { freq: 'MONTHLY', byDay: [(nth.last && nth.n >= 4 ? '-1' : String(nth.n)) + code] };
    }
    case 'yearly':
      return { freq: 'YEARLY' };
    case 'custom':
      return { freq: 'WEEKLY', byDay: [code] };
  }
}

const same = (a?: unknown[], b?: unknown[]) => JSON.stringify(a ?? []) === JSON.stringify(b ?? []);

/** Which preset (if any) describes rule for an event starting at start. */
export function presetOf(rule: RecurrenceRule | null | undefined, start: DayKey): RecurrencePreset {
  if (!rule || !rule.freq) return 'none';
  if (rule.count || rule.until || (rule.byMonth?.length ?? 0) > 0 || (rule.bySetPos?.length ?? 0) > 0) return 'custom';
  for (const p of ['daily', 'weekdays', 'weekly', 'biweekly', 'monthly', 'monthlyNth', 'yearly'] as const) {
    const r = ruleForPreset(p, start)!;
    if (r.freq === rule.freq && (r.interval ?? 1) === (rule.interval ?? 1) && same(r.byDay, rule.byDay) && same(r.byMonthDay, rule.byMonthDay)) return p;
    // Plain weekly/monthly without BY parts are the same as the anchored presets.
    if (r.freq === rule.freq && (r.interval ?? 1) === (rule.interval ?? 1) && !rule.byDay?.length && !rule.byMonthDay?.length && (p === 'weekly' || p === 'monthly' || p === 'biweekly'))
      return p;
  }
  return 'custom';
}

type T = (key: string, params?: Record<string, string | number>) => string;

/** Human-readable summary ("Every 2 weeks on Monday, Wednesday, 5 times"). */
export function describeRule(rule: RecurrenceRule | null | undefined, t: T, weekdayName: (code: WeekdayCode) => string, formatDate: (k: DayKey) => string): string {
  if (!rule || !rule.freq) return t('calendar.recurrence.none');
  const n = rule.interval && rule.interval > 1 ? rule.interval : 1;
  const unit: Record<string, string> = { DAILY: 'day', WEEKLY: 'week', MONTHLY: 'month', YEARLY: 'year' };
  const u = unit[rule.freq] ?? 'day';
  let s = n === 1 ? t(`calendar.recurrence.every.${u}`) : t(`calendar.recurrence.everyN.${u}`, { n });
  const days = (rule.byDay ?? []).map((d) => {
    const m = /^(-?\d+)?([A-Z]{2})$/.exec(d);
    if (!m) return d;
    const name = weekdayName(m[2] as WeekdayCode);
    if (!m[1]) return name;
    return t(m[1] === '-1' ? 'calendar.recurrence.lastWeekday' : 'calendar.recurrence.nthWeekday', { n: m[1], day: name });
  });
  if (days.length) s += ' ' + t('calendar.recurrence.on', { days: days.join(', ') });
  if (rule.byMonthDay?.length) s += ' ' + t('calendar.recurrence.onDay', { days: rule.byMonthDay.join(', ') });
  if (rule.count) s += ', ' + t('calendar.recurrence.times', { n: rule.count });
  else if (rule.until) s += ', ' + t('calendar.recurrence.until', { date: formatDate(rule.until) });
  return s;
}

function matchesByDay(k: DayKey, byDay: string[], freq: string): boolean {
  const wd = WEEKDAYS[weekday(k)];
  const { y, m, d } = parseKey(k);
  for (const entry of byDay) {
    const mm = /^(-?\d+)?([A-Z]{2})$/.exec(entry);
    if (!mm || mm[2] !== wd) continue;
    if (!mm[1] || freq === 'WEEKLY' || freq === 'DAILY') return true;
    const n = Number(mm[1]);
    const daysInMonth = new Date(Date.UTC(y, m, 0)).getUTCDate();
    if (n > 0 && Math.ceil(d / 7) === n) return true;
    if (n < 0 && Math.ceil((daysInMonth - d + 1) / 7) === -n) return true;
  }
  return false;
}

/**
 * Days of a recurring series starting at start (inclusive) that fall into
 * [from, to] (day keys). Supports FREQ DAILY/WEEKLY/MONTHLY/YEARLY with
 * INTERVAL, COUNT, UNTIL, BYDAY (incl. nth weekday), BYMONTHDAY, BYMONTH
 * and EXDATE days. The start day is always the first occurrence.
 */
export function expandDays(rule: RecurrenceRule, start: DayKey, from: DayKey, to: DayKey, exdates: DayKey[] = [], limit = 1000): DayKey[] {
  const out: DayKey[] = [];
  const interval = Math.max(1, rule.interval ?? 1);
  const freq = rule.freq;
  let count = 0;
  const { y: sy, m: sm, d: sd } = parseKey(start);
  const matches = (k: DayKey): boolean => {
    const { y, m, d } = parseKey(k);
    if (rule.byMonth?.length && !rule.byMonth.includes(m)) return false;
    switch (freq) {
      case 'DAILY':
        if (diffDays(start, k) % interval !== 0) return false;
        return rule.byDay?.length ? matchesByDay(k, rule.byDay, freq) : true;
      case 'WEEKLY': {
        const weeks = Math.floor(diffDays(addDays(start, -((weekday(start) + 6) % 7)), k) / 7);
        if (weeks % interval !== 0) return false;
        return rule.byDay?.length ? matchesByDay(k, rule.byDay, freq) : weekday(k) === weekday(start);
      }
      case 'MONTHLY': {
        const months = (y - sy) * 12 + (m - sm);
        if (months % interval !== 0) return false;
        if (rule.byDay?.length) return matchesByDay(k, rule.byDay, freq);
        return (rule.byMonthDay?.length ? rule.byMonthDay : [sd]).some((md) => md === d || (md < 0 && new Date(Date.UTC(y, m, 0)).getUTCDate() + md + 1 === d));
      }
      case 'YEARLY':
        if ((y - sy) % interval !== 0) return false;
        if (rule.byDay?.length && rule.byMonth?.length) return matchesByDay(k, rule.byDay, 'MONTHLY');
        return m === sm && d === sd;
    }
    return false;
  };
  const end = rule.until && rule.until < to ? rule.until : to;
  for (let k = start; k <= end && out.length < limit; k = addDays(k, 1)) {
    if (k !== start && !matches(k)) continue;
    count++;
    if (rule.count && count > rule.count) break;
    if (k >= from && !exdates.includes(k)) out.push(k);
    // Long series: jump close to the window for yearly/monthly rules.
    if (freq === 'YEARLY' && k < from && !rule.count) {
      const { y } = parseKey(k);
      const target = parseKey(from).y - 1;
      if (target > y) k = addDays(keyOf(target, 1, 1), -1);
    }
  }
  return out;
}

export { addMonths };
