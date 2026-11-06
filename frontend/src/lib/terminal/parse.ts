// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Port of internal/mailcmd/parse.go and sanitize.go for the terminal view. The
// syntax itself is not repeated: both interpret internal/mailcmd/commands.json,
// and internal/mailcmd/testdata/vectors.json pins the same results in Go and
// here (parse.test.ts).

import table from '$mailcmd/commands.json';

export interface Positional {
  name: string;
  type: string;
  optional?: boolean;
  values?: string[];
}
export interface Spec {
  name: string;
  aliases: string[];
  usage: string;
  ui?: boolean;
  positional: Positional[];
  flags: Record<string, string>;
}
export const specs = table.commands as Spec[];
export const ROLES: readonly string[] = table.roles;
export const UNIFIED: readonly string[] = table.unified;

export type ParseCode =
  | 'empty'
  | 'unterminated_quote'
  | 'unknown_command'
  | 'unknown_flag'
  | 'missing_value'
  | 'bad_value'
  | 'missing_argument'
  | 'too_many_arguments';

export class ParseError extends Error {
  constructor(
    readonly code: ParseCode,
    readonly detail = '',
  ) {
    super(code);
  }
}

type Value = string | number | boolean | string[] | number[];
export interface Command {
  name: string;
  args: Record<string, Value>;
  flags: Record<string, Value>;
}

export function tokenize(line: string): string[] {
  const out: string[] = [];
  let cur = '';
  let inToken = false;
  let quote = '';
  let escaped = false;
  for (const ch of line) {
    if (escaped) {
      cur += ch;
      escaped = false;
    } else if (quote === "'") {
      if (ch === "'") quote = '';
      else cur += ch;
    } else if (ch === '\\') {
      escaped = inToken = true;
    } else if (quote === '"') {
      if (ch === '"') quote = '';
      else cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      inToken = true;
    } else if (ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r') {
      if (inToken) {
        out.push(cur);
        cur = '';
        inToken = false;
      }
    } else {
      cur += ch;
      inToken = true;
    }
  }
  if (quote || escaped) throw new ParseError('unterminated_quote');
  if (inToken) out.push(cur);
  return out;
}

const DIGITS = /^[0-9]+$/;

function parseIds(s: string): number[] | null {
  const out: number[] = [];
  for (const p of s.split(',')) {
    if (!DIGITS.test(p) || Number(p) <= 0) return null;
    out.push(Number(p));
  }
  return out;
}

export function lookup(name: string): Spec | undefined {
  return specs.find((s) => s.name === name || s.aliases.includes(name));
}

export function parseLine(line: string): Command {
  const toks = tokenize(line);
  if (toks.length === 0) throw new ParseError('empty');
  const spec = lookup(toks[0]);
  if (!spec) throw new ParseError('unknown_command', toks[0]);
  const cmd: Command = { name: spec.name, args: {}, flags: {} };
  const words = toks.slice(1);
  let pos: string[] = [];
  for (let i = 0; i < words.length; i++) {
    const w = words[i];
    if (w === '--') {
      pos.push(...words.slice(i + 1));
      break;
    }
    if (!w.startsWith('--')) {
      pos.push(w);
      continue;
    }
    const eq = w.indexOf('=');
    const name = eq < 0 ? w.slice(2) : w.slice(2, eq);
    const hasVal = eq >= 0;
    let val = hasVal ? w.slice(eq + 1) : '';
    const kind = Object.hasOwn(spec.flags, name) ? spec.flags[name] : undefined;
    if (!kind) throw new ParseError('unknown_flag', name);
    if (kind === 'bool') {
      if (hasVal) throw new ParseError('bad_value', name);
      cmd.flags[name] = true;
      continue;
    }
    if (!hasVal) {
      if (i + 1 >= words.length || words[i + 1].startsWith('--')) throw new ParseError('missing_value', name);
      val = words[++i];
    }
    if (kind === 'int') {
      if (!DIGITS.test(val)) throw new ParseError('bad_value', name);
      cmd.flags[name] = Number(val);
    } else if (kind === 'list') {
      cmd.flags[name] = val
        .split(',')
        .map((p) => p.trim())
        .filter((p) => p !== '');
    } else {
      cmd.flags[name] = val;
    }
  }
  const required = spec.positional.filter((p) => !p.optional).length;
  let spare = pos.length - required;
  for (const p of spec.positional) {
    if (p.type === 'rest') {
      if (pos.length === 0) throw new ParseError('missing_argument', p.name);
      cmd.args[p.name] = pos.join(' ');
      pos = [];
      continue;
    }
    if (p.optional && spare <= 0) continue;
    if (pos.length === 0) throw new ParseError('missing_argument', p.name);
    if (p.optional) spare--;
    const v = pos.shift() as string;
    if (p.type === 'ids') {
      const ids = parseIds(v);
      if (!ids) throw new ParseError('bad_value', p.name);
      cmd.args[p.name] = ids;
    } else if (p.type === 'enum') {
      if (!p.values?.includes(v)) throw new ParseError('bad_value', p.name);
      cmd.args[p.name] = v;
    } else {
      cmd.args[p.name] = v;
    }
  }
  if (pos.length > 0) throw new ParseError('too_many_arguments', pos[0]);
  return cmd;
}

