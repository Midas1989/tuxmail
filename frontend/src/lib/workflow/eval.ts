// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Evaluate a structured workflow rule against field values (dry runs in the
// mock transport and the visual builder's live preview). The core compiles
// rules to expr-lang expressions; the semantics below match workflow.Rule.

import type { WorkflowRule } from '../types';

const lc = (v: unknown) => String(v ?? '').toLocaleLowerCase('de');

function cmpString(op: string, have: string, want: unknown): boolean {
  const h = lc(have);
  const w = lc(want);
  switch (op) {
    case 'eq':
      return h === w;
    case 'ne':
      return h !== w;
    case 'contains':
      return h.includes(w);
    case 'not_contains':
      return !h.includes(w);
    case 'starts_with':
      return h.startsWith(w);
    case 'ends_with':
      return h.endsWith(w);
    case 'matches':
      try {
        return new RegExp(String(want), 'i').test(have);
      } catch {
        return false;
      }
    case 'domain_is':
      return h.split('@').pop() === w.replace(/^@/, '');
    case 'in':
      return (Array.isArray(want) ? want : [want]).some((x) => lc(x) === h);
    case 'not_in':
      return !(Array.isArray(want) ? want : [want]).some((x) => lc(x) === h);
    case 'empty':
      return h === '';
    case 'not_empty':
      return h !== '';
    default:
      return false;
  }
}

export function evalRule(r: WorkflowRule, get: (field: string) => unknown): boolean {
  if (r.all) return r.all.every((x) => evalRule(x, get));
  if (r.any) return r.any.some((x) => evalRule(x, get));
  if (r.not) return !evalRule(r.not, get);
  const v = get(r.field ?? '');
  const op = r.op ?? 'eq';
  if (op === 'is_true') return v === true;
  if (op === 'is_false') return v !== true;
  if (Array.isArray(v)) {
    if (op === 'empty') return v.length === 0;
    if (op === 'not_empty') return v.length > 0;
    if (op === 'count_gt') return v.length > Number(r.value);
    if (op === 'count_lt') return v.length < Number(r.value);
    if (op === 'not_contains') return !v.some((x) => cmpString('contains', String(x), r.value));
    if (op === 'ne') return !v.some((x) => cmpString('eq', String(x), r.value));
    return v.some((x) => cmpString(op, String(x), r.value));
  }
  if (typeof v === 'number') {
    const n = Number(r.value);
    switch (op) {
      case 'eq':
        return v === n;
      case 'ne':
        return v !== n;
      case 'gt':
        return v > n;
      case 'gte':
        return v >= n;
      case 'lt':
        return v < n;
      case 'lte':
        return v <= n;
      case 'in':
        return (Array.isArray(r.value) ? r.value : [r.value]).map(Number).includes(v);
    }
    return false;
  }
  if (typeof v === 'boolean') return op === 'eq' ? v === (r.value === true || r.value === 'true') : false;
  return cmpString(op, String(v ?? ''), r.value);
}

/** Fill {{variables}} for previews. */
export function fillTemplate(text: string, vars: Record<string, string>): string {
  return text.replace(/\{\{\s*([\p{L}_][\p{L}\p{N}_.]*)\s*\}\}/gu, (m, k: string) => vars[k] ?? m);
}
