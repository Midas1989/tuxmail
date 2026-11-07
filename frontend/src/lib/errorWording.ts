// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Codes that can reach any dialog of any transport, in the user's language.
import { t } from './i18n/index.svelte';

export function errorWording(code: string): string | undefined {
  switch (code) {
    case 'device_token_forbidden':
      return t('errors.deviceTokenForbidden');
    case 'password_required':
      return t('errors.passwordRequired');
    default:
      return undefined;
  }
}
