// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Whether the signed-in user may administer the server (Settings ->
// Administration is shown only then). The role comes from GET /api/auth/me,
// not from probing an admin route; there is no client at all in the desktop
// shell and the native app (api/admin.ts createAdminClient).

import type { AdminClient, AdminMe, AdminServerConfig } from '../api/admin';

export class AdminState {
  /** Null where the server cannot be administered from this client. */
  readonly client: AdminClient | null;
  me = $state<AdminMe | null>(null);
  /** Server settings that shape the forms (password policy, invites); best effort. */
  config = $state<AdminServerConfig | null>(null);
  private started: Promise<void> | null = null;

  constructor(client: AdminClient | null) {
    this.client = client;
  }

  /** True for an administrator on a web client against a TuxMail server. */
  get isAdmin(): boolean {
    return this.client !== null && this.me?.role === 'admin';
  }

  /** Reads the role once; a failure (not signed in, server down) leaves the page hidden. */
  init(): Promise<void> {
    if (!this.client) return Promise.resolve();
    const c = this.client;
    this.started ??= (async () => {
      try {
        this.me = await c.me();
      } catch {
        this.me = null;
        return;
      }
      if (this.me.role === 'admin') this.config = await c.config().catch(() => null);
    })();
    return this.started;
  }
}
