// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Android share target ("Teilen" -> TuxMail). MainActivity copies the shared
// content URIs into the app cache and the TuxMail plugin hands us their
// paths; here they become File objects for the UI.
//
// Delivery to the UI:
//  1. a cancelable `tuxmail:share` window event with {text, subject, files}
//     - UI parts (e.g. a "share to chat" picker) call preventDefault() to take over;
//  2. otherwise the mail composer opens with subject/text and the files are
//     attached through the composer's regular drop handler (uploaded on send
//     via POST /api/uploads).

import type { SharePayload } from './plugins';

export const SHARE_EVENT = 'tuxmail:share';

export interface ShareDetail {
  text: string;
  subject: string;
  /** Recipients from mailto: links. */
  to: string[];
  files: File[];
}

const EMAIL = /^[^\s@<>,;]+@[^\s@<>,;]+\.[^\s@<>,;]+$/;

/** Plausible addresses only (mailto: input comes from other apps). */
export function shareRecipients(to: string[] | undefined): string[] {
  return (to ?? []).map((a) => a.trim()).filter((a) => EMAIL.test(a)).slice(0, 50);
}

/** Hard cap per shared file (the server limit maxUploadMB applies on send anyway). */
export const MAX_SHARE_BYTES = 200 * 1024 * 1024;

export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}

/** Plain shared text -> composer HTML (links stay plain text; the composer linkifies on send). */
export function shareTextToHtml(text: string): string {
  if (!text.trim()) return '';
  return text
    .split(/\n{2,}/)
    .map((p) => `<p>${escapeHtml(p).replace(/\n/g, '<br>')}</p>`)
    .join('');
}

/** Subject suggestion: explicit EXTRA_SUBJECT, else the first file name, else the first text line. */
export function shareSubject(p: Pick<SharePayload, 'subject' | 'text' | 'files'>): string {
  if (p.subject?.trim()) return p.subject.trim().slice(0, 200);
  if (p.files?.length === 1) return p.files[0].name;
  const line = (p.text ?? '').split('\n').find((l) => l.trim());
  return line && !/^https?:\/\/\S+$/.test(line.trim()) ? line.trim().slice(0, 120) : '';
}

export async function sharedFilesToFiles(p: SharePayload, toUrl: (path: string) => string, f: typeof fetch = fetch): Promise<File[]> {
  const out: File[] = [];
  for (const sf of p.files ?? []) {
    if (sf.size > MAX_SHARE_BYTES) continue;
    try {
      const res = await f(toUrl(sf.path));
      if (!res.ok) continue;
      const blob = await res.blob();
      out.push(new File([blob], sf.name || 'file', { type: sf.mimeType || blob.type || 'application/octet-stream' }));
    } catch {
      /* unreadable entry: skip */
    }
  }
  return out;
}

/** Fire the share event; true if a UI component took it over. */
export function dispatchShare(detail: ShareDetail, target: EventTarget = window): boolean {
  const ev = new CustomEvent<ShareDetail>(SHARE_EVENT, { detail, cancelable: true });
  target.dispatchEvent(ev);
  return ev.defaultPrevented;
}

/** Wait for an element to appear (the composer mounts asynchronously). */
export function waitForElement(selector: string, timeoutMs = 3000): Promise<HTMLElement | null> {
  return new Promise((resolve) => {
    const start = Date.now();
    const tick = () => {
      const el = document.querySelector<HTMLElement>(selector);
      if (el) return resolve(el);
      if (Date.now() - start > timeoutMs) return resolve(null);
      requestAnimationFrame(tick);
    };
    tick();
  });
}

/** Attach files through the element's drop handler (Composer.svelte `.composer`). */
export function dropFiles(el: HTMLElement, files: File[]): boolean {
  if (!files.length || typeof DataTransfer === 'undefined') return false;
  const dt = new DataTransfer();
  for (const file of files) dt.items.add(file);
  el.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
  return true;
}
