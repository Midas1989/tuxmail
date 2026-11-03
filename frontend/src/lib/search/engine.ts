// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Small in-memory search engine over "documents" for the MockTransport and
// tests: evaluates the expert query AST, applies the filter chips, scores,
// builds highlighted snippets, facets and a month histogram. The real core
// does this with SQLite FTS5 (KONZEPT 8.4.1); semantics should match.

import { parse, positiveTerms, type QueryNode } from './query';
import type { Channel, FacetBucket, SearchFacets, SearchHit, SearchHitKind, SearchQuery, SearchResult } from '../types';

export interface SearchDoc {
  id: string;
  kind: SearchHitKind;
  title: string;
  body: string;
  /** "Name <email>" strings. */
  from: string;
  to: string;
  cc: string;
  filenames: string[];
  accountId?: number;
  accountLabel: string;
  folderIds: number[];
  folderNames: string[];
  /** true if all locations are trash/junk. */
  inTrashOrSpam: boolean;
  channel?: Channel;
  categories: string[];
  date?: string;
  size: number;
  unread: boolean;
  flagged: boolean;
  answered: boolean;
  hasCalendar: boolean;
  senderName: string;
  senderEmail: string;
  messageId?: number;
  conversationId?: number;
  itemId?: string;
  personId?: number;
}

type TermNode = Extract<QueryNode, { t: 'term' }>;

// ---------------------------------------------------------------------------
// Value helpers
// ---------------------------------------------------------------------------

const lc = (s: string) => s.toLocaleLowerCase('de');

function wildcardRe(v: string): RegExp {
  const esc = v.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '\\S*').replace(/\?/g, '\\S');
  return new RegExp(esc, 'iu');
}

function levenshtein(a: string, b: string): number {
  const dp = Array.from({ length: b.length + 1 }, (_, i) => i);
  for (let i = 1; i <= a.length; i++) {
    let prev = dp[0];
    dp[0] = i;
    for (let j = 1; j <= b.length; j++) {
      const tmp = dp[j];
      dp[j] = Math.min(dp[j] + 1, dp[j - 1] + 1, prev + (a[i - 1] === b[j - 1] ? 0 : 1));
      prev = tmp;
    }
  }
  return dp[b.length];
}

export function textMatches(hay: string, term: Pick<TermNode, 'value' | 'kind' | 'fuzzy'>): boolean {
  if (!hay) return false;
  const h = lc(hay);
  const v = lc(term.value);
  if (term.kind === 'regex') {
    try {
      return new RegExp(term.value, 'iu').test(hay);
    } catch {
      return false;
    }
  }
  if (term.kind === 'phrase') return h.replace(/\s+/g, ' ').includes(v.replace(/\s+/g, ' '));
  if (/[*?]/.test(v)) return wildcardRe(v).test(h);
  if (term.fuzzy) {
    const max = v.length > 5 ? 2 : 1;
    return h.split(/[^\p{L}\p{N}]+/u).some((w) => w && Math.abs(w.length - v.length) <= max && levenshtein(w, v) <= max);
  }
  return h.includes(v);
}

const UNITS: Record<string, number> = { b: 1, kb: 1024, k: 1024, mb: 1024 ** 2, m: 1024 ** 2, gb: 1024 ** 3, g: 1024 ** 3 };

export function parseSize(v: string): number | null {
  const m = /^(\d+(?:[.,]\d+)?)\s*([kmg]?b?)?$/i.exec(v.trim());
  if (!m) return null;
  return Number(m[1].replace(',', '.')) * (UNITS[lc(m[2] || 'b')] ?? 1);
}

