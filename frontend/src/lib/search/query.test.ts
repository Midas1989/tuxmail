// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { contextAt, matchParens, parse, tokenize } from './query';
import { parseDateRange, parseSize, textMatches } from './engine';
import { MockTransport } from '../api/mock';
import type { SearchQuery } from '../types';
import { applySuggestion, suggestionsFor, type SuggestData } from './suggest';

const types = (s: string) => tokenize(s).filter((t) => t.type !== 'ws').map((t) => t.type);

describe('tokenize', () => {
  it('is lossless', () => {
    const q = '(from:anna OR from:"Max Weber") AND NOT subject:angebot* date:[2026-01-01 TO 2026-06-30]';
    expect(tokenize(q).map((t) => t.text).join('')).toBe(q);
  });

  it('recognises fields, values, operators, phrases, ranges and parens', () => {
    expect(types('(from:anna OR from:"Max Weber") AND NOT subject:angebot')).toEqual([
      'lparen', 'field', 'term', 'op', 'field', 'phrase', 'rparen', 'op', 'op', 'field', 'term',
    ]);
    expect(types('date:[2026-01-01 TO 2026-06-30] size:>10MB')).toEqual(['field', 'range', 'field', 'term']);
    const value = tokenize('from:anna').find((t) => t.type === 'term');
    expect(value?.field).toBe('from');
  });

  it('normalises German and symbolic operators', () => {
    const ops = tokenize('a UND b ODER NICHT c && d || !e').filter((t) => t.type === 'op').map((t) => t.op);
    expect(ops).toEqual(['AND', 'OR', 'NOT', 'AND', 'OR', 'NOT']);
  });

  it('does not treat lowercase "and"/"oder" as operators', () => {
    expect(types('rock and roll')).toEqual(['term', 'term', 'term']);
  });

  it('marks +/- modifiers and unterminated phrases', () => {
    expect(types('+vertrag -entwurf')).toEqual(['mod', 'term', 'mod', 'term']);
    expect(tokenize('"offen').at(-1)?.unterminated).toBe(true);
  });

  it('tags field groups', () => {
    const lp = tokenize('from:(anna OR max)').find((t) => t.type === 'lparen');
    expect(lp?.field).toBe('from');
  });

  it('matches nested parentheses and flags unbalanced ones', () => {
    const toks = tokenize('((a OR b) AND (c))');
    const info = matchParens(toks);
    expect(info.balanced).toBe(true);
    const idx = (n: number) => toks.findIndex((t, i) => t.type === 'lparen' && toks.slice(0, i).filter((x) => x.type === 'lparen').length === n);
    expect(toks[info.match.get(idx(0))!].end).toBe(18);
    expect(matchParens(tokenize('(a OR (b)')).balanced).toBe(false);
    expect(matchParens(tokenize('a)')).match.get(1)).toBe(-1);
  });
});

describe('parse', () => {
  it('applies precedence NOT > AND > OR', () => {
    const r = parse('a OR b AND NOT c');
    expect(r.ok && r.ast).toEqual({
      t: 'or',
      children: [
        { t: 'term', value: 'a', kind: 'word', field: undefined, boost: undefined, fuzzy: false, pos: 0 },
        {
          t: 'and',
          children: [
            { t: 'term', value: 'b', kind: 'word', field: undefined, boost: undefined, fuzzy: false, pos: 5 },
            { t: 'not', child: { t: 'term', value: 'c', kind: 'word', field: undefined, boost: undefined, fuzzy: false, pos: 15 } },
          ],
        },
      ],
    });
  });

  it('distributes a field over a field group', () => {
    const r = parse('from:(anna ODER max)');
    expect(r.ok).toBe(true);
    if (r.ok && r.ast.t === 'or') expect(r.ast.children.map((c) => (c.t === 'term' ? c.field : null))).toEqual(['from', 'from']);
  });

  it('parses comparisons, ranges, boosts, fuzzy and phrases with slop', () => {
    const r = parse('size:>10MB date:[2026-01-01 TO 2026-03-31] vertrag^3 meier~ "vertrag kündigung"~5');
    expect(r.ok).toBe(true);
    if (!r.ok || r.ast.t !== 'and') return;
    const [size, date, boost, fuzzy, phrase] = r.ast.children as Extract<typeof r.ast.children[number], { t: 'term' }>[];
    expect(size).toMatchObject({ field: 'size', kind: 'cmp', op: '>', value: '10MB' });
    expect(date).toMatchObject({ field: 'date', kind: 'range', from: '2026-01-01', to: '2026-03-31' });
    expect(boost).toMatchObject({ value: 'vertrag', boost: 3 });
    expect(fuzzy).toMatchObject({ value: 'meier', fuzzy: true });
    expect(phrase).toMatchObject({ kind: 'phrase', value: 'vertrag kündigung', slop: 5 });
  });

  it('reports errors with positions', () => {
    expect(parse('(a OR b')).toEqual({ ok: false, error: expect.objectContaining({ position: 7 }) });
    expect(parse('a OR')).toEqual({ ok: false, error: expect.objectContaining({ position: 4 }) });
    expect(parse('a )')).toEqual({ ok: false, error: expect.objectContaining({ position: 2 }) });
    expect(parse('subject: x')).toEqual({ ok: false, error: expect.objectContaining({ position: 8 }) });
    expect(parse('"offen')).toEqual({ ok: false, error: expect.objectContaining({ position: 0 }) });
  });

  it('accepts an empty query', () => {
    expect(parse('   ')).toEqual({ ok: true, ast: { t: 'all' } });
  });
});

