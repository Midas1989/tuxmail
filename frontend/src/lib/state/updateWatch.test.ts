// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { UPDATE_CHECK_MS, UpdateWatch } from './updateWatch.svelte';

afterEach(() => vi.useRealTimers());

describe('UpdateWatch', () => {
  it('does nothing without an own build id (desktop, native app, dev server)', async () => {
    const fetchServer = vi.fn(async () => 'x');
    const w = new UpdateWatch({ own: undefined, fetchServer });
    w.start()();
    await w.check();
    expect(fetchServer).not.toHaveBeenCalled();
    expect(w.available).toBe(false);
  });

  it('the same build changes nothing; an unreadable answer neither', async () => {
    let answer: string | undefined = 'a1';
    const w = new UpdateWatch({ own: 'a1', fetchServer: async () => answer });
    await w.check();
    answer = undefined;
    await w.check();
    expect(w.available).toBe(false);
  });

  it('another build shows the notice: on focus, when back online and every ten minutes', async () => {
    for (const trigger of ['focus', 'online', 'timer'] as const) {
      vi.useFakeTimers();
      let answer = 'a1';
      const fetchServer = vi.fn(async () => answer);
      const w = new UpdateWatch({ own: 'a1', fetchServer });
      const stop = w.start();
      answer = 'b2';
      if (trigger === 'timer') await vi.advanceTimersByTimeAsync(UPDATE_CHECK_MS);
      else window.dispatchEvent(new Event(trigger));
      await vi.waitFor(() => expect(w.available).toBe(true));
      stop();
      vi.useRealTimers();
    }
  });

  it('reload saves the drafts first and the page is not reloaded by itself', async () => {
    const order: string[] = [];
    const w = new UpdateWatch({ own: 'a1', fetchServer: async () => 'b2', reload: () => order.push('reload') });
    w.onBeforeReload(async () => {
      await Promise.resolve();
      order.push('draft saved');
    });
    await w.check();
    expect(w.available).toBe(true);
    expect(order).toEqual([]);
    await w.reload();
    expect(order).toEqual(['draft saved', 'reload']);
  });
});
