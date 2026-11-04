// SPDX-License-Identifier: AGPL-3.0-or-later
// @mention helpers of the team comment box (same syntax as
// internal/team.ParseMentions: "@username" not preceded by a word char, @ or .).

export interface ActiveMention {
  start: number; // index of "@"
  query: string; // lower-case text after "@" up to the caret
}

/** The @mention being typed at the caret, or null. */
export function activeMention(text: string, caret: number): ActiveMention | null {
  const before = text.slice(0, caret);
  const m = /(?:^|[^\w@.])@([A-Za-z0-9._-]*)$/.exec(before);
  if (!m) return null;
  return { start: caret - m[1].length - 1, query: m[1].toLowerCase() };
}

/** Replaces the active mention with "@username " and returns the new caret. */
export function insertMention(text: string, at: ActiveMention, username: string): { text: string; caret: number } {
  const end = at.start + 1 + at.query.length;
  const ins = '@' + username + ' ';
  return { text: text.slice(0, at.start) + ins + text.slice(end), caret: at.start + ins.length };
}

/** Splits a comment into text and mention segments for highlighting. */
export function mentionSegments(body: string): { text: string; mention: boolean }[] {
  const out: { text: string; mention: boolean }[] = [];
  const re = /(^|[^\w@.])@([A-Za-z0-9][A-Za-z0-9._-]*[A-Za-z0-9_]|[A-Za-z0-9])/g;
  let last = 0;
  for (let m = re.exec(body); m; m = re.exec(body)) {
    const at = m.index + m[1].length;
    if (at > last) out.push({ text: body.slice(last, at), mention: false });
    out.push({ text: m[2], mention: true });
    last = at + 1 + m[2].length;
  }
  if (last < body.length) out.push({ text: body.slice(last), mention: false });
  return out;
}
