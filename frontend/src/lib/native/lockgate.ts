// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// NG-03: while the app lock overlay is shown, deep links, notification-tap
// targets and share payloads must not act on the app underneath (opening a
// conversation marks it read and cancels its notifications; a share opens a
// composer). They are queued and applied, in order, after the unlock.

export interface LockGate<T> {
  /** Run `item` now, or queue it while locked. */
  submit(item: T): void;
  lock(): void;
  /** Clear the lock and apply the queued items in arrival order. */
  unlock(): void;
  readonly locked: boolean;
  readonly pending: number;
}

/** At most this many items are kept; the oldest are dropped first (a flood of intents must not pile up unbounded). */
export const MAX_QUEUED = 8;

export function createLockGate<T>(run: (item: T) => void, max = MAX_QUEUED): LockGate<T> {
  let locked = false;
  let queue: T[] = [];
  return {
    submit(item) {
      if (!locked) return run(item);
      queue.push(item);
      if (queue.length > max) queue = queue.slice(queue.length - max);
    },
    lock() {
      locked = true;
    },
    unlock() {
      locked = false;
      const items = queue;
      queue = [];
      for (const it of items) run(it);
    },
    get locked() {
      return locked;
    },
    get pending() {
      return queue.length;
    },
  };
}

/**
 * Side effects that must not happen behind the lock (e.g. dismissing the
 * notifications of the opened conversation): deferred while locked, flushed on unlock.
 */
export function createDeferredSet<T>(run: (item: T) => void, isLocked: () => boolean) {
  const pending = new Set<T>();
  return {
    request(item: T) {
      if (isLocked()) pending.add(item);
      else run(item);
    },
    flush() {
      const items = [...pending];
      pending.clear();
      for (const it of items) run(it);
    },
    get size() {
      return pending.size;
    },
  };
}
