// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Wires the offline core into the Android app: it becomes the transport's
// fetch, answers the connection state the desktop core answers
// (ServerConnectionStatus, ServerSyncNow, ServerOutbox*), so the connection
// bar, the outbox and the switched-off buttons work as on the desktop, and it
// decides when to load: at the start, when the app comes back, when the
// network returns, on push messages, on new mail and periodically. The
// background work of Android (WorkManager, TuxMailPlugin.kt) loads while the
// interface is not running; its answers are taken over here.

import { app } from '../../state';
import { connection, type CoreBackend } from '../../state/connection.svelte';
import { t } from '../../i18n/index.svelte';
import { noteUnclearOutboxId } from '../../desktop/bridge';
import { openOutboxDraft } from '../../state/outboxDraft';
import type { ConnectionStatus } from '../../types';
import { setNativeTransport } from '../session';
import { OPEN_FILE_EVENT } from '../files';
import type { KeyValueStore, ServerProfile } from '../profiles';
import type { AppPlugin, TuxMailPlugin } from '../plugins';
import { OfflineCore, type CoreEvent } from './core';
import { IdbDb, deleteStore, openIdb, sweepOtherStores, type ForeignStore, type OfflineDb } from './db';
import { outboxEntries, settingsFromJson, settingsToJson, RE_MESSAGE_PART, type OfflineSettings } from './rules';

export const settingsKey = (profileId: string) => `offline.settings.${profileId}`;

export interface OfflineHandle {
  core: OfflineCore;
  settings(): OfflineSettings;
  saveSettings(s: OfflineSettings): Promise<void>;
  syncNow(): Promise<void>;
  clearCache(): Promise<void>;
  stop(): void;
  /** Sign-out and profile switch (confirmed by the user): everything kept on the device goes, waiting mails included. */
  destroy(): Promise<void>;
  /** Stores of other profiles/users that still hold waiting items (never deleted without confirmation). */
  foreign(): ForeignStore[];
  discardForeign(name: string): Promise<void>;
}

export interface StartOptions {
  profile: ServerProfile;
  token: () => string | undefined;
  prefs: KeyValueStore;
  plugin: TuxMailPlugin;
  appPlugin: AppPlugin;
  /** "Neu anmelden" after the server rejected the device: the app's own sign-in. */
  reauth: () => void;
  /**
   * The server revoked the device (401): what was kept for reading is already
   * deleted, waiting items stay for the same user's next sign-in. The app
   * asks for the app lock (if set up) and shows the sign-in.
   */
  revoked: () => void;
  /** Tests: another store. */
  db?: OfflineDb;
}

const iso = (ms?: number) => (ms ? new Date(ms).toISOString() : undefined);

/**
 * Stores of all profiles except `keep` (sign-out, profile switch, leftovers):
 * reading data goes; stores with waiting items stay and are returned.
 */
export async function dropOfflineStores(keep: string | null): Promise<ForeignStore[]> {
  if (typeof indexedDB === 'undefined') return [];
  return sweepOtherStores(keep).catch(() => []);
}

