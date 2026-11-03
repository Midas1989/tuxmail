// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Minimal YAML subset for workflow files, with source positions for
// diagnostics: block mappings and sequences, "- key: value" items, flow
// sequences/mappings ([a, b], {field: x, op: eq}), plain/quoted scalars,
// block scalars (| |- > >-) and comments. Enough for the workflow format the
// core reads (internal/workflow uses gopkg.in/yaml.v3); anything else is
// reported as a syntax error instead of being guessed.

export interface YPos {
  /** 1-based. */
  line: number;
  col: number;
  endCol: number;
}

export type YNode =
  | { kind: 'map'; pos: YPos; entries: { key: string; keyPos: YPos; value: YNode }[] }
  | { kind: 'seq'; pos: YPos; items: YNode[] }
  | { kind: 'scalar'; pos: YPos; value: string | number | boolean | null; quoted: boolean };

export class YamlError extends Error {
  constructor(
    message: string,
    readonly line: number,
    readonly col: number,
    readonly endCol: number = col + 1,
  ) {
    super(message);
  }
}

interface Line {
  no: number;
  indent: number;
  text: string;
  raw: string;
}

/** Strip a trailing comment that is not inside quotes. */
function stripComment(s: string): string {
  let q: string | null = null;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (q) {
      if (c === '\\' && q === '"') i++;
      else if (c === q) q = null;
    } else if (c === '"' || c === "'") q = c;
    else if (c === '#' && (i === 0 || /\s/.test(s[i - 1]))) return s.slice(0, i).replace(/\s+$/, '');
  }
  return s.replace(/\s+$/, '');
}

function scalarValue(raw: string, line: number, col: number): { value: string | number | boolean | null; quoted: boolean } {
  const s = raw.trim();
  if (s.startsWith('"')) {
    if (!s.endsWith('"') || s.length < 2) throw new YamlError('Unterminated string', line, col, col + raw.length);
    try {
      return { value: JSON.parse(s) as string, quoted: true };
    } catch {
      throw new YamlError('Invalid escape in string', line, col, col + raw.length);
    }
  }
  if (s.startsWith("'")) {
    if (!s.endsWith("'") || s.length < 2) throw new YamlError('Unterminated string', line, col, col + raw.length);
    return { value: s.slice(1, -1).replace(/''/g, "'"), quoted: true };
  }
  if (s === '' || s === '~' || s === 'null') return { value: null, quoted: false };
  if (s === 'true' || s === 'false') return { value: s === 'true', quoted: false };
  if (/^-?\d+(\.\d+)?$/.test(s)) return { value: Number(s), quoted: false };
  return { value: s, quoted: false };
}

/** Parse a flow value ([..], {..} or scalar) starting at text[i]. */
function parseFlow(text: string, lineNo: number, colBase: number): YNode {
  let i = 0;
  const ws = () => {
    while (i < text.length && /\s/.test(text[i])) i++;
  };
  const pos = (s: number, e: number): YPos => ({ line: lineNo, col: colBase + s, endCol: colBase + e });
  function value(stops: string): YNode {
    ws();
    const s = i;
    if (text[i] === '[') {
      i++;
      const items: YNode[] = [];
      ws();
      if (text[i] === ']') {
        i++;
        return { kind: 'seq', pos: pos(s, i), items };
      }
      for (;;) {
        items.push(value(',]'));
        ws();
        if (text[i] === ',') {
          i++;
          continue;
        }
        if (text[i] === ']') {
          i++;
          break;
        }
        throw new YamlError('Expected "," or "]"', lineNo, colBase + i);
      }
      return { kind: 'seq', pos: pos(s, i), items };
    }
    if (text[i] === '{') {
      i++;
      const entries: { key: string; keyPos: YPos; value: YNode }[] = [];
      ws();
      if (text[i] === '}') {
        i++;
        return { kind: 'map', pos: pos(s, i), entries };
      }
      for (;;) {
        ws();
        const ks = i;
        while (i < text.length && text[i] !== ':' && text[i] !== '}' && text[i] !== ',') i++;
        const key = text.slice(ks, i).trim();
        if (text[i] !== ':') throw new YamlError('Expected ":" in flow mapping', lineNo, colBase + i);
        i++;
        const v = value(',}');
        entries.push({ key, keyPos: pos(ks, ks + key.length), value: v });
        ws();
        if (text[i] === ',') {
          i++;
          continue;
        }
        if (text[i] === '}') {
          i++;
          break;
        }
        throw new YamlError('Expected "," or "}"', lineNo, colBase + i);
      }
      return { kind: 'map', pos: pos(s, i), entries };
    }
    if (text[i] === '"' || text[i] === "'") {
      const q = text[i];
      i++;
      while (i < text.length && text[i] !== q) {
        if (text[i] === '\\' && q === '"') i++;
        i++;
      }
      if (i >= text.length) throw new YamlError('Unterminated string', lineNo, colBase + s, colBase + text.length);
      i++;
      const r = scalarValue(text.slice(s, i), lineNo, colBase + s);
      return { kind: 'scalar', pos: pos(s, i), ...r };
    }
    while (i < text.length && !stops.includes(text[i])) i++;
    const raw = text.slice(s, i).trimEnd();
    const r = scalarValue(raw, lineNo, colBase + s);
    return { kind: 'scalar', pos: pos(s, s + raw.length), ...r };
  }
  const node = value('');
  ws();
  if (i < text.length) throw new YamlError(`Unexpected "${text.slice(i)}"`, lineNo, colBase + i, colBase + text.length);
  return node;
}

