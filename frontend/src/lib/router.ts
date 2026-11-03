// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tiny hash router: #/mail, #/calendar, #/contacts, #/files, #/settings.
// Hash routing works identically in Wails (custom scheme), behind a reverse
// proxy and when the PWA is opened from the home screen.

import type { Route } from './state/app.svelte';

export const ROUTE_LIST: readonly Route[] = ['mail', 'search', 'calendar', 'contacts', 'files', 'settings'];

export function parseHash(hash: string): Route {
  const seg = hash.replace(/^#\/?/, '').split(/[/?]/)[0];
  return (ROUTE_LIST as readonly string[]).includes(seg) ? (seg as Route) : 'mail';
}

export function startRouter(onRoute: (r: Route) => void): () => void {
  const handler = () => onRoute(parseHash(location.hash));
  window.addEventListener('hashchange', handler);
  handler();
  return () => window.removeEventListener('hashchange', handler);
}
