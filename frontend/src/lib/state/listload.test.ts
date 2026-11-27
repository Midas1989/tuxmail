// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
// Loading the mail list: "load more" and full loads that overlap, and the
// event-driven refresh that must not pile up reloads.
import { describe, expect, it, vi } from 'vitest';
import { AppState, LONG_LIST_REFRESH_MS, mergeLoaded } from './app.svelte';
import { MockTransport } from '../api/mock';
import type { MessageSummary, Page } from '../types';

function rows(from: number, n: number): MessageSummary[] {
  return Array.from({ length: n }, (_, i) => ({ id: from + i }) as unknown as MessageSummary);
}
function page(items: MessageSummary[], total: number): Page<MessageSummary> {
  return { items, total, offset: 0 };
}
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

async function setup() {
  const api = new MockTransport({ latencyMs: 0 });
  const app = new AppState(api);
  await app.init();
  const list = vi.spyOn(api, 'listMessages');
  return { app, list };
}

describe('mergeLoaded', () => {
  it('appends only rows that are not there yet, in order', () => {
    expect(mergeLoaded(rows(1, 3), [...rows(3, 3)]).map((m) => m.id)).toEqual([1, 2, 3, 4, 5]);
  });
});

describe('loading the mail list', () => {
  it('drops a "load more" result that arrives after the list was loaded again', async () => {
    const { app, list } = await setup();
    list.mockResolvedValueOnce(page(rows(1, 200), 1000));
    await app.loadMessages();
    const more = deferred<Page<MessageSummary>>();
    list.mockReturnValueOnce(more.promise);
    const loadingMore = app.loadMoreMessages();
    // A full load (another folder, a refresh) overtakes the pending page.
    list.mockResolvedValueOnce(page(rows(5000, 200), 1000));
    await app.loadMessages();
    more.resolve(page(rows(201, 200), 1000));
    await loadingMore;
    expect(app.messages.map((m) => m.id)).toEqual(rows(5000, 200).map((m) => m.id));
    expect(app.messagesLoading).toBe(false);
    app.destroy();
  });

  it('does not add rows twice when new mail pushed them down', async () => {
    const { app, list } = await setup();
    list.mockResolvedValueOnce(page(rows(1, 200), 1000));
    await app.loadMessages();
    // Two new mails arrived on top: the next page starts with rows already shown.
    list.mockResolvedValueOnce(page(rows(199, 200), 1002));
    await app.loadMoreMessages();
    const ids = app.messages.map((m) => m.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids.length).toBe(398);
    app.destroy();
  });

  it('a stream of refresh requests runs one reload at a time', async () => {
    const { app, list } = await setup();
    list.mockResolvedValueOnce(page(rows(1, 50), 50));
    await app.loadMessages();
    const first = deferred<Page<MessageSummary>>();
    list.mockReturnValueOnce(first.promise);
    list.mockResolvedValue(page(rows(1, 50), 50));
    const refresh = (app as unknown as { refreshList(): void }).refreshList.bind(app);
    refresh();
    for (let i = 0; i < 20; i++) refresh(); // events while the first reload runs
    expect(list).toHaveBeenCalledTimes(2); // the initial load and one refresh
    first.resolve(page(rows(1, 50), 50));
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(3)); // exactly one follow-up
    await new Promise((r) => setTimeout(r, 20));
    expect(list).toHaveBeenCalledTimes(3);
    app.destroy();
  });

  it('a list scrolled beyond the first page is refreshed at most every LONG_LIST_REFRESH_MS', async () => {
    const { app, list } = await setup();
    list.mockResolvedValue(page(rows(1, 600), 1000));
    await app.loadMessages();
    vi.useFakeTimers();
    try {
      const refresh = (app as unknown as { refreshList(): void }).refreshList.bind(app);
      const before = list.mock.calls.length;
      refresh();
      await vi.advanceTimersByTimeAsync(0);
      expect(list.mock.calls.length).toBe(before + 1);
      refresh();
      await vi.advanceTimersByTimeAsync(LONG_LIST_REFRESH_MS / 2);
      expect(list.mock.calls.length).toBe(before + 1);
      await vi.advanceTimersByTimeAsync(LONG_LIST_REFRESH_MS);
      expect(list.mock.calls.length).toBe(before + 2);
    } finally {
      vi.useRealTimers();
      app.destroy();
    }
  });
});
