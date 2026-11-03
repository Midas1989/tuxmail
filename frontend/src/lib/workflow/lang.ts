// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Workflow source handling shared by the editor and the mock transport:
// YAML <-> Workflow, rule syntax <-> Workflow, validation with line/col
// diagnostics, context-aware completion and lossless-switch checks.
// The Go core implements validate/complete behind Api.validateWorkflow /
// Api.completeWorkflow; semantics should match.

import type { Workflow, WorkflowAction, WorkflowCompletion, WorkflowCompletionItem, WorkflowCondition, WorkflowDiagnostic, WorkflowRule, WorkflowSchema, WorkflowValidation } from '../types';
import { UNARY_OPS, actionSpec, fieldSpec, hasExternalEffects, type WorkflowContext } from './schema';
import { keywordOf, parseRule, printRule, tokenizeRule, variablesIn, type OffsetDiagnostic } from './rule';
import { parseYaml, stringifyYaml, YamlError, type YNode, type YPos } from './yaml';

export function lineColAt(src: string, offset: number): { line: number; col: number } {
  let line = 1;
  let last = 0;
  for (let i = 0; i < Math.min(offset, src.length); i++) {
    if (src[i] === '\n') {
      line++;
      last = i + 1;
    }
  }
  return { line, col: offset - last + 1 };
}

export function offsetAt(src: string, line: number, col: number): number {
  let l = 1;
  let i = 0;
  while (l < line && i < src.length) {
    if (src[i] === '\n') l++;
    i++;
  }
  return i + col - 1;
}

function fromOffsets(src: string, d: OffsetDiagnostic): WorkflowDiagnostic {
  const a = lineColAt(src, d.from);
  const b = lineColAt(src, d.to);
  return { line: a.line, col: a.col, endLine: b.line, endCol: b.col, severity: d.severity, code: d.code, detail: d.detail, message: d.message };
}

// ---------------------------------------------------------------------------
// Workflow -> YAML
// ---------------------------------------------------------------------------

function ruleToJs(r: WorkflowRule): unknown {
  if (r.all) return { all: r.all.map(ruleToJs) };
  if (r.any) return { any: r.any.map(ruleToJs) };
  if (r.not) return { not: ruleToJs(r.not) };
  const o: Record<string, unknown> = { field: r.field, op: r.op };
  if (r.value !== undefined) o.value = r.value;
  return o;
}

export function workflowToYaml(w: Workflow): string {
  const trigger: Record<string, unknown> = { event: w.trigger.event };
  if (w.trigger.accounts?.length) trigger.accounts = w.trigger.accounts;
  if (w.trigger.folders?.length) trigger.folders = w.trigger.folders;
  if (w.trigger.channels?.length) trigger.channels = w.trigger.channels;
  if (w.trigger.schedule) trigger.schedule = w.trigger.schedule;
  if (w.trigger.name) trigger.name = w.trigger.name;
  const doc: Record<string, unknown> = { id: w.id, name: w.name };
  if (w.description) doc.description = w.description;
  doc.enabled = w.enabled;
  doc.trigger = trigger;
  if (w.condition?.expr) doc.condition = w.condition.expr;
  else if (w.condition?.rule) doc.condition = ruleToJs(w.condition.rule);
  doc.actions = w.actions.map((a) => {
    const o: Record<string, unknown> = { type: a.type, ...a.params };
    if (a.if) o.if = a.if;
    if (a.continueOnError) o.continue_on_error = true;
    return o;
  });
  if (w.options && Object.keys(w.options).length) doc.options = w.options;
  return stringifyYaml(doc);
}

// ---------------------------------------------------------------------------
// YAML -> Workflow (+ semantic checks)
// ---------------------------------------------------------------------------

type Diag = (pos: YPos, code: string, message: string, detail?: string, severity?: WorkflowDiagnostic['severity']) => void;

function nodeJs(n: YNode): unknown {
  if (n.kind === 'scalar') return n.value;
  if (n.kind === 'seq') return n.items.map(nodeJs);
  return Object.fromEntries(n.entries.map((e) => [e.key, nodeJs(e.value)]));
}

