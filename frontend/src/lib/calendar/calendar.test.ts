// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import {
  addDays,
  addMonths,
  dayKeyIn,
  eventDays,
  isAllDayRow,
  layoutDay,
  minutesIn,
  monthGrid,
  moveTo,
  segmentOn,
  shiftEvent,
  startOfWeek,
  stepAnchor,
  viewDays,
  zonedInstant,
} from './datemath';
import { describeRule, expandDays, presetOf, ruleForPreset } from './recurrence';
import { parseQuick } from './quickparse';

describe('datemath', () => {
  it('handles day keys, weeks and months', () => {
    expect(addDays('2026-02-28', 1)).toBe('2026-03-01');
    expect(addDays('2026-01-01', -1)).toBe('2025-12-31');
    expect(addMonths('2026-01-31', 1)).toBe('2026-02-28');
    expect(startOfWeek('2026-11-05', 1)).toBe('2026-11-02');
    expect(startOfWeek('2026-11-05', 0)).toBe('2026-11-01');
    expect(viewDays('threeday', '2026-11-07', 1)).toEqual(['2026-11-07', '2026-11-08', '2026-11-09']);
    expect(stepAnchor('threeday', '2026-11-07', 1)).toBe('2026-11-10');
    expect(stepAnchor('threeday', '2026-11-07', -1)).toBe('2026-11-04');
    expect(viewDays('workweek', '2026-11-08', 0)).toEqual(['2026-11-02', '2026-11-03', '2026-11-04', '2026-11-05', '2026-11-06']);
    const grid = monthGrid('2026-11-15', 1);
    expect(grid).toHaveLength(42);
    expect(grid[0]).toBe('2026-10-26');
    expect(stepAnchor('month', '2026-01-31', 1)).toBe('2026-02-28');
  });

  it('converts wall-clock times across DST', () => {
    // Berlin: 2026-03-29 02:00 -> 03:00, 2026-10-25 03:00 -> 02:00
    expect(zonedInstant('2026-03-29', 9 * 60, 'Europe/Berlin').toISOString()).toBe('2026-03-29T07:00:00.000Z');
    expect(zonedInstant('2026-03-28', 9 * 60, 'Europe/Berlin').toISOString()).toBe('2026-03-28T08:00:00.000Z');
    expect(zonedInstant('2026-10-25', 12 * 60, 'Europe/Berlin').toISOString()).toBe('2026-10-25T11:00:00.000Z');
    expect(zonedInstant('2026-11-03', 0, 'America/New_York').toISOString()).toBe('2026-11-03T05:00:00.000Z');
    // nonexistent 02:30 moves forward
    expect(minutesIn(zonedInstant('2026-03-29', 150, 'Europe/Berlin'), 'Europe/Berlin')).toBe(210);
    expect(dayKeyIn(new Date('2026-11-03T23:30:00Z'), 'Europe/Berlin')).toBe('2026-11-04');
    // the DST day has 23 hours: an event until midnight ends at minute 1380
    const seg = segmentOn({ start: '2026-03-28T23:00:00Z', end: '2026-03-29T22:00:00Z' }, '2026-03-29', 'Europe/Berlin');
    expect(seg).toEqual({ start: 0, end: 1380 });
  });

  it('computes event day spans and the all-day row', () => {
    expect(eventDays({ start: '2026-11-06', end: '2026-11-08', allDay: true }, 'UTC')).toEqual({ first: '2026-11-06', endExcl: '2026-11-08' });
    expect(eventDays({ start: '2026-11-06T22:00:00Z', end: '2026-11-07T00:00:00Z', allDay: false }, 'UTC')).toEqual({ first: '2026-11-06', endExcl: '2026-11-07' });
    expect(isAllDayRow({ start: '2026-11-06T08:00:00Z', end: '2026-11-08T08:00:00Z', allDay: false }, 'UTC')).toBe(true);
    expect(isAllDayRow({ start: '2026-11-06T22:00:00Z', end: '2026-11-07T01:00:00Z', allDay: false }, 'UTC')).toBe(false);
  });

  it('lays out overlapping events in columns', () => {
    const placed = layoutDay([
      { item: 'a', start: 540, end: 600 },
      { item: 'b', start: 570, end: 630 },
      { item: 'c', start: 600, end: 660 },
      { item: 'd', start: 700, end: 720 },
    ]);
    const by = Object.fromEntries(placed.map((p) => [p.item, [p.col, p.cols]]));
    expect(by).toEqual({ a: [0, 2], b: [1, 2], c: [0, 2], d: [0, 1] });
  });

  it('moves and shifts events', () => {
    expect(shiftEvent({ start: '2026-11-06', end: '2026-11-07', allDay: true }, 2, 0)).toEqual({ start: '2026-11-08', end: '2026-11-09' });
    expect(moveTo({ start: '2026-10-24T08:00:00Z', end: '2026-10-24T09:00:00Z' }, '2026-10-26', 10 * 60, 'Europe/Berlin')).toEqual({
      start: '2026-10-26T09:00:00.000Z',
      end: '2026-10-26T10:00:00.000Z',
    });
  });
});

