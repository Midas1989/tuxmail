// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Autocomplete suggestions for the expert query bar, driven by contextAt().

import { quoteValue, type CompletionContext } from './query';

export type FieldCategory = 'person' | 'text' | 'place' | 'time' | 'state' | 'size' | 'type';

export interface FieldDef {
  name: string;
  category: FieldCategory;
}

/** Fields of the query language (KONZEPT 8.4.1). Descriptions live in the locale files (search.field.<name>). */
export const FIELDS: FieldDef[] = [
  { name: 'from', category: 'person' },
  { name: 'to', category: 'person' },
  { name: 'cc', category: 'person' },
  { name: 'participant', category: 'person' },
  { name: 'subject', category: 'text' },
  { name: 'body', category: 'text' },
  { name: 'attachment', category: 'text' },
  { name: 'filename', category: 'text' },
  { name: 'account', category: 'place' },
  { name: 'in', category: 'place' },
  { name: 'tag', category: 'type' },
  { name: 'project', category: 'place' },
  { name: 'channel', category: 'type' },
  { name: 'type', category: 'type' },
  { name: 'category', category: 'type' },
  { name: 'date', category: 'time' },
  { name: 'before', category: 'time' },
  { name: 'after', category: 'time' },
  { name: 'size', category: 'size' },
  { name: 'attachments', category: 'size' },
  { name: 'is', category: 'state' },
  { name: 'has', category: 'state' },
];

export function fieldCategory(name: string | undefined): FieldCategory | undefined {
  if (!name) return undefined;
  if (name === 'folder') return 'place';
  if (name === 'title') return 'text';
  if (name === 'label') return 'type';
  if (name === '_exists_') return 'state';
  return FIELDS.find((f) => f.name === name)?.category;
}

export interface Suggestion {
  id: string;
  kind: 'field' | 'value' | 'operator' | 'paren';
  label: string;
  /** Text inserted in place of the context range. */
  insert: string;
  /** Secondary text, e.g. e-mail address or i18n description key resolved by the UI. */
  detail?: string;
  /** i18n key for the description (fields, operators). */
  descKey?: string;
  category?: FieldCategory;
  avatar?: { name: string; seed: string };
  /** Complete clause: a space is appended and the clause becomes a chip. */
  complete: boolean;
  /** Tag colour swatch. */
  color?: string;
}

export interface SuggestData {
  accounts: { id: number; name: string; address?: string }[];
  folders: { id: number; name: string; account: string }[];
  people: { name: string; email: string }[];
  /** Tag paths and colours. */
  tags?: { path: string; color: string }[];
  projects?: { key: string; name: string }[];
}

const OPERATORS: { op: string; desc: string }[] = [
  { op: 'AND', desc: 'search.op.and' },
  { op: 'OR', desc: 'search.op.or' },
  { op: 'NOT', desc: 'search.op.not' },
  { op: 'UND', desc: 'search.op.and' },
  { op: 'ODER', desc: 'search.op.or' },
  { op: 'NICHT', desc: 'search.op.not' },
];

const STATIC_VALUES: Record<string, { value: string; desc?: string }[]> = {
  channel: [{ value: 'email' }, { value: 'whatsapp' }, { value: 'signal' }],
  type: [{ value: 'mail' }, { value: 'chat' }, { value: 'event' }, { value: 'task' }, { value: 'contact' }, { value: 'file' }],
  category: [{ value: 'rechnung' }, { value: 'newsletter' }, { value: 'arbeit' }],
  is: [{ value: 'unread' }, { value: 'read' }, { value: 'flagged' }, { value: 'answered' }],
  has: [{ value: 'attachment' }, { value: 'calendar' }],
  date: [
    { value: 'heute', desc: 'search.preset.today' },
    { value: 'gestern', desc: 'search.preset.yesterday' },
    { value: '"letzte Woche"', desc: 'search.preset.lastWeek' },
    { value: '>now-7d', desc: 'search.preset.last7' },
    { value: '>now-30d', desc: 'search.preset.last30' },
    { value: '>now-1y', desc: 'search.preset.lastYear' },
    { value: `[${new Date().getFullYear()}-01-01 TO ${new Date().getFullYear()}-12-31]`, desc: 'search.preset.range' },
  ],
  size: [{ value: '>1MB' }, { value: '>10MB' }, { value: '<100KB' }],
  attachments: [{ value: '>1' }, { value: '>2' }],
  filename: [{ value: '*.pdf' }, { value: '*.docx' }, { value: '*.jpg' }],
  attachment: [{ value: '*.pdf' }],
};
STATIC_VALUES.before = STATIC_VALUES.after = [{ value: 'heute' }, { value: 'now-7d' }, { value: 'now-30d' }, { value: `${new Date().getFullYear()}-01-01` }];

