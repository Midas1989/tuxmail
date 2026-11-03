// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Quick entry with natural language for German and English basics
// (KONZEPT 9.1: "Zahnarzt morgen 15 Uhr"):
//   dates   heute/today, morgen/tomorrow, übermorgen, (nächsten) Montag /
//           (next) monday, "in 3 Tagen" / "in 3 days", 24.12., 24.12.2026,
//           2026-12-24, 12/24, "am 3. Mai" / "May 3"
//   times   15 Uhr, 15:30, 9.30 Uhr, um 9, 3pm, 3:30 pm, at 9, mittags/noon
//   ranges  10-11 Uhr, 10:00–11:30, 9am-10am
//   length  1h, 90 min, 2 Std., 1,5 Stunden, for 2 hours
//   all day ganztägig / all day
// The recognized parts are removed; the rest is the title.

import { addDays, keyOf, parseKey, weekday, type DayKey } from './datemath';

export interface QuickResult {
  title: string;
  day: DayKey;
  /** Minutes since midnight; null = all day. */
  startMin: number | null;
  durationMin: number;
  allDay: boolean;
  /** True if a date or time was recognized. */
  matched: boolean;
}

const WEEKDAY_WORDS: Record<string, number> = {
  sonntag: 0, so: 0, sunday: 0, sun: 0,
  montag: 1, mo: 1, monday: 1, mon: 1,
  dienstag: 2, di: 2, tuesday: 2, tue: 2, tues: 2,
  mittwoch: 3, mi: 3, wednesday: 3, wed: 3,
  donnerstag: 4, do: 4, thursday: 4, thu: 4, thurs: 4,
  freitag: 5, fr: 5, friday: 5, fri: 5,
  samstag: 6, sa: 6, sonnabend: 6, saturday: 6, sat: 6,
};

const MONTH_WORDS: Record<string, number> = {
  januar: 1, jan: 1, january: 1, jänner: 1,
  februar: 2, feb: 2, february: 2,
  märz: 3, maerz: 3, mär: 3, march: 3, mar: 3,
  april: 4, apr: 4,
  mai: 5, may: 5,
  juni: 6, jun: 6, june: 6,
  juli: 7, jul: 7, july: 7,
  august: 8, aug: 8,
  september: 9, sep: 9, sept: 9,
  oktober: 10, okt: 10, october: 10, oct: 10,
  november: 11, nov: 11,
  dezember: 12, dez: 12, december: 12, dec: 12,
};

interface Cut {
  re: RegExp;
  on: (m: RegExpExecArray) => boolean | void;
}