function str(n: YNode | undefined): string | undefined {
  if (!n || n.kind !== 'scalar' || n.value === null) return undefined;
  return String(n.value);
}

function list(n: YNode | undefined): unknown[] {
  if (!n) return [];
  if (n.kind === 'seq') return n.items.map(nodeJs);
  if (n.kind === 'scalar' && n.value !== null) return [n.value];
  return [];
}

function checkVariables(text: string, pos: YPos, schema: WorkflowSchema, diag: Diag) {
  for (const v of variablesIn(text)) if (!schema.variables.includes(v.name)) diag(pos, 'unknown_variable', `Unknown variable {{${v.name}}}`, v.name, 'warning');
}

function ruleFromNode(n: YNode, schema: WorkflowSchema, diag: Diag): WorkflowRule | undefined {
  if (n.kind !== 'map') {
    diag(n.pos, 'bad_rule', 'A rule must be a mapping (all/any/not or field/op/value)');
    return undefined;
  }
  const get = (k: string) => n.entries.find((e) => e.key === k);
  const all = get('all');
  const any = get('any');
  const not = get('not');
  if (all || any) {
    const e = (all ?? any)!;
    if (e.value.kind !== 'seq') {
      diag(e.keyPos, 'bad_rule', `"${e.key}" needs a list`);
      return undefined;
    }
    const items = e.value.items.map((x) => ruleFromNode(x, schema, diag)).filter((x): x is WorkflowRule => !!x);
    return all ? { all: items } : { any: items };
  }
  if (not) {
    const inner = ruleFromNode(not.value, schema, diag);
    return inner ? { not: inner } : undefined;
  }
  const fieldE = get('field');
  const opE = get('op');
  const valueE = get('value');
  for (const e of n.entries) if (!['field', 'op', 'value'].includes(e.key)) diag(e.keyPos, 'unknown_key', `Unknown key "${e.key}" in rule`, e.key, 'warning');
  const field = str(fieldE?.value);
  const op = str(opE?.value);
  if (!field) {
    diag(n.pos, 'missing_field', 'Rule without "field"');
    return undefined;
  }
  const spec = fieldSpec(schema, field);
  if (!spec) diag(fieldE!.value.pos, 'unknown_field', `Unknown field "${field}"`, field);
  if (!op) diag(fieldE!.keyPos, 'missing_op', 'Rule without "op"', field);
  else if (spec && !schema.operators[spec.kind].includes(op)) diag(opE!.value.pos, 'bad_operator', `Operator "${op}" not allowed for ${spec.kind} field "${field}"`, op);
  if (op && !UNARY_OPS.has(op) && !valueE) diag(opE!.value.pos, 'missing_value', `Operator "${op}" needs a value`, op);
  const r: WorkflowRule = { field, op: op ?? 'eq' };
  if (valueE) r.value = nodeJs(valueE.value) as WorkflowRule['value'];
  return r;
}

