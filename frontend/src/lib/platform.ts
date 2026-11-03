// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Environment detection: Wails desktop vs. browser/PWA, iOS specifics.

import type { WailsWindow } from './api/wails';

export function isWails(win: unknown = typeof window !== 'undefined' ? window : undefined): boolean {
  return !!(win && typeof (win as WailsWindow).go === 'object' && (win as WailsWindow).go);
}

/** Open a URL in the user's real browser (Wails) or a new tab. */
export function openExternal(url: string): void {
  if (!/^https?:\/\//i.test(url)) return;
  const w = window as unknown as WailsWindow;
  if (w.runtime?.BrowserOpenURL) w.runtime.BrowserOpenURL(url);
  else window.open(url, '_blank', 'noopener,noreferrer');
}

export interface IosEnv {
  userAgent: string;
  platform?: string;
  maxTouchPoints?: number;
  /** navigator.standalone (iOS only) */
  standalone?: boolean;
  /** matchMedia('(display-mode: standalone)').matches */
  displayStandalone?: boolean;
  dismissed?: boolean;
}

export function isIos(env: Pick<IosEnv, 'userAgent' | 'platform' | 'maxTouchPoints'>): boolean {
  if (/iphone|ipad|ipod/i.test(env.userAgent)) return true;
  // iPadOS 13+ reports itself as a Mac.
  return env.platform === 'MacIntel' && (env.maxTouchPoints ?? 0) > 1;
}

/** Safari on iOS (not Chrome/Firefox/Edge wrappers, which cannot install PWAs on older iOS). */
export function isIosSafari(env: Pick<IosEnv, 'userAgent' | 'platform' | 'maxTouchPoints'>): boolean {
  return isIos(env) && /safari/i.test(env.userAgent) && !/crios|fxios|edgios|opios/i.test(env.userAgent);
}

/**
 * iOS only delivers Web Push to PWAs installed on the home screen, so we
 * explain "Zum Home-Bildschirm hinzufügen" until the app runs standalone.
 */
export function shouldShowIosInstallHint(env: IosEnv): boolean {
  if (env.dismissed) return false;
  if (env.standalone || env.displayStandalone) return false;
  return isIosSafari(env);
}

export function currentIosEnv(dismissed: boolean): IosEnv {
  const nav = navigator as Navigator & { standalone?: boolean };
  return {
    userAgent: nav.userAgent,
    platform: nav.platform,
    maxTouchPoints: nav.maxTouchPoints,
    standalone: nav.standalone,
    displayStandalone: typeof matchMedia !== 'undefined' && matchMedia('(display-mode: standalone)').matches,
    dismissed,
  };
}

/** VAPID public key (base64url) -> Uint8Array for pushManager.subscribe. */
export function urlBase64ToUint8Array(base64: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(b64);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** Normalise user input to an https origin + path ("cloud.example.de" -> "https://cloud.example.de"). */
export function normalizeServerUrl(input: string): string | null {
  let s = input.trim();
  if (!s) return null;
  if (!/^[a-z]+:\/\//i.test(s)) s = 'https://' + s;
  try {
    const u = new URL(s);
    if (u.protocol !== 'https:' && !(u.protocol === 'http:' && /^(localhost|127\.0\.0\.1|\[::1\])$/.test(u.hostname))) return null;
    return (u.origin + u.pathname).replace(/\/+$/, '');
  } catch {
    return null;
  }
}