/** Resolve a date value to an inclusive [start, end] range in ms. */
export function parseDateRange(v: string, now: Date): [number, number] | null {
  const s = lc(v.trim());
  const day = (d: Date) => {
    const a = new Date(d);
    a.setHours(0, 0, 0, 0);
    const b = new Date(a);
    b.setDate(b.getDate() + 1);
    return [a.getTime(), b.getTime() - 1] as [number, number];
  };
  if (s === 'now') return [now.getTime(), now.getTime()];
  const rel = /^now([+-])(\d+)([dwmyh])$/i.exec(v.trim());
  if (rel) {
    const d = new Date(now);
    const n = Number(rel[2]) * (rel[1] === '-' ? -1 : 1);
    switch (rel[3]) {
      case 'h':
        d.setHours(d.getHours() + n);
        break;
      case 'd':
        d.setDate(d.getDate() + n);
        break;
      case 'w':
        d.setDate(d.getDate() + 7 * n);
        break;
      case 'M':
        d.setMonth(d.getMonth() + n);
        break;
      case 'm':
        d.setMonth(d.getMonth() + n);
        break;
      case 'y':
        d.setFullYear(d.getFullYear() + n);
        break;
    }
    return [d.getTime(), d.getTime()];
  }
  if (s === 'heute' || s === 'today') return day(now);
  if (s === 'gestern' || s === 'yesterday') {
    const d = new Date(now);
    d.setDate(d.getDate() - 1);
    return day(d);
  }
  if (s === 'letzte woche' || s === 'last week' || s === 'diese woche' || s === 'this week') {
    const a = new Date(now);
    a.setDate(a.getDate() - 7);
    return [a.getTime(), now.getTime()];
  }
  if (s === 'letzter monat' || s === 'last month') {
    const a = new Date(now);
    a.setMonth(a.getMonth() - 1);
    return [a.getTime(), now.getTime()];
  }
  let m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (m) return day(new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
  m = /^(\d{4})-(\d{2})$/.exec(s);
  if (m) {
    const a = new Date(Number(m[1]), Number(m[2]) - 1, 1);
    const b = new Date(Number(m[1]), Number(m[2]), 1);
    return [a.getTime(), b.getTime() - 1];
  }
  m = /^(\d{4})$/.exec(s);
  if (m) return [new Date(Number(m[1]), 0, 1).getTime(), new Date(Number(m[1]) + 1, 0, 1).getTime() - 1];
  m = /^(\d{1,2})\.(\d{1,2})\.(\d{4})$/.exec(s);
  if (m) return day(new Date(Number(m[3]), Number(m[2]) - 1, Number(m[1])));
  return null;
}

function cmpNum(actual: number, op: string, expected: number): boolean {
  switch (op) {
    case '>':
      return actual > expected;
    case '>=':
      return actual >= expected;
    case '<':
      return actual < expected;
    case '<=':
      return actual <= expected;
    default:
      return actual === expected;
  }
}

// ---------------------------------------------------------------------------
// Evaluation
// ---------------------------------------------------------------------------

function fieldTexts(doc: SearchDoc, field?: string): string[] {
  switch (field) {
    case undefined:
      return [doc.title, doc.body, doc.from, doc.to, doc.filenames.join(' ')];
    case 'from':
      return [doc.from];
    case 'to':
      return [doc.to];
    case 'cc':
      return [doc.cc];
    case 'participant':
      return [doc.from, doc.to, doc.cc];
    case 'subject':
    case 'title':
      return [doc.title];
    case 'body':
      return [doc.body];
    case 'attachment':
    case 'filename':
      return doc.filenames;
    case 'account':
      return [doc.accountLabel];
    case 'in':
    case 'folder':
      return doc.folderNames;
    case 'category':
    case 'label':
      return doc.categories;
    default:
      return [];
  }
}

function matchTerm(term: TermNode, doc: SearchDoc, now: Date): boolean {
  const f = term.field;
  const v = lc(term.value);
  switch (f) {
    case 'is':
      return v === 'unread' || v === 'ungelesen'
        ? doc.unread
        : v === 'read' || v === 'gelesen'
          ? !doc.unread
          : v === 'flagged' || v === 'markiert' || v === 'starred'
            ? doc.flagged
            : v === 'answered' || v === 'beantwortet'
              ? doc.answered
              : false;
    case 'has':
    case '_exists_':
      return v.startsWith('attachment') || v.startsWith('anhang') ? doc.filenames.length > 0 : v === 'calendar' || v === 'kalender' ? doc.hasCalendar : false;
    case 'channel':
      return !!doc.channel && (doc.channel === v || (v === 'mail' && doc.channel === 'email'));
    case 'type': {
      const map: Record<string, SearchHitKind> = { email: 'mail', mail: 'mail', chat: 'chat', message: 'chat', event: 'event', termin: 'event', task: 'task', aufgabe: 'task', contact: 'contact', kontakt: 'contact', file: 'file', datei: 'file' };
      return doc.kind === map[v];
    }
    case 'size':
    case 'attachments': {
      const actual = f === 'size' ? doc.size : doc.filenames.length;
      const expected = f === 'size' ? parseSize(term.value) : Number(term.value);
      if (expected == null || Number.isNaN(expected)) return false;
      if (term.kind === 'range') {
        const a = f === 'size' ? parseSize(term.from!) : Number(term.from);
        const b = f === 'size' ? parseSize(term.to!) : Number(term.to);
        return a != null && b != null && actual >= a && actual <= b;
      }
      return cmpNum(actual, term.op ?? '>=', expected);
    }
    case 'date':
    case 'before':
    case 'after': {
      if (!doc.date) return false;
      const t = Date.parse(doc.date);
      if (term.kind === 'range') {
        const a = parseDateRange(term.from!, now);
        const b = parseDateRange(term.to!, now);
        return !!a && !!b && t >= a[0] && t <= b[1];
      }
      const r = parseDateRange(term.value, now);
      if (!r) return false;
      const op = f === 'before' ? '<' : f === 'after' ? '>' : term.op;
      if (!op || op === '=') return t >= r[0] && t <= r[1];
      if (op === '>') return t > r[1] || (r[0] === r[1] && t > r[0]);
      if (op === '>=') return t >= r[0];
      if (op === '<') return t < r[0];
      return t <= r[1];
    }
  }
  return fieldTexts(doc, f).some((h) => textMatches(h, term));
}

export function evaluate(node: QueryNode, doc: SearchDoc, now: Date): boolean {
  switch (node.t) {
    case 'all':
      return true;
    case 'and':
      return node.children.every((c) => evaluate(c, doc, now));
    case 'or':
      return node.children.some((c) => evaluate(c, doc, now));
    case 'not':
      return !evaluate(node.child, doc, now);
    case 'term':
      return matchTerm(node, doc, now);
  }
}

function score(terms: TermNode[], doc: SearchDoc): number {
  let s = 0;
  for (const t of terms) {
    if (t.field && !['subject', 'title', 'body', 'from', 'to', 'filename', 'attachment'].includes(t.field)) continue;
    const w = t.boost ?? 1;
    if ((!t.field || t.field === 'subject' || t.field === 'title') && textMatches(doc.title, t)) s += 3 * w;
    if ((!t.field || t.field === 'from') && textMatches(doc.from, t)) s += 2 * w;
    if ((!t.field || t.field === 'body') && textMatches(doc.body, t)) s += 1 * w;
    if ((!t.field || t.field === 'filename' || t.field === 'attachment') && doc.filenames.some((f) => textMatches(f, t))) s += 1 * w;
  }
  return s;
}

// ---------------------------------------------------------------------------
// Highlighting
// ---------------------------------------------------------------------------

function termRegex(t: TermNode): RegExp | null {
  try {
    if (t.kind === 'regex') return new RegExp(t.value, 'giu');
    if (t.kind === 'phrase' || t.kind === 'word') {
      if (t.fuzzy) return null;
      const src = t.value.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '[\\p{L}\\p{N}_-]*').replace(/\?/g, '[\\p{L}\\p{N}]').replace(/\s+/g, '\\s+');
      return new RegExp(src, 'giu');
    }
  } catch {
    /* invalid regex */
  }
  return null;
}

