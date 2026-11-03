// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Plain message text -> safe segments (text / link / search hit). No HTML
// from messages is ever rendered in bubbles.

export interface Segment {
  text: string;
  href?: string;
  hit?: boolean;
}

const URL_RE = /\bhttps?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)\]]/gi;

export function linkify(text: string): Segment[] {
  const out: Segment[] = [];
  let pos = 0;
  for (const m of text.matchAll(URL_RE)) {
    if (m.index! > pos) out.push({ text: text.slice(pos, m.index) });
    out.push({ text: m[0], href: m[0] });
    pos = m.index! + m[0].length;
  }
  if (pos < text.length) out.push({ text: text.slice(pos) });
  return out;
}

/** Split segments further so occurrences of `query` are marked as hits (case-insensitive). */
export function highlight(segments: Segment[], query: string): Segment[] {
  const q = query.trim().toLocaleLowerCase();
  if (!q) return segments;
  const out: Segment[] = [];
  for (const s of segments) {
    const lower = s.text.toLocaleLowerCase();
    let pos = 0;
    let idx = lower.indexOf(q);
    while (idx !== -1) {
      if (idx > pos) out.push({ ...s, text: s.text.slice(pos, idx) });
      out.push({ ...s, text: s.text.slice(idx, idx + q.length), hit: true });
      pos = idx + q.length;
      idx = lower.indexOf(q, pos);
    }
    if (pos < s.text.length) out.push({ ...s, text: s.text.slice(pos) });
  }
  return out;
}

/** Text consisting only of 1-3 emoji is shown large (like messengers do). */
export function isEmojiOnly(text: string): boolean {
  const t = text.trim();
  if (!t || t.length > 16) return false;
  return /^(?:\p{Extended_Pictographic}(?:️|‍\p{Extended_Pictographic}|\p{Emoji_Modifier})*\s*){1,3}$/u.test(t);
}
