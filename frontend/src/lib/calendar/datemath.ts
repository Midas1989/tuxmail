// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Date math for the calendar views in the user's configured time zone
// (Formatter.tz). Days are handled as keys "YYYY-MM-DD" (plain calendar
// dates, no zone), times as minutes since local midnight. zonedInstant()
// converts a wall-clock time in a zone into an instant, so the grid is
// correct across DST changes and independent of the machine's zone.

import { zonedParts } from '../format';
import type { CalendarEvent } from '../types';

export type DayKey = string;
export type CalendarView = 'day' | 'threeday' | 'workweek' | 'week' | 'month' | 'agenda';

const pad = (n: number) => String(n).padStart(2, '0');

export function keyOf(y: number, m: number, d: number): DayKey {
  const t = new Date(Date.UTC(y, m - 1, d));
  return `${t.getUTCFullYear()}-${pad(t.getUTCMonth() + 1)}-${pad(t.getUTCDate())}`;
}

export function parseKey(k: DayKey): { y: number; m: number; d: number } {
  const [y, m, d] = k.split('-').map(Number);
  return { y, m, d };
}

export function addDays(k: DayKey, n: number): DayKey {
  const { y, m, d } = parseKey(k);
  return keyOf(y, m, d + n);
}

export function addMonths(k: DayKey, n: number): DayKey {
  const { y, m, d } = parseKey(k);
  const last = new Date(Date.UTC(y, m - 1 + n + 1, 0)).getUTCDate();
  return keyOf(y, m + n, Math.min(d, last));
}

/** 0 = Sunday. */
export function weekday(k: DayKey): number {
  const { y, m, d } = parseKey(k);
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay();
}

export function diffDays(a: DayKey, b: DayKey): number {
  const pa = parseKey(a);
  const pb = parseKey(b);
  return Math.round((Date.UTC(pb.y, pb.m - 1, pb.d) - Date.UTC(pa.y, pa.m - 1, pa.d)) / 86_400_000);
}

export function startOfWeek(k: DayKey, weekStart: number): DayKey {
  return addDays(k, -((weekday(k) - weekStart + 7) % 7));
}

/** Day key of an instant in tz. */
export function dayKeyIn(date: Date, tz: string): DayKey {
  const p = zonedParts(date, tz);
  return keyOf(p.year, p.month, p.day);
}

/** Minutes since midnight of an instant in tz. */
export function minutesIn(date: Date, tz: string): number {
  const p = zonedParts(date, tz);
  return p.hour * 60 + p.minute;
}

function offsetMinutes(date: Date, tz: string): number {
  const p = zonedParts(date, tz);
  const asUTC = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second);
  return Math.round((asUTC - Math.floor(date.getTime() / 1000) * 1000) / 60_000);
}

/** The instant of wall-clock time (day k, minutes) in tz. Nonexistent times (DST gap) move forward. */
export function zonedInstant(k: DayKey, minutes: number, tz: string): Date {
  const { y, m, d } = parseKey(k);
  const guess = Date.UTC(y, m - 1, d, 0, minutes);
  let t = guess - offsetMinutes(new Date(guess), tz) * 60_000;
  // Second pass for instants close to an offset change.
  const off2 = offsetMinutes(new Date(t), tz);
  t = guess - off2 * 60_000;
  return new Date(t);
}

/** The days shown by a view around anchor. */
export function viewDays(view: CalendarView, anchor: DayKey, weekStart: number): DayKey[] {
  switch (view) {
    case 'day':
      return [anchor];
    case 'threeday':
      return [anchor, addDays(anchor, 1), addDays(anchor, 2)];
    case 'workweek': {
      const mon = startOfWeek(anchor, 1);
      return Array.from({ length: 5 }, (_, i) => addDays(mon, i));
    }
    case 'week': {
      const s = startOfWeek(anchor, weekStart);
      return Array.from({ length: 7 }, (_, i) => addDays(s, i));
    }
    case 'month':
      return monthGrid(anchor, weekStart);
    case 'agenda':
      return Array.from({ length: 30 }, (_, i) => addDays(anchor, i));
  }
}

/** 6 weeks covering the month of k. */
export function monthGrid(k: DayKey, weekStart: number): DayKey[] {
  const { y, m } = parseKey(k);
  const first = startOfWeek(keyOf(y, m, 1), weekStart);
  return Array.from({ length: 42 }, (_, i) => addDays(first, i));
}

/** Step the anchor by one view unit. */
export function stepAnchor(view: CalendarView, anchor: DayKey, dir: number): DayKey {
  switch (view) {
    case 'day':
      return addDays(anchor, dir);
    case 'threeday':
      return addDays(anchor, 3 * dir);
    case 'workweek':
    case 'week':
      return addDays(anchor, 7 * dir);
    case 'month':
      return addMonths(anchor, dir);
    case 'agenda':
      return addDays(anchor, 30 * dir);
  }
}

/** [from, to) instants of the days in tz. */
export function rangeOf(days: DayKey[], tz: string): { from: Date; to: Date } {
  return { from: zonedInstant(days[0], 0, tz), to: zonedInstant(addDays(days[days.length - 1], 1), 0, tz) };
}