export function highlightRanges(text: string, terms: TermNode[]): [number, number][] {
  const ranges: [number, number][] = [];
  for (const t of terms) {
    const re = termRegex(t);
    if (!re) continue;
    for (const m of text.matchAll(re)) {
      if (m[0].length === 0) continue;
      ranges.push([m.index!, m.index! + m[0].length]);
    }
  }
  ranges.sort((a, b) => a[0] - b[0]);
  const merged: [number, number][] = [];
  for (const r of ranges) {
    const last = merged[merged.length - 1];
    if (last && r[0] <= last[1]) last[1] = Math.max(last[1], r[1]);
    else merged.push([...r]);
  }
  return merged;
}

export function makeSnippet(body: string, terms: TermNode[], width = 180): { snippet: string; highlights: [number, number][] } {
  const flat = body.replace(/\s+/g, ' ').trim();
  const all = highlightRanges(flat, terms);
  let start = 0;
  if (all.length && all[0][0] > 60) start = flat.lastIndexOf(' ', all[0][0] - 50) + 1;
  let snippet = flat.slice(start, start + width);
  let offset = start;
  if (start > 0) {
    snippet = '…' + snippet;
    offset -= 1;
  }
  if (start + width < flat.length) snippet += '…';
  const highlights = all.map(([a, b]) => [a - offset, b - offset] as [number, number]).filter(([a, b]) => a >= 0 && b <= snippet.length);
  return { snippet, highlights };
}

