// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Central date/time/number formatting. Every date shown in the app goes
// through a Formatter built from the UI locale + the user's "Language &
// region" settings (time zone, date/time patterns, week start, number
// format, relative times). Uses Intl with an explicit timeZone, so the
// output does not depend on the machine's zone unless "system" is chosen.

import type { RegionSettings } from './types';

export const DEFAULT_REGION: RegionSettings = {
  timeZone: 'system',
  dateFormat: 'locale',
  customDatePattern: 'DD.MM.YYYY',
  timeFormat: 'locale',
  weekStart: 'locale',
  numberFormat: 'locale',
  relativeTimes: true,
  secondaryTimeZone: '',
};

export const DATE_FORMATS = ['locale', 'DD.MM.YYYY', 'YYYY-MM-DD', 'MM/DD/YYYY', 'DD/MM/YYYY', 'D. MMMM YYYY', 'custom'] as const;
export const NUMBER_FORMATS = ['locale', '1.234,56', '1,234.56', '1 234,56'] as const;

export function systemTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

export function listTimeZones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (k: string) => string[] };
  try {
    const zones = intl.supportedValuesOf?.('timeZone');
    if (zones?.length) return zones.includes('UTC') ? zones : ['UTC', ...zones];
  } catch {
    /* older engines */
  }
  return ['UTC', 'Europe/Berlin', 'Europe/Vienna', 'Europe/Zurich', 'Europe/London', 'Europe/Paris', 'America/New_York', 'America/Los_Angeles', 'Asia/Tokyo', 'Australia/Sydney'];
}

interface ZParts {
  year: number;
  month: number; // 1-12
  day: number;
  hour: number; // 0-23
  minute: number;
  second: number;
  weekday: number; // 0 = Sunday
}

const partsCache = new Map<string, Intl.DateTimeFormat>();
function partsFormatter(tz: string): Intl.DateTimeFormat {
  let f = partsCache.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', { timeZone: tz, year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric', second: 'numeric', weekday: 'short', hourCycle: 'h23' });
    partsCache.set(tz, f);
  }
  return f;
}

const WD: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 };

