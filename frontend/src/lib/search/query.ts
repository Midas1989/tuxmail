// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Expert search language (KONZEPT 8.4.1), client side:
//  - tokenize(): lossless tokenizer used for syntax highlighting, chips and
//    autocomplete context (every character belongs to exactly one token),
//  - parse(): recursive-descent parser -> AST with positioned errors
//    (precedence NOT > AND > OR, implicit AND, field groups, phrases,
//    ranges, comparisons, wildcards, regex, boosts, fuzzy),
//  - contextAt(): which kind of suggestion fits at a cursor position.
// The Go core implements the same grammar for the real search; this module
// is the UI's view of it and powers the mock transport's evaluator.

export type TokenType =
  | 'ws'
  | 'lparen'
  | 'rparen'
  | 'op' // AND / OR / NOT (incl. UND ODER NICHT && || !)
  | 'mod' // + / - directly before a clause
  | 'field' // "from:" (incl. colon)
  | 'phrase' // "..." with optional ~N, ^N
  | 'range' // [a TO b] / {a TO b}
  | 'regex' // /.../
  | 'term';

export interface Token {
  type: TokenType;
  text: string;
  start: number;
  end: number;
  /** op: normalised operator. */
  op?: 'AND' | 'OR' | 'NOT';
  /** phrase/regex/range not closed. */
  unterminated?: boolean;
  /** value token directly following a field token. */
  field?: string;
}

const OPS: Record<string, 'AND' | 'OR' | 'NOT'> = {
  AND: 'AND',
  UND: 'AND',
  '&&': 'AND',
  OR: 'OR',
  ODER: 'OR',
  '||': 'OR',
  NOT: 'NOT',
  NICHT: 'NOT',
  '!': 'NOT',
};

const FIELD_RE = /^([A-Za-z_][\w.-]*):/;