// ---------------------------------------------------------------------------
// Query construction
// ---------------------------------------------------------------------------

/** Simple mode: every word (or "phrase") must occur somewhere; no syntax. */
export function simpleQuery(text: string): QueryNode {
  const parts: QueryNode[] = [];
  const re = /"([^"]+)"|(\S+)/g;
  for (const m of text.matchAll(re)) {
    parts.push({ t: 'term', value: m[1] ?? m[2], kind: m[1] ? 'phrase' : 'word', pos: m.index! });
  }
  if (parts.length === 0) return { t: 'all' };
  return parts.length === 1 ? parts[0] : { t: 'and', children: parts };
}

function quote(v: string): string {
  return /[\s()":]/.test(v) ? `"${v.replace(/"/g, '\\"')}"` : v;
}

function bucketize(entries: { key: string; label: string; clause: string }[]): FacetBucket[] {
  const map = new Map<string, FacetBucket>();
  for (const e of entries) {
    const b = map.get(e.key);
    if (b) b.count++;
    else map.set(e.key, { ...e, count: 1 });
  }
  return [...map.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label)).slice(0, 10);
}

export interface EngineContext {
  now: Date;
  /** folderId -> all descendant folder ids (for includeSubfolders). */
  descendants: (folderId: number) => number[];
  monthLabel?: (year: number, month: number) => string;
}

export function runSearch(docs: SearchDoc[], q: SearchQuery, ctx: EngineContext): SearchResult {
  const t0 = typeof performance !== 'undefined' ? performance.now() : Date.now();
  let ast: QueryNode;
  let error: SearchResult['error'];
  let fallback = false;
  if (q.mode === 'expert') {
    const r = parse(q.text);
    if (r.ok) ast = r.ast;
    else {
      error = r.error;
      fallback = true;
      ast = simpleQuery(q.text.replace(/[()"]/g, ' '));
    }
  } else {
    ast = simpleQuery(q.text);
  }
  const terms = positiveTerms(ast);

  const folderSet = new Set<number>();
  for (const id of q.folderIds) {
    folderSet.add(id);
    if (q.includeSubfolders) ctx.descendants(id).forEach((d) => folderSet.add(d));
  }
  const fromMs = q.from ? Date.parse(q.from) : undefined;
  const toMs = q.to ? Date.parse(q.to) + 86_399_999 : undefined;

  const matched = docs.filter((d) => {
    if (q.scopes.length && !q.scopes.includes(d.kind)) return false;
    if (q.accountIds.length && (d.accountId == null || !q.accountIds.includes(d.accountId))) return false;
    if (folderSet.size && !d.folderIds.some((f) => folderSet.has(f))) return false;
    if (q.excludeTrashSpam && d.inTrashOrSpam) return false;
    if (fromMs != null && (!d.date || Date.parse(d.date) < fromMs)) return false;
    if (toMs != null && (!d.date || Date.parse(d.date) > toMs)) return false;
    return evaluate(ast, d, ctx.now);
  });

  const scored = matched.map((d) => ({ d, s: score(terms, d) }));
  const by = q.sort;
  scored.sort((a, b) => {
    if (by === 'date') return (b.d.date ?? '').localeCompare(a.d.date ?? '');
    if (by === 'sender') return a.d.senderName.localeCompare(b.d.senderName, 'de') || (b.d.date ?? '').localeCompare(a.d.date ?? '');
    if (by === 'size') return b.d.size - a.d.size;
    return b.s - a.s || (b.d.date ?? '').localeCompare(a.d.date ?? '');
  });

  const facets: SearchFacets = {
    account: bucketize(matched.filter((d) => d.accountLabel).map((d) => ({ key: d.accountLabel, label: d.accountLabel, clause: `account:${quote(d.accountLabel)}` }))),
    folder: bucketize(matched.flatMap((d) => d.folderNames.map((n) => ({ key: n, label: n, clause: `in:${quote(n)}` })))),
    sender: bucketize(matched.filter((d) => d.senderName).map((d) => ({ key: d.senderEmail || d.senderName, label: d.senderName, clause: `from:${quote(d.senderEmail || d.senderName)}` }))),
    domain: bucketize(
      matched
        .filter((d) => d.senderEmail.includes('@'))
        .map((d) => {
          const dom = d.senderEmail.split('@')[1];
          return { key: dom, label: dom, clause: `from:${quote('@' + dom)}` };
        }),
    ),
    channel: bucketize(matched.filter((d) => d.channel).map((d) => ({ key: d.channel!, label: d.channel!, clause: `channel:${d.channel}` }))),
    type: bucketize(matched.map((d) => ({ key: d.kind, label: d.kind, clause: `type:${d.kind}` }))),
    attachment: bucketize(
      matched.map((d) => (d.filenames.length ? { key: 'yes', label: 'yes', clause: 'has:attachment' } : { key: 'no', label: 'no', clause: 'NOT has:attachment' })),
    ),
  };

  // Month histogram (continuous range).
  const dates = matched.filter((d) => d.date).map((d) => new Date(d.date!));
  const histogram: FacetBucket[] = [];
  if (dates.length) {
    const min = new Date(Math.min(...dates.map((d) => d.getTime())));
    const max = new Date(Math.max(...dates.map((d) => d.getTime())));
    const cur = new Date(min.getFullYear(), min.getMonth(), 1);
    while (cur <= max) {
      const y = cur.getFullYear();
      const m = cur.getMonth();
      const key = `${y}-${String(m + 1).padStart(2, '0')}`;
      histogram.push({
        key,
        label: ctx.monthLabel ? ctx.monthLabel(y, m) : key,
        count: dates.filter((d) => d.getFullYear() === y && d.getMonth() === m).length,
        clause: `date:${key}`,
      });
      cur.setMonth(m + 1);
    }
  }

  const offset = Number(q.cursor ?? 0) || 0;
  const limit = q.limit ?? 50;
  const titleTerms = terms.filter((t) => !t.field || t.field === 'subject' || t.field === 'title');
  const bodyTerms = terms.filter((t) => !t.field || t.field === 'body' || t.field === 'subject' || t.field === 'attachment' || t.field === 'filename');
  const hits: SearchHit[] = scored.slice(offset, offset + limit).map(({ d, s }) => {
    const { snippet, highlights } = makeSnippet(d.body || d.filenames.join(', '), bodyTerms);
    return {
      id: d.id,
      kind: d.kind,
      title: d.title,
      titleHighlights: highlightRanges(d.title, titleTerms),
      snippet,
      highlights,
      date: d.date,
      accountId: d.accountId,
      folderId: d.folderIds[0],
      channel: d.channel,
      sender: d.senderName,
      size: d.size,
      hasAttachments: d.filenames.length > 0,
      unread: d.unread,
      score: s,
      messageId: d.messageId,
      conversationId: d.conversationId,
      itemId: d.itemId,
      personId: d.personId,
    };
  });
  const t1 = typeof performance !== 'undefined' ? performance.now() : Date.now();
  return {
    hits,
    total: matched.length,
    facets,
    histogram,
    nextCursor: offset + limit < matched.length ? String(offset + limit) : undefined,
    error,
    fallback,
    tookMs: Math.round((t1 - t0) * 10) / 10,
  };
}