function matches(candidate: string, prefix: string): boolean {
  if (!prefix) return true;
  const c = candidate.toLocaleLowerCase('de');
  const p = prefix.toLocaleLowerCase('de').replace(/^"/, '');
  return c.startsWith(p) || c.includes(p);
}

export function suggestionsFor(ctx: CompletionContext, data: SuggestData, limit = 24): Suggestion[] {
  const out: Suggestion[] = [];
  const fieldSugs = (prefix: string) =>
    FIELDS.filter((f) => f.name.startsWith(prefix.toLowerCase())).map(
      (f): Suggestion => ({ id: 'f:' + f.name, kind: 'field', label: f.name + ':', insert: f.name + ':', descKey: 'search.field.' + f.name, category: f.category, complete: false }),
    );

  switch (ctx.kind) {
    case 'none':
      return [];
    case 'start': {
      out.push(...fieldSugs(ctx.prefix));
      if (matches('(', ctx.prefix) || !ctx.prefix) out.push({ id: 'p:(', kind: 'paren', label: '(', insert: '(', descKey: 'search.op.group', complete: false });
      for (const o of OPERATORS.filter((o) => o.op === 'NOT' || o.op === 'NICHT')) {
        if (!ctx.prefix || o.op.toLowerCase().startsWith(ctx.prefix.toLowerCase())) out.push({ id: 'o:' + o.op, kind: 'operator', label: o.op, insert: o.op, descKey: o.desc, complete: true });
      }
      break;
    }
    case 'operator': {
      const p = ctx.prefix.toLowerCase();
      if (ctx.depth > 0 && (!p || p === ')')) out.push({ id: 'p:)', kind: 'paren', label: ')', insert: ')', descKey: 'search.op.closeGroup', complete: true });
      for (const o of OPERATORS) if (!p || o.op.toLowerCase().startsWith(p)) out.push({ id: 'o:' + o.op, kind: 'operator', label: o.op, insert: o.op, descKey: o.desc, complete: true });
      if (p) out.push(...fieldSugs(ctx.prefix));
      break;
    }
    case 'value': {
      const f = ctx.field ?? '';
      const cat = fieldCategory(f);
      if (cat === 'person') {
        for (const p of data.people) {
          if (matches(p.name, ctx.prefix) || matches(p.email, ctx.prefix))
            out.push({ id: 'v:' + p.email, kind: 'value', label: p.name, detail: p.email, insert: quoteValue(p.email), avatar: { name: p.name, seed: p.email }, category: cat, complete: true });
        }
      } else if (f === 'account') {
        for (const a of data.accounts) if (matches(a.name, ctx.prefix)) out.push({ id: 'v:a' + a.id, kind: 'value', label: a.name, detail: a.address, insert: quoteValue(a.name), category: cat, complete: true });
      } else if (f === 'tag') {
        for (const tg of data.tags ?? []) if (matches(tg.path, ctx.prefix)) out.push({ id: 'v:t' + tg.path, kind: 'value', label: tg.path, insert: quoteValue(tg.path), category: cat, complete: true, color: tg.color });
      } else if (f === 'project') {
        for (const pr of data.projects ?? []) if (matches(pr.key, ctx.prefix) || matches(pr.name, ctx.prefix)) out.push({ id: 'v:p' + pr.key, kind: 'value', label: pr.key, detail: pr.name, insert: quoteValue(pr.key), category: cat, complete: true });
      } else if (f === 'in' || f === 'folder') {
        const seen = new Set<string>();
        for (const fo of data.folders) {
          if (seen.has(fo.name) || !matches(fo.name, ctx.prefix)) continue;
          seen.add(fo.name);
          out.push({ id: 'v:f' + fo.id, kind: 'value', label: fo.name, detail: fo.account, insert: quoteValue(fo.name), category: cat, complete: true });
        }
      } else {
        for (const v of STATIC_VALUES[f] ?? []) if (matches(v.value, ctx.prefix)) out.push({ id: 'v:' + v.value, kind: 'value', label: v.value, insert: v.value, descKey: v.desc, category: cat, complete: true });
      }
      break;
    }
  }
  return out.slice(0, limit);
}

/** Apply a suggestion to the query text; returns the new text and caret. */
export function applySuggestion(text: string, ctx: CompletionContext, s: Suggestion): { text: string; caret: number } {
  const before = text.slice(0, ctx.from);
  let after = text.slice(ctx.to);
  let insert = s.insert;
  if (s.complete) {
    after = after.replace(/^\s+/, '');
    insert += ' ';
  }
  // keep a space between a previous clause and an inserted operator/field
  const needsSpace = before.length > 0 && !/[\s(:]$/.test(before) && s.kind !== 'value';
  const pre = needsSpace ? before + ' ' : before;
  return { text: pre + insert + after, caret: (pre + insert).length };
}
