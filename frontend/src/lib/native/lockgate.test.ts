// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it, vi } from 'vitest';
import { MAX_QUEUED, createDeferredSet, createLockGate } from './lockgate';

describe('createLockGate (NG-03)', () => {
  it('runs items immediately while unlocked', () => {
    const run = vi.fn();
    const g = createLockGate<string>(run);
    g.submit('a');
    expect(run).toHaveBeenCalledWith('a');
  });

  it('queues while locked and applies in arrival order after unlock', () => {
    const run = vi.fn();
    const g = createLockGate<string>(run);
    g.lock();
    g.submit('link:1');
    g.submit('share:2');
    g.submit('link:3');
    expect(run).not.toHaveBeenCalled();
    expect(g.pending).toBe(3);
    g.unlock();
    expect(run.mock.calls.map((c) => c[0])).toEqual(['link:1', 'share:2', 'link:3']);
    expect(g.pending).toBe(0);
    expect(g.locked).toBe(false);
    g.submit('after');
    expect(run).toHaveBeenLastCalledWith('after');
  });

  it('does not replay twice and keeps the newest items when flooded', () => {
    const run = vi.fn();
    const g = createLockGate<number>(run);
    g.lock();
    for (let i = 0; i < MAX_QUEUED + 5; i++) g.submit(i);
    expect(g.pending).toBe(MAX_QUEUED);
    g.unlock();
    expect(run).toHaveBeenCalledTimes(MAX_QUEUED);
    expect(run.mock.calls[0][0]).toBe(5);
    g.unlock();
    expect(run).toHaveBeenCalledTimes(MAX_QUEUED);
  });

  it('items submitted by a handler during unlock run directly', () => {
    const seen: string[] = [];
    const g: ReturnType<typeof createLockGate<string>> = createLockGate<string>((x) => {
      seen.push(x);
      if (x === 'first') g.submit('nested');
    });
    g.lock();
    g.submit('first');
    g.unlock();
    expect(seen).toEqual(['first', 'nested']);
  });
});

describe('createDeferredSet', () => {
  it('defers side effects (notification cancel) while locked and flushes once', () => {
    let locked = true;
    const run = vi.fn();
    const d = createDeferredSet<number>(run, () => locked);
    d.request(7);
    d.request(7);
    d.request(9);
    expect(run).not.toHaveBeenCalled();
    expect(d.size).toBe(2);
    locked = false;
    d.flush();
    expect(run.mock.calls.map((c) => c[0])).toEqual([7, 9]);
    d.flush();
    expect(run).toHaveBeenCalledTimes(2);
    d.request(11);
    expect(run).toHaveBeenLastCalledWith(11);
  });
});
