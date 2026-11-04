// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { safeHref } from './safeurl';

// Audit B (docs/security/AUDIT-B-content.md, B-03): links from untrusted
// content (invitations, vCards, link previews, subscribed calendars) are
// rendered as <a href> in the app window; only safe schemes may pass.
describe('safeHref', () => {
  it('keeps http(s), mailto, tel', () => {
    for (const u of ['https://example.test/x', 'http://example.test', 'mailto:a@b.test', 'tel:+49123', 'sip:a@b.test']) {
      expect(safeHref(u)).toBe(u);
    }
  });
  it('drops javascript, data, file, vbscript and obfuscations', () => {
    for (const u of [
      'javascript:alert(1)',
      'JaVaScRiPt:alert(1)',
      ' javascript:alert(1)',
      'java\tscript:alert(1)',
      'java\nscript:alert(1)',
      'data:text/html,<script>top.__pwned=1</script>',
      'file:///etc/passwd',
      'vbscript:msgbox(1)',
      'chrome://settings',
      '\u0000https://ok.test',
      'https://',
    ]) {
      expect(safeHref(u)).toBeUndefined();
    }
  });
  it('returns undefined for empty input', () => {
    expect(safeHref('')).toBeUndefined();
    expect(safeHref(null)).toBeUndefined();
    expect(safeHref(undefined)).toBeUndefined();
  });
});
