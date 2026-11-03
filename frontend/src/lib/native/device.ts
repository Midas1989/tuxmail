// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

/** "TuxMail Android (Pixel 8)" – shown in the server's device/session list. */
export function deviceName(ua: string = typeof navigator !== 'undefined' ? navigator.userAgent : ''): string {
  const m = /Android [\d.]+;\s*([^;)]+?)(?:\s+Build\/[^;)]*)?[;)]/.exec(ua);
  const model = m?.[1]?.trim();
  // Chrome's reduced User-Agent reports the model as "K".
  return model && model !== 'K' ? `TuxMail Android (${model})` : 'TuxMail Android';
}