export function yamlToWorkflow(src: string, schema: WorkflowSchema, base?: Partial<Workflow>): WorkflowValidation {
  const diagnostics: WorkflowDiagnostic[] = [];
  const diag: Diag = (pos, code, message, detail, severity = 'error') =>
    diagnostics.push({ line: pos.line, col: pos.col, endLine: pos.line, endCol: Math.max(pos.endCol, pos.col + 1), severity, code, detail, message });
  let root: YNode | null;
  try {
    root = parseYaml(src);
  } catch (e) {
    if (e instanceof YamlError) return { diagnostics: [{ line: e.line, col: e.col, endLine: e.line, endCol: e.endCol, severity: 'error', code: 'syntax', message: e.message, detail: e.message }] };
    throw e;
  }
  if (!root || root.kind !== 'map') {
    return { diagnostics: [{ line: 1, col: 1, endLine: 1, endCol: 2, severity: 'error', code: 'not_a_mapping', message: 'A workflow is a mapping with trigger and actions' }] };
  }
  const get = (k: string) => root.entries.find((e) => e.key === k);
  const KNOWN = ['id', 'name', 'description', 'enabled', 'trigger', 'condition', 'actions', 'options'];
  for (const e of root.entries) if (!KNOWN.includes(e.key)) diag(e.keyPos, 'unknown_key', `Unknown key "${e.key}"`, e.key, 'warning');

  const w: Workflow = {
    id: str(get('id')?.value) ?? base?.id ?? '',
    name: str(get('name')?.value) ?? base?.name ?? '',
    description: str(get('description')?.value) ?? undefined,
    enabled: (get('enabled')?.value.kind === 'scalar' ? (get('enabled')!.value as { value: unknown }).value === true : base?.enabled) ?? false,
    trigger: { event: '' },
    actions: [],
    projectId: base?.projectId,
    templateId: base?.templateId,
  };
  if (!get('name')) diag(root.pos, 'missing_name', 'Missing "name"', undefined, 'warning');

  // trigger
  const tr = get('trigger');
  if (!tr) diag(root.pos, 'missing_trigger', 'Missing "trigger"');
  else if (tr.value.kind !== 'map') diag(tr.keyPos, 'bad_trigger', '"trigger" must be a mapping');
  else {
    const tget = (k: string) => (tr.value as YNode & { kind: 'map' }).entries.find((e) => e.key === k);
    for (const e of tr.value.entries) if (!['event', 'accounts', 'folders', 'channels', 'schedule', 'name'].includes(e.key)) diag(e.keyPos, 'unknown_key', `Unknown trigger key "${e.key}"`, e.key, 'warning');
    const ev = tget('event');
    const event = str(ev?.value) ?? '';
    w.trigger.event = event;
    if (!ev) diag(tr.keyPos, 'missing_event', 'trigger.event is required');
    else if (!schema.triggers.some((x) => x.event === event)) diag(ev.value.pos, 'unknown_trigger', `Unknown trigger "${event}"`, event);
    const accounts = list(tget('accounts')?.value).map(Number);
    if (accounts.length) w.trigger.accounts = accounts;
    const folders = list(tget('folders')?.value).map(String);
    if (folders.length) w.trigger.folders = folders;
    const channels = list(tget('channels')?.value).map(String);
    if (channels.length) w.trigger.channels = channels;
    const schedule = str(tget('schedule')?.value);
    if (schedule) w.trigger.schedule = schedule;
    const name = str(tget('name')?.value);
    if (name) w.trigger.name = name;
    if (event === 'schedule.tick' && !schedule) diag(ev!.value.pos, 'missing_schedule', 'schedule.tick needs trigger.schedule');
  }

  // condition
  const c = get('condition');
  if (c && !(c.value.kind === 'scalar' && c.value.value === null)) {
    if (c.value.kind === 'scalar') w.condition = { expr: String(c.value.value) };
    else {
      const r = ruleFromNode(c.value, schema, diag);
      if (r) w.condition = { rule: r };
    }
  }

  // actions
  const acts = get('actions');
  if (!acts) diag(root.pos, 'missing_actions', 'Missing "actions"');
  else if (acts.value.kind !== 'seq') diag(acts.keyPos, 'bad_actions', '"actions" must be a list');
  else {
    if (!acts.value.items.length) diag(acts.keyPos, 'missing_actions', 'At least one action is required');
    for (const item of acts.value.items) {
      if (item.kind !== 'map') {
        diag(item.pos, 'bad_action', 'An action is a mapping with "type"');
        continue;
      }
      const typeE = item.entries.find((e) => e.key === 'type');
      const type = str(typeE?.value) ?? '';
      const a: WorkflowAction = { type, params: {} };
      const spec = actionSpec(schema, type);
      if (!typeE) diag(item.pos, 'missing_type', 'Action without "type"');
      else if (!spec) diag(typeE.value.pos, 'unknown_action', `Unknown action "${type}"`, type);
      else if (spec.triggers.length && w.trigger.event && !spec.triggers.includes(w.trigger.event)) diag(typeE.value.pos, 'action_not_allowed', `"${type}" is not available for trigger ${w.trigger.event}`, type);
      for (const e of item.entries) {
        if (e.key === 'type') continue;
        if (e.key === 'if') a.if = str(e.value);
        else if (e.key === 'continue_on_error') a.continueOnError = e.value.kind === 'scalar' && e.value.value === true;
        else {
          if (spec && !spec.params.some((p) => p.name === e.key)) diag(e.keyPos, 'unknown_param', `Unknown parameter "${e.key}" for ${type}`, e.key, 'warning');
          a.params[e.key] = nodeJs(e.value);
          if (e.value.kind === 'scalar' && typeof e.value.value === 'string') checkVariables(e.value.value, e.value.pos, schema, diag);
        }
      }
      if (spec && typeE) for (const p of spec.params) if (p.required && a.params[p.name] === undefined) diag(typeE.value.pos, 'missing_param', `${type}: missing "${p.name}"`, p.name);
      w.actions.push(a);
    }
  }
  const opt = get('options');
  if (opt && opt.value.kind === 'map') w.options = nodeJs(opt.value) as Record<string, unknown>;
  w.externalEffects = hasExternalEffects(schema, w.actions.map((a) => a.type));
  const ok = !diagnostics.some((d) => d.severity === 'error');
  return { diagnostics, workflow: ok ? w : undefined };
}

