// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { DEFAULT_SCHEMA as S, type WorkflowContext } from './schema';
import { parseRule, printRule, tokenizeRule } from './rule';
import { completeSource, ruleToWorkflow, validateSource, workflowToRule, workflowToYaml, workflowToYamlChecked, yamlToWorkflow } from './lang';
import { parseYaml, toJs } from './yaml';

const ctx: WorkflowContext = {
  accounts: [
    { id: 1, name: 'Privat' },
    { id: 2, name: 'Firma' },
  ],
  folders: ['INBOX', 'Kunden/Eingang', 'Archiv'],
  tags: ['Kunde', 'Kunde/Müller'],
  projects: ['Hausbau 2026'],
  templates: ['Danke'],
};

const EXAMPLE = 'WHEN mail.received IN account:"Firma" IF from:*@kunde.de AND has:attachment THEN tag "Kunde", move "Kunden/Eingang", notify signal "Neue Kundenmail: {{betreff}}"';

describe('rule syntax', () => {
  it('parses the reference example', () => {
    const r = parseRule(EXAMPLE, S, ctx);
    expect(r.diagnostics).toEqual([]);
    expect(r.trigger).toEqual({ event: 'mail.received', accounts: [2] });
    expect(r.condition).toEqual({
      rule: {
        all: [
          { field: 'mail.from', op: 'ends_with', value: '@kunde.de' },
          { field: 'mail.hasAttachments', op: 'is_true' },
        ],
      },
    });
    expect(r.actions).toEqual([
      { type: 'tag', params: { tags: ['Kunde'] } },
      { type: 'move', params: { folder: 'Kunden/Eingang' } },
      { type: 'notify', params: { channel: 'signal', text: 'Neue Kundenmail: {{betreff}}' } },
    ]);
    const roles = r.tokens.filter((t) => t.type !== 'ws').map((t) => t.role);
    expect(roles.slice(0, 6)).toEqual(['kw', 'trigger', 'kw', 'scope', 'op', 'value']);
  });

  it('round-trips through the printer', () => {
    const r = ruleToWorkflow(EXAMPLE, S, ctx, { id: 'x', name: 'X', enabled: false });
    expect(r.workflow).toBeTruthy();
    const back = workflowToRule(r.workflow!, S, ctx);
    expect(back.lossless).toBe(true);
    expect(back.src).toBe('WHEN mail.received IN account:Firma IF from:*@kunde.de AND has:attachment THEN tag Kunde, move Kunden/Eingang, notify signal "Neue Kundenmail: {{betreff}}"');
  });

  it('handles OR groups, negation, numbers and named params', () => {
    const src = 'wenn mail.received falls (subject:Rechnung OR is:invoice) UND -from:newsletter UND size>5MB dann flag flagged, notify desktop "Groß" title="Achtung" if="vars.x > 1"';
    const r = parseRule(src, S, ctx);
    expect(r.diagnostics).toEqual([]);
    expect(r.condition).toEqual({
      rule: {
        all: [
          { any: [{ field: 'mail.subject', op: 'contains', value: 'Rechnung' }, { field: 'mail.isInvoice', op: 'is_true' }] },
          { field: 'mail.from', op: 'not_contains', value: 'newsletter' },
          { field: 'mail.size', op: 'gt', value: 5 * 1024 * 1024 },
        ],
      },
    });
    expect(r.actions[1]).toEqual({ type: 'notify', params: { channel: 'desktop', text: 'Groß', title: 'Achtung' }, if: 'vars.x > 1' });
    const w = ruleToWorkflow(src, S, ctx).workflow!;
    expect(workflowToRule(w, S, ctx).lossless).toBe(true);
  });

  it('reports diagnostics with positions', () => {
    const src = 'WHEN mail.recieved IF fromm:x THEN mvoe Archiv';
    const v = validateSource(src, 'rule', S, ctx);
    expect(v.workflow).toBeUndefined();
    const codes = v.diagnostics.map((d) => [d.code, d.col]);
    expect(codes).toEqual([
      ['unknown_trigger', 6],
      ['unknown_field', 23],
      ['unknown_action', 36],
    ]);
    expect(validateSource('WHEN mail.received THEN move', 'rule', S, ctx).diagnostics[0]).toMatchObject({ code: 'missing_param', detail: 'folder' });
    expect(validateSource('WHEN mail.received THEN', 'rule', S, ctx).diagnostics[0].code).toBe('expected_action');
    expect(validateSource('WHEN chat.received THEN move X', 'rule', S, ctx).diagnostics[0].code).toBe('action_not_allowed');
    expect(validateSource('WHEN mail.received IN account:Nope THEN archive', 'rule', S, ctx).diagnostics[0]).toMatchObject({ code: 'unknown_account', detail: 'Nope' });
  });

  it('keeps URLs and channel targets as one value', () => {
    const r = parseRule('WHEN manual THEN webhook https://hooks.example.org/x?y=1, notify signal:me "hi"', S, ctx);
    expect(r.diagnostics).toEqual([]);
    expect(r.actions[0].params.url).toBe('https://hooks.example.org/x?y=1');
    expect(r.actions[1].params.channel).toBe('signal:me');
  });

  it('supports EXPR conditions', () => {
    const r = parseRule('WHEN mail.received IF EXPR "mail.isInvoice || mail.size > 10" THEN archive', S, ctx);
    expect(r.condition).toEqual({ expr: 'mail.isInvoice || mail.size > 10' });
  });

  it('tokenizes strings, minus and ops', () => {
    expect(tokenizeRule('-a:"b c"').map((t) => t.type)).toEqual(['minus', 'word', 'op', 'string']);
  });
});

