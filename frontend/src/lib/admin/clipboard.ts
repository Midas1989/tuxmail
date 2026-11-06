// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Copy a one-time secret (generated password, invite link) to the clipboard.
// The async Clipboard API needs a secure context; plain http://localhost
// development and old WebViews fall back to a hidden textarea + execCommand.
// Resolves to false when neither works, so the dialog can ask the user to
// select the text manually.

export async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* denied or unavailable: try the fallback */
  }
  try {
    if (typeof document === 'undefined') return false;
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.append(ta);
    ta.select();
    const ok = typeof document.execCommand === 'function' && document.execCommand('copy');
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}
