// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Account security state of the web client: whether the server demands that
// this (administrator) account sets up two-factor authentication before
// anything else. The server reports it in /api/auth/me (`mfaSetupRequired`,
// also part of the login response, which the server-rendered /login page
// consumes) and by answering 403 `mfa_setup_required` on privileged routes;
// both end in the blocking dialog (MfaSetupDialog.svelte).

import type { Api } from '../api/api';
import type { AuthMe } from '../api/security';
import { onMfaSetupRequired } from '../mfaGate';
import { isNativeShell } from '../platform';

export interface SecurityDeps {
  /** Only the web client served by `tuxmail serve` has account security pages (not Wails, not the native app). */
  web?: boolean;
  /** Where to listen for 403 mfa_setup_required of the transport. */
  subscribe?: (listener: () => void) => () => void;
  /** Reload the application state after the set-up (default: reload the page). */
  reload?: () => void;
}

export class SecurityState {
  /** The blocking "Set up two-factor authentication" dialog is shown. */
  mfaSetupRequired = $state(false);
  /** Last known account data (for the dialog: hasPassword / username). */
  me = $state<AuthMe | null>(null);

  private readonly api: Api;
  private readonly web: boolean;
  private readonly subscribe: (listener: () => void) => () => void;
  private readonly reload: () => void;

  constructor(api: Api, deps: SecurityDeps = {}) {
    this.api = api;
    this.web = deps.web ?? (api.kind === 'http' && !isNativeShell());
    this.subscribe = deps.subscribe ?? onMfaSetupRequired;
    this.reload = deps.reload ?? (() => location.reload());
  }

  /** Account pages exist (server mode in the browser). */
  get available(): boolean {
    return this.web;
  }

  /** Start watching: ask /api/auth/me once and follow 403 mfa_setup_required. Returns the stop function. */
  start(): () => void {
    if (!this.web) return () => {};
    const stop = this.subscribe(() => this.require());
    void this.refresh();
    return stop;
  }

  /** Re-read /api/auth/me; shows or hides the dialog accordingly. A failing request changes nothing (401 is handled by the transport). */
  async refresh(): Promise<AuthMe | null> {
    if (!this.web) return null;
    try {
      const me = await this.api.getAuthMe();
      this.me = me;
      this.mfaSetupRequired = me.mfaSetupRequired;
      return me;
    } catch {
      return null;
    }
  }

  /** Show the dialog (server answered 403 mfa_setup_required). */
  require(): void {
    if (this.web) this.mfaSetupRequired = true;
  }

  /** TOTP is active now: close the dialog and reload everything (privileged functions work from here on). */
  finished(): void {
    this.mfaSetupRequired = false;
    this.reload();
  }
}