const YAML = `id: vip-mail-signal
name: VIP-Mails per Signal melden
enabled: false
trigger:
  event: mail.received
  folders: [INBOX]
condition:
  all:
    - {field: mail.fromDomain, op: eq, value: vip-kunde.example}
    - any:
        - field: mail.subject
          op: contains
          value: Dringend
        - {field: mail.isInvoice, op: is_true}
actions:
  - type: notify
    channel: signal
    title: "VIP-Mail von {{absender}}"
    text: |-
      {{absender}}: {{betreff}}
      Bitte prüfen.
options:
  rate_limit:
    max: 20
    per: 1h
`;

describe('YAML', () => {
  it('parses the subset', () => {
    const js = toJs(parseYaml(YAML)) as Record<string, unknown>;
    expect(js.trigger).toEqual({ event: 'mail.received', folders: ['INBOX'] });
    expect((js.actions as Record<string, unknown>[])[0].text).toBe('{{absender}}: {{betreff}}\nBitte prüfen.');
    expect(js.options).toEqual({ rate_limit: { max: 20, per: '1h' } });
  });

  it('converts to a workflow and back losslessly', () => {
    const v = yamlToWorkflow(YAML, S);
    expect(v.diagnostics).toEqual([]);
    const w = v.workflow!;
    expect(w.condition?.rule?.all?.[1].any?.[0]).toEqual({ field: 'mail.subject', op: 'contains', value: 'Dringend' });
    expect(workflowToYamlChecked(w, S).lossless).toBe(true);
    const y = workflowToYaml(w);
    expect(y).toContain('  folders: [INBOX]');
    expect(y).toContain('    text: |-');
    // and into the rule syntax
    const r = workflowToRule(w, S, ctx);
    expect(r.src).toContain('IF domain=vip-kunde.example AND (subject:Dringend OR is:invoice)');
    expect(r.lossless).toBe(true);
  });

  it('reports YAML syntax and semantic errors with line/col', () => {
    const bad = 'name: x\ntrigger:\n  event: mail.received\nactions:\n  - type: move\n    folderr: Archiv\n';
    const v = yamlToWorkflow(bad, S);
    expect(v.diagnostics.map((d) => [d.code, d.line])).toEqual([
      ['unknown_param', 6],
      ['missing_param', 5],
    ]);
    const syntax = yamlToWorkflow('name: "x\ntrigger: {}', S);
    expect(syntax.diagnostics[0]).toMatchObject({ code: 'syntax', line: 1 });
    expect(yamlToWorkflow('trigger:\n  event: mail.received\n   bad: 1\nactions: []', S).diagnostics[0].code).toBe('syntax');
  });
});

describe('completion', () => {
  const c = (src: string, mode: 'rule' | 'yaml' = 'rule', offset = src.length) => completeSource(src, offset, mode, S, ctx);
  it('rule mode: keywords, triggers, scopes, fields, actions, params, variables', () => {
    expect(c('').items[0].label).toBe('WHEN');
    expect(c('WHEN ').items.map((i) => i.label)).toContain('mail.received');
    expect(c('WHEN mail.received ').items.map((i) => i.label)).toEqual(['IN', 'IF', 'THEN']);
    expect(c('WHEN mail.received IN ').items[0].label).toBe('account:');
    expect(c('WHEN mail.received IN account:').items.map((i) => i.label)).toEqual(['Privat', 'Firma']);
    expect(c('WHEN mail.received IF fr').items[0].label).toBe('from:');
    expect(c('WHEN mail.received IF has:').items.map((i) => i.label)).toEqual(['attachment', 'media']);
    expect(c('WHEN mail.received IF from:x ').items.map((i) => i.label)).toEqual(['AND', 'OR', 'THEN']);
    expect(c('WHEN mail.received THEN mo').items[0].label).toBe('move');
    expect(c('WHEN mail.received THEN move ').items.map((i) => i.label)).toContain('Kunden/Eingang');
    expect(c('WHEN mail.received THEN notify ').items.map((i) => i.label)).toContain('signal');
    expect(c('WHEN mail.received THEN notify signal "Hi {{bet').items).toEqual([{ label: '{{betreff}}', insert: 'betreff}}', kind: 'variable' }]);
    const r = c('WHEN mail.received THEN mo');
    expect([r.from, r.to]).toEqual([24, 26]);
  });
  it('yaml mode: keys and values by context', () => {
    expect(c('na', 'yaml').items[0].label).toBe('name');
    expect(c('trigger:\n  event: mail.', 'yaml').items.map((i) => i.label)).toContain('mail.received');
    expect(c('trigger:\n  ev', 'yaml').items[0].label).toBe('event');
    expect(c('actions:\n  - type: ', 'yaml').items.map((i) => i.label)).toContain('move');
    expect(c('actions:\n  - type: move\n    ', 'yaml').items.map((i) => i.label)).toContain('folder');
    expect(c('actions:\n  - type: move\n    folder: ', 'yaml').items.map((i) => i.label)).toContain('Archiv');
  });
});

describe('printRule', () => {
  it('prints empty trigger defaults', () => {
    expect(printRule(S, { trigger: { event: 'manual' }, actions: [{ type: 'archive', params: {} }] }, ctx)).toBe('WHEN manual THEN archive');
  });
});
