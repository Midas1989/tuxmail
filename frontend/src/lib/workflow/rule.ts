// SPDX-License-Identifier: AGPL-3.0-or-later
//
// One-line "Regel" syntax for workflows (expert mode):
//
//   WHEN mail.received IN account:"Firma" IF from:*@kunde.de AND has:attachment
//   THEN tag "Kunde", move "Kunden/Eingang", notify signal "Neue Kundenmail: {{betreff}}"
//
// Grammar
//   rule    := WHEN event [IN scope {"," scope}] [IF cond] THEN action {"," action}
//   scope   := (account|folder|channel|schedule|name) ":" value
//   cond    := EXPR string | or
//   or      := and {OR and}         and := unary {[AND] unary}
//   unary   := NOT unary | "-" term | "#" term | "(" or ")" | term
//   term    := field [op value]     op  := : = != ^= $= ~ @= > >= < <=
//   action  := type {value | key=value}       (positional params in schema order)
// Keywords are case-insensitive; German aliases WENN/IN/FALLS/DANN/UND/ODER/NICHT.
// The parser annotates tokens with roles (chips / highlighting) and reports
// diagnostics with offsets. The Go core implements the same language.

import type { WorkflowAction, WorkflowCondition, WorkflowRule, WorkflowSchema, WorkflowTrigger } from '../types';
import { FLAG_ALIASES, UNARY_OPS, actionSpec, fieldSpec, type WorkflowContext } from './schema';

export type RuleTokType = 'word' | 'string' | 'op' | 'lparen' | 'rparen' | 'lbracket' | 'rbracket' | 'comma' | 'minus' | 'hash' | 'ws';
export type TokRole = 'kw' | 'trigger' | 'scope' | 'field' | 'op' | 'value' | 'action' | 'param' | 'punct' | 'error';

export interface RuleToken {
  type: RuleTokType;
  text: string;
  start: number;
  end: number;
  /** Unquoted string value. */
  value: string;
  unterminated?: boolean;
  role?: TokRole;
}

export interface OffsetDiagnostic {
  from: number;
  to: number;
  severity: 'error' | 'warning' | 'info';
  code: string;
  detail?: string;
  message: string;
}

export interface RuleParseResult {
  tokens: RuleToken[];
  trigger?: WorkflowTrigger;
  condition?: WorkflowCondition;
  actions: WorkflowAction[];
  diagnostics: OffsetDiagnostic[];
}

const KW: Record<string, string> = {
  when: 'WHEN', wenn: 'WHEN', in: 'IN', if: 'IF', falls: 'IF', then: 'THEN', dann: 'THEN',
  and: 'AND', und: 'AND', or: 'OR', oder: 'OR', not: 'NOT', nicht: 'NOT', expr: 'EXPR',
};
export const keywordOf = (w: string): string | undefined => KW[w.toLowerCase()];

