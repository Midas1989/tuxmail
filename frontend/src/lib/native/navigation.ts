// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Android back button and deep links (pure decision logic + small adapters).

import { normalizeServerUrl } from '../platform';

export type BackAction =
  | 'handled'
  | 'close-dialog'
  | 'cancel-reply'
  | 'close-info'
  | 'close-sidebar'
  | 'close-conversation'
  | 'go-mail'
  | 'minimize';

export interface BackState {
  /** A component called preventDefault() on the `tuxmail:back` event. */
  handledByUi: boolean;
  dialogOpen: boolean;
  replying: boolean;
  infoOpen: boolean;
  sidebarOpen: boolean;
  /** A conversation/message is open in the single-pane phone layout. */
  detailOpen: boolean;
  route: string;
}

/** Innermost UI layer first, like native Android apps; never exit silently. */
export function decideBack(s: BackState): BackAction {
  if (s.handledByUi) return 'handled';
  if (s.dialogOpen) return 'close-dialog';
  if (s.replying) return 'cancel-reply';
  if (s.infoOpen) return 'close-info';
  if (s.sidebarOpen) return 'close-sidebar';
  if (s.detailOpen) return 'close-conversation';
  if (s.route !== 'mail') return 'go-mail';
  return 'minimize';
}

/** Name of the cancelable window event components may use to consume "back". */
export const BACK_EVENT = 'tuxmail:back';

export function dispatchBackEvent(target: EventTarget = window): boolean {
  const ev = new CustomEvent(BACK_EVENT, { cancelable: true });
  target.dispatchEvent(ev);
  return ev.defaultPrevented;
}

export interface DeepLinkTarget {
  /** Hash route to show, e.g. "#/mail?view=chat". */
  hash: string;
  conversationId?: number;
  messageId?: number;
  compose?: boolean;
}

export interface ConnectLink {
  /** tuxmail://connect?server=https://mail.example.org (QR code / link from the web UI). */
  server: string;
}

const ROUTES = new Set(['mail', 'search', 'calendar', 'contacts', 'files', 'settings', 'native-settings']);

function sanitizeHash(h: string): string {
  const raw = h.replace(/^#?\/?/, '');
  const [route, query = ''] = raw.split('?');
  const [seg, ...rest] = route.split('/');
  if (!ROUTES.has(seg)) return '#/mail';
  // Settings deep links: #/settings/<category>/<section> (plain slugs only).
  const sub = seg === 'settings' && rest.length <= 2 && rest.every((r) => /^[a-z0-9-]+$/.test(r)) ? rest.map((r) => '/' + r).join('') : '';
  return '#/' + seg + sub + (query ? '?' + query : '');
}

function num(v: string | null): number | undefined {
  if (v == null || !/^\d+$/.test(v)) return undefined;
  return Number(v);
}

/**
 * Map an opened URL to an in-app target. Accepted forms:
 *  - tuxmail://open?url=<payload url>&conversationId=12   (notification taps)
 *  - tuxmail://conversation/12, tuxmail://message/34, tuxmail://compose
 *  - any URL with a hash route ("https://server/#/calendar", "./#/mail?view=chat")
 * Unknown routes fall back to #/mail; nothing outside the app is navigated to.
 */
export function parseDeepLink(url: string): DeepLinkTarget | null {
  let u: URL;
  try {
    u = new URL(url, 'https://localhost/');
  } catch {
    return null;
  }
  if (u.protocol === 'tuxmail:') {
    // new URL('tuxmail://conversation/12'): host = "conversation", pathname = "/12"
    const kind = u.host || u.pathname.replace(/^\/+/, '').split('/')[0];
    const rest = u.host ? u.pathname.replace(/^\/+/, '') : u.pathname.replace(/^\/+/, '').split('/').slice(1).join('/');
    if (kind === 'open') {
      const inner = u.searchParams.get('url');
      const base: DeepLinkTarget = inner ? (parseDeepLink(inner) ?? { hash: '#/mail' }) : { hash: '#/mail' };
      const conv = num(u.searchParams.get('conversationId'));
      return conv != null ? { ...base, hash: base.hash === '#/mail' ? '#/mail?view=chat' : base.hash, conversationId: conv } : base;
    }
    if (kind === 'conversation') {
      const id = num(rest.split('/')[0]);
      return id != null ? { hash: '#/mail?view=chat', conversationId: id } : { hash: '#/mail' };
    }
    if (kind === 'message') {
      const id = num(rest.split('/')[0]);
      return id != null ? { hash: '#/mail', messageId: id } : { hash: '#/mail' };
    }
    if (kind === 'compose') return { hash: '#/mail', compose: true };
    if (kind === 'connect') return null;
    return { hash: '#/mail' };
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return null;
  const t: DeepLinkTarget = { hash: u.hash ? sanitizeHash(u.hash) : '#/mail' };
  const q = new URLSearchParams(t.hash.split('?')[1] ?? '');
  const conv = num(q.get('conversation') ?? q.get('conversationId'));
  if (conv != null) t.conversationId = conv;
  const msg = num(q.get('message') ?? q.get('messageId'));
  if (msg != null) t.messageId = msg;
  if (q.get('compose')) t.compose = true;
  return t;
}

/**
 * tuxmail://connect?server=<url>. The link is attacker-controllable (exported
 * BROWSABLE intent filter), so the server value is normalised with
 * normalizeServerUrl(): https only (plain http only for loopback development),
 * no query/fragment. Anything else is refused.
 */
export function parseConnectLink(url: string): ConnectLink | null {
  try {
    const u = new URL(url);
    if (u.protocol !== 'tuxmail:' || u.host !== 'connect') return null;
    const raw = (u.searchParams.get('server') ?? '').trim();
    // "https://mail.example.org@evil.example" is a classic look-alike trick: refuse userinfo (and whitespace/backslashes) outright.
    if (!raw || /[\s@\\]/.test(raw)) return null;
    const server = normalizeServerUrl(raw);
    return server ? { server } : null;
  } catch {
    return null;
  }
}

/** Full host (punycode, with port) and path of a normalised server URL, e.g. "mail.example.org:8443/tuxmail". Never abbreviated. */
export function serverDisplay(serverUrl: string): string {
  try {
    const u = new URL(serverUrl);
    return (u.host + u.pathname).replace(/\/+$/, '');
  } catch {
    return serverUrl;
  }
}