export function tokenize(text: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  let pendingField: string | undefined;
  const push = (t: Token) => {
    if (pendingField && t.type !== 'ws' && t.type !== 'field') t.field = pendingField;
    if (t.type !== 'field') pendingField = undefined;
    out.push(t);
  };
  while (i < text.length) {
    const c = text[i];
    const start = i;
    if (/\s/.test(c)) {
      while (i < text.length && /\s/.test(text[i])) i++;
      // whitespace ends a field (field: value with a space is not a value)
      out.push({ type: 'ws', text: text.slice(start, i), start, end: i });
      pendingField = undefined;
      continue;
    }
    if (c === '(') {
      // keep pendingField so "from:(a OR b)" marks the group as a field group
      out.push({ type: 'lparen', text: c, start, end: ++i, field: pendingField });
      pendingField = undefined;
      continue;
    }
    if (c === ')') {
      push({ type: 'rparen', text: c, start, end: ++i });
      continue;
    }
    if (c === '"') {
      i++;
      while (i < text.length && text[i] !== '"') i += text[i] === '\\' ? 2 : 1;
      const closed = i < text.length;
      if (closed) i++;
      while (i < text.length && /[~^\d.]/.test(text[i])) i++;
      push({ type: 'phrase', text: text.slice(start, Math.min(i, text.length)), start, end: Math.min(i, text.length), unterminated: !closed });
      continue;
    }
    if ((c === '[' || c === '{') && pendingField) {
      const close = text.slice(i).search(/[\]}]/);
      i = close === -1 ? text.length : i + close + 1;
      push({ type: 'range', text: text.slice(start, i), start, end: i, unterminated: close === -1 });
      continue;
    }
    if (c === '/' && (out.length === 0 || out[out.length - 1].type !== 'term')) {
      const close = text.indexOf('/', i + 1);
      i = close === -1 ? text.length : close + 1;
      push({ type: 'regex', text: text.slice(start, i), start, end: i, unterminated: close === -1 });
      continue;
    }
    if ((c === '+' || c === '-') && !pendingField && i + 1 < text.length && !/[\s)]/.test(text[i + 1])) {
      const prev = out[out.length - 1];
      if (!prev || prev.type === 'ws' || prev.type === 'lparen') {
        push({ type: 'mod', text: c, start, end: ++i });
        continue;
      }
    }
    if (text.startsWith('&&', i) || text.startsWith('||', i)) {
      i += 2;
      push({ type: 'op', text: text.slice(start, i), start, end: i, op: OPS[text.slice(start, i)] });
      continue;
    }
    if (c === '!' && !pendingField) {
      push({ type: 'op', text: c, start, end: ++i, op: 'NOT' });
      continue;
    }
    // word
    while (i < text.length && !/[\s()"]/.test(text[i])) {
      i++;
      // stop after a field colon so the value becomes its own token
      if (text[i - 1] === ':' && FIELD_RE.test(text.slice(start, i)) && !pendingField) break;
    }
    const word = text.slice(start, i);
    const fm = FIELD_RE.exec(word);
    if (fm && fm[0].length === word.length && !pendingField) {
      out.push({ type: 'field', text: word, start, end: i, field: fm[1].toLowerCase() });
      pendingField = fm[1].toLowerCase();
      continue;
    }
    if (!pendingField && OPS[word] && word === word.toUpperCase()) {
      push({ type: 'op', text: word, start, end: i, op: OPS[word] });
      continue;
    }
    push({ type: 'term', text: word, start, end: i });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Parentheses
// ---------------------------------------------------------------------------

export interface ParenInfo {
  /** token index -> matching token index (or -1 when unbalanced). */
  match: Map<number, number>;
  balanced: boolean;
}

export function matchParens(tokens: Token[]): ParenInfo {
  const match = new Map<number, number>();
  const stack: number[] = [];
  tokens.forEach((t, idx) => {
    if (t.type === 'lparen') stack.push(idx);
    else if (t.type === 'rparen') {
      const open = stack.pop();
      if (open === undefined) match.set(idx, -1);
      else {
        match.set(idx, open);
        match.set(open, idx);
      }
    }
  });
  for (const open of stack) match.set(open, -1);
  return { match, balanced: [...match.values()].every((v) => v !== -1) };
}

// ---------------------------------------------------------------------------
// AST + parser
// ---------------------------------------------------------------------------

export type CmpOp = '>' | '>=' | '<' | '<=' | '=';

export type QueryNode =
  | { t: 'and'; children: QueryNode[] }
  | { t: 'or'; children: QueryNode[] }
  | { t: 'not'; child: QueryNode }
  | {
      t: 'term';
      field?: string;
      value: string;
      kind: 'word' | 'phrase' | 'range' | 'regex' | 'cmp';
      op?: CmpOp;
      from?: string;
      to?: string;
      boost?: number;
      fuzzy?: boolean;
      slop?: number;
      pos: number;
    }
  | { t: 'all' };

export type QueryErrorCode =
  | 'missingOperand'
  | 'unexpectedEnd'
  | 'emptyGroup'
  | 'missingClose'
  | 'unexpectedClose'
  | 'unexpectedOperator'
  | 'fieldWithoutValue'
  | 'unterminatedPhrase'
  | 'unterminatedRange'
  | 'unterminatedRegex'
  | 'badRange'
  | 'emptyTerm'
  | 'unexpectedToken';

export interface QueryError {
  /** English fallback text; the UI translates `code` (search.error.<code>). */
  message: string;
  position: number;
  code?: QueryErrorCode;
  /** Offending token text, if any. */
  token?: string;
}

export type ParseResult = { ok: true; ast: QueryNode } | { ok: false; error: QueryError };

const MESSAGES: Record<QueryErrorCode, string> = {
  missingOperand: 'Expression expected after "{token}"',
  unexpectedEnd: 'Unexpected end of query',
  emptyGroup: 'Empty brackets',
  missingClose: 'Closing bracket ")" missing',
  unexpectedClose: 'Unexpected closing bracket ")"',
  unexpectedOperator: 'Operator "{token}" not expected here',
  fieldWithoutValue: 'Field "{token}" has no value',
  unterminatedPhrase: 'Closing quote missing',
  unterminatedRange: 'Closing range bracket "]" missing',
  unterminatedRegex: 'Closing slash "/" missing',
  badRange: 'Range expected: [from TO to]',
  emptyTerm: 'Empty search term',
  unexpectedToken: 'Unexpected "{token}"',
};

class ParseFail extends Error {
  constructor(
    readonly code: QueryErrorCode,
    readonly position: number,
    readonly token?: string,
  ) {
    super(MESSAGES[code].replace('{token}', token ?? ''));
  }
}

export function parse(text: string): ParseResult {
  const toks = tokenize(text).filter((t) => t.type !== 'ws');
  let p = 0;
  const peek = () => toks[p];
  const isPrimaryStart = (t?: Token) => !!t && (t.type === 'lparen' || t.type === 'field' || t.type === 'phrase' || t.type === 'term' || t.type === 'regex' || t.type === 'mod' || (t.type === 'op' && t.op === 'NOT'));

  function parseOr(field?: string): QueryNode {
    const parts = [parseAnd(field)];
    while (peek()?.type === 'op' && peek().op === 'OR') {
      const op = toks[p++];
      if (!isPrimaryStart(peek())) throw new ParseFail('missingOperand', op.end, op.text);
      parts.push(parseAnd(field));
    }
    return parts.length === 1 ? parts[0] : { t: 'or', children: parts };
  }

  function parseAnd(field?: string): QueryNode {
    const parts = [parseNot(field)];
    for (;;) {
      const t = peek();
      if (t?.type === 'op' && t.op === 'AND') {
        p++;
        if (!isPrimaryStart(peek())) throw new ParseFail('missingOperand', t.end, t.text);
        parts.push(parseNot(field));
      } else if (isPrimaryStart(t)) {
        parts.push(parseNot(field)); // implicit AND
      } else break;
    }
    return parts.length === 1 ? parts[0] : { t: 'and', children: parts };
  }

  function parseNot(field?: string): QueryNode {
    const t = peek();
    if (t?.type === 'op' && t.op === 'NOT') {
      p++;
      if (!isPrimaryStart(peek())) throw new ParseFail('missingOperand', t.end, t.text);
      return { t: 'not', child: parseNot(field) };
    }
    if (t?.type === 'mod') {
      p++;
      const inner = parsePrimary(field);
      return t.text === '-' ? { t: 'not', child: inner } : inner;
    }
    return parsePrimary(field);
  }

  function parsePrimary(field?: string): QueryNode {
    const t = peek();
    if (!t) throw new ParseFail('unexpectedEnd', text.length);
    if (t.type === 'lparen') {
      p++;
      if (peek()?.type === 'rparen') throw new ParseFail('emptyGroup', peek().start);
      const inner = parseOr(t.field ?? field);
      if (peek()?.type !== 'rparen') throw new ParseFail('missingClose', peek()?.start ?? text.length);
      p++;
      return inner;
    }
    if (t.type === 'rparen') throw new ParseFail('unexpectedClose', t.start);
    if (t.type === 'op') throw new ParseFail('unexpectedOperator', t.start, t.text);
    if (t.type === 'field') {
      p++;
      const v = peek();
      if (v?.type === 'lparen' && v.start === t.end) return parsePrimary(t.field);
      if (!v || v.start !== t.end || !(v.type === 'term' || v.type === 'phrase' || v.type === 'range' || v.type === 'regex'))
        throw new ParseFail('fieldWithoutValue', t.end, t.text);
      return parseValue(t.field!, v);
    }
    return parseValue(field, t);
  }

  function parseValue(field: string | undefined, t: Token): QueryNode {
    p++;
    if (t.unterminated) {
      throw new ParseFail(t.type === 'phrase' ? 'unterminatedPhrase' : t.type === 'range' ? 'unterminatedRange' : 'unterminatedRegex', t.start);
    }
    if (t.type === 'phrase') {
      const m = /^"((?:[^"\\]|\\.)*)"(?:~(\d+))?(?:\^(\d+(?:\.\d+)?))?$/.exec(t.text);
      return { t: 'term', field, value: (m?.[1] ?? t.text).replace(/\\(.)/g, '$1'), kind: 'phrase', slop: m?.[2] ? Number(m[2]) : undefined, boost: m?.[3] ? Number(m[3]) : undefined, pos: t.start };
    }
    if (t.type === 'range') {
      const m = /^[[{]\s*(\S+)\s+(?:TO|BIS)\s+(\S+)\s*[\]}]$/i.exec(t.text);
      if (!m) throw new ParseFail('badRange', t.start);
      return { t: 'term', field, value: t.text, kind: 'range', from: m[1], to: m[2], pos: t.start };
    }
    if (t.type === 'regex') return { t: 'term', field, value: t.text.slice(1, -1), kind: 'regex', pos: t.start };
    let v = t.text;
    let boost: number | undefined;
    const bm = /\^(\d+(?:\.\d+)?)$/.exec(v);
    if (bm) {
      boost = Number(bm[1]);
      v = v.slice(0, -bm[0].length);
    }
    const cm = /^(>=|<=|>|<|=)(.+)$/.exec(v);
    if (cm && field) return { t: 'term', field, value: cm[2], kind: 'cmp', op: cm[1] as CmpOp, boost, pos: t.start };
    let fuzzy = false;
    if (/[^~]~$/.test(v)) {
      fuzzy = true;
      v = v.slice(0, -1);
    }
    if (!v) throw new ParseFail('emptyTerm', t.start);
    return { t: 'term', field, value: v, kind: 'word', boost, fuzzy, pos: t.start };
  }

  try {
    if (toks.length === 0) return { ok: true, ast: { t: 'all' } };
    const ast = parseOr();
    if (p < toks.length) {
      const t = toks[p];
      throw new ParseFail(t.type === 'rparen' ? 'unexpectedClose' : 'unexpectedToken', t.start, t.text);
    }
    return { ok: true, ast };
  } catch (e) {
    if (e instanceof ParseFail) return { ok: false, error: { message: e.message, position: e.position, code: e.code, token: e.token } };
    throw e;
  }
}

