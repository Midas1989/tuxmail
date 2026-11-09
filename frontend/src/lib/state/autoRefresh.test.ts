// SPDX-License-Identifier: AGPL-3.0-or-later
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AutoRefresh, type AutoRefreshTarget } from './autoRefresh';

class Target implements AutoRefreshTarget {
  private readonly fns = new Map<string, Set<() => void>>();
  addEventListener(type: string, fn: () => void) {
    if (!this.fns.has(type)) this.fns.set(type, new Set());
    this.fns.get(type)!.add(fn);
  }
  removeEventListener(type: string, fn: () => void) {
    this.fns.get(type)?.delete(fn);
  }
  fire(type: string) {
    for (const f of this.fns.get(type) ?? []) f();
  }
  count() {
    return [...this.fns.values()].reduce((n, s) => n + s.size, 0);
  }
}

describe('AutoRefresh', () => {
  let live: boolean;
  let visible: boolean;
  let paused: boolean;
  let fail: boolean;
  let refresh: ReturnType<typeof vi.fn<() => Promise<void>>>;
  let doc: Target;
  let win: Target;
  let a: AutoRefresh | null;

  beforeEach(() => {
    vi.useFakeTimers();
    live = false;
    visible = true;
    paused = false;
    fail = false;
    refresh = vi.fn(async () => {
      if (fail) throw new Error('offline');
    });
    doc = new Target();
    win = new Target();
    a = null;
  });
  afterEach(() => {
    a?.stop();
    vi.useRealTimers();
  });

  const make = (random = () => 0.5) =>
    (a = new AutoRefresh({ refresh, live: () => live, visible: () => visible, paused: () => paused, doc, win, random, now: () => Date.now() }));

  it('with a live event stream the timer sends no request', async () => {
    live = true;
    make();
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(refresh).not.toHaveBeenCalled();
  });

  it('without the stream it refreshes about once a minute, only while visible and not paused', async () => {
    make();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    visible = false;
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    visible = true;
    paused = true;
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('jitter spreads the interval by ±20 %', () => {
    expect(make(() => 0).nextDelay()).toBe(48_000);
    a!.stop();
    expect(make(() => 1).nextDelay()).toBe(72_000);
    a!.stop();
    expect(make(() => 0.5).nextDelay()).toBe(60_000);
  });

  it('failures double the pause up to 15 minutes; success resets it', async () => {
    fail = true;
    make();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(a!.failureCount).toBe(1);
    expect(a!.nextDelay()).toBe(120_000);
    await vi.advanceTimersByTimeAsync(119_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(refresh).toHaveBeenCalledTimes(2);
    for (let i = 0; i < 6; i++) await vi.advanceTimersByTimeAsync(a!.nextDelay());
    expect(a!.nextDelay()).toBe(15 * 60_000);
    fail = false;
    await vi.advanceTimersByTimeAsync(15 * 60_000);
    expect(a!.failureCount).toBe(0);
    expect(a!.nextDelay()).toBe(60_000);
  });

  it('coming back to the page refreshes once, at most every 30 s, also with a live stream', async () => {
    live = true;
    make();
    doc.fire('visibilitychange');
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(1);
    win.fire('focus');
    win.fire('online');
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(30_000);
    win.fire('focus');
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(2);
    visible = false;
    await vi.advanceTimersByTimeAsync(30_000);
    doc.fire('visibilitychange');
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it('stop removes the timer and every listener', async () => {
    make().stop();
    expect(doc.count() + win.count()).toBe(0);
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(refresh).not.toHaveBeenCalled();
  });
});
