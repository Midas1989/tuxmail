// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Leaving a session (NG-07): sign-out, server switch and a 401 that ends the
// session must not leave per-user conveniences behind in the browser/WebView
// (recipient addresses in tuxmail.pwdelivery.v1, cloud paths, cached
// preferences, ...). Everything the app stores under the `tuxmail.` prefix is
// removed from localStorage and sessionStorage.

import type { HttpTransportOptions } from './api/http';
import { nativeHttpOptions } from './native/session';
import { isNativeShell } from './platform';
import { notifyMfaSetupRequired } from './mfaGate';

export const STORAGE_PREFIX = 'tuxmail.';

/** Guard against reload loops if the server keeps answering 401 right after a reload (not a tuxmail.* key: survives the clearing). */
const GUARD_KEY = 'tm.sessionLostAt';
const GUARD_MS = 30_000;

function stores(): Storage[] {
  const out: Storage[] = [];
  for (const get of [() => localStorage, () => sessionStorage]) {
    try {
      out.push(get());
    } catch {
      /* storage blocked: nothing to clear there */
    }
  }
  return out;
}

/** Removes every `tuxmail.*` key (except `keep`); returns how many were removed. */
export function clearTuxmailStorage(targets: Storage[] = stores(), keep: readonly string[] = []): number {
  let n = 0;
  for (const s of targets) {
    try {
      const keys: string[] = [];
      for (let i = 0; i < s.length; i++) {
        const k = s.key(i);
        if (k?.startsWith(STORAGE_PREFIX) && !keep.includes(k)) keys.push(k);
      }
      for (const k of keys) {
        s.removeItem(k);
        n++;
      }
    } catch {
      /* ignore */
    }
  }
  return n;
}

export interface SignOutDeps {
  api: { logout(): Promise<void> };
  /** Where to go afterwards (the server answers with its login page). */
  reload?: () => void;
  clear?: () => void;
}

/**
 * Web sign-out: end the server session (POST /api/auth/logout through the API
 * layer), clear local data, reload to the login page. Local data is cleared
 * even if the server cannot be reached; in that case the error is rethrown
 * instead of reloading so the caller can tell the user the session may still be active.
 */
export async function signOutWeb({ api, reload = () => location.replace(location.pathname), clear = () => void clearTuxmailStorage() }: SignOutDeps): Promise<void> {
  let failure: unknown;
  try {
    await api.logout();
  } catch (err) {
    failure = err;
  }
  clear();
  if (failure) throw failure;
  reload();
}

/** A request failed with 401 "unauthenticated" on a cookie session: the session is gone. Clear local data and go to the login page. */
export function sessionLost(reload: () => void = () => location.reload(), clear: () => void = () => void clearTuxmailStorage()): boolean {
  clear();
  try {
    const last = Number(sessionStorage.getItem(GUARD_KEY) ?? 0);
    if (last && Date.now() - last < GUARD_MS) return false;
    sessionStorage.setItem(GUARD_KEY, String(Date.now()));
  } catch {
    /* no guard available: reload once per page load below */
  }
  reload();
  return true;
}

/** HttpTransport options of this platform: native bearer session, or the web cookie session with session-loss handling and the forced two-factor set-up (403 mfa_setup_required). */
export function platformHttpOptions(): HttpTransportOptions {
  return isNativeShell() ? nativeHttpOptions() : { onUnauthorized: () => void sessionLost(), onMfaSetupRequired: notifyMfaSetupRequired };
}
