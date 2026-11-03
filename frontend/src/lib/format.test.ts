// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { Formatter, zonedParts } from './format';

const fixedNow = (iso: string) => () => new Date(iso);

describe('Formatter: time zones', () => {
  it('renders the same instant in different zones', () => {
    const d = new Date('2026-11-03T13:05:00Z');
    expect(new Formatter('de', { timeZone: 'Europe/Berlin', timeFormat: '24' }).time(d)).toBe('14:05');
    expect(new Formatter('de', { timeZone: 'America/New_York', timeFormat: '24' }).time(d)).toBe('08:05');
    expect(new Formatter('de', { timeZone: 'Asia/Tokyo', timeFormat: '24' }).time(d)).toBe('22:05');
  });

  it('handles the DST switch in Europe/Berlin (29 March 2026)', () => {
    const f = new Formatter('de', { timeZone: 'Europe/Berlin', timeFormat: '24' });
    expect(f.time(new Date('2026-03-29T00:30:00Z'))).toBe('01:30'); // CET, UTC+1
    expect(f.time(new Date('2026-03-29T01:30:00Z'))).toBe('03:30'); // CEST, UTC+2 (02:xx skipped)
    // back to CET on 25 October 2026: 01:30Z is 02:30 CET (second pass)
    expect(f.time(new Date('2026-10-25T00:30:00Z'))).toBe('02:30');
    expect(f.time(new Date('2026-10-25T01:30:00Z'))).toBe('02:30');
  });

  it('computes calendar days in the configured zone', () => {
    const late = new Date('2026-11-03T23:30:00Z');
    expect(new Formatter('en', { timeZone: 'UTC' }).dayKey(late)).toBe('2026-11-03');
    expect(new Formatter('en', { timeZone: 'Europe/Berlin' }).dayKey(late)).toBe('2026-11-04');
    expect(zonedParts(late, 'America/Los_Angeles')).toMatchObject({ day: 3, hour: 15 });
  });
});

describe('Formatter: 12/24 h and patterns', () => {
  const d = new Date('2026-03-03T13:05:09Z');

  it('switches between 24 h and 12 h independent of the language', () => {
    expect(new Formatter('de', { timeZone: 'UTC', timeFormat: '12' }).time(d)).toMatch(/^0?1:05\s?PM$/i);
    expect(new Formatter('en-US', { timeZone: 'UTC', timeFormat: '24' }).time(d)).toBe('13:05');
  });

  it('applies the predefined date formats', () => {
    const f = (dateFormat: string) => new Formatter('en', { timeZone: 'UTC', dateFormat: dateFormat as never }).date(d);
    expect(f('DD.MM.YYYY')).toBe('03.03.2026');
    expect(f('YYYY-MM-DD')).toBe('2026-03-03');
    expect(f('MM/DD/YYYY')).toBe('03/03/2026');
    expect(f('DD/MM/YYYY')).toBe('03/03/2026');
    expect(new Formatter('de', { timeZone: 'UTC', dateFormat: 'D. MMMM YYYY' }).date(d)).toBe('3. März 2026');
  });

  it('supports custom patterns with tokens and literals', () => {
    const f = new Formatter('en', { timeZone: 'Europe/Berlin', dateFormat: 'custom', customDatePattern: 'ddd, D MMM YY' });
    expect(f.date(d)).toBe('Tue, 3 Mar 26');
    expect(f.pattern(d, 'YYYY-MM-DD[T]HH:mm:ss')).toBe('2026-03-03T14:05:09');
    expect(f.pattern(d, 'h:mm A')).toBe('2:05 PM');
  });

  it('drops the year for this year in lists', () => {
    const f = new Formatter('en', { timeZone: 'UTC', dateFormat: 'DD.MM.YYYY', relativeTimes: false }, fixedNow('2026-11-03T12:00:00Z'));
    expect(f.listDate('2026-03-03T13:05:09Z')).toBe('03.03');
    expect(f.listDate('2025-03-03T13:05:09Z')).toBe('03.03.2025');
  });
});

describe('Formatter: relative times and lists', () => {
  const now = fixedNow('2026-11-03T12:00:00Z');

  it('uses relative times for the last hour when enabled', () => {
    const on = new Formatter('en', { timeZone: 'UTC', relativeTimes: true, timeFormat: '24' }, now);
    const off = new Formatter('en', { timeZone: 'UTC', relativeTimes: false, timeFormat: '24' }, now);
    expect(on.listDate('2026-11-03T11:55:00Z')).toBe('5 minutes ago');
    expect(off.listDate('2026-11-03T11:55:00Z')).toBe('11:55');
    expect(on.listDate('2026-11-03T08:00:00Z')).toBe('08:00');
  });

  it('localises "yesterday" through Intl.RelativeTimeFormat', () => {
    expect(new Formatter('en', { timeZone: 'UTC' }, now).listDate('2026-11-02T08:00:00Z')).toBe('yesterday');
    expect(new Formatter('de', { timeZone: 'UTC' }, now).listDate('2026-11-02T08:00:00Z')).toBe('gestern');
    expect(new Formatter('de', { timeZone: 'UTC' }, now).day('2026-11-03T08:00:00Z')).toBe('Heute');
  });
});

describe('Formatter: numbers and week start', () => {
  it('formats numbers by locale or explicit separators', () => {
    expect(new Formatter('en', {}).number(1234.5, { minimumFractionDigits: 2 })).toBe('1,234.50');
    expect(new Formatter('en', { numberFormat: '1.234,56' }).number(1234.5, { minimumFractionDigits: 2 })).toBe('1.234,50');
    expect(new Formatter('de', { numberFormat: '1 234,56' }).number(1234.5, { minimumFractionDigits: 2 }).replace(/\s/g, ' ')).toBe('1 234,50');
    expect(new Formatter('de', {}).size(24_300_000)).toBe('23,2 MB');
  });

  it('honours an explicit first day of week', () => {
    expect(new Formatter('en-US', { weekStart: 1 }).weekStart()).toBe(1);
    expect(new Formatter('de', { weekStart: 0 }).weekStart()).toBe(0);
    expect(new Formatter('de-DE', {}).weekStart()).toBe(1);
  });
});
