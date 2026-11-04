// SPDX-License-Identifier: AGPL-3.0-or-later
// Remote content of HTML mails (MailFrame, KONZEPT 8.3).

/**
 * Remote images are replaced by the sanitizer with a transparent
 * placeholder and the original URL in data-remote-src (tracking pixels
 * get data-tracker). After the user allowed remote content, the https
 * images are restored; trackers stay blocked.
 */
export function restoreRemoteImages(html: string): string {
  if (typeof DOMParser === 'undefined' || !html.includes('data-remote-src')) return html;
  const doc = new DOMParser().parseFromString(html, 'text/html');
  for (const img of doc.querySelectorAll<HTMLImageElement>('img[data-remote-src]')) {
    const src = img.getAttribute('data-remote-src') ?? '';
    if (img.hasAttribute('data-tracker') || !/^https:\/\//i.test(src)) continue;
    img.setAttribute('src', src);
    img.removeAttribute('data-remote-src');
  }
  return doc.head.innerHTML + doc.body.innerHTML;
}