// ---------------------------------------------------------------------------
// Control characters: harmless in the DOM, but the view must show the same
// text as the terminal interface, and bidi overrides mislead here as well.
// ---------------------------------------------------------------------------

function stripSequences(s: string): string {
  let out = '';
  const rs = [...s];
  for (let i = 0; i < rs.length; i++) {
    const c = rs[i].codePointAt(0) as number;
    let kind: number;
    if (c === 0x1b) {
      if (i + 1 >= rs.length) continue;
      kind = rs[++i].codePointAt(0) as number;
    } else if (c >= 0x80 && c <= 0x9f) {
      kind = c - 0x40;
    } else {
      out += rs[i];
      continue;
    }
    const at = (n: number) => (n < rs.length ? (rs[n].codePointAt(0) as number) : -1);
    if (kind === 0x5b) {
      while (at(i + 1) >= 0x20 && at(i + 1) <= 0x3f) i++;
      if (at(i + 1) >= 0x40 && at(i + 1) <= 0x7e) i++;
    } else if (kind === 0x5d || kind === 0x50 || kind === 0x58 || kind === 0x5e || kind === 0x5f) {
      while (i + 1 < rs.length) {
        const x = at(++i);
        if (x === 0x07 || x === 0x9c) break;
        if (x === 0x1b) {
          if (at(i + 1) === 0x5c) i++;
          break;
        }
      }
    } else if (kind >= 0x20 && kind <= 0x2f && i + 1 < rs.length) {
      i++;
    }
  }
  return out;
}

const INVISIBLE = /[\p{Cc}\p{Cf}]/u;

export function cleanLine(s: string): string {
  let out = '';
  let space = false;
  for (const ch of stripSequences(s)) {
    if (ch === '\n' || ch === '\r' || ch === '\t' || ch === '\u2028' || ch === '\u2029') {
      if (!space) out += ' ';
      space = true;
      continue;
    }
    if (INVISIBLE.test(ch)) continue;
    out += ch;
    space = ch === ' ';
  }
  return out.trim();
}

export function cleanText(s: string): string {
  const t = stripSequences(s).replace(/\r\n|[\r\u2028\u2029]/g, '\n');
  let out = '';
  for (const ch of t) {
    if (ch !== '\n' && ch !== '\t' && INVISIBLE.test(ch)) continue;
    out += ch;
  }
  return out;
}

const BLOCK = new Set(['P', 'DIV', 'BR', 'LI', 'TR', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'BLOCKQUOTE', 'PRE', 'UL', 'OL', 'TABLE', 'HR']);
const SKIP = new Set(['SCRIPT', 'STYLE', 'HEAD', 'TITLE', 'IMG']);

/** HTML as plain text. DOMParser documents are inert: nothing loads or runs. */
export function htmlAsText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  let out = '';
  const walk = (n: Node) => {
    if (n.nodeType === Node.TEXT_NODE) {
      out += n.textContent ?? '';
      return;
    }
    if (n.nodeType !== Node.ELEMENT_NODE) return;
    const name = (n as Element).tagName;
    if (SKIP.has(name)) return;
    n.childNodes.forEach(walk);
    if (BLOCK.has(name)) out += '\n';
  };
  walk(doc.body);
  return out.replace(/\n{3,}/g, '\n\n').trim();
}

export function bodyText(text: string | undefined, html: string | undefined): string {
  const t = text && text.trim() ? text : html ? htmlAsText(html) : '';
  return cleanText(t).trimEnd();
}
