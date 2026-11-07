// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Shared phone breakpoint, the same as the media queries of the CSS: narrow screens and a phone held sideways
// (touch device that is not tall), so landscape phones get the phone layout as well.

import { MediaQuery } from 'svelte/reactivity';

export const phone: { readonly current: boolean } =
  typeof matchMedia === 'undefined' ? { current: false } : new MediaQuery('(max-width: 639px), (pointer: coarse) and (max-height: 500px)');

/**
 * Text scale the user chose in the system (Android font size / WebView text zoom). Fixed pixel heights do not grow
 * with it, so lists with a fixed row height multiply their height by this factor. 1 = normal.
 */
export function textScale(): number {
  if (typeof document === 'undefined' || !document.body) return 1;
  const probe = document.createElement('span');
  probe.textContent = 'M';
  probe.style.cssText = 'position:absolute;visibility:hidden;font-size:16px;line-height:1;padding:0;border:0';
  document.body.appendChild(probe);
  const h = probe.getBoundingClientRect().height;
  probe.remove();
  return h > 0 ? Math.min(2, Math.max(1, h / 16)) : 1;
}
