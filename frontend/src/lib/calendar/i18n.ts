// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Translation of keys computed at runtime (partstat, kinds, types ...).
// All possible keys exist in locales/en.json (checked by the i18n tests
// through the static keys of the PIM screens).

import { t, type MessageKey } from '../i18n/index.svelte';

export function tk(key: string, params?: Record<string, string | number>): string {
  return t(key as MessageKey, params);
}
