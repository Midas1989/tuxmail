// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Avatar URLs come from API data (Person.avatarUrl, Conversation.avatarUrl,
// PimContact.photoUrl) and may originate from third parties (messenger
// plugins, vCards). Only sources that cannot reach a foreign host are
// rendered; everything else falls back to the initials (NG-09).

const RASTER_DATA = /^data:image\/(png|jpe?g|gif|webp|avif|bmp);base64,[a-z0-9+/=\s]+$/i;

/**
 * Returns `url` if it is a raster `data:` image (no SVG), a `blob:` URL, a
 * same-origin `/api/` path, or - in the native app, where the transport makes
 * API paths absolute - an `/api/` URL below the configured server
 * (`serverBase`, e.g. "https://mail.example.org" or ".../tuxmail"). Else undefined.
 */
export function safeAvatarUrl(url: string | undefined | null, serverBase?: string | null): string | undefined {
  if (!url) return undefined;
  const u = url.trim();
  if (!u || (/[\s\\]/.test(u) && !/^data:/i.test(u))) return undefined;
  if (RASTER_DATA.test(u)) return u;
  if (/^blob:/i.test(u)) return u;
  if (u.startsWith('/api/') && !u.includes('..')) return u; // "//host/api/..." starts with "//" and is rejected
  const base = serverBase?.replace(/\/+$/, '');
  if (base && /^https:\/\//i.test(base) && u.startsWith(base + '/api/') && !u.includes('..')) {
    try {
      const a = new URL(u);
      const b = new URL(base);
      if (a.origin === b.origin) return u;
    } catch {
      /* not a URL */
    }
  }
  return undefined;
}