describe('contextAt', () => {
  const ctx = (s: string, caret = s.length) => contextAt(s, caret);

  it('suggests fields at the start of a term', () => {
    expect(ctx('')).toMatchObject({ kind: 'start', prefix: '' });
    expect(ctx('fr')).toMatchObject({ kind: 'start', prefix: 'fr', from: 0, to: 2 });
    expect(ctx('a AND ')).toMatchObject({ kind: 'start' });
    expect(ctx('(')).toMatchObject({ kind: 'start', depth: 1 });
    expect(ctx('NICHT ')).toMatchObject({ kind: 'start' });
  });

  it('suggests values after a field', () => {
    expect(ctx('from:')).toMatchObject({ kind: 'value', field: 'from', prefix: '' });
    expect(ctx('subject:x from:an')).toMatchObject({ kind: 'value', field: 'from', prefix: 'an', from: 15, to: 17 });
    expect(ctx('(a OR in:Pro')).toMatchObject({ kind: 'value', field: 'in', depth: 1 });
  });

  it('suggests operators after a complete clause', () => {
    expect(ctx('from:anna ')).toMatchObject({ kind: 'operator', depth: 0 });
    expect(ctx('(from:anna ')).toMatchObject({ kind: 'operator', depth: 1 });
    expect(ctx('((a OR b) ')).toMatchObject({ kind: 'operator', depth: 1 });
    expect(ctx('"angebot q4" ')).toMatchObject({ kind: 'operator' });
    expect(ctx('a O')).toMatchObject({ kind: 'operator', prefix: 'O', from: 2 });
    expect(ctx('a UND b OD')).toMatchObject({ kind: 'operator', prefix: 'OD' });
  });

  it('offers nothing inside quotes', () => {
    expect(ctx('subject:"angebot q')).toMatchObject({ kind: 'none' });
    expect(ctx('"a b" c "d', 7)).toMatchObject({ kind: 'operator' });
    expect(ctx('"abc"', 2)).toMatchObject({ kind: 'none' });
  });

  it('uses the caret, not the end of the text', () => {
    const s = 'from:anna AND subject:x';
    expect(contextAt(s, 5)).toMatchObject({ kind: 'value', field: 'from' });
    expect(contextAt(s, 10)).toMatchObject({ kind: 'operator' });
    expect(contextAt(s, 14)).toMatchObject({ kind: 'start' });
  });
});

describe('engine helpers', () => {
  it('matches wildcards, phrases and fuzzy terms', () => {
    expect(textMatches('Angebot_Q4.pdf', { value: 'angeb*', kind: 'word' })).toBe(true);
    expect(textMatches('Termin Bodenplatte', { value: 'm?ier', kind: 'word' })).toBe(false);
    expect(textMatches('Herr Meier', { value: 'meyer', kind: 'word', fuzzy: true })).toBe(true);
    expect(textMatches('das   Angebot  Q4', { value: 'angebot q4', kind: 'phrase' })).toBe(true);
  });

  it('parses sizes and dates', () => {
    expect(parseSize('10MB')).toBe(10 * 1024 * 1024);
    expect(parseSize('1,5 KB')).toBe(1536);
    const now = new Date(2026, 10, 3, 12, 0);
    const [a, b] = parseDateRange('2026-10', now)!;
    expect(new Date(a).getDate()).toBe(1);
    expect(new Date(b).getMonth()).toBe(9);
    expect(parseDateRange('now-7d', now)![0]).toBe(new Date(2026, 9, 27, 12, 0).getTime());
  });
});