const KEY_RE = /^([A-Za-z_][\w.-]*|"[^"]*"|'[^']*')\s*:(\s|$)/;

export function parseYaml(src: string): YNode | null {
  const rawLines = src.split('\n');
  const lines: Line[] = [];
  rawLines.forEach((raw, idx) => {
    const tab = raw.match(/^\s*\t/);
    if (tab) throw new YamlError('Tabs are not allowed for indentation', idx + 1, raw.indexOf('\t') + 1);
    const text = stripComment(raw);
    if (!text.trim()) return;
    if (text.trim() === '---') return;
    const indent = text.length - text.trimStart().length;
    lines.push({ no: idx + 1, indent, text: text.trim(), raw });
  });
  let li = 0;

  function parseNode(): YNode {
    const l = lines[li];
    if (l.text === '-' || l.text.startsWith('- ')) return parseSeq(l.indent);
    if (KEY_RE.test(l.text)) return parseMap(l.indent);
    li++;
    return parseFlow(l.text, l.no, l.indent + 1);
  }

  function blockScalar(header: string, parentIndent: number, startLine: number): YNode {
    const keep = header.includes('|');
    const strip = header.includes('-');
    const out: string[] = [];
    let blockIndent = -1;
    let lastNo = startLine;
    // consume raw lines after startLine with indent > parentIndent (blank lines allowed)
    let r = startLine; // 1-based number of header line; raw index = r
    while (r < rawLines.length) {
      const raw = rawLines[r];
      const ind = raw.length - raw.trimStart().length;
      if (raw.trim() && ind <= parentIndent) break;
      if (raw.trim() && blockIndent < 0) blockIndent = ind;
      out.push(raw.trim() ? raw.slice(blockIndent) : '');
      lastNo = r + 1;
      r++;
    }
    while (li < lines.length && lines[li].no <= lastNo) li++;
    while (out.length && out[out.length - 1] === '') out.pop();
    let value = keep ? out.join('\n') : out.reduce((acc, s, i) => (i === 0 ? s : s === '' ? acc + '\n' : acc.endsWith('\n') ? acc + s : acc + ' ' + s), '');
    if (!strip) value += '\n';
    return { kind: 'scalar', pos: { line: startLine, col: parentIndent + 1, endCol: parentIndent + 2 }, value, quoted: true };
  }

  function parseMap(indent: number): YNode {
    const first = lines[li];
    const node: YNode & { kind: 'map' } = { kind: 'map', pos: { line: first.no, col: indent + 1, endCol: indent + 2 }, entries: [] };
    while (li < lines.length && lines[li].indent === indent) {
      const l = lines[li];
      const m = KEY_RE.exec(l.text);
      if (!m) {
        if (l.text.startsWith('- ') || l.text === '-') throw new YamlError('Unexpected list item in mapping', l.no, indent + 1);
        throw new YamlError('Expected "key: value"', l.no, indent + 1, indent + 1 + l.text.length);
      }
      let key = m[1];
      if (/^["']/.test(key)) key = key.slice(1, -1);
      const keyPos: YPos = { line: l.no, col: indent + 1, endCol: indent + 1 + m[1].length };
      if (node.entries.some((e) => e.key === key)) throw new YamlError(`Duplicate key "${key}"`, l.no, keyPos.col, keyPos.endCol);
      const restStart = m[0].length;
      const rest = l.text.slice(restStart).trim();
      const restCol = indent + 1 + l.text.indexOf(rest, restStart);
      li++;
      let value: YNode;
      if (/^[|>][+-]?$/.test(rest)) value = blockScalar(rest, indent, l.no);
      else if (rest) value = parseFlow(rest, l.no, restCol);
      else if (li < lines.length && lines[li].indent > indent) value = parseNode();
      else if (li < lines.length && lines[li].indent === indent && (lines[li].text.startsWith('- ') || lines[li].text === '-')) value = parseSeq(indent);
      else value = { kind: 'scalar', pos: { line: l.no, col: keyPos.endCol + 1, endCol: keyPos.endCol + 2 }, value: null, quoted: false };
      node.entries.push({ key, keyPos, value });
    }
    if (li < lines.length && lines[li].indent > indent) {
      const l = lines[li];
      throw new YamlError('Bad indentation', l.no, l.indent + 1, l.indent + 1 + l.text.length);
    }
    return node;
  }

  function parseSeq(indent: number): YNode {
    const first = lines[li];
    const node: YNode & { kind: 'seq' } = { kind: 'seq', pos: { line: first.no, col: indent + 1, endCol: indent + 2 }, items: [] };
    while (li < lines.length && lines[li].indent === indent && (lines[li].text === '-' || lines[li].text.startsWith('- '))) {
      const l = lines[li];
      const rest = l.text === '-' ? '' : l.text.slice(2).trimStart();
      const restIndent = indent + (l.text.length - rest.length);
      if (!rest) {
        li++;
        if (li < lines.length && lines[li].indent > indent) node.items.push(parseNode());
        else node.items.push({ kind: 'scalar', pos: { line: l.no, col: indent + 1, endCol: indent + 2 }, value: null, quoted: false });
        continue;
      }
      if (KEY_RE.test(rest)) {
        // "- key: value" starts a mapping indented at the item content
        lines[li] = { ...l, indent: restIndent, text: rest };
        node.items.push(parseMap(restIndent));
        continue;
      }
      li++;
      node.items.push(parseFlow(rest, l.no, restIndent + 1));
    }
    return node;
  }

  if (!lines.length) return null;
  const root = parseNode();
  if (li < lines.length) {
    const l = lines[li];
    throw new YamlError('Unexpected content (check the indentation)', l.no, l.indent + 1, l.indent + 1 + l.text.length);
  }
  return root;
}

/** Plain JS value of a node. */
export function toJs(n: YNode | null): unknown {
  if (!n) return null;
  if (n.kind === 'scalar') return n.value;
  if (n.kind === 'seq') return n.items.map(toJs);
  return Object.fromEntries(n.entries.map((e) => [e.key, toJs(e.value)]));
}

// ---------------------------------------------------------------------------
// Stringify
// ---------------------------------------------------------------------------

const PLAIN_RE = /^[\p{L}\p{N}_./+(][\p{L}\p{N}_ ./@*+(),!?-]*$/u;

export function yamlScalar(v: unknown): string {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  const s = String(v);
  if (s === '') return '""';
  if (PLAIN_RE.test(s) && !/^(true|false|null|~|-?\d+(\.\d+)?)$/.test(s) && !s.endsWith(' ') && !s.includes(': ') && !s.includes(' #') && !s.startsWith('- ')) return s;
  return JSON.stringify(s);
}

function isFlowable(v: unknown): boolean {
  return Array.isArray(v) && v.every((x) => x === null || typeof x !== 'object') && JSON.stringify(v).length < 70;
}

export function stringifyYaml(v: unknown, indent = 0): string {
  const pad = ' '.repeat(indent);
  if (Array.isArray(v)) {
    if (!v.length) return pad + '[]\n';
    return v
      .map((item) => {
        if (item && typeof item === 'object' && !Array.isArray(item)) {
          const body = stringifyYaml(item, indent + 2);
          return pad + '- ' + body.slice(indent + 2);
        }
        if (Array.isArray(item)) return pad + '-\n' + stringifyYaml(item, indent + 2);
        return pad + '- ' + yamlScalar(item) + '\n';
      })
      .join('');
  }
  if (v && typeof v === 'object') {
    let out = '';
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
      if (val === undefined) continue;
      if (typeof val === 'string' && val.includes('\n')) {
        out += `${pad}${k}: |-\n` + val.split('\n').map((l) => (l ? pad + '  ' + l : '')).join('\n') + '\n';
      } else if (Array.isArray(val)) {
        out += isFlowable(val) ? `${pad}${k}: [${val.map(yamlScalar).join(', ')}]\n` : val.length ? `${pad}${k}:\n${stringifyYaml(val, indent + 2)}` : `${pad}${k}: []\n`;
      } else if (val && typeof val === 'object') {
        const keys = Object.keys(val);
        out += keys.length ? `${pad}${k}:\n${stringifyYaml(val, indent + 2)}` : `${pad}${k}: {}\n`;
      } else out += `${pad}${k}: ${yamlScalar(val)}\n`;
    }
    return out;
  }
  return pad + yamlScalar(v) + '\n';
}
