// SPDX-License-Identifier: AGPL-3.0-or-later
// Sieve (RFC 5228) tokenizer for syntax highlighting and a quick local lint
// in the server rules editor. The server's CHECKSCRIPT stays authoritative.

export type SieveTokenKind = 'comment' | 'string' | 'number' | 'tag' | 'keyword' | 'identifier' | 'symbol' | 'space' | 'error';

export interface SieveToken {
  kind: SieveTokenKind;
  text: string;
  line: number;
}

const KEYWORDS = new Set([
  'require', 'if', 'elsif', 'else', 'stop', 'keep', 'discard', 'fileinto', 'redirect', 'reject', 'ereject', 'vacation',
  'addflag', 'setflag', 'removeflag', 'include', 'return', 'set', 'allof', 'anyof', 'not', 'true', 'false',
  'header', 'address', 'envelope', 'exists', 'size', 'body', 'date', 'currentdate', 'hasflag',
]);

/** Splits a script into tokens; the concatenated texts equal the input. */
export function tokenizeSieve(src: string): SieveToken[] {
  const out: SieveToken[] = [];
  let i = 0;
  let line = 1;
  const push = (kind: SieveTokenKind, text: string) => {
    out.push({ kind, text, line });
    line += (text.match(/\n/g) ?? []).length;
  };
  while (i < src.length) {
    const c = src[i];
    const rest = src.slice(i);
    let m: RegExpMatchArray | null;
    if ((m = /^\s+/.exec(rest))) {
      push('space', m[0]);
    } else if (c === '#') {
      const e = src.indexOf('\n', i);
      push('comment', e < 0 ? rest : src.slice(i, e));
    } else if (rest.startsWith('/*')) {
      const e = src.indexOf('*/', i + 2);
      push(e < 0 ? 'error' : 'comment', e < 0 ? rest : src.slice(i, e + 2));
    } else if (c === '"') {
      let j = i + 1;
      while (j < src.length && src[j] !== '"') j += src[j] === '\\' ? 2 : 1;
      push(j >= src.length ? 'error' : 'string', src.slice(i, j + 1));
    } else if (/^text:/.test(rest)) {
      m = /^text:[^\n]*\n(?:.*\n)*?\.\r?\n/.exec(rest);
      push(m ? 'string' : 'error', m ? m[0] : rest);
    } else if ((m = /^\d+[KMGkmg]?/.exec(rest))) {
      push('number', m[0]);
    } else if ((m = /^:[A-Za-z_][A-Za-z0-9_]*/.exec(rest))) {
      push('tag', m[0]);
    } else if ((m = /^[A-Za-z_][A-Za-z0-9_.]*/.exec(rest))) {
      push(KEYWORDS.has(m[0].toLowerCase()) ? 'keyword' : 'identifier', m[0]);
    } else if (';,{}[]()'.includes(c)) {
      push('symbol', c);
    } else {
      push('error', c);
    }
    i += out[out.length - 1].text.length;
  }
  return out;
}

function esc(s: string): string {
  return s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!);
}

/** HTML with <span class="sv-…"> per token (for an overlay editor). */
export function highlightSieve(src: string): string {
  return tokenizeSieve(src)
    .map((t) => (t.kind === 'space' ? esc(t.text) : `<span class="sv-${t.kind}">${esc(t.text)}</span>`))
    .join('');
}

export interface SieveLint {
  error?: string;
  line?: number;
}

/** Local checks: unterminated strings/comments, unbalanced brackets. */
export function lintSieve(src: string): SieveLint {
  const stack: { c: string; line: number }[] = [];
  const pairs: Record<string, string> = { '}': '{', ']': '[', ')': '(' };
  for (const t of tokenizeSieve(src)) {
    if (t.kind === 'error') return { error: `unexpected or unterminated ${JSON.stringify(t.text.slice(0, 12))}`, line: t.line };
    if (t.kind !== 'symbol') continue;
    if ('{[('.includes(t.text)) stack.push({ c: t.text, line: t.line });
    else if (pairs[t.text]) {
      const top = stack.pop();
      if (!top || top.c !== pairs[t.text]) return { error: `unbalanced '${t.text}'`, line: t.line };
    }
  }
  const open = stack.pop();
  if (open) return { error: `'${open.c}' is not closed`, line: open.line };
  return {};
}
