// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import {
  MAX_SIGNATURE_IMAGE,
  SignatureImageError,
  applyLink,
  cleanSignatureHtml,
  htmlToPlain,
  insertImage,
  isAllowedLink,
  normalizeLink,
  readImageFile,
  removeLink,
  setImageWidth,
} from './signature';

const png = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';

function editor(html: string): HTMLDivElement {
  const div = document.createElement('div');
  div.innerHTML = html;
  document.body.appendChild(div);
  return div;
}

function select(node: Node, start: number, end: number): Range {
  const r = document.createRange();
  r.setStart(node, start);
  r.setEnd(node, end);
  return r;
}

describe('links', () => {
  it('allows only http, https, mailto and tel', () => {
    expect(isAllowedLink('https://acme.example')).toBe(true);
    expect(isAllowedLink('mailto:anna@acme.example')).toBe(true);
    expect(isAllowedLink('tel:+49891234')).toBe(true);
    for (const bad of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,<script>', 'vbscript:x', 'file:///etc/passwd', '/relative', 'ftp://x']) {
      expect(isAllowedLink(bad), bad).toBe(false);
    }
  });

  it('normalizes typed input', () => {
    expect(normalizeLink('www.acme.example')).toBe('https://www.acme.example');
    expect(normalizeLink('acme.example/kontakt')).toBe('https://acme.example/kontakt');
    expect(normalizeLink('anna@acme.example')).toBe('mailto:anna@acme.example');
    expect(normalizeLink('+49 (89) 123-456')).toBe('tel:+4989123456');
    expect(normalizeLink('javascript:alert(1)')).toBeNull();
    expect(normalizeLink('')).toBeNull();
  });

  it('wraps a selection, edits an existing link and removes it', () => {
    const ed = editor('<p>Besuchen Sie ACME heute</p>');
    const text = ed.querySelector('p')!.firstChild!;
    const a = applyLink(ed, select(text, 13, 17), 'acme.example');
    expect(a?.outerHTML).toBe('<a href="https://acme.example">ACME</a>');
    // Edit: cursor inside the link.
    const inLink = select(a!.firstChild!, 1, 1);
    applyLink(ed, inLink, 'https://acme.example/de', 'ACME GmbH');
    expect(ed.innerHTML).toBe('<p>Besuchen Sie <a href="https://acme.example/de">ACME GmbH</a> heute</p>');
    expect(removeLink(ed, select(ed.querySelector('a')!.firstChild!, 2, 2))).toBe(true);
    expect(ed.innerHTML).toBe('<p>Besuchen Sie ACME GmbH heute</p>');
  });

  it('inserts a new link at the cursor and rejects unsafe targets', () => {
    const ed = editor('<p>Mail: </p>');
    const p = ed.querySelector('p')!;
    applyLink(ed, select(p.firstChild!, 6, 6), 'anna@acme.example');
    expect(p.innerHTML).toBe('Mail: <a href="mailto:anna@acme.example">anna@acme.example</a>');
    expect(applyLink(ed, select(p.firstChild!, 0, 4), 'javascript:alert(1)')).toBeNull();
    expect(ed.querySelectorAll('a').length).toBe(1);
  });
});

describe('images', () => {
  it('inserts data: and https images, refuses others, resizes by width', () => {
    const ed = editor('<p>x</p>');
    const img = insertImage(ed, select(ed.querySelector('p')!.firstChild!, 1, 1), png, 'Logo', 120);
    expect(img?.getAttribute('width')).toBe('120');
    expect(insertImage(ed, null, 'https://cdn.example/logo.png')).not.toBeNull();
    expect(insertImage(ed, null, 'http://cdn.example/logo.png')).toBeNull();
    expect(insertImage(ed, null, 'javascript:alert(1)')).toBeNull();
    expect(insertImage(ed, null, 'data:image/svg+xml;base64,PHN2Zz4=')).toBeNull();
    img!.setAttribute('height', '50');
    setImageWidth(img!, 5000);
    expect(img!.getAttribute('width')).toBe('800');
    expect(img!.hasAttribute('height')).toBe(false);
  });

  it('rejects large and non-image files', async () => {
    await expect(readImageFile(new Blob([new Uint8Array(MAX_SIGNATURE_IMAGE + 1)], { type: 'image/png' }))).rejects.toBeInstanceOf(SignatureImageError);
    await expect(readImageFile(new Blob(['<svg/>'], { type: 'image/svg+xml' }))).rejects.toMatchObject({ reason: 'type' });
    await expect(readImageFile(new Blob([new Uint8Array(10)], { type: 'image/png' }))).resolves.toMatch(/^data:image\/png;base64,/);
  });
});

describe('cleanSignatureHtml', () => {
  it('removes scripts, handlers, unsafe links, frames, forms and external CSS', () => {
    const dirty =
      '<script>alert(1)</script><img src=x onerror=alert(1)><a href="javascript:alert(1)" onclick="x">a</a>' +
      '<iframe src="https://evil.example"></iframe><form><input></form><link rel=stylesheet href="https://evil.example/x.css">' +
      '<style>@import url(x)</style><svg onload=alert(1)></svg><div style="background:url(javascript:alert(1));color:red;position:fixed">t</div>' +
      `<img src="${png}" width="90" onload="x"><img src="https://cdn.example/a.png"><b onmouseover="x">B</b><custom>keep</custom>`;
    const { html, remote } = cleanSignatureHtml(dirty);
    for (const bad of ['script', 'onerror', 'onclick', 'onload', 'onmouseover', 'javascript', 'iframe', 'form', 'input', 'link', 'style>', '@import', 'svg', 'position', 'src="x"']) {
      expect(html, bad).not.toContain(bad);
    }
    expect(html).toContain('<div style="color:red">t</div>');
    expect(html).toContain(`<img src="${png}" width="90">`);
    expect(html).toContain('<b>B</b>');
    expect(html).toContain('keep');
    expect(remote).toBe(1);
  });

  it('keeps table layouts from signature generators', () => {
    const src = '<table cellpadding="0" cellspacing="0" style="border-collapse:collapse"><tbody><tr><td width="100" valign="top"><img src="https://cdn.example/l.png" width="90"></td><td><font color="#336699" size="2">Anna</font></td></tr></tbody></table>';
    expect(cleanSignatureHtml(src).html).toBe(src.replace('border-collapse:collapse', 'border-collapse:collapse'));
  });

  it('drops oversized data: images', () => {
    const big = 'data:image/png;base64,' + 'A'.repeat(Math.ceil((MAX_SIGNATURE_IMAGE * 4) / 3) + 8);
    const r = cleanSignatureHtml(`<img src="${big}"><p>x</p>`);
    expect(r.oversized).toBe(1);
    expect(r.html).toBe('<p>x</p>');
  });
});

describe('htmlToPlain', () => {
  it('writes links as "text <url>" and keeps line structure', () => {
    const html =
      '<p><b>Anna Berger</b><br>Geschäftsführerin</p><p><a href="https://acme.example">ACME GmbH</a> · ' +
      '<a href="mailto:anna@acme.example">anna@acme.example</a> · <a href="https://acme.example">https://acme.example</a></p><ul><li>eins</li><li>zwei</li></ul>';
    expect(htmlToPlain(html)).toBe('Anna Berger\nGeschäftsführerin\nACME GmbH <https://acme.example> · anna@acme.example · https://acme.example\n- eins\n- zwei');
  });

  it('uses alt text for images and ignores scripts', () => {
    expect(htmlToPlain(`<img src="${png}" alt="ACME Logo"><br>Anna<script>x</script>`)).toBe('ACME Logo\nAnna');
  });
});