export function zonedParts(date: Date, tz: string): ZParts {
  const p: Record<string, string> = {};
  for (const x of partsFormatter(tz).formatToParts(date)) p[x.type] = x.value;
  return {
    year: Number(p.year),
    month: Number(p.month),
    day: Number(p.day),
    hour: Number(p.hour) % 24,
    minute: Number(p.minute),
    second: Number(p.second),
    weekday: WD[p.weekday] ?? 0,
  };
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0');

export class Formatter {
  readonly locale: string;
  readonly tz: string;
  readonly s: RegionSettings;
  private readonly now: () => Date;

  constructor(locale: string, settings: Partial<RegionSettings> = {}, now: () => Date = () => new Date()) {
    this.locale = locale;
    this.s = { ...DEFAULT_REGION, ...settings };
    this.tz = !this.s.timeZone || this.s.timeZone === 'system' ? systemTimeZone() : this.s.timeZone;
    this.now = now;
  }

  private hour12(): boolean | undefined {
    return this.s.timeFormat === '12' ? true : this.s.timeFormat === '24' ? false : undefined;
  }

  private intl(opts: Intl.DateTimeFormatOptions, tz = this.tz): Intl.DateTimeFormat {
    const h12 = this.hour12();
    const o: Intl.DateTimeFormatOptions = { ...opts, timeZone: tz };
    if (h12 !== undefined && (opts.hour || opts.timeStyle)) o.hourCycle = h12 ? 'h12' : 'h23';
    return new Intl.DateTimeFormat(this.locale, o);
  }

  /** Format with a token pattern: YYYY YY MMMM MMM MM M DD D dddd ddd HH H hh h mm ss A. */
  pattern(input: Date | string, pattern: string, tz = this.tz): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    const p = zonedParts(d, tz);
    const month = (style: 'long' | 'short') => new Intl.DateTimeFormat(this.locale, { month: style, timeZone: tz }).format(d);
    const weekday = (style: 'long' | 'short') => new Intl.DateTimeFormat(this.locale, { weekday: style, timeZone: tz }).format(d);
    const h12 = p.hour % 12 === 0 ? 12 : p.hour % 12;
    return pattern.replace(/\[([^\]]*)]|YYYY|YY|MMMM|MMM|MM|M|DD|D|dddd|ddd|HH|H|hh|h|mm|ss|A/g, (tok, lit?: string) => {
      if (lit !== undefined) return lit;
      switch (tok) {
        case 'YYYY':
          return String(p.year);
        case 'YY':
          return pad(p.year % 100);
        case 'MMMM':
          return month('long');
        case 'MMM':
          return month('short');
        case 'MM':
          return pad(p.month);
        case 'M':
          return String(p.month);
        case 'DD':
          return pad(p.day);
        case 'D':
          return String(p.day);
        case 'dddd':
          return weekday('long');
        case 'ddd':
          return weekday('short');
        case 'HH':
          return pad(p.hour);
        case 'H':
          return String(p.hour);
        case 'hh':
          return pad(h12);
        case 'h':
          return String(h12);
        case 'mm':
          return pad(p.minute);
        case 'ss':
          return pad(p.second);
        case 'A':
          return p.hour < 12 ? 'AM' : 'PM';
      }
      return tok;
    });
  }

  private datePattern(): string | null {
    const f = this.s.dateFormat;
    if (f === 'locale') return null;
    if (f === 'custom') return this.s.customDatePattern || 'DD.MM.YYYY';
    return f;
  }

  date(input: Date | string): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    const p = this.datePattern();
    return p ? this.pattern(d, p) : this.intl({ day: '2-digit', month: '2-digit', year: 'numeric' }).format(d);
  }

  /** Date without year (for this year's items in lists). */
  dateShort(input: Date | string): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    const p = this.datePattern();
    if (p) {
      const noYear = p.replace(/[\s./-]*YYYY|[\s./-]*YY/g, '').replace(/^YYYY[\s./-]*/, '').trim();
      return this.pattern(d, noYear || p);
    }
    return this.intl({ day: '2-digit', month: '2-digit' }).format(d);
  }

  dateLong(input: Date | string): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    if (this.s.dateFormat === 'custom' || this.s.dateFormat === 'D. MMMM YYYY') return this.pattern(d, 'dddd, ' + (this.datePattern() ?? 'D. MMMM YYYY'));
    return this.intl({ weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' }).format(d);
  }

  time(input: Date | string, tz = this.tz): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    return this.intl({ hour: '2-digit', minute: '2-digit' }, tz).format(d);
  }

  dateTime(input: Date | string): string {
    return `${this.date(input)} ${this.time(input)}`;
  }

  /** Long form for the reading pane: "Tue, 3 November 2026, 08:41". */
  full(input: Date | string): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    if (this.datePattern()) return `${this.pattern(d, 'ddd')}, ${this.date(d)}, ${this.time(d)}`;
    return `${this.intl({ weekday: 'short', day: '2-digit', month: 'long', year: 'numeric' }).format(d)}, ${this.time(d)}`;
  }

  /** Calendar-day key in the configured zone. */
  dayKey(input: Date | string): string {
    const p = zonedParts(typeof input === 'string' ? new Date(input) : input, this.tz);
    return `${p.year}-${pad(p.month)}-${pad(p.day)}`;
  }

  private dayDiff(d: Date): number {
    const a = zonedParts(d, this.tz);
    const b = zonedParts(this.now(), this.tz);
    return Math.round((Date.UTC(b.year, b.month - 1, b.day) - Date.UTC(a.year, a.month - 1, a.day)) / 86400_000);
  }

  relative(input: Date | string): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    const diff = (d.getTime() - this.now().getTime()) / 1000;
    const rtf = new Intl.RelativeTimeFormat(this.locale, { numeric: 'auto' });
    const abs = Math.abs(diff);
    if (abs < 45) return rtf.format(0, 'second');
    if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
    if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
    const days = -this.dayDiff(d);
    if (Math.abs(days) < 7) return rtf.format(days, 'day');
    if (Math.abs(days) < 30) return rtf.format(Math.round(days / 7), 'week');
    if (Math.abs(days) < 365) return rtf.format(Math.round(days / 30), 'month');
    return rtf.format(Math.round(days / 365), 'year');
  }

  /** Compact list date: relative/time today, "yesterday", weekday, date. */
  listDate(input: Date | string): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    const days = this.dayDiff(d);
    if (days === 0) {
      const mins = (this.now().getTime() - d.getTime()) / 60000;
      if (this.s.relativeTimes && mins >= 0 && mins < 60) return this.relative(d);
      return this.time(d);
    }
    if (days === 1) return new Intl.RelativeTimeFormat(this.locale, { numeric: 'auto' }).format(-1, 'day');
    if (days > 1 && days < 7) return this.intl({ weekday: 'short' }).format(d);
    if (zonedParts(d, this.tz).year === zonedParts(this.now(), this.tz).year) return this.dateShort(d);
    return this.date(d);
  }

  /** Day separator in chats: "today", "yesterday", "Monday, 2 November". */
  day(input: Date | string): string {
    const d = typeof input === 'string' ? new Date(input) : input;
    const days = this.dayDiff(d);
    if (days === 0 || days === 1) {
      const s = new Intl.RelativeTimeFormat(this.locale, { numeric: 'auto' }).format(-days, 'day');
      return s.charAt(0).toLocaleUpperCase(this.locale) + s.slice(1);
    }
    return this.intl({ weekday: 'long', day: 'numeric', month: 'long' }).format(d);
  }

  monthLabel(year: number, month0: number): string {
    return new Intl.DateTimeFormat(this.locale, { month: 'short', year: '2-digit', timeZone: 'UTC' }).format(new Date(Date.UTC(year, month0, 15)));
  }

  private numberLocale(): string {
    switch (this.s.numberFormat) {
      case '1.234,56':
        return 'de-DE';
      case '1,234.56':
        return 'en-US';
      case '1 234,56':
        return 'fr-FR';
      default:
        return this.locale;
    }
  }

  number(n: number, opts: Intl.NumberFormatOptions = {}): string {
    return new Intl.NumberFormat(this.numberLocale(), opts).format(n);
  }

  size(bytes: number): string {
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let v = bytes;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return `${this.number(v, { maximumFractionDigits: i === 0 ? 0 : 1 })} ${units[i]}`;
  }

  /** 0 = Sunday, 1 = Monday, 6 = Saturday. */
  weekStart(): number {
    if (this.s.weekStart !== 'locale') return Number(this.s.weekStart);
    try {
      const loc = new Intl.Locale(this.locale) as Intl.Locale & { getWeekInfo?: () => { firstDay: number }; weekInfo?: { firstDay: number } };
      const info = loc.getWeekInfo?.() ?? loc.weekInfo;
      if (info) return info.firstDay % 7;
    } catch {
      /* not supported */
    }
    return /-(US|CA|JP|BR|IL)$/i.test(this.locale) || this.locale === 'en' ? 0 : 1;
  }
}