const OPS = ['!=', '^=', '$=', '@=', '>=', '<=', ':', '=', '~', '>', '<'];
const WORD_RE = /[^\s:=!^$~<>,()[\]"']/u;

// ---------------------------------------------------------------------------
// Tokenizer
// ---------------------------------------------------------------------------

export function tokenizeRule(src: string): RuleToken[] {
  const out: RuleToken[] = [];
  let i = 0;
  const push = (type: RuleTokType, start: number, end: number, value = src.slice(start, end), unterminated = false) =>
    out.push({ type, text: src.slice(start, end), start, end, value, unterminated: unterminated || undefined });
  while (i < src.length) {
    const c = src[i];
    if (/\s/.test(c)) {
      const s = i;
      while (i < src.length && /\s/.test(src[i])) i++;
      push('ws', s, i);
      continue;
    }
    if (c === '"' || c === "'") {
      const s = i++;
      let v = '';
      let closed = false;
      while (i < src.length) {
        const ch = src[i];
        if (ch === '\\' && i + 1 < src.length) {
          const n = src[i + 1];
          v += n === 'n' ? '\n' : n === 't' ? '\t' : n;
          i += 2;
          continue;
        }
        if (ch === c) {
          closed = true;
          i++;
          break;
        }
        v += ch;
        i++;
      }
      push('string', s, i, v, !closed);
      continue;
    }
    const single: Record<string, RuleTokType> = { '(': 'lparen', ')': 'rparen', '[': 'lbracket', ']': 'rbracket', ',': 'comma' };
    if (single[c]) {
      push(single[c], i, i + 1);
      i++;
      continue;
    }
    const prev = out[out.length - 1];
    const atTermStart = !prev || prev.type === 'ws' || prev.type === 'lparen';
    if ((c === '-' || c === '#') && atTermStart && i + 1 < src.length && /[\p{L}_]/u.test(src[i + 1])) {
      push(c === '-' ? 'minus' : 'hash', i, i + 1);
      i++;
      continue;
    }
    const op = OPS.find((o) => src.startsWith(o, i));
    if (op) {
      push('op', i, i + op.length);
      i += op.length;
      // the value after an operator may contain ':' (URLs, "signal:me")
      if (i < src.length && src[i] !== '"' && src[i] !== "'" && src[i] !== '[' && !/\s/.test(src[i])) {
        const s = i;
        while (i < src.length && !/[\s,()[\]]/.test(src[i])) i++;
        push('word', s, i);
      }
      continue;
    }
    if (WORD_RE.test(c)) {
      const s = i;
      while (i < src.length && WORD_RE.test(src[i])) i++;
      push('word', s, i);
      continue;
    }
    // stray character (e.g. lone "!" or "^")
    push('word', i, i + 1);
    i++;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

const NEGATE: Record<string, string> = { contains: 'not_contains', not_contains: 'contains', eq: 'ne', ne: 'eq', in: 'not_in', not_in: 'in', is_true: 'is_false', is_false: 'is_true', empty: 'not_empty', not_empty: 'empty' };

function parseNumber(v: string): number | null {
  const m = /^(-?\d+(?:[.,]\d+)?)\s*(kb|mb|gb|k|m|g)?$/i.exec(v.trim());
  if (!m) return null;
  const n = parseFloat(m[1].replace(',', '.'));
  const unit = (m[2] ?? '').toLowerCase();
  const mul = unit.startsWith('k') ? 1024 : unit.startsWith('m') ? 1024 ** 2 : unit.startsWith('g') ? 1024 ** 3 : 1;
  return Math.round(n * mul);
}

export function variablesIn(text: string): { name: string; index: number }[] {
  const out: { name: string; index: number }[] = [];
  const re = /\{\{\s*([\p{L}_][\p{L}\p{N}_.]*)\s*\}\}/gu;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) out.push({ name: m[1], index: m.index });
  return out;
}

export function parseRule(src: string, schema: WorkflowSchema, ctx: WorkflowContext): RuleParseResult {
  const tokens = tokenizeRule(src);
  const toks = tokens.filter((t) => t.type !== 'ws');
  const diagnostics: OffsetDiagnostic[] = [];
  let p = 0;
  const peek = (o = 0) => toks[p + o];
  const kwAt = (o = 0) => {
    const t = peek(o);
    return t && t.type === 'word' ? keywordOf(t.text) : undefined;
  };
  const diag = (from: number, to: number, code: string, message: string, detail?: string, severity: OffsetDiagnostic['severity'] = 'error') =>
    diagnostics.push({ from, to: Math.max(to, from + 1), severity, code, message, detail });
  const end = src.length;
  const adjacent = (a: RuleToken | undefined, b: RuleToken | undefined) => !!a && !!b && a.end === b.start;

  for (const t of toks) if (t.type === 'string' && t.unterminated) diag(t.start, t.end, 'unterminated_string', 'Unterminated string');

  const result: RuleParseResult = { tokens, actions: [], diagnostics };

  // WHEN event
  if (kwAt() !== 'WHEN') {
    const t = peek();
    diag(t?.start ?? 0, t?.end ?? Math.max(1, end), 'expected_when', 'The rule must start with WHEN');
    if (!t) return result;
  } else {
    peek()!.role = 'kw';
    p++;
  }
  const evTok = peek();
  let event = '';
  if (!evTok || evTok.type !== 'word' || keywordOf(evTok.text)) {
    diag(evTok?.start ?? end, evTok?.end ?? end + 1, 'expected_trigger', 'Expected a trigger event after WHEN');
  } else {
    event = evTok.text;
    evTok.role = 'trigger';
    if (!schema.triggers.some((x) => x.event === event)) diag(evTok.start, evTok.end, 'unknown_trigger', `Unknown trigger "${event}"`, event);
    p++;
  }
  const trigger: WorkflowTrigger = { event };
  result.trigger = trigger;

  // IN scopes
  if (kwAt() === 'IN') {
    peek()!.role = 'kw';
    p++;
    for (;;) {
      const k = peek();
      if (!k || k.type !== 'word' || keywordOf(k.text)) {
        diag(k?.start ?? end, k?.end ?? end + 1, 'expected_scope', 'Expected account:, folder: or channel: after IN');
        break;
      }
      k.role = 'scope';
      const key = k.text.toLowerCase();
      p++;
      const op = peek();
      if (!op || op.type !== 'op' || op.text !== ':' || !adjacent(k, op)) {
        diag(k.start, k.end, 'expected_colon', `Expected ":" after "${k.text}"`, k.text);
        continue;
      }
      op.role = 'op';
      p++;
      const values = readValues();
      if (!values.length) diag(op.start, op.end, 'missing_value', 'Missing value', k.text);
      if (key === 'account' || key === 'konto') {
        for (const v of values) {
          const n = Number(v.value);
          const acc = ctx.accounts.find((a) => a.name.toLowerCase() === v.value.toLowerCase()) ?? (Number.isInteger(n) ? ctx.accounts.find((a) => a.id === n) : undefined);
          if (!acc) diag(v.tok.start, v.tok.end, 'unknown_account', `Unknown account "${v.value}"`, v.value);
          (trigger.accounts ??= []).push(acc ? acc.id : Number.isInteger(n) ? n : 0);
        }
      } else if (key === 'folder' || key === 'ordner') (trigger.folders ??= []).push(...values.map((v) => v.value));
      else if (key === 'channel' || key === 'kanal') (trigger.channels ??= []).push(...values.map((v) => v.value));
      else if (key === 'schedule') trigger.schedule = values[0]?.value;
      else if (key === 'name') trigger.name = values[0]?.value;
      else diag(k.start, k.end, 'unknown_scope', `Unknown scope "${k.text}"`, k.text);
      if (peek()?.type === 'comma') {
        peek()!.role = 'punct';
        p++;
        continue;
      }
      break;
    }
  }

  function readValues(): { value: string; tok: RuleToken }[] {
    const t = peek();
    if (!t) return [];
    if (t.type === 'lbracket') {
      t.role = 'punct';
      p++;
      const out: { value: string; tok: RuleToken }[] = [];
      while (peek() && peek()!.type !== 'rbracket') {
        const v = peek()!;
        if (v.type === 'comma') {
          v.role = 'punct';
          p++;
          continue;
        }
        if (v.type !== 'word' && v.type !== 'string') break;
        v.role = 'value';
        out.push({ value: v.value, tok: v });
        p++;
      }
      if (peek()?.type === 'rbracket') {
        peek()!.role = 'punct';
        p++;
      } else diag(t.start, t.end, 'unclosed_bracket', 'Missing "]"');
      return out;
    }
    if (t.type === 'word' || t.type === 'string') {
      t.role = 'value';
      p++;
      return [{ value: t.value, tok: t }];
    }
    return [];
  }

  // IF condition
  if (kwAt() === 'IF') {
    peek()!.role = 'kw';
    p++;
    if (kwAt() === 'EXPR') {
      peek()!.role = 'kw';
      p++;
      const s = peek();
      if (s && (s.type === 'string' || s.type === 'word')) {
        s.role = 'value';
        p++;
        result.condition = { expr: s.value };
      } else diag(s?.start ?? end, s?.end ?? end + 1, 'expected_expr', 'Expected a quoted expression after EXPR');
    } else {
      const r = parseOr();
      if (r) result.condition = { rule: r };
      else diag(peek()?.start ?? end, peek()?.end ?? end + 1, 'expected_condition', 'Expected a condition after IF');
    }
  }

  function stop(): boolean {
    const t = peek();
    if (!t) return true;
    if (t.type === 'rparen') return true;
    const k = kwAt();
    return k === 'THEN' || k === 'OR';
  }

  function parseOr(): WorkflowRule | null {
    const items: WorkflowRule[] = [];
    const first = parseAnd();
    if (first) items.push(first);
    while (kwAt() === 'OR') {
      peek()!.role = 'kw';
      p++;
      const n = parseAnd();
      if (n) items.push(n);
      else diag(peek()?.start ?? end, peek()?.end ?? end + 1, 'expected_condition', 'Expected a condition after OR');
    }
    if (!items.length) return null;
    return items.length === 1 ? items[0] : { any: items.flatMap((x) => (x.any ? x.any : [x])) };
  }

  function parseAnd(): WorkflowRule | null {
    const items: WorkflowRule[] = [];
    while (!stop()) {
      if (kwAt() === 'AND') {
        peek()!.role = 'kw';
        p++;
        continue;
      }
      const before = p;
      const u = parseUnary();
      if (u) items.push(u);
      if (p === before) {
        const t = peek()!;
        t.role = 'error';
        diag(t.start, t.end, 'unexpected', `Unexpected "${t.text}"`, t.text);
        p++;
      }
    }
    if (!items.length) return null;
    return items.length === 1 ? items[0] : { all: items.flatMap((x) => (x.all ? x.all : [x])) };
  }

  function parseUnary(): WorkflowRule | null {
    const t = peek();
    if (!t) return null;
    if (kwAt() === 'NOT') {
      t.role = 'kw';
      p++;
      const inner = parseUnary();
      return inner ? { not: inner } : null;
    }
    if (t.type === 'lparen') {
      t.role = 'punct';
      p++;
      const inner = parseOr();
      if (peek()?.type === 'rparen') {
        peek()!.role = 'punct';
        p++;
      } else diag(t.start, t.end, 'unclosed_paren', 'Missing ")"');
      return inner;
    }
    if (t.type === 'minus' || t.type === 'hash') {
      t.role = 'op';
      p++;
      const term = parseTerm(t.type === 'hash');
      if (!term) return null;
      if (t.type === 'hash') return term;
      const neg = term.op ? NEGATE[term.op] : undefined;
      return neg ? { ...term, op: neg } : { not: term };
    }
    return parseTerm(false);
  }

  function parseTerm(count: boolean): WorkflowRule | null {
    const t = peek();
    if (!t || t.type !== 'word' || keywordOf(t.text)) return null;
    t.role = 'field';
    p++;
    const opTok = adjacent(t, peek()) && peek()?.type === 'op' ? peek() : undefined;
    if (opTok) {
      opTok.role = 'op';
      p++;
    }
    const valTok = opTok && adjacent(opTok, peek()) ? peek() : undefined;
    let values: { value: string; tok: RuleToken }[] = [];
    let isList = false;
    if (valTok) {
      isList = valTok.type === 'lbracket';
      values = readValues();
    }
    const name = t.text;
    const lname = name.toLowerCase();
    if (FLAG_ALIASES[lname] && opTok?.text === ':') {
      const v = values[0]?.value.toLowerCase() ?? '';
      const field = FLAG_ALIASES[lname][v];
      if (!field) diag(t.start, values[0]?.tok.end ?? opTok.end, 'unknown_flag', `Unknown flag "${name}:${v}"`, `${name}:${v}`);
      return { field: field ?? `${lname}.${v}`, op: 'is_true' };
    }
    const spec = fieldSpec(schema, name);
    if (!spec) diag(t.start, t.end, 'unknown_field', `Unknown field "${name}"`, name);
    const field = spec?.name ?? name;
    const kind = spec?.kind ?? 'string';
    if (!opTok) {
      if (kind === 'bool') return { field, op: 'is_true' };
      diag(t.start, t.end, 'missing_value', `"${name}" needs an operator and a value`, name);
      return { field, op: 'is_true' };
    }
    if (!values.length) {
      diag(opTok.start, opTok.end, 'missing_value', `Missing value after "${name}${opTok.text}"`, name);
      return { field, op: 'eq', value: '' };
    }
    const raw = values[0].value;
    let op: string;
    let value: WorkflowRule['value'] = raw;
    const sym = opTok.text;
    if (count) op = sym === '<' ? 'count_lt' : 'count_gt';
    else if (sym === ':') {
      if (isList) {
        op = 'in';
        value = values.map((v) => v.value);
      } else if (kind === 'bool') {
        op = raw.toLowerCase() === 'false' ? 'is_false' : 'is_true';
        value = undefined;
      } else if (kind === 'number') op = 'eq';
      else if (raw.length > 1 && raw.startsWith('*') && raw.endsWith('*')) {
        op = 'contains';
        value = raw.slice(1, -1);
      } else if (raw.startsWith('*') && raw.length > 1) {
        op = 'ends_with';
        value = raw.slice(1);
      } else if (raw.endsWith('*') && raw.length > 1) {
        op = 'starts_with';
        value = raw.slice(0, -1);
      } else op = 'contains';
    } else if (sym === '=') op = raw === '' && values[0].tok.type === 'string' ? 'empty' : 'eq';
    else if (sym === '!=') op = raw === '' && values[0].tok.type === 'string' ? 'not_empty' : 'ne';
    else op = ({ '^=': 'starts_with', '$=': 'ends_with', '~': 'matches', '@=': 'domain_is', '>': 'gt', '>=': 'gte', '<': 'lt', '<=': 'lte' } as Record<string, string>)[sym] ?? 'eq';
    if (UNARY_OPS.has(op)) value = undefined;
    if (kind === 'number' || op.startsWith('count_')) {
      const n = parseNumber(raw);
      if (n == null) diag(values[0].tok.start, values[0].tok.end, 'bad_number', `"${raw}" is not a number`, raw);
      else value = n;
    } else if (kind === 'bool' && op === 'eq') value = raw.toLowerCase() === 'true';
    if (spec && !schema.operators[kind].includes(op)) diag(opTok.start, opTok.end, 'bad_operator', `Operator not allowed for ${kind} field "${name}"`, name);
    const node: WorkflowRule = { field, op };
    if (value !== undefined) node.value = value;
    return node;
  }

  // THEN actions
  if (kwAt() !== 'THEN') {
    const t = peek();
    diag(t?.start ?? end, t?.end ?? end + 1, 'expected_then', 'Expected THEN followed by at least one action');
    while (peek() && kwAt() !== 'THEN') {
      peek()!.role = 'error';
      p++;
    }
  }
  if (kwAt() === 'THEN') {
    peek()!.role = 'kw';
    p++;
    if (!peek()) diag(end, end + 1, 'expected_action', 'Expected an action after THEN');
    while (peek()) {
      const a = parseAction();
      if (a) result.actions.push(a);
      if (peek()?.type === 'comma') {
        peek()!.role = 'punct';
        p++;
        if (!peek()) diag(end, end + 1, 'expected_action', 'Expected an action after ","');
        continue;
      }
      if (peek()) {
        const t = peek()!;
        t.role = 'error';
        diag(t.start, t.end, 'unexpected', `Unexpected "${t.text}"`, t.text);
        p++;
      }
    }
  }

  function parseAction(): WorkflowAction | null {
    const t = peek();
    if (!t || t.type !== 'word') {
      if (t) diag(t.start, t.end, 'expected_action', 'Expected an action');
      return null;
    }
    t.role = 'action';
    p++;
    const spec = actionSpec(schema, t.text);
    if (!spec) diag(t.start, t.end, 'unknown_action', `Unknown action "${t.text}"`, t.text);
    else if (spec.triggers.length && event && !spec.triggers.includes(event)) diag(t.start, t.end, 'action_not_allowed', `"${t.text}" is not available for trigger ${event}`, t.text);
    const action: WorkflowAction = { type: t.text, params: {} };
    const positional = spec?.params.filter((x) => x.positional) ?? [];
    let pos = 0;
    while (peek() && peek()!.type !== 'comma') {
      const g = peek()!;
      // named: key=value
      if (g.type === 'word' && /^[a-z_]+$/.test(g.text) && peek(1)?.type === 'op' && peek(1)!.text === '=' && adjacent(g, peek(1))) {
        g.role = 'param';
        peek(1)!.role = 'op';
        p += 2;
        const vals = adjacent(peek(-1), peek()) ? readValues() : [];
        const key = g.text;
        if (!vals.length) diag(g.start, g.end, 'missing_value', `Missing value for "${key}"`, key);
        if (key === 'if') action.if = vals[0]?.value;
        else if (key === 'continue_on_error') action.continueOnError = vals[0]?.value === 'true';
        else {
          const ps = spec?.params.find((x) => x.name === key);
          if (spec && !ps) diag(g.start, g.end, 'unknown_param', `Unknown parameter "${key}" for ${t.text}`, key);
          action.params[key] = convert(vals, ps?.kind);
        }
        continue;
      }
      if (g.type === 'word' || g.type === 'string' || g.type === 'lbracket') {
        const startTok = g;
        const isList = g.type === 'lbracket';
        let vals = readValues();
        // join adjacent bare pieces: signal:me, https://...
        if (!isList && vals.length === 1) {
          let v = vals[0].value;
          while (peek() && adjacent(peek(-1), peek()) && (peek()!.type === 'op' || peek()!.type === 'word')) {
            peek()!.role = 'value';
            v += peek()!.text;
            p++;
          }
          vals = [{ value: v, tok: startTok }];
        }
        const ps = positional[pos++];
        if (!ps) {
          if (spec) diag(startTok.start, peek(-1)!.end, 'too_many_args', `Too many arguments for ${t.text}`, t.text);
        } else action.params[ps.name] = convert(vals, ps.kind);
        for (const v of vals) for (const vr of variablesIn(v.value)) if (!schema.variables.includes(vr.name)) diag(v.tok.start, v.tok.end, 'unknown_variable', `Unknown variable {{${vr.name}}}`, vr.name, 'warning');
        continue;
      }
      break;
    }
    if (spec) for (const ps of spec.params) if (ps.required && action.params[ps.name] === undefined) diag(t.start, t.end, 'missing_param', `${t.text}: missing "${ps.name}"`, ps.name);
    return action;
  }

  function convert(vals: { value: string }[], kind: string | undefined): unknown {
    if (kind === 'list' || kind === 'tag') return vals.map((v) => v.value);
    const v = vals[0]?.value ?? '';
    if (kind === 'number') return parseNumber(v) ?? v;
    if (kind === 'bool') return v === 'true';
    return v;
  }

  return result;
}

// ---------------------------------------------------------------------------
// Printer
// ---------------------------------------------------------------------------

const BARE_RE = /^[\p{L}\p{N}_.@*/+%-]+$/u;

export function formatValue(v: unknown): string {
  if (Array.isArray(v)) return '[' + v.map(formatValue).join(', ') + ']';
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  const s = String(v ?? '');
  if (s && BARE_RE.test(s) && !keywordOf(s) && !/^[-#]/.test(s) && !/^\d/.test(s)) return s;
  return JSON.stringify(s);
}

function fieldName(schema: WorkflowSchema, field: string): string {
  return schema.fields.find((x) => x.name === field)?.alias ?? field;
}

function termText(schema: WorkflowSchema, r: WorkflowRule): string {
  const field = r.field ?? '';
  const op = r.op ?? 'eq';
  if (op === 'is_true' || op === 'is_false') {
    let name = fieldName(schema, field);
    for (const [k, map] of Object.entries(FLAG_ALIASES)) {
      const hit = Object.entries(map).find(([, f]) => f === field);
      if (hit) {
        name = `${k}:${hit[0]}`;
        break;
      }
    }
    return (op === 'is_false' ? '-' : '') + name;
  }
  const name = fieldName(schema, field);
  const v = r.value;
  const fv = (x: unknown) => formatValue(x);
  const strV = String(v ?? '');
  switch (op) {
    case 'contains':
      return `${name}:${fv(v)}`;
    case 'not_contains':
      return `-${name}:${fv(v)}`;
    case 'ends_with':
      return `${name}:${formatValue('*' + strV)}`;
    case 'starts_with':
      return `${name}:${formatValue(strV + '*')}`;
    case 'eq':
      return `${name}=${fv(v)}`;
    case 'ne':
      return `${name}!=${fv(v)}`;
    case 'matches':
      return `${name}~${JSON.stringify(strV)}`;
    case 'domain_is':
      return `${name}@=${fv(v)}`;
    case 'gt':
      return `${name}>${v}`;
    case 'gte':
      return `${name}>=${v}`;
    case 'lt':
      return `${name}<${v}`;
    case 'lte':
      return `${name}<=${v}`;
    case 'in':
      return `${name}:${formatValue(Array.isArray(v) ? v : [v])}`;
    case 'not_in':
      return `-${name}:${formatValue(Array.isArray(v) ? v : [v])}`;
    case 'empty':
      return `${name}=""`;
    case 'not_empty':
      return `${name}!=""`;
    case 'count_gt':
      return `#${name}>${v}`;
    case 'count_lt':
      return `#${name}<${v}`;
    default:
      return `${name}=${fv(v)}`;
  }
}

export function printCondition(schema: WorkflowSchema, r: WorkflowRule): string {
  if (r.all) return r.all.map((x) => (x.any ? `(${printCondition(schema, x)})` : printCondition(schema, x))).join(' AND ');
  if (r.any) return r.any.map((x) => (x.all ? `(${printCondition(schema, x)})` : printCondition(schema, x))).join(' OR ');
  if (r.not) {
    const inner = r.not;
    if (inner.field && !inner.all && !inner.any && !inner.not) {
      // -term only round-trips when the op has no negated form
      const neg = inner.op ? NEGATE[inner.op] : undefined;
      if (!neg) return '-' + termText(schema, inner);
    }
    return `NOT (${printCondition(schema, inner)})`;
  }
  return termText(schema, r);
}

export function printAction(schema: WorkflowSchema, a: WorkflowAction): string {
  const spec = actionSpec(schema, a.type);
  const parts = [a.type];
  const used = new Set<string>();
  const positional = spec?.params.filter((x) => x.positional) ?? [];
  for (const ps of positional) {
    const v = a.params[ps.name];
    if (v === undefined) break;
    used.add(ps.name);
    parts.push(Array.isArray(v) && v.length === 1 ? formatValue(v[0]) : formatValue(v));
  }
  for (const [k, v] of Object.entries(a.params)) {
    if (used.has(k) || v === undefined) continue;
    const ps = spec?.params.find((x) => x.name === k);
    parts.push(`${k}=${(ps?.kind === 'list' || ps?.kind === 'tag') && Array.isArray(v) && v.length === 1 ? formatValue(v[0]) : formatValue(v)}`);
  }
  if (a.if) parts.push(`if=${JSON.stringify(a.if)}`);
  if (a.continueOnError) parts.push('continue_on_error=true');
  return parts.join(' ');
}

export function printRule(schema: WorkflowSchema, w: { trigger: WorkflowTrigger; condition?: WorkflowCondition; actions: WorkflowAction[] }, ctx: WorkflowContext): string {
  let s = `WHEN ${w.trigger.event || 'mail.received'}`;
  const scopes: string[] = [];
  for (const id of w.trigger.accounts ?? []) {
    const a = ctx.accounts.find((x) => x.id === id);
    scopes.push(`account:${a ? formatValue(a.name) : id}`);
  }
  for (const f of w.trigger.folders ?? []) scopes.push(`folder:${formatValue(f)}`);
  for (const c of w.trigger.channels ?? []) scopes.push(`channel:${formatValue(c)}`);
  if (w.trigger.schedule) scopes.push(`schedule:${formatValue(w.trigger.schedule)}`);
  if (w.trigger.name) scopes.push(`name:${formatValue(w.trigger.name)}`);
  if (scopes.length) s += ' IN ' + scopes.join(', ');
  if (w.condition?.expr) s += ` IF EXPR ${JSON.stringify(w.condition.expr)}`;
  else if (w.condition?.rule) s += ' IF ' + printCondition(schema, w.condition.rule);
  s += ' THEN ' + w.actions.map((a) => printAction(schema, a)).join(', ');
  return s;
}