export async function startOffline(o: StartOptions): Promise<OfflineHandle | null> {
  if (!o.db && typeof indexedDB === 'undefined') return null;
  let db: OfflineDb;
  try {
    db = o.db ?? (await openIdb(o.profile.id));
  } catch (err) {
    console.warn('offline store unavailable', err);
    return null;
  }
  // Whose store this is, for the warning another user may get later (no content).
  void db.setMeta('account', `${o.profile.username ?? ''}@${o.profile.label}`).catch(() => {});
  let foreign: ForeignStore[] = [];
  void dropOfflineStores(o.profile.id).then((f) => {
    foreign = f;
    if (f.length) {
      // Waiting items of another account: said once at the start, decided in the settings.
      const n = f.reduce((a, x) => a + x.mails + x.actions, 0);
      const wait = () => (app.ready ? app.toast(t('native.offline.foreignWaiting', { count: n }), { label: t('connection.attentionOpen'), run: () => (location.hash = '#/native-settings') }) : setTimeout(wait, 500));
      wait();
    }
  });
  let settings = settingsFromJson(await o.prefs.get(settingsKey(o.profile.id)).catch(() => null));
  const listeners = new Set<() => void>();
  const changed = () => listeners.forEach((l) => l());

  const net = await o.plugin.networkStatus().catch(() => null);
  const deviceOffline = net ? !net.connected : typeof navigator !== 'undefined' && navigator.onLine === false;

  const core: OfflineCore = new OfflineCore({
    db,
    baseUrl: o.profile.serverUrl,
    net: (input, init) => fetch(input, init),
    token: o.token,
    settings: () => settings,
    unmetered: async () => {
      const s = await o.plugin.networkStatus().catch(() => null);
      if (s) return s.unmetered;
      const c = (navigator as Navigator & { connection?: { type?: string } }).connection;
      return c?.type ? c.type === 'wifi' || c.type === 'ethernet' : true;
    },
    texts: { offline: t('connection.notAvailableOffline'), unclear: t('connection.unclear.text') },
    initial: deviceOffline ? 'offline' : 'online',
    onChange: changed,
    onEvent: (e) => onCoreEvent(e),
  });

  function onCoreEvent(e: CoreEvent): void {
    switch (e.type) {
      case 'state':
        if (e.state === 'expired') {
          // Revoked by the server: nothing readable stays on the device, waiting items do.
          void core.clearCache().finally(() => o.revoked());
          break;
        }
        if (e.state === 'offline') scheduleProbe(true);
        if (e.state === 'online') {
          void core.flush().then(() => scheduleSync(2000));
          scheduleProbe(false);
        }
        break;
      case 'reload':
        void app.reloadAfterReconnect();
        break;
      case 'sent':
        app.toast(t('connection.outbox.sentLater'));
        break;
      case 'discarded':
        app.toast(e.outcome === 'sent' ? t('connection.outbox.discardedSent') : e.outcome === 'unclear' ? t('connection.outbox.discardedUnclear') : t('connection.outbox.discardedNotSent'), undefined, e.outcome === 'unclear' ? 'error' : 'info');
        break;
      case 'conflict':
        app.toast(e.item.error === 'gone' ? t('connection.outbox.conflictGone') : t('connection.outbox.conflictRefused'), { label: t('connection.attentionOpen'), run: () => (location.hash = '#/native-settings') }, 'error');
        break;
      case 'blocked': {
        // It comes back as a draft: one tap opens it with the reason.
        const entry = outboxEntries([e.item])[0];
        app.toast(t('connection.outbox.blockedToast'), { label: t('connection.outbox.asDraft'), run: () => void openOutboxDraft(connection, app, entry) }, 'error');
        break;
      }
    }
  }

  // ---- the transport ---------------------------------------------------------
  const fetchImpl: typeof fetch = async (input, init) => {
    const res = await core.fetch(input, init);
    if (res.status === 409) {
      const doc = (await res
        .clone()
        .json()
        .catch(() => null)) as { error?: string; outboxId?: string } | null;
      if (doc?.error === 'send_unclear') noteUnclearOutboxId(doc.outboxId ?? null);
    }
    const method = (init?.method ?? 'GET').toUpperCase();
    // A noted action changes what the lists show: draw them again from the store.
    if (method === 'POST' && res.status === 204 && res.headers.get('X-Tuxmail-Offline')) scheduleListReload();
    return res;
  };
  setNativeTransport(fetchImpl, undefined);

  // ---- the connection state the interface asks for ---------------------------
  async function status(): Promise<ConnectionStatus> {
    const [items, bytes, times] = await Promise.all([core.outbox(), core.cacheBytes(), core.lastTimes()]);
    return {
      connected: true,
      mode: 'thin',
      serverUrl: o.profile.serverUrl,
      serverName: o.profile.label,
      username: o.profile.username,
      displayName: o.profile.displayName,
      state: core.state,
      lastConnectedAt: iso(times.lastConnectedAt),
      lastDataSyncAt: iso(times.lastDataSyncAt),
      cacheBytes: bytes,
      outbox: outboxEntries(items),
      settingsLog: [],
      queuesMoves: true,
    };
  }

  const backend: CoreBackend = {
    async call(method, ...args) {
      switch (method) {
        case 'ServerConnectionStatus':
          return status();
        case 'ServerSyncNow':
          await syncNow();
          return status();
        case 'ServerOutboxDiscard':
          return core.outboxDiscard(String(args[0]));
        case 'ServerOutboxMessage':
          return core.outboxMessage(String(args[0]));
        case 'ServerOutboxRetry':
          return core.outboxRetry(String(args[0]));
        default:
          throw JSON.stringify({ code: 'not_bound', message: `${method} is not available in the app` });
      }
    },
    subscribe(fn) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    reauth: o.reauth,
  };
  connection.useBackend(backend);

  // ---- when to load ------------------------------------------------------------
  let probeTimer: ReturnType<typeof setTimeout> | undefined;
  let probeDelay = 5000;
  function scheduleProbe(start: boolean): void {
    clearTimeout(probeTimer);
    probeTimer = undefined;
    if (!start || core.state !== 'offline') {
      probeDelay = 5000;
      return;
    }
    probeTimer = setTimeout(async () => {
      probeTimer = undefined;
      if ((await core.probe()) === 'offline') {
        probeDelay = Math.min(probeDelay * 2, 60000);
        scheduleProbe(true);
      }
    }, probeDelay);
  }

  let syncTimer: ReturnType<typeof setTimeout> | undefined;
  function scheduleSync(wait: number): void {
    clearTimeout(syncTimer);
    syncTimer = setTimeout(() => void core.sync().catch((err) => console.warn('offline sync failed', err)), wait);
  }

  let listTimer: ReturnType<typeof setTimeout> | undefined;
  function scheduleListReload(): void {
    clearTimeout(listTimer);
    listTimer = setTimeout(() => void app.reloadAfterReconnect(), 150);
  }

  async function takeStaged(): Promise<void> {
    for (let i = 0; i < 40; i++) {
      const r = await o.plugin.takeOfflineStaging({ profileId: o.profile.id, limit: 50 }).catch(() => null);
      if (!r?.entries.length) return;
      await core.importStaged(r.entries);
      if (!r.more) return;
    }
  }

  async function syncNow(): Promise<void> {
    if (core.state === 'expired') return;
    if ((await core.probe()) !== 'online') return;
    await core.flush();
    await core.sync();
  }

  async function resume(): Promise<void> {
    await takeStaged();
    if (core.state === 'expired') return;
    const s = await core.probe();
    if (s === 'online') scheduleSync(1500);
  }

  // The device reports a network again: ask the server at once, and soon again if it is not reachable yet.
  const onOnline = () => {
    probeDelay = 2000;
    void core.probe().then((st) => {
      if (st === 'offline') {
        probeDelay = 2000;
        scheduleProbe(true);
      }
    });
  };
  const onOffline = () => core.markOffline();
  window.addEventListener('online', onOnline);
  window.addEventListener('offline', onOffline);
  const handles = [
    o.appPlugin.addListener('appStateChange', ({ isActive }) => {
      if (isActive) void resume();
    }),
    o.plugin.addListener('pushReceived', () => scheduleSync(1000)),
  ];
  const stopEvents = app.api.subscribe((e) => {
    if (e.type === 'message.added') scheduleSync(20000);
  });
  // Foreground: the same rhythm as the background work (or every 30 minutes).
  const tick = setInterval(() => scheduleSync(0), Math.max(settings.intervalMinutes || 30, 15) * 60000);

  // Opening attachments: with the device token and from the store when offline.
  const onClick = (ev: MouseEvent) => {
    const a = (ev.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
    if (!a || !a.href.startsWith(o.profile.serverUrl + '/api/messages/')) return;
    const u = new URL(a.href);
    if (!RE_MESSAGE_PART.test(u.pathname)) return;
    ev.preventDefault();
    const name = a.getAttribute('download') || decodeURIComponent(u.pathname.split('/').pop() ?? 'file');
    void (async () => {
      const res = await fetchImpl(o.profile.serverUrl + u.pathname, { headers: { Authorization: `Bearer ${o.token() ?? ''}`, Accept: '*/*' } });
      if (!res.ok) {
        app.toast(res.status === 503 ? t('connection.notAvailableOffline') : t('connection.outbox.conflictRefused'), undefined, 'error');
        return;
      }
      window.dispatchEvent(new CustomEvent(OPEN_FILE_EVENT, { detail: { blob: await res.blob(), name }, cancelable: true }));
    })();
  };
  document.addEventListener('click', onClick, true);

  await configureBackground(o, settings);
  void takeStaged().then(() => {
    if (core.state === 'offline') scheduleProbe(true);
    else scheduleSync(3000);
  });

  function stop(): void {
    clearTimeout(probeTimer);
    clearTimeout(syncTimer);
    clearTimeout(listTimer);
    clearInterval(tick);
    stopEvents();
    window.removeEventListener('online', onOnline);
    window.removeEventListener('offline', onOffline);
    document.removeEventListener('click', onClick, true);
    for (const h of handles) void h.then((x) => x.remove()).catch(() => {});
    connection.useBackend(null);
    setNativeTransport(undefined, undefined);
  }

  return {
    core,
    settings: () => settings,
    async saveSettings(next) {
      settings = next;
      await o.prefs.set(settingsKey(o.profile.id), settingsToJson(next));
      await configureBackground(o, next);
      await core.prune();
      changed();
    },
    syncNow,
    clearCache: () => core.clearCache(),
    stop,
    foreign: () => foreign,
    async discardForeign(name) {
      await deleteStore(name);
      foreign = foreign.filter((f) => f.name !== name);
      changed();
    },
    async destroy() {
      stop();
      await db.clear().catch(() => {});
      if (db instanceof IdbDb) {
        db.db.close();
        await deleteStore(db.db.name);
      }
      await o.plugin.clearOfflineStaging().catch(() => {});
      await o.plugin.configureOfflineSync({ profileId: o.profile.id, intervalMinutes: 0, wifiOnly: false, headersOnlyOlderThanDays: 0 }).catch(() => {});
    },
  };
}

async function configureBackground(o: StartOptions, s: OfflineSettings): Promise<void> {
  await o.plugin
    .configureOfflineSync({ profileId: o.profile.id, intervalMinutes: s.intervalMinutes, wifiOnly: s.network === 'wifi', headersOnlyOlderThanDays: s.headersOnlyOlderThanDays ?? 0 })
    .catch(() => {});
}
