// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Helpers of the HTML signature editor (SignatureEditor.svelte): a
// client-side allowlist cleaner (the server sanitizes again on save and
// when sending - this one only keeps pasted HTML from running in the app),
// link and image handling, and the plain-text fallback.

import { withStyleNonce } from './stylenonce';

/** Size limit of one signature image (the server enforces the same). */
export const MAX_SIGNATURE_IMAGE = 1024 * 1024;
export const SIGNATURE_IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];

const ALLOWED_TAGS = new Set([
  'a', 'abbr', 'address', 'b', 'big', 'blockquote', 'br', 'caption', 'center', 'cite', 'code', 'col', 'colgroup', 'dd', 'del', 'div', 'dl', 'dt',
  'em', 'font', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'hr', 'i', 'img', 'ins', 'li', 'mark', 'ol', 'p', 'pre', 'q', 's', 'small', 'span', 'strike',
  'strong', 'sub', 'sup', 'table', 'tbody', 'td', 'tfoot', 'th', 'thead', 'tr', 'tt', 'u', 'ul',
]);
/** Elements removed together with their content. */
const DROP_TAGS = new Set([
  'script', 'style', 'iframe', 'frame', 'frameset', 'object', 'embed', 'applet', 'form', 'input', 'button', 'select', 'textarea', 'option',
  'template', 'svg', 'math', 'video', 'audio', 'canvas', 'title', 'base', 'meta', 'link', 'noscript', 'head', 'dialog', 'param', 'source',
]);
const GLOBAL_ATTRS = new Set(['style', 'class', 'dir', 'lang', 'title', 'align', 'valign']);
const TAG_ATTRS: Record<string, string[]> = {
  a: ['href'],
  img: ['src', 'alt', 'width', 'height', 'border'],
  font: ['color', 'face', 'size'],
  table: ['width', 'height', 'border', 'cellpadding', 'cellspacing', 'bgcolor'],
  td: ['width', 'height', 'colspan', 'rowspan', 'bgcolor', 'nowrap'],
  th: ['width', 'height', 'colspan', 'rowspan', 'bgcolor', 'nowrap'],
  tr: ['bgcolor', 'height'],
  col: ['width', 'span'],
  colgroup: ['width', 'span'],
  ol: ['start', 'type'],
  ul: ['type'],
  hr: ['width', 'size', 'color'],
  div: ['width'],
};
const CSS_BLOCKED = /expression|javascript|vbscript|behavior|binding|@import|url\s*\(|[\\<>{}]|position\s*:/i;

/** Allowed link targets: http, https, mailto and tel. */
export function isAllowedLink(url: string): boolean {
  return /^(https?:\/\/[^\s/]+|mailto:[^\s]+|tel:[+\d][\d\s()./-]*$)/i.test(url.trim());
}

/**
 * Normalizes what a user types into a link field: bare host names become
 * https://, e-mail addresses mailto:, phone numbers tel:. Returns null for
 * anything that is not an allowed link.
 */
export function normalizeLink(input: string): string | null {
  let u = input.trim();
  if (!u) return null;
  if (/^[^\s@:/]+@[^\s@:/]+\.[^\s@:/]+$/.test(u)) u = 'mailto:' + u;
  else if (/^\+?[\d][\d\s()./-]{4,}$/.test(u)) u = 'tel:' + u.replace(/[\s()./-]/g, '');
  else if (/^(www\.)?[a-z0-9-]+(\.[a-z0-9-]+)+([/?#].*)?$/i.test(u)) u = 'https://' + u;
  return isAllowedLink(u) ? u : null;
}

/** Allowed image sources: https URLs and raster data: URIs. */
export function isAllowedImageSrc(src: string): boolean {
  const s = src.trim();
  return /^https:\/\/\S+$/i.test(s) || /^data:image\/(png|jpeg|jpg|gif|webp);base64,[a-z0-9+/=\s]+$/i.test(s);
}

/** Approximate decoded size of a base64 data: URI. */
export function dataURISize(uri: string): number {
  const i = uri.indexOf(',');
  if (i < 0) return 0;
  const b64 = uri.slice(i + 1).replace(/\s/g, '');
  return Math.floor((b64.length * 3) / 4) - (b64.endsWith('==') ? 2 : b64.endsWith('=') ? 1 : 0);
}

function cleanStyle(style: string, dropHiding = false): string {
  return style
    .split(';')
    .map((d) => d.trim())
    .filter((d) => d && d.includes(':') && !CSS_BLOCKED.test(d) && !(dropHiding && CSS_HIDING.test(d)))
    .join('; ');
}

/**
 * Cleans HTML for the editor with an allowlist (tags, attributes, CSS).
 * Parsing uses an inert document, so nothing loads or runs while cleaning.
 * Data: images above the size limit are reported in `oversized`.
 */
export function cleanSignatureHtml(html: string): { html: string; oversized: number; remote: number } {
  return cleanHtml(html, {});
}

interface CleanOptions {
  /** Drop images with remote (https) sources instead of keeping them (composer paste: no tracking pixels). */
  dropRemoteImages?: boolean;
  /** Drop CSS declarations that hide content (display:none, font-size:0, ...). */
  dropHiding?: boolean;
  /** Drop `class` attributes: inside the live app DOM they could pick up the app's own CSS (overlays, UI spoofing). */
  dropClass?: boolean;
}

/** Declarations that hide pasted text from the user but not from the recipient. */
const CSS_HIDING = /display\s*:\s*none|visibility\s*:\s*hidden|opacity\s*:\s*0(?![.\d])|font-size\s*:\s*0(?![.\d])|max-(height|width)\s*:\s*0(?![.\d])|color\s*:\s*transparent|mso-hide/i;

/**
 * Cleans clipboard/dropped HTML for the mail composer (NG-05): the signature
 * allow-list (no scripts, handlers, forms, svg, resource-loading CSS), plus no
 * remote images (tracking pixels) and no content hidden via CSS. Basic
 * formatting (b/i/u, lists, quotes, links, tables, colours) is kept.
 */
export function cleanComposerHtml(html: string): { html: string; oversized: number; remote: number } {
  return cleanHtml(html, { dropRemoteImages: true, dropHiding: true, dropClass: true });
}

export type PastePlan = { kind: 'html'; html: string } | { kind: 'text'; text: string } | null;

/**
 * What to insert for a paste/drop: allow-list cleaned HTML if the clipboard
 * has any, else the plain text. Never the raw HTML. Returns null if empty.
 */
export function planPaste(getData: (type: string) => string): PastePlan {
  const raw = getData('text/html');
  if (raw) {
    const cleaned = cleanComposerHtml(raw).html;
    // Only structure left (no text, no image): fall back to the plain text flavour.
    if (/<img\b/i.test(cleaned) || cleaned.replace(/<[^>]*>/g, '').trim()) return { kind: 'html', html: cleaned };
  }
  const text = getData('text/plain');
  return text ? { kind: 'text', text } : null;
}

/** Signature block as inserted into the live composer: server HTML is re-cleaned client-side (NG-06, defence in depth). */
export function composerSignatureHtml(signatureHtml: string): string {
  return `-- <br>${cleanHtml(signatureHtml, { dropClass: true }).html}`;
}

function cleanHtml(html: string, opts: CleanOptions): { html: string; oversized: number; remote: number } {
  const doc = new DOMParser().parseFromString(`<!DOCTYPE html><html><body>${html}</body></html>`, 'text/html');
  let oversized = 0;
  let remote = 0;
  const walk = (parent: Element) => {
    for (const node of [...parent.childNodes]) {
      if (node.nodeType === Node.COMMENT_NODE) {
        node.remove();
        continue;
      }
      if (node.nodeType !== Node.ELEMENT_NODE) continue;
      const el = node as Element;
      const tag = el.tagName.toLowerCase();
      if (DROP_TAGS.has(tag) || el.namespaceURI !== 'http://www.w3.org/1999/xhtml') {
        el.remove();
        continue;
      }
      walk(el);
      if (!ALLOWED_TAGS.has(tag)) {
        el.replaceWith(...el.childNodes); // keep the content of unknown wrappers
        continue;
      }
      const allowed = TAG_ATTRS[tag] ?? [];
      for (const a of [...el.attributes]) {
        const name = a.name.toLowerCase();
        const globalOk = GLOBAL_ATTRS.has(name) && !(opts.dropClass && name === 'class');
        if (!globalOk && !allowed.includes(name)) el.removeAttribute(a.name);
      }
      const style = el.getAttribute('style');
      if (style != null) {
        const c = cleanStyle(style, opts.dropHiding);
        if (c) el.setAttribute('style', c);
        else el.removeAttribute('style');
      }
      if (tag === 'a') {
        const href = el.getAttribute('href');
        if (href != null && !isAllowedLink(href)) el.removeAttribute('href');
      }
      if (tag === 'img') {
        const src = el.getAttribute('src') ?? '';
        if (!isAllowedImageSrc(src)) {
          el.remove();
          continue;
        }
        if (src.startsWith('data:') && dataURISize(src) > MAX_SIGNATURE_IMAGE) {
          oversized++;
          el.remove();
          continue;
        }
        if (/^https:/i.test(src)) {
          remote++;
          if (opts.dropRemoteImages) {
            el.remove();
            continue;
          }
        }
      }
    }
  };
  walk(doc.body);
  return { html: doc.body.innerHTML, oversized, remote };
}

/**
 * Plain-text version of signature HTML: blocks and <br> become line breaks,
 * list items "- ", links "text <url>" (only the URL when the text is the
 * URL), images their alt text. Mirrors sanitize.ToText on the server.
 */
export function htmlToPlain(html: string): string {
  const doc = new DOMParser().parseFromString(`<!DOCTYPE html><html><body>${html}</body></html>`, 'text/html');
  const BLOCK = /^(p|div|table|tr|ul|ol|li|h[1-6]|blockquote|pre|address|center|dl|dt|dd|hr)$/;
  let out = '';
  const nl = () => {
    if (out && !out.endsWith('\n')) out += '\n';
  };
  const walk = (n: Node) => {
    if (n.nodeType === Node.TEXT_NODE) {
      const text = (n.textContent ?? '').replace(/[\s ]+/g, ' ');
      if (text.trim() || (out && !out.endsWith(' ') && !out.endsWith('\n'))) out += out.endsWith('\n') ? text.trimStart() : text;
      return;
    }
    if (n.nodeType !== Node.ELEMENT_NODE) return;
    const el = n as Element;
    const tag = el.tagName.toLowerCase();
    if (tag === 'script' || tag === 'style') return;
    if (tag === 'br') {
      out = out.replace(/ +$/, '') + '\n';
      return;
    }
    if (tag === 'img') {
      const alt = el.getAttribute('alt')?.trim();
      if (alt) out += alt;
      return;
    }
    const block = BLOCK.test(tag);
    if (block) nl();
    if (tag === 'li') out += '- ';
    if (tag === 'td' || tag === 'th') {
      if (out && !out.endsWith('\n') && !out.endsWith(' ')) out += ' ';
    }
    const start = out.length;
    el.childNodes.forEach(walk);
    if (tag === 'a') {
      const href = el.getAttribute('href') ?? '';
      const text = out.slice(start).trim();
      const bare = href.replace(/^mailto:/i, '').replace(/^tel:/i, '');
      if (href && isAllowedLink(href) && text !== href && text !== bare) out += ` <${href}>`;
      else if (href && !text) out += bare;
    }
    if (block) nl();
  };
  doc.body.childNodes.forEach(walk);
  return out
    .split('\n')
    .map((l) => l.replace(/[ \t]+$/g, '').replace(/^ +/, ''))
    .join('\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

/** Reads an image file as data: URI; rejects other types and files above the limit. */
export function readImageFile(file: Blob): Promise<string> {
  if (!SIGNATURE_IMAGE_TYPES.includes(file.type)) return Promise.reject(new SignatureImageError('type'));
  if (file.size > MAX_SIGNATURE_IMAGE) return Promise.reject(new SignatureImageError('size'));
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error ?? new Error('read failed'));
    r.readAsDataURL(file);
  });
}

export class SignatureImageError extends Error {
  constructor(readonly reason: 'type' | 'size') {
    super(reason === 'size' ? 'image too large' : 'unsupported image type');
  }
}

/** The <a> containing node (inside root), if any. */
export function linkAt(node: Node | null, root: Element): HTMLAnchorElement | null {
  for (let n: Node | null = node; n && n !== root; n = n.parentNode) {
    if (n.nodeType === Node.ELEMENT_NODE && (n as Element).tagName === 'A') return n as HTMLAnchorElement;
  }
  return null;
}

/**
 * Inserts or updates a link at range (inside root): an existing link around
 * the selection gets the new target (and text), a selection is wrapped, an
 * empty selection inserts the text (or the URL) as new link.
 */
export function applyLink(root: Element, range: Range, url: string, text?: string): HTMLAnchorElement | null {
  const href = normalizeLink(url);
  if (!href || !root.contains(range.commonAncestorContainer)) return null;
  const doc = root.ownerDocument;
  const existing = linkAt(range.commonAncestorContainer, root);
  if (existing) {
    existing.setAttribute('href', href);
    if (text != null && text.trim() && text !== existing.textContent) existing.textContent = text;
    return existing;
  }
  const a = doc.createElement('a');
  a.setAttribute('href', href);
  if (range.collapsed || (text != null && text.trim() && text !== range.toString())) {
    a.textContent = text?.trim() || href.replace(/^(mailto|tel):/i, '');
    range.deleteContents();
    range.insertNode(a);
  } else {
    // Links nested in the selection are replaced by the new one.
    const frag = range.extractContents();
    frag.querySelectorAll('a').forEach((inner) => inner.replaceWith(...inner.childNodes));
    a.appendChild(frag);
    range.insertNode(a);
  }
  range.setStartAfter(a);
  range.collapse(true);
  return a;
}

/** Removes the link around range, keeping its text. */
export function removeLink(root: Element, range: Range): boolean {
  const a = linkAt(range.commonAncestorContainer, root);
  if (!a) return false;
  a.replaceWith(...a.childNodes);
  return true;
}

/** Inserts an image at range (inside root). */
export function insertImage(root: Element, range: Range | null, src: string, alt = '', width?: number): HTMLImageElement | null {
  if (!isAllowedImageSrc(src)) return null;
  const img = root.ownerDocument.createElement('img');
  img.setAttribute('src', src);
  img.setAttribute('alt', alt);
  if (width) setImageWidth(img, width);
  if (range && root.contains(range.commonAncestorContainer)) {
    range.deleteContents();
    range.insertNode(img);
    range.setStartAfter(img);
    range.collapse(true);
  } else {
    root.appendChild(img);
  }
  return img;
}

/** Resizes an image by width (keeps the aspect ratio: height is dropped). */
export function setImageWidth(img: HTMLImageElement, width: number): void {
  const w = Math.max(16, Math.min(800, Math.round(width)));
  img.setAttribute('width', String(w));
  img.removeAttribute('height');
  img.style.removeProperty('height');
  img.style.removeProperty('width');
  if (!img.getAttribute('style')) img.removeAttribute('style');
}

/** Document for the sandboxed preview frames. */
export function previewDocument(html: string, dark: boolean): string {
  const bg = dark ? '#1f2328' : '#ffffff';
  const fg = dark ? '#e6e6e6' : '#1f2328';
  return withStyleNonce(
    '<!DOCTYPE html><html><head><meta charset="utf-8">' +
      `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data: https:; style-src 'unsafe-inline'">` +
      `<style>html,body{margin:0;padding:10px;background:${bg};color:${fg};font:13px/1.45 system-ui,sans-serif}a{color:${dark ? '#7ab7ff' : '#0b62c4'}}img{max-width:100%}</style>` +
      `</head><body>${html}</body></html>`,
  );
}
