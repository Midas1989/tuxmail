// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Shared types between the native controller (capacitor.ts) and its UI.

import type { LockSettings } from './applock';
import type { PushInfo } from './plugins';

export type PushState = 'idle' | 'pending' | 'active' | 'denied' | 'not-configured' | 'no-distributor' | 'server-unsupported' | 'failed';

export interface PushStatus {
  state: PushState;
  message?: string;
}

export interface NativeController {
  serverLabel: string;
  userLabel: string;
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