describe('MockTransport.search', () => {
  const base: SearchQuery = { text: '', mode: 'expert', scopes: [], accountIds: [], folderIds: [], includeSubfolders: true, excludeTrashSpam: true, location: 'auto', sort: 'relevance' };
  const api = new MockTransport({ latencyMs: 0 });

  it('evaluates boolean expert queries with fields', async () => {
    const r = await api.search({ ...base, text: '(from:anna OR from:max) AND subject:angebot', scopes: ['mail'] });
    expect(r.error).toBeUndefined();
    expect(r.hits.length).toBeGreaterThan(0);
    expect(r.hits.every((h) => /angebot/i.test(h.title))).toBe(true);
  });

  it('returns highlights, facets and a histogram', async () => {
    const r = await api.search({ ...base, mode: 'simple', text: 'Bodenplatte' });
    const hit = r.hits.find((h) => h.kind === 'mail')!;
    expect(hit.titleHighlights.length).toBeGreaterThan(0);
    expect(r.facets.account.length).toBeGreaterThan(0);
    expect(r.facets.account[0].clause).toMatch(/^account:/);
    expect(r.histogram.reduce((n, b) => n + b.count, 0)).toBe(r.total);
  });

  it('reports a positioned error and falls back to a simple search', async () => {
    const r = await api.search({ ...base, text: '(angebot OR' });
    expect(r.error?.position).toBe(11);
    expect(r.fallback).toBe(true);
    expect(r.hits.length).toBeGreaterThan(0);
  });

  it('honours scope chips and trash/spam exclusion', async () => {
    const spam = await api.search({ ...base, mode: 'simple', text: 'gewonnen' });
    expect(spam.total).toBe(0);
    const withSpam = await api.search({ ...base, mode: 'simple', text: 'gewonnen', excludeTrashSpam: false });
    expect(withSpam.total).toBe(1);
    const chats = await api.search({ ...base, text: 'channel:signal', scopes: ['chat'] });
    expect(chats.hits.every((h) => h.channel === 'signal')).toBe(true);
  });

  it('records history', async () => {
    const h = await api.getSearchHistory();
    expect(h[0]?.text).toBe('channel:signal');
  });
});

describe('suggestions', () => {
  const data: SuggestData = {
    accounts: [{ id: 2, name: 'Kanzlei Sommer' }],
    folders: [{ id: 17, name: 'Hausbau', account: 'Privat' }],
    people: [{ name: 'Anna Müller', email: 'anna.mueller@mueller-gmbh.de' }],
  };
  const sugg = (s: string) => suggestionsFor(contextAt(s, s.length), data);

  it('offers fields, "(" and NOT at the start', () => {
    const ids = sugg('').map((x) => x.id);
    expect(ids).toContain('f:from');
    expect(ids).toContain('p:(');
    expect(sugg('su').map((x) => x.label)).toEqual(['subject:']);
  });

  it('offers values with avatars after a person field and quotes when needed', () => {
    const s = sugg('from:an');
    expect(s[0]).toMatchObject({ kind: 'value', avatar: { name: 'Anna Müller' }, insert: 'anna.mueller@mueller-gmbh.de' });
    expect(sugg('account:')[0].insert).toBe('"Kanzlei Sommer"');
    expect(sugg('in:hau')[0].insert).toBe('Hausbau');
    expect(sugg('is:').map((x) => x.label)).toContain('unread');
  });

  it('offers operators (incl. German) and ")" after a clause inside a group', () => {
    const labels = sugg('(from:anna ').map((x) => x.label);
    expect(labels[0]).toBe(')');
    expect(labels).toEqual(expect.arrayContaining(['AND', 'OR', 'NOT', 'UND', 'ODER', 'NICHT']));
    expect(sugg('a OD').map((x) => x.label)).toEqual(['ODER']);
  });

  it('offers nothing inside quotes', () => {
    expect(sugg('subject:"ange')).toEqual([]);
  });

  it('applies a suggestion and appends a space for complete clauses', () => {
    let text = 'from:an';
    let s = suggestionsFor(contextAt(text, text.length), data)[0];
    text = applySuggestion(text, contextAt(text, text.length), s).text;
    expect(text).toBe('from:anna.mueller@mueller-gmbh.de ');
    s = suggestionsFor(contextAt(text, text.length), data).find((x) => x.label === 'AND')!;
    text = applySuggestion(text, contextAt(text, text.length), s).text;
    expect(text).toBe('from:anna.mueller@mueller-gmbh.de AND ');
    s = suggestionsFor(contextAt(text, text.length), data).find((x) => x.label === 'subject:')!;
    expect(applySuggestion(text, contextAt(text, text.length), s).text).toBe('from:anna.mueller@mueller-gmbh.de AND subject:');
  });
});