/** Terms that must match (not below a NOT) - used for highlighting. */
export function positiveTerms(node: QueryNode, negated = false): Extract<QueryNode, { t: 'term' }>[] {
  switch (node.t) {
    case 'and':
    case 'or':
      return node.children.flatMap((c) => positiveTerms(c, negated));
    case 'not':
      return positiveTerms(node.child, !negated);
    case 'term':
      return negated ? [] : [node];
    default:
      return [];
  }
}

// ---------------------------------------------------------------------------
// Autocomplete context
// ---------------------------------------------------------------------------

export type ContextKind =
  /** Beginning of a clause: fields, "(", NOT. */
  | 'start'
  /** After a complete clause: AND/OR/NOT, ")" if a group is open. */
  | 'operator'
  /** Directly after "field:": values for that field. */
  | 'value'
  /** Inside a phrase/regex/range: no suggestions. */
  | 'none';

export interface CompletionContext {
  kind: ContextKind;
  field?: string;
  /** Text already typed for the current token (used to filter suggestions). */
  prefix: string;
  /** Range in the query text the accepted suggestion replaces. */
  from: number;
  to: number;
  /** Number of unclosed "(" before the cursor. */
  depth: number;
}

function depthBefore(tokens: Token[], pos: number): number {
  let d = 0;
  for (const t of tokens) {
    if (t.start >= pos) break;
    if (t.type === 'lparen') d++;
    else if (t.type === 'rparen') d = Math.max(0, d - 1);
  }
  return d;
}

