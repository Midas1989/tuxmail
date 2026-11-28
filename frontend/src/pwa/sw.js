// SPDX-License-Identifier: AGPL-3.0-or-later
//
// TuxMail service worker (web/PWA mode only - never registered in Wails).
// The build (vite.config.ts, plugin "tuxmail-sw") replaces the two
// placeholders below with the list of emitted files and a build id.
//
//  - App shell: precached on install, served cache-first; navigations are
//    network-first with the cached shell, then offline.html, as fallback.
//  - /api/* is never cached (local-first data lives in the core).
//  - Web Push: `push` shows a notification, `notificationclick` focuses or
//    opens the app at payload.url.

/* eslint-disable no-restricted-globals */
const BUILD_ID = '__BUILD_ID__';
const PRECACHE = /** @type {string[]} */ (__PRECACHE_MANIFEST__);
const CACHE = `tuxmail-shell-${BUILD_ID}`;
const OFFLINE_URL = './offline.html';
// The app shell is stored under the scope root (hash routing => one document).
const SHELL_URL = './';

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) => cache.addAll(PRECACHE.map((p) => new Request(p, { cache: 'reload' })))),
  );
  // No skipWaiting here: an open tab keeps the files of its own build (lazy chunks
  // still resolve) until the user confirms the update; a first install activates anyway.
});

self.addEventListener('message', (event) => {
  // Only pages of this origin may ask for the update (a service worker only gets
  // messages from its own clients; the check makes that explicit).
  const src = event.source;
  if (!src || !('url' in src) || new URL(src.url).origin !== self.location.origin) return;
  if (event.data && event.data.type === 'skip-waiting') self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith('tuxmail-shell-') && k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.includes('/api/')) return; // live data, websockets, uploads

  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => {
          // Only the real shell is stored, never /login, /device, /secret/* or a
          // redirected answer (L-16).
          const shell = new URL(SHELL_URL, self.location.href);
          const isShell = url.pathname === shell.pathname || url.pathname === shell.pathname + 'index.html';
          const copy = res.clone();
          if (res.ok && !res.redirected && isShell) caches.open(CACHE).then((c) => c.put(SHELL_URL, copy)).catch(() => {});
          return res;
        })
        .catch(async () => (await caches.match(SHELL_URL)) || (await caches.match(OFFLINE_URL)) || Response.error()),
    );
    return;
  }

  // Static assets: cache-first, fill the cache at runtime for anything missed.
  event.respondWith(
    // ignoreVary: dev/preview servers send "Vary: Origin", module scripts carry an Origin header.
    caches.match(req, { ignoreVary: true }).then(
      (hit) =>
        hit ||
        fetch(req).then((res) => {
          if (res.ok && res.type === 'basic') {
            const copy = res.clone();
            caches.open(CACHE).then((c) => c.put(req, copy)).catch(() => {});
          }
          return res;
        }),
    ),
  );
});

// ---------------------------------------------------------------------------
// Web Push
// payload: { type, title, body, url, tag, accountId, conversationId, sender, itemId, teamId, badge }
// ---------------------------------------------------------------------------

self.addEventListener('push', (event) => {
  let p = {};
  try {
    p = event.data ? event.data.json() : {};
  } catch {
    p = { title: 'TuxMail', body: event.data ? event.data.text() : '' };
  }
  const title = p.title || 'TuxMail';
  const options = {
    body: p.body || '',
    tag: p.tag || (p.conversationId != null ? `conv-${p.conversationId}` : undefined),
    renotify: !!p.tag,
    icon: './icons/icon-192.png',
    badge: './icons/badge-72.png',
    data: {
      url: p.url || './#/mail',
      type: p.type,
      accountId: p.accountId,
      conversationId: p.conversationId,
      teamId: p.teamId,
    },
  };
  const shown = self.registration.showNotification(title, options);
  // Unread counter on the app icon (Badging API, where supported).
  const badge = p.badge > 0 && self.navigator && self.navigator.setAppBadge ? self.navigator.setAppBadge(p.badge).catch(() => {}) : null;
  event.waitUntil(badge ? Promise.all([shown, badge]) : shown);
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  let target = new URL((event.notification.data && event.notification.data.url) || './', self.registration.scope).href;
  // Only pages of this app: a push payload must not open foreign sites (AUDIT-D D-11).
  if (!target.startsWith(self.registration.scope)) target = self.registration.scope;
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(async (list) => {
      const scope = self.registration.scope;
      const client = list.find((c) => c.url.startsWith(scope));
      if (client) {
        await client.focus();
        // Hash routes: let the page navigate without a reload.
        client.postMessage({ type: 'navigate', url: target });
        return;
      }
      await self.clients.openWindow(target);
    }),
  );
});