/** Day span of an event in tz: first day and exclusive end day. */
export function eventDays(e: Pick<CalendarEvent, 'start' | 'end' | 'allDay'>, tz: string): { first: DayKey; endExcl: DayKey } {
  if (e.allDay) {
    const end = e.end && e.end > e.start ? e.end : addDays(e.start, 1);
    return { first: e.start.slice(0, 10), endExcl: end.slice(0, 10) };
  }
  const s = new Date(e.start);
  const en = new Date(e.end);
  const first = dayKeyIn(s, tz);
  let last = dayKeyIn(new Date(Math.max(s.getTime(), en.getTime() - 1)), tz);
  if (last < first) last = first;
  return { first, endExcl: addDays(last, 1) };
}

/** Does the event cover day k? */
export function onDay(e: Pick<CalendarEvent, 'start' | 'end' | 'allDay'>, k: DayKey, tz: string): boolean {
  const { first, endExcl } = eventDays(e, tz);
  return k >= first && k < endExcl;
}

/** Timed events spanning several days or all-day events go into the all-day row. */
export function isAllDayRow(e: Pick<CalendarEvent, 'start' | 'end' | 'allDay'>, tz: string): boolean {
  if (e.allDay) return true;
  const { first, endExcl } = eventDays(e, tz);
  return diffDays(first, endExcl) > 1 && new Date(e.end).getTime() - new Date(e.start).getTime() >= 24 * 3_600_000;
}

/** Minutes [start, end) of a timed event on day k, clipped to the day. */
export function segmentOn(e: Pick<CalendarEvent, 'start' | 'end'>, k: DayKey, tz: string): { start: number; end: number } {
  const dayStart = zonedInstant(k, 0, tz).getTime();
  const dayEnd = zonedInstant(addDays(k, 1), 0, tz).getTime();
  const s = Math.max(new Date(e.start).getTime(), dayStart);
  const en = Math.min(new Date(e.end).getTime(), dayEnd);
  const total = (dayEnd - dayStart) / 60_000;
  const a = Math.round((s - dayStart) / 60_000);
  const b = Math.max(a + 15, Math.round((en - dayStart) / 60_000));
  return { start: Math.min(a, total), end: Math.min(b, total) };
}

export interface Placed<T> {
  item: T;
  start: number;
  end: number;
  col: number;
  cols: number;
}

/** Overlap layout: events in clusters get side-by-side columns. */
export function layoutDay<T>(items: { item: T; start: number; end: number }[]): Placed<T>[] {
  const sorted = [...items].sort((a, b) => a.start - b.start || b.end - a.end);
  const out: Placed<T>[] = [];
  let cluster: Placed<T>[] = [];
  let clusterEnd = -1;
  const flush = () => {
    const cols = Math.max(1, ...cluster.map((p) => p.col + 1));
    for (const p of cluster) p.cols = cols;
    out.push(...cluster);
    cluster = [];
  };
  for (const it of sorted) {
    if (it.start >= clusterEnd && cluster.length) flush();
    const used = new Set(cluster.filter((p) => p.end > it.start).map((p) => p.col));
    let col = 0;
    while (used.has(col)) col++;
    cluster.push({ ...it, col, cols: 1 });
    clusterEnd = Math.max(clusterEnd, it.end);
  }
  if (cluster.length) flush();
  return out;
}

/** Round minutes to a step. */
export function snap(minutes: number, step = 15): number {
  return Math.round(minutes / step) * step;
}

/** Shift an event by days and minutes (timed) or days (all-day); keeps its length. */
export function shiftEvent(e: Pick<CalendarEvent, 'start' | 'end' | 'allDay'>, days: number, minutes: number): { start: string; end: string } {
  if (e.allDay) return { start: addDays(e.start, days), end: addDays(e.end || addDays(e.start, 1), days) };
  const delta = (days * 1440 + minutes) * 60_000;
  return { start: new Date(new Date(e.start).getTime() + delta).toISOString(), end: new Date(new Date(e.end).getTime() + delta).toISOString() };
}

/** Move a timed event to wall-clock (k, minutes) in tz keeping its length (DST-safe). */
export function moveTo(e: Pick<CalendarEvent, 'start' | 'end'>, k: DayKey, minutes: number, tz: string): { start: string; end: string } {
  const len = new Date(e.end).getTime() - new Date(e.start).getTime();
  const s = zonedInstant(k, minutes, tz);
  return { start: s.toISOString(), end: new Date(s.getTime() + len).toISOString() };
}

/** ISO weeks number (for the month view). */
export function isoWeek(k: DayKey): number {
  const { y, m, d } = parseKey(k);
  const t = new Date(Date.UTC(y, m - 1, d));
  const day = t.getUTCDay() || 7;
  t.setUTCDate(t.getUTCDate() + 4 - day);
  const yearStart = new Date(Date.UTC(t.getUTCFullYear(), 0, 1));
  return Math.ceil(((t.getTime() - yearStart.getTime()) / 86_400_000 + 1) / 7);
}