const CLAUSE_END: TokenType[] = ['term', 'phrase', 'range', 'regex', 'rparen'];

export function contextAt(text: string, caret: number): CompletionContext {
  const tokens = tokenize(text);
  const depth = depthBefore(tokens, caret);
  // token that contains the caret (caret at its end counts as "inside")
  const cur = tokens.find((t) => t.type !== 'ws' && t.start < caret && caret <= t.end);

  if (cur && (cur.type === 'phrase' || cur.type === 'regex' || cur.type === 'range')) {
    const closedBefore = !cur.unterminated && caret === cur.end;
    if (!closedBefore) return { kind: 'none', prefix: '', from: caret, to: caret, depth };
  }
  if (cur?.type === 'field') {
    return { kind: 'value', field: cur.field, prefix: '', from: cur.end, to: cur.end, depth };
  }
  if (cur && cur.field && (cur.type === 'term')) {
    return { kind: 'value', field: cur.field, prefix: text.slice(cur.start, caret), from: cur.start, to: cur.end, depth };
  }

  // previous significant token before the current word (or the caret)
  const boundary = cur && (cur.type === 'term' || cur.type === 'op') ? cur.start : caret;
  const before = tokens.filter((t) => t.type !== 'ws' && t.end <= boundary);
  const prev = before[before.length - 1];
  const prefix = cur && (cur.type === 'term' || cur.type === 'op') ? text.slice(cur.start, caret) : '';
  const from = cur && (cur.type === 'term' || cur.type === 'op') ? cur.start : caret;
  const to = cur && (cur.type === 'term' || cur.type === 'op') ? cur.end : caret;

  // A complete clause directly before the caret without a space: still editing it.
  if (!cur && prev && prev.end === caret && CLAUSE_END.includes(prev.type)) {
    return { kind: 'operator', prefix: '', from: caret, to: caret, depth };
  }
  if (!prev || prev.type === 'op' || prev.type === 'lparen' || prev.type === 'mod' || prev.type === 'field') {
    return { kind: 'start', prefix, from, to, depth };
  }
  return { kind: 'operator', prefix, from, to, depth };
}

/** Quote a value for insertion if it contains spaces or syntax characters. */
export function quoteValue(v: string): string {
  return /[\s()":]/.test(v) ? `"${v.replace(/[\\"]/g, '\\$&')}"` : v;
}