// ---------------------------------------------------------------------------
// Rule syntax <-> Workflow
// ---------------------------------------------------------------------------

export function ruleToWorkflow(src: string, schema: WorkflowSchema, ctx: WorkflowContext, base?: Partial<Workflow>): WorkflowValidation {
  const r = parseRule(src, schema, ctx);
  const diagnostics = r.diagnostics.map((d) => fromOffsets(src, d));
  if (diagnostics.some((d) => d.severity === 'error') || !r.trigger) return { diagnostics };
  const w: Workflow = {
    id: base?.id ?? '',
    name: base?.name ?? '',
    description: base?.description,
    enabled: base?.enabled ?? false,
    options: base?.options,
    projectId: base?.projectId,
    templateId: base?.templateId,
    trigger: r.trigger,
    condition: r.condition,
    actions: r.actions,
  };
  w.externalEffects = hasExternalEffects(schema, w.actions.map((a) => a.type));
  return { diagnostics, workflow: w };
}

function norm(w: { trigger: Workflow['trigger']; condition?: WorkflowCondition; actions: WorkflowAction[] }): string {
  const clean = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(clean);
    if (v && typeof v === 'object') {
      const out: Record<string, unknown> = {};
      for (const k of Object.keys(v).sort()) {
        const x = (v as Record<string, unknown>)[k];
        if (x === undefined || (Array.isArray(x) && !x.length && k !== 'params')) continue;
        out[k] = clean(x);
      }
      return out;
    }
    return v;
  };
  return JSON.stringify(clean({ trigger: w.trigger, condition: w.condition ?? null, actions: w.actions.map((a) => ({ ...a, continueOnError: a.continueOnError || undefined })) }));
}

/** Print as rule syntax; `lossless` is false when re-parsing would change the workflow. */
export function workflowToRule(w: Workflow, schema: WorkflowSchema, ctx: WorkflowContext): { src: string; lossless: boolean } {
  const src = printRule(schema, w, ctx);
  const back = ruleToWorkflow(src, schema, ctx, w);
  return { src, lossless: !!back.workflow && norm(back.workflow) === norm(w) };
}

/** YAML keeps everything except editor metadata. */
export function workflowToYamlChecked(w: Workflow, schema: WorkflowSchema): { src: string; lossless: boolean } {
  const src = workflowToYaml(w);
  const back = yamlToWorkflow(src, schema, w);
  return { src, lossless: !!back.workflow && norm(back.workflow) === norm(w) };
}

export function validateSource(src: string, mode: 'rule' | 'yaml', schema: WorkflowSchema, ctx: WorkflowContext, base?: Partial<Workflow>): WorkflowValidation {
  return mode === 'rule' ? ruleToWorkflow(src, schema, ctx, base) : yamlToWorkflow(src, schema, base);
}

// ---------------------------------------------------------------------------
// Completion
// ---------------------------------------------------------------------------

const quoteIfNeeded = (s: string) => (/^[\p{L}\p{N}_.@*/+%-]+$/u.test(s) && !/^\d/.test(s) ? s : JSON.stringify(s));

