// SPDX-License-Identifier: AGPL-3.0-or-later
// What the AI assistant may see (KONZEPT 8.10). The core enforces it
// (internal/app/ai_access.go); these helpers only mirror it for the settings
// page, the chat hint and the mock transport.
import type { Account, AiLevel, AiSettings, AiSource, AiSourceAccess, AiSourceKind } from './types';

export const AI_LEVELS: AiLevel[] = ['off', 'metadata', 'content'];

const MAIL_KINDS = new Set(['imap', 'pop3', 'jmap', 'eas', 'graph', 'ews', 'gmail', 'local']);
const MESSENGER_KINDS = new Set(['whatsapp', 'signal', 'telegram', 'matrix', 'matrix_bridge']);

/** Mail or messenger source kind of an account (null = neither). */
export function accountSourceKind(kind: string): AiSourceKind | null {
  if (MAIL_KINDS.has(kind)) return 'mail';
  if (MESSENGER_KINDS.has(kind)) return 'messenger';
  return null;
}

export const accountKey = (id: number): string => `account:${id}`;

const rank = (l: AiLevel | undefined): number => (l === 'content' ? 2 : l === 'metadata' ? 1 : 0);

/** The lower of two levels; an empty limit does not cap. */
export function minLevel(l: AiLevel | undefined, limit?: AiLevel): AiLevel {
  const v: AiLevel = rank(l) === 2 ? 'content' : rank(l) === 1 ? 'metadata' : 'off';
  return limit && rank(limit) < rank(v) ? limit : v;
}

/** Effective level of a source; sources without an entry are off. */
export function sourceLevel(s: AiSettings | null | undefined, key: string): AiLevel {
  if (!s) return 'off';
  return minLevel(s.sources?.[key]?.level, s.maxLevel);
}

/** Is the folder (by id and role) left out for this source? */
export function folderExcluded(acc: AiSourceAccess | undefined, id: number, role?: string): boolean {
  if (acc?.excludeFolders?.includes(id)) return true;
  return !acc?.allowJunkTrash && (role === 'junk' || role === 'trash');
}

/** The sources of the given accounts with their levels, for the chat hint. */
export function conversationSources(s: AiSettings | null | undefined, accountIds: number[]): { key: string; name: string; level: AiLevel }[] {
  const out: { key: string; name: string; level: AiLevel }[] = [];
  for (const id of new Set(accountIds)) {
    const key = accountKey(id);
    const src = s?.catalog?.find((c) => c.key === key);
    if (src) out.push({ key, name: src.name, level: sourceLevel(s, key) });
  }
  return out;
}

/** The source list as the core builds it (used by the mock transport). */
export function buildCatalog(
  s: AiSettings,
  accounts: Account[],
  folders: { id: number; accountId: number; name: string; role?: string }[],
  calendars: { id: number; name: string; accountName?: string }[],
  books: { id: number; name: string; accountName?: string }[],
  clouds: { id: number; displayName: string; server?: string; provider?: string }[],
): AiSource[] {
  const out: AiSource[] = [];
  for (const a of accounts) {
    const kind = accountSourceKind(a.kind);
    if (!kind) continue;
    const key = accountKey(a.id);
    const src: AiSource = { key, kind, name: a.displayName, level: sourceLevel(s, key) };
    if (kind === 'mail') src.folders = folders.filter((f) => f.accountId === a.id).map((f) => ({ id: f.id, name: f.name, role: f.role }));
    out.push(src);
  }
  for (const c of calendars) out.push({ key: `calendar:${c.id}`, kind: 'calendar', name: c.name, detail: c.accountName, level: sourceLevel(s, `calendar:${c.id}`) });
  for (const b of books) out.push({ key: `addressbook:${b.id}`, kind: 'addressbook', name: b.name, detail: b.accountName, level: sourceLevel(s, `addressbook:${b.id}`) });
  // a password vault is never an AI source
  for (const c of clouds.filter((x) => x.provider !== 'vaultwarden')) out.push({ key: `cloud:${c.id}`, kind: 'cloud', name: c.displayName, detail: c.server, level: sourceLevel(s, `cloud:${c.id}`) });
  out.push({ key: 'projects', kind: 'projects', name: 'projects', level: sourceLevel(s, 'projects') });
  return out;
}
