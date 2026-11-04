// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { buildSrcdoc, plainToHtml } from './components/mail/MailFrame.svelte';

// Audit B (docs/security/AUDIT-B-content.md): the mail iframe srcdoc must
// carry a CSP that blocks scripts, framing, objects, base and forms, and
// must not restore blocked remote images to their direct URLs.
describe('buildSrcdoc CSP', () => {
  const doc = buildSrcdoc({ html: '<p>hi</p>', allowRemote: false, dark: false });
  it('denies active content and framing', () => {
    const csp = /content="([^"]*Content-Security|[^"]*default-src[^"]*)"/i;
    for (const token of ["default-src 'none'", "script-src 'none'", "object-src 'none'", "frame-src 'none'", "base-uri 'none'", "form-action 'none'"]) {
      expect(doc).toContain(token);
    }
    expect(csp.test(doc) || doc.includes('Content-Security-Policy')).toBe(true);
  });
  it('blocks remote images unless allowed', () => {
    expect(doc).toContain('img-src data: cid:');
    expect(doc).not.toContain('https:');
  });
  it('never emits a direct remote src for a blocked image', () => {
    const html = '<img src="data:image/gif;base64,AA" data-remote-src="https://tracker.test/p.gif">';
    const out = buildSrcdoc({ html, allowRemote: true, dark: false });
    expect(/(^|[^-])src="https:\/\/tracker\.test/.test(out)).toBe(false);
    expect(out).toContain('data-remote-src="https://tracker.test/p.gif"');
  });
});

describe('plainToHtml', () => {
  it('escapes HTML from plain-text mails', () => {
    const out = plainToHtml('<script>top.__pwned=1</script> & <b>');
    expect(out).not.toContain('<script>');
    expect(out).toContain('&lt;script&gt;');
  });
  it('linkifies but escapes the url', () => {
    const out = plainToHtml('see https://example.test/"><x');
    expect(out).toContain('<a href="https://example.test/');
    expect(out).not.toContain('"><x');
  });
});