function variableCompletion(src: string, offset: number, schema: WorkflowSchema): WorkflowCompletion | null {
  const before = src.slice(0, offset);
  const m = /\{\{\s*([\p{L}_]*)$/u.exec(before);
  if (!m) return null;
  const prefix = m[1];
  const from = offset - prefix.length;
  const closing = src.slice(offset).startsWith('}}') ? '' : '}}';
  return { from, to: offset, items: schema.variables.filter((v) => v.startsWith(prefix)).map((v) => ({ label: `{{${v}}}`, insert: v + closing, kind: 'variable' as const })) };
}

function filterItems(items: WorkflowCompletionItem[], prefix: string): WorkflowCompletionItem[] {
  const p = prefix.toLowerCase().replace(/^["']/, '');
  if (!p) return items;
  const starts = items.filter((i) => i.label.toLowerCase().startsWith(p));
  const contains = items.filter((i) => !starts.includes(i) && i.label.toLowerCase().includes(p));
  return [...starts, ...contains];
}

function valueItems(kind: string, ctx: WorkflowContext, schema: WorkflowSchema, options?: string[]): WorkflowCompletionItem[] {
  const mk = (xs: string[], detail?: string) => xs.map((x) => ({ label: x, insert: quoteIfNeeded(x), kind: 'value' as const, detail }));
  if (options?.length) return mk(options);
  switch (kind) {
    case 'folder':
      return mk(ctx.folders);
    case 'tag':
      return mk(ctx.tags);
    case 'project':
      return mk(ctx.projects);
    case 'template':
      return mk(ctx.templates);
    case 'account':
      return mk(ctx.accounts.map((a) => a.name));
    case 'channel':
      return mk(['email', 'whatsapp', 'signal', 'telegram']);
    case 'bool':
      return mk(['true', 'false']);
    case 'duration':
      return mk(['1h', '1d', '3d', '7d']);
    default:
      void schema;
      return [];
  }
}

function completeRule(src: string, offset: number, schema: WorkflowSchema, ctx: WorkflowContext): WorkflowCompletion {
  const v = variableCompletion(src, offset, schema);
  if (v) return v;
  const toks = tokenizeRule(src).filter((t) => t.type !== 'ws' && t.start < offset);
  // the word being typed
  const last = toks[toks.length - 1];
  const typing = last && last.end >= offset && (last.type === 'word' || last.type === 'string') ? last : undefined;
  const prefix = typing ? src.slice(typing.start, offset) : '';
  const from = typing ? typing.start : offset;
  const prev = typing ? toks.slice(0, -1) : toks;
  const done = (items: WorkflowCompletionItem[]): WorkflowCompletion => ({ from, to: typing ? Math.max(offset, typing.end) : offset, items: filterItems(items, prefix).slice(0, 40) });

  const kwIndex = (k: string) => {
    for (let i = prev.length - 1; i >= 0; i--) if (prev[i].type === 'word' && keywordOf(prev[i].text) === k) return i;
    return -1;
  };
  const kw = (label: string, detail?: string): WorkflowCompletionItem => ({ label, insert: label + ' ', kind: 'keyword', detail });
  const iWhen = kwIndex('WHEN');
  const iIn = kwIndex('IN');
  const iIf = kwIndex('IF');
  const iThen = kwIndex('THEN');
  const p1 = prev[prev.length - 1];
  const p2 = prev[prev.length - 2];

  if (iWhen < 0) return done([kw('WHEN')]);
  const section = Math.max(iWhen, iIn, iIf, iThen);
  if (section === iWhen) {
    if (prev.length - 1 === iWhen) return done(schema.triggers.map((t) => ({ label: t.event, insert: t.event + ' ', kind: 'trigger', detail: t.group })));
    return done([kw('IN'), kw('IF'), kw('THEN')]);
  }
  if (section === iIn) {
    if (p1?.type === 'op' && p2) {
      const key = p2.text.toLowerCase();
      return done(valueItems(key === 'account' ? 'account' : key === 'channel' ? 'channel' : 'folder', ctx, schema).map((i) => ({ ...i, insert: i.insert + ' ' })));
    }
    if (prev.length - 1 === iIn || p1?.type === 'comma') return done(['account:', 'folder:', 'channel:', 'schedule:', 'name:'].map((s) => ({ label: s, insert: s, kind: 'scope' as const })));
    return done([kw('IF'), kw('THEN'), { label: ',', insert: ', ', kind: 'keyword' }]);
  }
  if (section === iIf) {
    if (p1?.type === 'op' && p2?.type === 'word') {
      const f = p2.text.toLowerCase();
      if (f === 'has') return done(['attachment', 'media'].map((x) => ({ label: x, insert: x + ' ', kind: 'value' })));
      if (f === 'is') return done(['invoice', 'encrypted', 'signed', 'group', 'weekend', 'workday'].map((x) => ({ label: x, insert: x + ' ', kind: 'value' })));
      const spec = fieldSpec(schema, p2.text);
      const kind = spec?.name === 'mail.folder' ? 'folder' : spec?.name === 'mail.tags' ? 'tag' : spec?.name === 'mail.project' ? 'project' : spec?.name === 'mail.account' ? 'account' : spec?.name === 'chat.channel' ? 'channel' : spec?.kind === 'bool' ? 'bool' : '';
      return done(valueItems(kind, ctx, schema).map((i) => ({ ...i, insert: i.insert + ' ' })));
    }
    const afterTerm = p1 && (p1.role === 'value' || p1.type === 'rparen' || (p1.type === 'word' && prev.length - 1 !== iIf && !keywordOf(p1.text) && p1.type === 'word'));
    const fieldItems: WorkflowCompletionItem[] = [
      ...schema.fields.filter((x) => x.alias).map((x) => ({ label: x.alias! + (x.kind === 'bool' ? '' : ':'), insert: x.alias! + (x.kind === 'bool' ? ' ' : ':'), kind: 'field' as const, detail: x.name })),
      { label: 'has:', insert: 'has:', kind: 'field', detail: 'attachment, media' },
      { label: 'is:', insert: 'is:', kind: 'field', detail: 'invoice, encrypted …' },
      ...schema.fields.map((x) => ({ label: x.name, insert: x.name + (x.kind === 'bool' ? ' ' : x.kind === 'number' ? '>' : ':'), kind: 'field' as const, detail: x.kind })),
      kw('NOT'),
      { label: '(', insert: '(', kind: 'operator' },
      kw('EXPR', 'expr-lang'),
    ];
    if (afterTerm && !typing) return done([kw('AND'), kw('OR'), kw('THEN')]);
    if (afterTerm && typing && /^[A-Za-z]+$/.test(prefix) && prev.length - 1 !== iIf) return done([kw('AND'), kw('OR'), kw('THEN'), ...fieldItems]);
    return done(fieldItems);
  }
  // THEN section: actions and their arguments
  let i = prev.length - 1;
  while (i > iThen && prev[i].type !== 'comma') i--;
  const actionTok = prev[i + 1];
  if (!actionTok || i + 1 > prev.length - 1) return done(schema.actions.filter((a) => !a.triggers.length || a.triggers.includes(prev[iWhen + 1]?.text ?? '')).map((a) => ({ label: a.type, insert: a.type + ' ', kind: 'action', detail: a.group + (a.external ? ' · external' : '') })));
  const spec = actionSpec(schema, actionTok.text);
  if (!spec) return done([]);
  if (p1?.type === 'op' && p1.text === '=' && p2) {
    const ps = spec.params.find((x) => x.name === p2.text);
    return done(ps ? valueItems(ps.kind, ctx, schema, ps.options).map((it) => ({ ...it, insert: it.insert + ' ' })) : []);
  }
  const after = prev.slice(i + 2);
  let posCount = 0;
  for (let k = 0; k < after.length; k++) {
    const t = after[k];
    if (t.type === 'word' && after[k + 1]?.type === 'op' && after[k + 1].text === '=') {
      k += 2;
      continue;
    }
    if (t.type === 'lbracket') {
      while (k < after.length && after[k].type !== 'rbracket') k++;
      posCount++;
      continue;
    }
    if (t.type === 'word' || t.type === 'string') {
      while (after[k + 1] && after[k + 1].start === after[k].end && (after[k + 1].type === 'op' || after[k + 1].type === 'word')) k++;
      posCount++;
    }
  }
  const nextPos = spec.params.filter((x) => x.positional)[posCount];
  const items: WorkflowCompletionItem[] = [];
  if (nextPos) items.push(...valueItems(nextPos.kind, ctx, schema, nextPos.options).map((it) => ({ ...it, insert: it.insert + ' ', detail: nextPos.name })));
  for (const ps of spec.params) if (!ps.positional) items.push({ label: ps.name + '=', insert: ps.name + '=', kind: 'param', detail: ps.kind });
  items.push({ label: ',', insert: ', ', kind: 'keyword', detail: 'next action' });
  return done(items);
}

function completeYaml(src: string, offset: number, schema: WorkflowSchema, ctx: WorkflowContext): WorkflowCompletion {
  const v = variableCompletion(src, offset, schema);
  if (v) return v;
  const lineStart = src.lastIndexOf('\n', offset - 1) + 1;
  const lineText = src.slice(lineStart, offset);
  const indent = lineText.length - lineText.trimStart().length;
  const trimmed = lineText.trimStart();
  const inItem = trimmed.startsWith('- ');
  const body = inItem ? trimmed.slice(2) : trimmed;
  const bodyCol = lineStart + indent + (inItem ? 2 : 0);
  // parents: walk previous lines with smaller indent
  const parents: string[] = [];
  let curIndent = indent + (inItem ? 2 : 0);
  const prevLines = src.slice(0, lineStart).split('\n').reverse();
  let actionType: string | undefined;
  let sawItemStart = inItem;
  for (const l of prevLines) {
    if (!l.trim() || l.trim().startsWith('#')) continue;
    const ind = l.length - l.trimStart().length;
    const t = l.trimStart();
    const isItem = t.startsWith('- ');
    const content = isItem ? t.slice(2) : t;
    const contentInd = ind + (isItem ? 2 : 0);
    if (!sawItemStart && contentInd === curIndent) {
      const tm = /^type:\s*(\S+)/.exec(content);
      if (tm) actionType = tm[1];
      if (isItem) {
        sawItemStart = true;
        curIndent = ind;
        continue;
      }
    }
    if (ind < curIndent) {
      const m = /^([\w.-]+):/.exec(content);
      if (m) parents.unshift(m[1]);
      curIndent = ind;
      if (ind === 0) break;
    }
  }
  // also look ahead in the current item for "type:"
  if (!actionType) {
    const rest = src.slice(offset).split('\n').slice(1);
    for (const l of rest) {
      const ind = l.length - l.trimStart().length;
      if (!l.trim()) continue;
      if (ind < indent + (inItem ? 2 : 0) || l.trimStart().startsWith('- ')) break;
      const tm = /^\s*type:\s*(\S+)/.exec(l);
      if (tm) actionType = tm[1];
    }
  }
  const keyMatch = /^([\w.-]+):\s*(.*)$/.exec(body);
  const keyItem = (k: string, detail?: string): WorkflowCompletionItem => ({ label: k, insert: k + ': ', kind: 'param', detail });
  if (!keyMatch) {
    const prefix = body;
    const from = bodyCol;
    let items: WorkflowCompletionItem[] = [];
    const parent = parents[parents.length - 1];
    if (!parents.length) items = ['id', 'name', 'description', 'enabled', 'trigger', 'condition', 'actions', 'options'].map((k) => keyItem(k));
    else if (parent === 'trigger') items = ['event', 'accounts', 'folders', 'channels', 'schedule', 'name'].map((k) => keyItem(k));
    else if (parent === 'actions') {
      if (!actionType) items = [keyItem('type')];
      else {
        const spec = actionSpec(schema, actionType);
        items = [...(spec?.params.map((p) => keyItem(p.name, p.kind + (p.required ? ' *' : ''))) ?? []), keyItem('if'), keyItem('continue_on_error')];
      }
    } else if (parent === 'condition' || parent === 'all' || parent === 'any' || parent === 'not') items = ['field', 'op', 'value', 'all', 'any', 'not'].map((k) => keyItem(k));
    return { from, to: offset, items: filterItems(items, prefix) };
  }
  const key = keyMatch[1];
  const valuePrefix = keyMatch[2];
  const from = offset - valuePrefix.length;
  const parent = parents[parents.length - 1];
  let items: WorkflowCompletionItem[] = [];
  if (key === 'event') items = schema.triggers.map((t) => ({ label: t.event, insert: t.event, kind: 'trigger', detail: t.group }));
  else if (key === 'type' && parent === 'actions') items = schema.actions.map((a) => ({ label: a.type, insert: a.type, kind: 'action', detail: a.group + (a.external ? ' · external' : '') }));
  else if (key === 'field') items = schema.fields.map((f) => ({ label: f.name, insert: f.name, kind: 'field', detail: f.kind }));
  else if (key === 'op') {
    const fieldLine = /field:\s*(\S+)/.exec(src.slice(Math.max(0, lineStart - 200), lineStart).split('\n').reverse().find((l) => /field:/.test(l)) ?? '');
    const spec = fieldLine ? fieldSpec(schema, fieldLine[1]) : undefined;
    items = (spec ? schema.operators[spec.kind] : [...new Set(Object.values(schema.operators).flat())]).map((o) => ({ label: o, insert: o, kind: 'operator' }));
  } else if (key === 'enabled' || key === 'continue_on_error') items = valueItems('bool', ctx, schema);
  else if (parent === 'actions' && actionType) {
    const ps = actionSpec(schema, actionType)?.params.find((p) => p.name === key);
    if (ps) items = valueItems(ps.kind, ctx, schema, ps.options);
  } else if (key === 'channels') items = valueItems('channel', ctx, schema);
  else if (key === 'folders') items = valueItems('folder', ctx, schema);
  return { from, to: offset, items: filterItems(items, valuePrefix) };
}

export function completeSource(src: string, offset: number, mode: 'rule' | 'yaml', schema: WorkflowSchema, ctx: WorkflowContext): WorkflowCompletion {
  return mode === 'rule' ? completeRule(src, offset, schema, ctx) : completeYaml(src, offset, schema, ctx);
}

// ---------------------------------------------------------------------------
// YAML highlighting (editor)
// ---------------------------------------------------------------------------

export interface HlSeg {
  text: string;
  cls?: 'key' | 'str' | 'num' | 'bool' | 'comment' | 'var' | 'punct' | 'kw';
}

export function highlightYaml(src: string): HlSeg[][] {
  return src.split('\n').map((line) => {
    const segs: HlSeg[] = [];
    let rest = line;
    const ind = /^(\s*)(- )?/.exec(rest)!;
    if (ind[0]) {
      segs.push({ text: ind[1] });
      if (ind[2]) segs.push({ text: ind[2], cls: 'punct' });
      rest = rest.slice(ind[0].length);
    }
    const km = /^([\w.-]+)(:)(\s|$)/.exec(rest);
    if (km) {
      segs.push({ text: km[1], cls: km[1] === 'type' || km[1] === 'event' ? 'kw' : 'key' });
      segs.push({ text: km[2], cls: 'punct' });
      rest = rest.slice(km[1].length + 1);
    }
    // value part with comments and {{vars}}
    const re = /("(?:[^"\\]|\\.)*"?|'[^']*'?|#.*$|\{\{[^}]*\}\}|\b(?:true|false|null)\b|-?\b\d+(?:\.\d+)?\b|[[\]{},])/g;
    let last = 0;
    let m: RegExpExecArray | null;
    while ((m = re.exec(rest))) {
      if (m[0].startsWith('#') && m.index > 0 && !/\s/.test(rest[m.index - 1])) continue;
      if (m.index > last) segs.push({ text: rest.slice(last, m.index) });
      const tk = m[0];
      if (tk.startsWith('"') || tk.startsWith("'")) {
        // highlight vars inside strings
        const parts = tk.split(/(\{\{[^}]*\}\})/);
        for (const p of parts) if (p) segs.push({ text: p, cls: p.startsWith('{{') ? 'var' : 'str' });
      } else segs.push({ text: tk, cls: tk.startsWith('#') ? 'comment' : tk.startsWith('{{') ? 'var' : /^(true|false|null)$/.test(tk) ? 'bool' : /^-?\d/.test(tk) ? 'num' : 'punct' });
      last = m.index + tk.length;
    }
    if (last < rest.length) segs.push({ text: rest.slice(last) });
    return segs;
  });
}

