// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render } from '../security/mountUtil';
import { flushSync } from 'svelte';

vi.hoisted(() => {
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as never;
});

import MonthView from './MonthView.svelte';
import { monthGrid } from '../../calendar/datemath';
import type { CalendarEvent } from '../../types';

afterEach(cleanup);

const ev = (id: number, title: string, start: string): CalendarEvent => ({ id, calendarId: 1, title, start, end: start, allDay: true, color: '#2563eb' }) as unknown as CalendarEvent;

describe('MonthView on phones', () => {
  const days = monthGrid('2026-11-07', 1);
  const events = [ev(1, 'Urlaub Lena', '2026-11-08'), ev(2, 'Grillfest', '2026-11-08'), ev(3, 'Zahnarzt', '2026-11-12')];
  const props = { days, month: 11, events, colors: { 1: '#2563eb' }, tz: 'Europe/Berlin', today: '2026-11-07', onopen: vi.fn(), oncreate: vi.fn(), onmove: vi.fn(), onday: vi.fn() };

  it('shows dots in the grid and no event buttons', () => {
    const root = render(MonthView, props);
    expect(root.querySelectorAll('button.item').length).toBe(0);
    expect(root.querySelectorAll('.dots i').length).toBe(3);
  });

  it('tapping a day lists its events underneath', () => {
    const root = render(MonthView, props);
    const cell = [...root.querySelectorAll<HTMLButtonElement>('button.cell')].find((b) => b.getAttribute('aria-label')!.includes('November 8'))!;
    cell.click();
    flushSync();
    expect([...root.querySelectorAll('.dl .li .ti')].map((x) => x.textContent)).toEqual(['Urlaub Lena', 'Grillfest']);
    root.querySelector<HTMLButtonElement>('.dl .li')!.click();
    expect(props.onopen).toHaveBeenCalledWith(events[0]);
  });
});
