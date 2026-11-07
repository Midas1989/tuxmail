// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Update watch of the web client served by `tuxmail serve`. A tab can stay open
// across a server update and then runs the old client against the new server
// (t-101: the old two-factor dialog sent requests the new server counted as wrong
// passwords). The server puts a hash of its current client into every index.html
// (`<meta name="tuxmail-build">`) and into /api/auth/config (`clientBuild`); it is a
// hash, not the version, so it is also there when `security.expose_version` is off.
// The tab compares both when it comes back online, when it gets the focus and at
// the latest every ten minutes. A difference shows a notice with a button; the page
// is never reloaded by itself, so nothing that is being written gets lost.
// Not in Wails and not in the native app: there the client comes with the app (and
// without the meta tag nothing happens anyway).

import { isNativeShell, isWails } from '../platform';

export const UPDATE_CHECK_MS = 10 * 60 * 1000;

export interface UpdateDeps {
  /** Build id of the running client; undefined: nothing to compare (desktop, native, dev server). */
  own?: string;
  /** Build id the server serves now; undefined when it cannot be read (offline, old server). */
  fetchServer?: () => Promise<string | undefined>;
  reload?: () => void;
  interval?: number;
}

function ownBuild(): string | undefined {
  if (typeof document === 'undefined' || isWails() || isNativeShell()) return undefined;
  return document.querySelector<HTMLMetaElement>('meta[name="tuxmail-build"]')?.content || undefined;
}

async function serverBuild(): Promise<string | undefined> {
  try {
    const res = await fetch('/api/auth/config', { cache: 'no-store', credentials: 'same-origin' });
    if (!res.ok) return undefined;
    const j = (await res.json()) as { clientBuild?: unknown };
    return typeof j.clientBuild === 'string' && j.clientBuild ? j.clientBuild : undefined;
  } catch {
    return undefined;
  }
}

export class UpdateWatch {
  /** The server serves another client than this tab runs. */
  available = $state(false);
  private readonly own: string | undefined;
  private readonly fetchServer: () => Promise<string | undefined>;
  private readonly doReload: () => void;
  private readonly interval: number;
  private flushers = new Set<() => Promise<void> | void>();
  private checking = false;

  constructor(deps: UpdateDeps = {}) {
    this.own = 'own' in deps ? deps.own : ownBuild();
    this.fetchServer = deps.fetchServer ?? serverBuild;
    this.doReload = deps.reload ?? (() => location.reload());
    this.interval = deps.interval ?? UPDATE_CHECK_MS;
  }

  /** Start watching; returns the stop function. */
  start(): () => void {
    if (!this.own) return () => {};
    const check = () => void this.check();
    const onVisible = () => {
      if (document.visibilityState === 'visible') check();
    };
    window.addEventListener('online', check);
    window.addEventListener('focus', check);
    document.addEventListener('visibilitychange', onVisible);
    const timer = setInterval(check, this.interval);
    return () => {
      window.removeEventListener('online', check);
      window.removeEventListener('focus', check);
      document.removeEventListener('visibilitychange', onVisible);
      clearInterval(timer);
    };
  }

  /** Compare once. An unreadable answer changes nothing. */
  async check(): Promise<void> {
    if (!this.own || this.available || this.checking) return;
    this.checking = true;
    try {
      const now = await this.fetchServer();
      if (now && now !== this.own) this.available = true;
    } finally {
      this.checking = false;
    }
  }

  /** A part of the UI that holds unsaved input (drafts) registers how to save it before a reload. */
  onBeforeReload(flush: () => Promise<void> | void): () => void {
    this.flushers.add(flush);
    return () => this.flushers.delete(flush);
  }

  /** Save the drafts, activate the new service worker (if one waits) and reload. */
  async reload(): Promise<void> {
    await Promise.allSettled([...this.flushers].map((f) => f()));
    await activateWaitingWorker();
    this.doReload();
  }
}

/** Let a waiting service worker take over (it only does after this confirmation), at most one second. */
async function activateWaitingWorker(): Promise<void> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
  try {
    const reg = await navigator.serviceWorker.getRegistration();
    await reg?.update().catch(() => undefined);
    const installing = reg?.installing;
    if (installing && installing.state !== 'installed') {
      // The new worker is still fetching its files: wait for it, at most three seconds.
      await new Promise<void>((resolve) => {
        const done = () => installing.state === 'installed' && resolve();
        installing.addEventListener('statechange', done);
        setTimeout(resolve, 3000);
      });
    }
    const waiting = reg?.waiting;
    if (!waiting) return;
    await new Promise<void>((resolve) => {
      const done = () => resolve();
      navigator.serviceWorker.addEventListener('controllerchange', done, { once: true });
      setTimeout(done, 1000);
      waiting.postMessage({ type: 'skip-waiting' });
    });
  } catch {
    /* the reload fetches the page from the network anyway */
  }
}

export const updateWatch = new UpdateWatch();
