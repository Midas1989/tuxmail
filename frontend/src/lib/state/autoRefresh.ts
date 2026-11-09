// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Automatic refresh of the mail list (AppState.startAutoRefresh).
//
// The server pushes changes over the event stream (WebSocket, Wails events), so
// while it is live there is no timer work at all. The timer is only the fallback
// for a stream that is down (proxy without WebSocket, reconnecting): a light
// refresh (list and sync status, never message contents) about once a minute,
// with jitter so many tabs and devices do not hit the server together, and with
// a doubling pause after failures. Coming back to the page (visible again,
// network back) refreshes once, at most every `minGapMs`, because a phone may
// have suspended the socket without noticing.

export interface AutoRefreshTarget {
  addEventListener(type: string, fn: () => void): void;
  removeEventListener(type: string, fn: () => void): void;
}

export interface AutoRefreshOptions {
  /** The light refresh; rejects when it failed (counts towards the backoff). */
  refresh: () => Promise<void>;
  /** The event stream delivers changes: timer ticks are skipped. */
  live: () => boolean;
  /** Nothing to do now (offline store without network, a manual refresh running). */
  paused?: () => boolean;
  visible?: () => boolean;
  doc?: AutoRefreshTarget;
  win?: AutoRefreshTarget;
  intervalMs?: number;
  maxIntervalMs?: number;
  /** Shortest time between two refreshes caused by coming back to the page. */
  minGapMs?: number;
  /** ± share of the interval (0.2 = 48–72 s for one minute). */
  jitter?: number;
  random?: () => number;
  now?: () => number;
}

export class AutoRefresh {
  private timer: ReturnType<typeof setTimeout> | null = null;
  private failures = 0;
  private last = Number.NEGATIVE_INFINITY;
  private running = false;
  private stopped = false;
  private readonly o: Required<Omit<AutoRefreshOptions, 'doc' | 'win'>> & Pick<AutoRefreshOptions, 'doc' | 'win'>;
  private readonly onResume = () => void this.resume();

  constructor(opts: AutoRefreshOptions) {
    this.o = {
      paused: () => false,
      visible: () => typeof document === 'undefined' || document.visibilityState === 'visible',
      intervalMs: 60_000,
      maxIntervalMs: 15 * 60_000,
      minGapMs: 30_000,
      jitter: 0.2,
      random: Math.random,
      now: Date.now,
      ...opts,
    };
    this.o.doc?.addEventListener('visibilitychange', this.onResume);
    this.o.win?.addEventListener('online', this.onResume);
    this.o.win?.addEventListener('focus', this.onResume);
    this.schedule();
  }

  /** The wait before the next tick: interval doubled per failure (capped), with jitter. */
  nextDelay(): number {
    const base = Math.min(this.o.intervalMs * 2 ** this.failures, this.o.maxIntervalMs);
    return Math.round(base * (1 - this.o.jitter + 2 * this.o.jitter * this.o.random()));
  }

  get failureCount(): number {
    return this.failures;
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    this.o.doc?.removeEventListener('visibilitychange', this.onResume);
    this.o.win?.removeEventListener('online', this.onResume);
    this.o.win?.removeEventListener('focus', this.onResume);
  }

  private schedule(): void {
    if (this.stopped) return;
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.tick(), this.nextDelay());
  }

  private idle(): boolean {
    return !this.o.visible() || this.o.paused() || this.running;
  }

  private async tick(): Promise<void> {
    this.timer = null;
    if (!this.idle() && !this.o.live()) await this.run();
    this.schedule();
  }

  private async resume(): Promise<void> {
    if (this.stopped || this.idle() || this.o.now() - this.last < this.o.minGapMs) return;
    await this.run();
    this.schedule();
  }

  private async run(): Promise<void> {
    this.running = true;
    this.last = this.o.now();
    try {
      await this.o.refresh();
      this.failures = 0;
    } catch {
      this.failures++;
    } finally {
      this.running = false;
    }
  }
}
