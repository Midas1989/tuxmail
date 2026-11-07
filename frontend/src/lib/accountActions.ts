// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Sign-out of the web client as the account menu and the settings pages offer
// it: session.ts does the work, this tells the user if the server was not reached.

import { signOutWeb } from './session';
import { app } from './state';
import { t } from './i18n/index.svelte';

export async function signOut(): Promise<void> {
  try {
    await signOutWeb({ api: app.api });
  } catch (err) {
    app.toast(t('settings.pages.session.failed', { error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
  }
}
