// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Shared types between the native controller (capacitor.ts) and its UI.

import type { LockSettings } from './applock';
import type { PushInfo } from './plugins';
import type { OfflineSettings } from './offline/rules';
import type { ForeignStore } from './offline/db';

/** The offline store of the active profile (settings section "Offline"). */
export interface OfflineController {
  settings(): OfflineSettings;
  save(s: OfflineSettings): Promise<void>;
  syncNow(): Promise<void>;
  clear(): Promise<void>;
  /** Stores of other accounts that still hold waiting items. */
  foreign(): ForeignStore[];
  discardForeign(name: string): Promise<void>;
  lockEnabled(): Promise<boolean>;
}

export type PushState = 'idle' | 'pending' | 'active' | 'denied' | 'not-configured' | 'no-distributor' | 'server-unsupported' | 'failed';

export interface PushStatus {
  state: PushState;
  message?: string;
}

export interface NativeController {
  serverLabel: string;
  userLabel: string;
  /** null when the WebView has no IndexedDB (then the app works online only, as before). */
  offline?: OfflineController | null;
  appVersion(): Promise<string>;
  lockSettings(): Promise<LockSettings>;
  saveLockSettings(s: LockSettings): Promise<void>;
  biometricAvailable(): Promise<boolean>;
  pushInfo(): Promise<PushInfo>;
  pushStatus(): PushStatus;
  enablePush(distributor?: string): Promise<PushStatus>;
  signOut(): Promise<void>;
  switchServer(): Promise<void>;
}
