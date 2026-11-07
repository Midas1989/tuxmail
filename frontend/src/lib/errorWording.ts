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
    case 'cloud_not_api':
      return t('errors.cloudNotApi');
    case 'cloud_auth':
      return t('errors.cloudAuth');
    case 'cloud_forbidden':
      return t('errors.cloudForbidden');
    case 'cloud_quota':
      return t('errors.cloudQuota');
    case 'cloud_unreachable':
      return t('errors.cloudUnreachable');
    case 'cloud_error':
      return t('errors.cloudError');
    case 'notify_unavailable':
      return t('errors.notifyUnavailable');
    default:
      return undefined;
  }
}
