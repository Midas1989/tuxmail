// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Optional app lock (KONZEPT 15.9): BiometricPrompt with device-credential
// fallback (TuxMail.authenticate, Kotlin). Settings are per device in
// Capacitor Preferences.

import type { KeyValueStore } from './profiles';

export interface LockSettings {
  enabled: boolean;
  /** Seconds the app may stay in the background before it locks again (0 = immediately). */
  graceSeconds: number;
}

export const LOCK_KEY = 'tuxmail.appLock';
export const DEFAULT_LOCK: LockSettings = { enabled: false, graceSeconds: 0 };
export const GRACE_CHOICES = [0, 60, 300, 900] as const;

export function parseLockSettings(raw: string | null): LockSettings {
  if (!raw) return { ...DEFAULT_LOCK };
  try {
    const v = JSON.parse(raw) as Partial<LockSettings>;
    const grace = typeof v.graceSeconds === 'number' && v.graceSeconds >= 0 ? Math.min(Math.floor(v.graceSeconds), 86400) : 0;
    return { enabled: v.enabled === true, graceSeconds: grace };
  } catch {
    return { ...DEFAULT_LOCK };
  }
}

export async function loadLockSettings(prefs: KeyValueStore): Promise<LockSettings> {
  return parseLockSettings(await prefs.get(LOCK_KEY));
}

export async function saveLockSettings(prefs: KeyValueStore, s: LockSettings): Promise<void> {
  await prefs.set(LOCK_KEY, JSON.stringify(s));
}

/**
 * Lock on cold start whenever enabled; after returning from the background
 * only once the grace period has passed.
 */
export function shouldLock(s: LockSettings, backgroundSince: number | null, now: number): boolean {
  if (!s.enabled) return false;
  if (backgroundSince == null) return true;
  return now - backgroundSince >= s.graceSeconds * 1000;
}
