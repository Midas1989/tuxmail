// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render } from '../security/mountUtil';
import { flushSync } from 'svelte';
import TimeGrid from './TimeGrid.svelte';
import type { CalendarEvent } from '../../types';

const ev = { id: 1, calendarId: 1, title: 'Jour fixe', start: '2026-11-09T09:00:00.000Z', end: '2026-11-09T10:00:00.000Z', allDay: false, color: '#7c3aed' } as unknown as CalendarEvent;
const days = ['2026-11-07', '2026-11-08', '2026-11-09'];

function pointer(el: Element, type: string, x: number, y: number, pointerType = 'touch') {
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 0 });
  Object.defineProperty(e, 'pointerType', { value: pointerType });
  Object.defineProperty(e, 'pointerId', { value: 7 });
  el.dispatchEvent(e);
}

describe('TimeGrid with touch', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Element.prototype.setPointerCapture = () => {};
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  const mount = () => {
    const onmove = vi.fn();
    const onopen = vi.fn();
    const oncreate = vi.fn();
    const root = render(TimeGrid, { days, events: [ev], colors: { 1: '#7c3aed' }, tz: 'UTC', today: '2026-11-07', onopen, oncreate, onmove });
    return { root, onmove, onopen, oncreate, el: root.querySelector('.ev')! };
  };

  it('a swipe over an event scrolls and never moves it', () => {
    const { el, onmove, onopen } = mount();
    pointer(el, 'pointerdown', 100, 300);
    pointer(el, 'pointermove', 100, 340);
    vi.advanceTimersByTime(600);
    pointer(el, 'pointerup', 100, 400);
    flushSync();
    expect(onmove).not.toHaveBeenCalled();
    expect(onopen).not.toHaveBeenCalled();
  });

  it('a short tap opens the event', () => {
    const { el, onmove, onopen } = mount();
    pointer(el, 'pointerdown', 100, 300);
    vi.advanceTimersByTime(100);
    pointer(el, 'pointerup', 100, 300);
    expect(onopen).toHaveBeenCalledWith(ev);
    expect(onmove).not.toHaveBeenCalled();
  });

  it('after a long press the event can be moved', () => {
    const { el, onmove } = mount();
    pointer(el, 'pointerdown', 100, 300);
    vi.advanceTimersByTime(500);
    pointer(el, 'pointermove', 100, 360);
    pointer(el, 'pointerup', 100, 360);
    expect(onmove).toHaveBeenCalledTimes(1);
  });

  it('the mouse still drags at once', () => {
    const { el, onmove } = mount();
    pointer(el, 'pointerdown', 100, 300, 'mouse');
    pointer(el, 'pointermove', 100, 360, 'mouse');
    pointer(el, 'pointerup', 100, 360, 'mouse');
    expect(onmove).toHaveBeenCalledTimes(1);
  });

  it('scrolling over an empty column does not create an event', () => {
    const { root, oncreate } = mount();
    const col = root.querySelector('.col')!;
    pointer(col, 'pointerdown', 50, 200);
    pointer(col, 'pointermove', 50, 260);
    pointer(col, 'pointercancel', 50, 260);
    pointer(col, 'pointerup', 50, 260);
    vi.advanceTimersByTime(600);
    expect(oncreate).not.toHaveBeenCalled();
  });
});
