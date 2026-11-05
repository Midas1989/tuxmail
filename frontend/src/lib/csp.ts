// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Content-Security-Policy of the <meta> tag in index.html per shell (NG-08).
// Pure module: used by vite.config.ts (build-time injection) and by the native
// bridge (runtime tightening), unit-tested in csp.test.ts.
//
//   shell                   who sets the CSP                         effective policy
//   ----------------------  ---------------------------------------  -----------------------------------------
//   web (tuxmail serve)     HTTP header (internal/server) AND meta   header AND meta: both strict, the meta is
//                                                                    never looser than the header
//   Wails desktop           meta only (shared build with web)        connect-src 'self', img-src 'self' data: blob:
//   Capacitor (Android)     meta only (vite --mode capacitor)        connect-src 'self' https: wss: (user-chosen
//                                                                    server, no cleartext ws:), img-src adds https:
//                                                                    for the image proxy; at runtime both are
//                                                                    narrowed to the active server's origin
//   vite dev server         meta (HMR)                               web policy + ws:/wss: for hot reload
//
// Sources the app does not need are absent on purpose: the app talks to its
// own origin (REST + WebSocket) or, in the thin clients, to the configured
// server; remote mail images only ever load through that server's image proxy.

export type CspTarget = 'web' | 'capacitor';

type Directives = Record<string, string[]>;

const BASE: Directives = {
  'default-src': ["'self'"],
  'script-src': ["'self'"],
  'style-src': ["'self'", "'unsafe-inline'"],
  'img-src': ["'self'", 'data:', 'blob:'],
  'font-src': ["'self'"],
  'connect-src': ["'self'"],
  // about: = srcdoc frames of the sandboxed mail view
  'frame-src': ["'self'", 'about:'],
  'worker-src': ["'self'"],
  'manifest-src': ["'self'"],
  'object-src': ["'none'"],
  'base-uri': ["'none'"],
  'form-action': ["'none'"],
};

const serialize = (d: Directives): string =>
  Object.entries(d)
    .map(([k, v]) => `${k} ${v.join(' ')}`)
    .join('; ');

/** Policy of the meta tag for a build target. `dev` adds the websocket of the Vite dev server. */
export function cspFor(target: CspTarget, opts: { dev?: boolean } = {}): string {
  const d: Directives = Object.fromEntries(Object.entries(BASE).map(([k, v]) => [k, [...v]]));
  if (target === 'capacitor') {
    d['connect-src'].push('https:', 'wss:');
    d['img-src'].push('https:');
  }
  if (opts.dev) d['connect-src'].push('ws:', 'wss:');
  return serialize(d);
}

/** Vite mode -> CSP target: `vite build --mode capacitor` (npm run build:capacitor) is the Android app; every other mode is web/Wails. */
export function targetForMode(mode: string | undefined): CspTarget {
  return mode === 'capacitor' ? 'capacitor' : 'web';
}

const META_CSP = /(<meta\s+http-equiv="Content-Security-Policy"\s+content=")[^"]*(")/;
const META_TARGET = /(<meta\s+name="tuxmail-csp-target"\s+content=")[^"]*(")/;

/** Rewrites the CSP meta tag and the target marker of index.html. */
export function applyCsp(html: string, target: CspTarget, opts: { dev?: boolean } = {}): string {
  if (!META_CSP.test(html) || !META_TARGET.test(html)) throw new Error('index.html: CSP meta tag or tuxmail-csp-target marker missing');
  return html.replace(META_CSP, `$1${cspFor(target, opts)}$2`).replace(META_TARGET, `$1${target}$2`);
}

/**
 * Additional policy for the Capacitor app once a server is configured: a
 * second meta CSP can only narrow, so connect/img are cut down from "any https
 * host" to the origin of the active server. Null for servers that are not https.
 */
export function nativeServerCsp(serverUrl: string): string | null {
  let u: URL;
  try {
    u = new URL(serverUrl);
  } catch {
    return null;
  }
  if (u.protocol !== 'https:') return null;
  return serialize({
    'connect-src': ["'self'", `https://${u.host}`, `wss://${u.host}`],
    'img-src': ["'self'", 'data:', 'blob:', `https://${u.host}`],
  });
}

/** Adds the narrowing policy to the document (before the app is mounted). Returns false if there is none. */
export function applyNativeServerCsp(doc: MetaHost, serverUrl: string): boolean {
  const policy = nativeServerCsp(serverUrl);
  if (!policy) return false;
  const meta = doc.createElement('meta');
  meta.httpEquiv = 'Content-Security-Policy';
  meta.content = policy;
  doc.head.appendChild(meta as never);
  return true;
}

/** The part of `Document` needed here (this module is also compiled by the DOM-less node tsconfig of vite.config.ts). */
export interface MetaHost {
  createElement(tag: 'meta'): { httpEquiv: string; content: string };
  head: { appendChild(node: never): unknown };
}
