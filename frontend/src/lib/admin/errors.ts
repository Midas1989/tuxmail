// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Error messages of the administration in the user's language. The server
// answers with {error: code, message}, the message is English; known codes get
// a translated sentence, anything else falls back to the server's text.

import { adminErrorCode, type AdminErrorCode } from '../api/admin';
import type { MessageKey } from '../i18n/index.svelte';

type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

const KEYS = {
  weak_password: 'admin.errors.weak_password',
  exists: 'admin.errors.exists',
  last_admin: 'admin.errors.last_admin',
  invalid_input: 'admin.errors.invalid_input',
  not_found: 'admin.errors.not_found',
  mfa_setup_required: 'admin.errors.mfa_setup_required',
  forbidden: 'admin.errors.forbidden',
  registration_disabled: 'admin.errors.registration_disabled',
  throttled: 'admin.errors.throttled',
  reauth_required: 'admin.errors.reauth_required',
  network: 'admin.errors.network',
} as const satisfies Record<AdminErrorCode, MessageKey>;

/** The message for a failed admin call; `minLength` is the server's password policy if known. */
export function adminErrorMessage(err: unknown, tr: Translate, minLength = 0): string {
  const code = adminErrorCode(err);
  if (code === 'weak_password') return tr(KEYS.weak_password, { min: minLength || 12 });
  if (code) return tr(KEYS[code]);
  return err instanceof Error && err.message ? err.message : tr('admin.errors.unknown');
}
