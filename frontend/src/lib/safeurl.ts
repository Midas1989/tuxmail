// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Links from untrusted content (invitations, subscribed calendars, vCards,
// chat link previews, shared links) that are rendered as <a href> in the
// app window - never inside the sandboxed mail frame. Svelte does not filter
// href values, so a "javascript:" URL would run in the app's origin (with
// access to the Wails bindings on desktop) if the CSP ever failed to block
// it, and "file:"/custom schemes would hand local paths to the OS.
// docs/security/AUDIT-B-content.md (B-03).

const SAFE = /^(?:https?:\/\/[^\s/]|mailto:|tel:|sips?:)/i;

/** The URL itself if it is a safe link target, otherwise undefined (renders no href). */
export function safeHref(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  const u = url.trim();
  // Control characters and whitespace inside a scheme are tolerated by URL
  // parsers ("java\tscript:") - such values are never links.
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(u)) return undefined;
  return SAFE.test(u) ? u : undefined;
}
