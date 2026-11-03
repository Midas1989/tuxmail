// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Synchronous view of the active server session inside the native app
// (Capacitor). The API transport is created when the state module is
// evaluated (state/index.ts), i.e. before any async storage can be read, so
// the non-secret part (server URL, profile id) is mirrored into localStorage
// and the device token is handed over by boot.ts before <App> is mounted.
// This module must stay free of Capacitor imports: it is part of the main
// bundle that also runs in the browser and in Wails.

import type { HttpTransportOptions } from '../api/http';

/** localStorage key of the mirrored active profile (no secrets). */
export const ACTIVE_MIRROR_KEY = 'tuxmail.native.active';

export interface ActiveMirror {
  profileId: string;
  serverUrl: string;
}

let token: string | undefined;
let unauthorizedHandler: (() => void) | undefined;

function storage(): Storage | undefined {
  try {
    return typeof localStorage !== 'undefined' ? localStorage : undefined;
  } catch {
    return undefined;
  }
}

export function readActiveMirror(store: Storage | undefined = storage()): ActiveMirror | null {
  try {
    const raw = store?.getItem(ACTIVE_MIRROR_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<ActiveMirror>;
    return typeof v.serverUrl === 'string' && typeof v.profileId === 'string' && v.serverUrl ? { profileId: v.profileId, serverUrl: v.serverUrl } : null;
  } catch {
    return null;
  }
}

export function writeActiveMirror(m: ActiveMirror | null, store: Storage | undefined = storage()): void {
  try {
    if (m) store?.setItem(ACTIVE_MIRROR_KEY, JSON.stringify(m));
    else store?.removeItem(ACTIVE_MIRROR_KEY);
  } catch {
    /* storage unavailable: the next start shows the connect screen */
  }
}

/** Set by boot.ts once the token has been read from secure storage. */
export function setSessionToken(t: string | undefined): void {
  token = t;
}

export function sessionToken(): string | undefined {
  return token;
}

export function setUnauthorizedHandler(fn: (() => void) | undefined): void {
  unauthorizedHandler = fn;
}

/** True when the native shell has an active server profile. */
export function hasNativeSession(store?: Storage): boolean {
  return readActiveMirror(store) !== null;
}

/** HttpTransport options for the active profile (base URL + Bearer token). */
export function nativeHttpOptions(store?: Storage): HttpTransportOptions {
  const m = readActiveMirror(store);
  return {
    baseUrl: m?.serverUrl ?? '',
    authToken: () => token,
    onUnauthorized: () => unauthorizedHandler?.(),
  };
}
