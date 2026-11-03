// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Pure logic for the chat-mode features: chat folders, snooze presets,
// focus-mode schedule, "/" snippets and "@" mentions, scheduling presets.
// Shared by the UI and the mock transport (the Go core implements the same).

import type { ChatFolderRules, Conversation, FocusSettings } from './types';

// ---------------------------------------------------------------------------
// Chat folders (Telegram-style)
// ---------------------------------------------------------------------------

export const EMPTY_RULES: ChatFolderRules = {
  includeConversationIds: [],
  includePersonIds: [],
  excludeConversationIds: [],
  channels: [],
  accountIds: [],
  groups: 'any',
  unreadOnly: false,
  excludeMuted: false,
  excludeArchived: true,
};

/**
 * Does a conversation belong to a chat folder? Explicit includes always
 * match (unless excluded); otherwise every configured criterion must match
 * and at least one criterion must be configured.
 */
export function matchesFolder(c: Conversation, r: ChatFolderRules): boolean {
  if (r.excludeConversationIds.includes(c.id)) return false;
  if (r.excludeArchived && c.archived) return false;
  if (r.excludeMuted && c.muted) return false;
  if (r.unreadOnly && !(c.unreadCount > 0 || c.markedUnread)) return false;
  if (r.includeConversationIds.includes(c.id)) return true;
  // Persons match their 1:1 chats (person views), not every group they are in.
  if (r.includePersonIds.length && !c.isGroup && c.participantIds.some((p) => r.includePersonIds.includes(p))) return true;
  const criteria: boolean[] = [];
  if (r.channels.length) criteria.push(c.channels.some((ch) => r.channels.includes(ch)));
  if (r.accountIds.length) criteria.push(c.accountIds.some((a) => r.accountIds.includes(a)));
  if (r.groups === 'only') criteria.push(c.isGroup);
  if (r.groups === 'exclude') criteria.push(!c.isGroup);
  if (criteria.length === 0) return r.unreadOnly; // "unread only" folder alone
  return criteria.every(Boolean);
}

// ---------------------------------------------------------------------------
// Snooze & schedule presets
// ---------------------------------------------------------------------------

export type PresetId = 'hour' | 'evening' | 'tomorrow' | 'monday';

function at(base: Date, days: number, hour: number): Date {
  const d = new Date(base);
  d.setDate(d.getDate() + days);
  d.setHours(hour, 0, 0, 0);
  return d;
}

/** 1 h, this evening 18:00 (or tomorrow evening if past), tomorrow 08:00, next Monday 08:00. */
export function timePresets(now: Date): { id: PresetId; at: Date }[] {
  const hour = new Date(now.getTime() + 3600_000);
  hour.setSeconds(0, 0);
  const evening = now.getHours() < 17 ? at(now, 0, 18) : at(now, 1, 18);
  const tomorrow = at(now, 1, 8);
  const toMonday = ((8 - now.getDay()) % 7) || 7;
  const monday = at(now, toMonday, 8);
  return [
    { id: 'hour', at: hour },
    { id: 'evening', at: evening },
    { id: 'tomorrow', at: tomorrow },
    { id: 'monday', at: monday },
  ];
}

/** Value for <input type="datetime-local"> in local time. */
export function toLocalInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

// ---------------------------------------------------------------------------
// Focus mode
// ---------------------------------------------------------------------------

function minutes(hm: string): number {
  const [h, m] = hm.split(':').map(Number);
  return h * 60 + (m || 0);
}

/** Focus mode is active when enabled and (no schedule, or now inside the schedule). */
export function isFocusActive(s: FocusSettings, now: Date): boolean {
  if (!s.enabled) return false;
  const sch = s.schedule;
  if (!sch?.enabled) return true;
  if (sch.days.length && !sch.days.includes(now.getDay())) return false;
  const cur = now.getHours() * 60 + now.getMinutes();
  const a = minutes(sch.start);
  const b = minutes(sch.end);
  return a <= b ? cur >= a && cur < b : cur >= a || cur < b; // over midnight
}

export function isVip(c: Conversation, s: FocusSettings, frequentPersonIds: number[] = []): boolean {
  if (s.vipConversationIds.includes(c.id)) return true;
  const people = [...s.vipPersonIds, ...(s.includeFrequent ? frequentPersonIds : [])];
  return !c.isGroup && c.participantIds.some((p) => people.includes(p));
}

// ---------------------------------------------------------------------------
// "/" snippets and "@" mentions in the composer
// ---------------------------------------------------------------------------

export interface Trigger {
  kind: 'slash' | 'mention';
  query: string;
  /** Range to replace (including the trigger character). */
  from: number;
  to: number;
}

/** "/" at the start of a line, or "@" after whitespace/start, up to the caret. */
export function detectTrigger(text: string, caret: number): Trigger | null {
  const before = text.slice(0, caret);
  const slash = /(^|\n)\/([\p{L}\p{N}_-]*)$/u.exec(before);
  if (slash) {
    const from = caret - slash[2].length - 1;
    return { kind: 'slash', query: slash[2], from, to: caret };
  }
  const at = /(^|\s)@([\p{L}\p{N}_.-]*)$/u.exec(before);
  if (at) {
    const from = caret - at[2].length - 1;
    return { kind: 'mention', query: at[2], from, to: caret };
  }
  return null;
}

export interface SnippetContext {
  firstName?: string;
  name?: string;
  myName?: string;
  now?: Date;
  locale?: string;
}

/** Fill {vorname}/{firstname}, {name}, {meinname}/{myname}, {datum}/{date}, {uhrzeit}/{time}. */
export function fillSnippet(text: string, ctx: SnippetContext): string {
  const now = ctx.now ?? new Date();
  const loc = ctx.locale ?? 'de';
  const values: Record<string, string | undefined> = {
    vorname: ctx.firstName,
    firstname: ctx.firstName,
    name: ctx.name,
    meinname: ctx.myName,
    myname: ctx.myName,
    datum: now.toLocaleDateString(loc, { day: '2-digit', month: '2-digit', year: 'numeric' }),
    date: now.toLocaleDateString(loc, { day: '2-digit', month: '2-digit', year: 'numeric' }),
    uhrzeit: now.toLocaleTimeString(loc, { hour: '2-digit', minute: '2-digit' }),
    time: now.toLocaleTimeString(loc, { hour: '2-digit', minute: '2-digit' }),
  };
  return text.replace(/\{(\w+)\}/g, (m, k: string) => values[k.toLowerCase()] ?? m);
}

/** Subsequence fuzzy match score (higher is better, -1 = no match). */
export function fuzzy(text: string, q: string): number {
  const t = text.toLowerCase();
  const query = q.toLowerCase();
  if (!query) return 0;
  if (t.startsWith(query)) return 100 - t.length;
  const i = t.indexOf(query);
  if (i >= 0) return 50 - i;
  let pos = 0;
  for (const ch of query) {
    pos = t.indexOf(ch, pos);
    if (pos < 0) return -1;
    pos++;
  }
  return 10;
}
