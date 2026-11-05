// SPDX-License-Identifier: AGPL-3.0-or-later
import type { Api, TransportKind } from './api';
import { HttpTransport } from './http';
import { MockTransport } from './mock';
import { WailsTransport, type WailsWindow } from './wails';
import { hasNativeSession, nativeHttpOptions } from '../native/session';
import { isNativeShell } from '../platform';

export interface TransportEnv {
  /** `window.go` when running inside Wails. */
  go?: unknown;
  /** `location.search`, e.g. "?api=http". */
  search?: string;
  /** Native app (Capacitor) with a configured server profile. */
  native?: boolean;
  /** `<meta name="tuxmail-api">` injected by `tuxmail serve` ("http"). */
  meta?: string;
  /** Development build (default: `import.meta.env.DEV`). `?api=mock` is honoured only here. */
  dev?: boolean;
}

/**
 * Decide which transport to use:
 *  1. Wails if `window.go` exists (desktop shell),
 *  2. HTTP if `?api=http` (web client against `tuxmail serve`),
 *  3. otherwise the in-memory mock (demo / development).
 * `?api=mock` forces the mock (useful for UI work) but ONLY in development
 * builds and never when the server injected the tuxmail-api meta tag, inside
 * Wails or in a native shell: otherwise a crafted link would show fabricated
 * mailboxes under the real, trusted origin (NG-02).
 */
export function selectTransportKind(env: TransportEnv): TransportKind {
  const api = new URLSearchParams(env.search ?? '').get('api');
  const dev = env.dev ?? import.meta.env.DEV;
  const hostedByTuxMail = !!env.meta || !!env.go || !!env.native;
  if (api === 'mock' && dev && !hostedByTuxMail) return 'mock';
  if (env.go && typeof env.go === 'object') return 'wails';
  if (env.native) return 'http';
  if (api === 'http' || env.meta === 'http') return 'http';
  return 'mock';
}

export function createApi(kind: TransportKind, win?: WailsWindow): Api {
  switch (kind) {
    case 'wails':
      return new WailsTransport(win);
    case 'http':
      return new HttpTransport(isNativeShell() ? nativeHttpOptions() : {});
    case 'mock':
      return new MockTransport();
  }
}

export function createApiFromWindow(): Api {
  const w = (typeof window !== 'undefined' ? window : {}) as WailsWindow & { location?: Location };
  const meta = typeof document !== 'undefined' ? document.querySelector<HTMLMetaElement>('meta[name="tuxmail-api"]')?.content : undefined;
  const kind = selectTransportKind({ go: w.go, search: w.location?.search, native: isNativeShell() && hasNativeSession(), meta, dev: import.meta.env.DEV && !isNativeShell() });
  return createApi(kind, w);
}