describe('recurrence', () => {
  const t = (k: string, p?: Record<string, string | number>) => k + (p ? JSON.stringify(p) : '');
  it('maps presets both ways', () => {
    const start = '2026-11-26'; // 4th (and last) Thursday
    expect(ruleForPreset('monthlyNth', start)).toEqual({ freq: 'MONTHLY', byDay: ['-1TH'] });
    expect(ruleForPreset('weekly', start)).toEqual({ freq: 'WEEKLY', byDay: ['TH'] });
    for (const p of ['none', 'daily', 'weekdays', 'weekly', 'biweekly', 'monthly', 'monthlyNth', 'yearly'] as const) {
      expect(presetOf(ruleForPreset(p, start), start)).toBe(p);
    }
    expect(presetOf({ freq: 'WEEKLY' }, start)).toBe('weekly');
    expect(presetOf({ freq: 'DAILY', count: 3 }, start)).toBe('custom');
  });

  it('describes rules', () => {
    const s = describeRule({ freq: 'WEEKLY', interval: 2, byDay: ['MO', 'WE'], count: 5 }, t, (c) => c, (k) => k);
    expect(s).toBe('calendar.recurrence.everyN.week{"n":2} calendar.recurrence.on{"days":"MO, WE"}, calendar.recurrence.times{"n":5}');
    expect(describeRule(null, t, (c) => c, (k) => k)).toBe('calendar.recurrence.none');
  });

  it('expands series', () => {
    expect(expandDays({ freq: 'WEEKLY', byDay: ['MO', 'WE'], count: 4 }, '2026-11-02', '2026-11-01', '2026-12-31')).toEqual([
      '2026-11-02',
      '2026-11-04',
      '2026-11-09',
      '2026-11-11',
    ]);
    expect(expandDays({ freq: 'MONTHLY', byDay: ['-1FR'] }, '2026-10-30', '2026-10-01', '2027-01-31')).toEqual(['2026-10-30', '2026-11-27', '2026-12-25', '2027-01-29']);
    expect(expandDays({ freq: 'MONTHLY' }, '2026-01-31', '2026-01-01', '2026-05-31')).toEqual(['2026-01-31', '2026-03-31', '2026-05-31']);
    expect(expandDays({ freq: 'DAILY', interval: 2, until: '2026-11-08' }, '2026-11-02', '2026-11-03', '2026-12-01', ['2026-11-06'])).toEqual(['2026-11-04', '2026-11-08']);
    expect(expandDays({ freq: 'YEARLY' }, '1990-11-05', '2026-01-01', '2027-12-31')).toEqual(['2026-11-05', '2027-11-05']);
  });
});

describe('quick entry', () => {
  const today = '2026-11-03'; // Tuesday
  it.each([
    ['Zahnarzt morgen 15 Uhr', 'Zahnarzt', '2026-11-04', 900, 60, false],
    ['Meeting tomorrow 3pm for 30 min', 'Meeting', '2026-11-04', 900, 30, false],
    ['Teammeeting Freitag 10:30 1,5 Std', 'Teammeeting', '2026-11-06', 630, 90, false],
    ['Lunch next monday at noon', 'Lunch', '2026-11-09', 720, 60, false],
    ['Weihnachtsfeier am 24.12. 18-22 Uhr', 'Weihnachtsfeier', '2026-12-24', 1080, 240, false],
    ['Urlaub 2026-11-20 ganztägig', 'Urlaub', '2026-11-20', null, 60, true],
    ['Release May 3 9am-10am', 'Release', '2027-05-03', 540, 60, false],
    ['Call in 3 Tagen um 9', 'Call', '2026-11-06', 540, 60, false],
    ['Steuererklärung übermorgen', 'Steuererklärung', '2026-11-05', null, 60, true],
    ['Einkaufen', 'Einkaufen', '2026-11-03', null, 60, false],
  ])('%s', (input, title, day, start, dur, allDay) => {
    const r = parseQuick(input, today);
    expect(r.title).toBe(title);
    expect(r.day).toBe(day);
    expect(r.startMin).toBe(start);
    expect(r.durationMin).toBe(dur);
    expect(r.allDay).toBe(allDay);
  });
});
