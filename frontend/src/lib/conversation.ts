// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Pure helpers for the conversation (messenger) view, KONZEPT 6.6:
// quote detection, bubble grouping and the selection mapping between the
// classic list and the chat timeline. Kept framework-free for unit tests.

import type { ConversationItem, Folder, FolderRole } from './types';

// ---------------------------------------------------------------------------
// Item ids
// ---------------------------------------------------------------------------

export function mailItemId(messageId: number): string {
  return `m:${messageId}`;
}

export function chatItemId(chatMessageId: number): string {
  return `c:${chatMessageId}`;
}

export function messageIdFromItemId(itemId: string | null | undefined): number | null {
  if (!itemId || !itemId.startsWith('m:')) return null;
  const n = Number(itemId.slice(2));
  return Number.isFinite(n) ? n : null;
}

// ---------------------------------------------------------------------------
// Subjects
// ---------------------------------------------------------------------------

const PREFIX_RE = /^\s*((re|aw|antw|fwd?|wg|wtr|sv|vs)\s*(\[\d+\])?\s*:\s*)+/i;

export function normalizeSubject(subject: string): string {
  return subject.replace(PREFIX_RE, '').trim();
}

export function replySubject(subject: string): string {
  return 'Re: ' + normalizeSubject(subject);
}

export function forwardSubject(subject: string): string {
  return 'Fwd: ' + normalizeSubject(subject);
}

// ---------------------------------------------------------------------------
// Quote / signature detection
// ---------------------------------------------------------------------------

const QUOTE_HEADER_PATTERNS: RegExp[] = [
  /^am .{3,120} schrieb .{1,120}:\s*$/i, // Am 02.11.2026 um 10:42 schrieb Anna Müller:
  /^on .{3,120} wrote:\s*$/i,
  /^-{2,}\s*(original message|ursprüngliche nachricht)\s*-{2,}\s*$/i,
  /^-{2,}\s*(forwarded message|weitergeleitete nachricht)\s*-{2,}\s*$/i,
  /^(von|from):\s.+$/i, // Outlook header block (only when followed by Gesendet/Sent)
];

export interface SplitText {
  body: string;
  quoted: string;
}

/**
 * Split a plain-text mail body into the newly written part and the quoted
 * history / signature. The backend does this too (with HTML awareness); this
 * is the client-side fallback and is used by the mock transport.
 */
export function splitQuoted(text: string): SplitText {
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  let cut = -1;
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i];
    const line = raw.trim();
    if (raw === '-- ' || line === '--') {
      cut = i;
      break;
    }
    if (line.startsWith('>')) {
      // Quote block; include a directly preceding attribution line.
      cut = i;
      break;
    }
    if (QUOTE_HEADER_PATTERNS.slice(0, 4).some((re) => re.test(line))) {
      cut = i;
      break;
    }
    if (QUOTE_HEADER_PATTERNS[4].test(line)) {
      const next = (lines[i + 1] ?? '').trim();
      if (/^(gesendet|sent|datum|date):\s/i.test(next)) {
        cut = i;
        break;
      }
    }
  }
  if (cut === -1) return { body: text.trim(), quoted: '' };
  return {
    body: lines.slice(0, cut).join('\n').trim(),
    quoted: lines.slice(cut).join('\n').trim(),
  };
}

// ---------------------------------------------------------------------------
// Timeline grouping
// ---------------------------------------------------------------------------

export type TimelineEntry =
  | { type: 'day'; key: string; date: string }
  | { type: 'item'; key: string; item: ConversationItem; groupStart: boolean; groupEnd: boolean };

/** Bubbles of the same sender within this window are visually grouped. */
export const GROUP_WINDOW_MS = 5 * 60 * 1000;

function dayKey(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`;
}

export function buildTimeline(items: ConversationItem[]): TimelineEntry[] {
  const sorted = [...items].sort((a, b) => a.sentAt.localeCompare(b.sentAt));
  const out: TimelineEntry[] = [];
  let lastDay = '';
  for (let i = 0; i < sorted.length; i++) {
    const it = sorted[i];
    const dk = dayKey(it.sentAt);
    if (dk !== lastDay) {
      out.push({ type: 'day', key: 'day:' + dk, date: it.sentAt });
      lastDay = dk;
    }
    const prev = sorted[i - 1];
    const next = sorted[i + 1];
    const samePrev =
      !!prev &&
      prev.senderPersonId === it.senderPersonId &&
      prev.channel === it.channel &&
      dayKey(prev.sentAt) === dk &&
      Date.parse(it.sentAt) - Date.parse(prev.sentAt) < GROUP_WINDOW_MS;
    const sameNext =
      !!next &&
      next.senderPersonId === it.senderPersonId &&
      next.channel === it.channel &&
      dayKey(next.sentAt) === dk &&
      Date.parse(next.sentAt) - Date.parse(it.sentAt) < GROUP_WINDOW_MS;
    out.push({ type: 'item', key: it.id, item: it, groupStart: !samePrev, groupEnd: !sameNext });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Selection mapping classic <-> chat (KONZEPT 6.6 "Nahtloser Wechsel")
// ---------------------------------------------------------------------------

/**
 * Chat -> classic: which mail should be selected in the classic list when the
 * user leaves the chat view with `selectedItemId` focused?
 *  - a mail bubble maps to its message,
 *  - a messenger bubble maps to the closest earlier mail of the conversation,
 *    falling back to the closest later one,
 *  - without a selected bubble, the newest mail of the conversation.
 */
export function classicSelectionFromChat(items: ConversationItem[], selectedItemId: string | null): number | null {
  const sorted = [...items].sort((a, b) => a.sentAt.localeCompare(b.sentAt));
  const mails = sorted.filter((i) => i.messageId != null);
  if (mails.length === 0) return null;
  if (!selectedItemId) return mails[mails.length - 1].messageId ?? null;
  const direct = messageIdFromItemId(selectedItemId);
  if (direct != null && mails.some((m) => m.messageId === direct)) return direct;
  const idx = sorted.findIndex((i) => i.id === selectedItemId);
  if (idx === -1) return mails[mails.length - 1].messageId ?? null;
  for (let i = idx; i >= 0; i--) if (sorted[i].messageId != null) return sorted[i].messageId!;
  for (let i = idx; i < sorted.length; i++) if (sorted[i].messageId != null) return sorted[i].messageId!;
  return null;
}

const ROLE_PREFERENCE: FolderRole[] = ['inbox', '', 'archive', 'sent', 'drafts', 'junk', 'trash'];

/**
 * Pick the folder to show when a message must become visible in the classic
 * list. Keeps the current folder if it already contains the message.
 */
export function pickFolderForMessage(folderIds: number[], folders: Folder[], currentFolderId: number | null): number | null {
  if (currentFolderId != null && folderIds.includes(currentFolderId)) return currentFolderId;
  const candidates = folders.filter((f) => folderIds.includes(f.id));
  if (candidates.length === 0) return folderIds[0] ?? null;
  candidates.sort((a, b) => ROLE_PREFERENCE.indexOf(a.role ?? '') - ROLE_PREFERENCE.indexOf(b.role ?? ''));
  return candidates[0].id;
}

// ---------------------------------------------------------------------------
// Misc
// ---------------------------------------------------------------------------

export function initials(name: string): string {
  const parts = name
    .replace(/[<>"]/g, '')
    .split(/[\s@._-]+/)
    .filter(Boolean);
  if (parts.length === 0) return '?';
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

/** Deterministic pleasant hue for avatars. */
export function hueFor(seed: string | number): number {
  const s = String(seed);
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return h % 360;
}