/** Parse a quick-entry line relative to today (day key in the user's zone). */
export function parseQuick(input: string, today: DayKey, defaultDuration = 60): QuickResult {
  let text = ` ${input.trim()} `;
  let day: DayKey | null = null;
  let start: number | null = null;
  let end: number | null = null;
  let duration: number | null = null;
  let allDay = false;
  const thisYear = parseKey(today).y;

  const futureDate = (m: number, d: number, y?: number): DayKey => {
    let k = keyOf(y ?? thisYear, m, d);
    if (y === undefined && k < today) k = keyOf(thisYear + 1, m, d);
    return k;
  };
  const hm = (h: string, min?: string, ampm?: string): number | null => {
    let hh = Number(h);
    const mm = min ? Number(min) : 0;
    if (ampm) {
      const pm = ampm.toLowerCase().startsWith('p');
      if (hh === 12) hh = pm ? 12 : 0;
      else if (pm) hh += 12;
    }
    if (hh > 23 || mm > 59) return null;
    return hh * 60 + mm;
  };
  const W = '(?<=\\s|^)';
  const E = '(?=[\\s,.!?]|$)';
  const cuts: Cut[] = [
    { re: new RegExp(`${W}(ganztägig|ganztags|den ganzen tag|all[- ]day)${E}`, 'i'), on: () => void (allDay = true) },
    { re: new RegExp(`${W}übermorgen${E}`, 'i'), on: () => void (day = addDays(today, 2)) },
    { re: new RegExp(`${W}(day after tomorrow)${E}`, 'i'), on: () => void (day = addDays(today, 2)) },
    { re: new RegExp(`${W}(morgen|tomorrow|tmrw)${E}`, 'i'), on: () => void (day = addDays(today, 1)) },
    { re: new RegExp(`${W}(heute|today)${E}`, 'i'), on: () => void (day = today) },
    {
      re: new RegExp(`${W}in (\\d+) (tagen|tag|days?|wochen|woche|weeks?)${E}`, 'i'),
      on: (m) => void (day = addDays(today, Number(m[1]) * (/^w/i.test(m[2]) ? 7 : 1))),
    },
    {
      // 2026-12-24
      re: new RegExp(`${W}(\\d{4})-(\\d{2})-(\\d{2})${E}`),
      on: (m) => void (day = keyOf(Number(m[1]), Number(m[2]), Number(m[3]))),
    },
    {
      // 24.12.2026 / 24.12.26 / 24.12. / am 24.12
      re: new RegExp(`${W}(?:am |on )?(\\d{1,2})\\.(\\d{1,2})\\.(\\d{2,4})?${E}`, 'i'),
      on: (m) => {
        const y = m[3] ? (m[3].length === 2 ? 2000 + Number(m[3]) : Number(m[3])) : undefined;
        day = futureDate(Number(m[2]), Number(m[1]), y);
      },
    },
    {
      // 12/24 (US)
      re: new RegExp(`${W}(?:on )?(\\d{1,2})/(\\d{1,2})(?:/(\\d{4}))?${E}`, 'i'),
      on: (m) => void (day = futureDate(Number(m[1]), Number(m[2]), m[3] ? Number(m[3]) : undefined)),
    },
    {
      // 3. Mai / 3 May
      re: new RegExp(`${W}(?:am |on )?(\\d{1,2})\\.? (${Object.keys(MONTH_WORDS).join('|')})\\.?(?: (\\d{4}))?${E}`, 'i'),
      on: (m) => void (day = futureDate(MONTH_WORDS[m[2].toLowerCase()], Number(m[1]), m[3] ? Number(m[3]) : undefined)),
    },
    {
      // May 3
      re: new RegExp(`${W}(?:on )?(${Object.keys(MONTH_WORDS).join('|')}) (\\d{1,2})(?:st|nd|rd|th)?(?:,? (\\d{4}))?${E}`, 'i'),
      on: (m) => void (day = futureDate(MONTH_WORDS[m[1].toLowerCase()], Number(m[2]), m[3] ? Number(m[3]) : undefined)),
    },
    {
      // (nächsten|next|am|on) Montag
      re: new RegExp(`${W}(?:(nächsten|nächste|kommenden|next|this|am|on) )?(${Object.keys(WEEKDAY_WORDS).filter((w) => w.length > 2).join('|')})${E}`, 'i'),
      on: (m) => {
        const target = WEEKDAY_WORDS[m[2].toLowerCase()];
        let diff = (target - weekday(today) + 7) % 7;
        if (diff === 0 || /^(nächst|next)/i.test(m[1] ?? '')) diff = diff === 0 ? 7 : diff;
        day = addDays(today, diff);
      },
    },
    {
      // ranges: 10-11 Uhr, 10:00–11:30, 9am-10am, von 9 bis 10
      re: new RegExp(`${W}(?:von |from )?(\\d{1,2})(?:[:.](\\d{2}))? ?(am|pm)? ?(?:-|–|bis|to|until) ?(\\d{1,2})(?:[:.](\\d{2}))? ?(am|pm|uhr|h)?${E}`, 'i'),
      on: (m) => {
        const ampm2 = m[6] && /am|pm/i.test(m[6]) ? m[6] : undefined;
        const a = hm(m[1], m[2], m[3] ?? ampm2);
        const b = hm(m[4], m[5], ampm2);
        if (a === null || b === null || (!m[2] && !m[5] && !m[3] && !m[6])) return false;
        start = a;
        end = b > a ? b : b + 12 * 60 > a && !ampm2 ? b + 12 * 60 : b;
      },
    },
    {
      // 15:30 Uhr / um 9 Uhr / 9.30 Uhr / at 3pm / 3:30 pm / 15h
      re: new RegExp(`${W}(?:um |at |gegen )?(\\d{1,2})(?:[:.](\\d{2}))? ?(uhr|h|am|pm|a\\.m\\.|p\\.m\\.)${E}`, 'i'),
      on: (m) => {
        const ap = /^[ap]/i.test(m[3]) ? m[3] : undefined;
        const v = hm(m[1], m[2], ap);
        if (v === null) return false;
        start = v;
      },
    },
    {
      // 15:30 (with colon) / um 9 / at 9
      re: new RegExp(`${W}(?:(um|at|gegen) (\\d{1,2})(?:[:.](\\d{2}))?|(\\d{1,2}):(\\d{2}))${E}`, 'i'),
      on: (m) => {
        const v = m[2] !== undefined ? hm(m[2], m[3]) : hm(m[4], m[5]);
        if (v === null) return false;
        start = v;
      },
    },
    { re: new RegExp(`${W}(mittags|noon|zum mittag)${E}`, 'i'), on: () => void (start = 12 * 60) },
    { re: new RegExp(`${W}(abends|tonight|heute abend)${E}`, 'i'), on: () => void (start ??= 19 * 60) },
    {
      // durations: 1h, 90 min, 2 Std., 1,5 Stunden, for 2 hours, für 30 Minuten
      re: new RegExp(`${W}(?:für |for )?(\\d+(?:[.,]\\d+)?) ?(h|std\\.?|stunden?|hours?|hrs?|min\\.?|minuten?|minutes?|mins?)${E}`, 'i'),
      on: (m) => {
        const n = Number(m[1].replace(',', '.'));
        duration = Math.round(/^(h|std|stun|hour|hr)/i.test(m[2]) ? n * 60 : n);
      },
    },
  ];
  let matched = false;
  for (const c of cuts) {
    const m = c.re.exec(text);
    if (!m) continue;
    if (c.on(m) === false) continue;
    matched = true;
    text = text.slice(0, m.index) + ' ' + text.slice(m.index + m[0].length);
  }
  const title = text
    .replace(/\s+(am|um|at|on|von|from)\s*$/i, ' ')
    .replace(/\s{2,}/g, ' ')
    .replace(/^[\s,.-]+|[\s,.-]+$/g, '');
  const startMin = allDay ? null : (start as number | null);
  let dur = duration ?? defaultDuration;
  if (end !== null && start !== null) dur = Math.max(15, (end as number) - (start as number));
  return {
    title,
    day: day ?? today,
    startMin,
    durationMin: dur,
    allDay: allDay || (startMin === null && day !== null && duration === null),
    matched,
  };
}
