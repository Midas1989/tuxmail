// SPDX-License-Identifier: AGPL-3.0-or-later
// Remote content of HTML mails (MailFrame, KONZEPT 8.3).

/**
 * Remote images are replaced by the sanitizer with a transparent
 * placeholder and the original URL in data-remote-src (tracking pixels
 * get data-tracker). After the user allowed remote content, the https
 * images are restored; trackers stay blocked.
 */
/**
 * "Load images": the HTML with remote images routed through the image
 * proxy (the sender never sees the reader's address). null if the proxy
 * rendering is unavailable; callers then keep the blocked HTML - images
 * are never restored to direct remote URLs in the app (audit B-05).
 */
export async function proxiedRemoteHtml(api: { loadRemoteContent(id: number): Promise<{ html: string }> }, id: number): Promise<string | null> {
  try {
    const r = await api.loadRemoteContent(id);
    return r.html || null;
  } catch {
    return null;
  }
}

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
