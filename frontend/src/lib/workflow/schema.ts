// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Default workflow schema (mirrors internal/workflow: events.Type, ruleFields,
// RuleOps, builtinActions). The real core returns it from
// Api.describeWorkflows(); the mock transport and the tests use this copy.

import type { WorkflowActionSpec, WorkflowFieldKind, WorkflowFieldSpec, WorkflowSchema } from '../types';

const MAIL = ['mail.received', 'mail.sent', 'mail.moved', 'mail.flagged', 'noreply.timeout', 'manual'];

const actions: WorkflowActionSpec[] = [
  { type: 'move', group: 'mail', external: false, triggers: MAIL, params: [{ name: 'folder', kind: 'folder', required: true, positional: true }] },
  { type: 'copy', group: 'mail', external: false, triggers: MAIL, params: [{ name: 'folder', kind: 'folder', required: true, positional: true }] },
  { type: 'archive', group: 'mail', external: false, triggers: MAIL, params: [] },
  { type: 'delete', group: 'mail', external: false, triggers: MAIL, params: [{ name: 'permanent', kind: 'bool' }] },
  { type: 'flag', group: 'mail', external: false, triggers: MAIL, params: [{ name: 'add', kind: 'list', positional: true, options: ['seen', 'flagged', 'answered'] }, { name: 'remove', kind: 'list', options: ['seen', 'flagged', 'answered'] }] },
  { type: 'tag', group: 'organize', external: false, triggers: [], params: [{ name: 'tags', kind: 'tag', required: true, positional: true }] },
  { type: 'untag', group: 'organize', external: false, triggers: [], params: [{ name: 'tags', kind: 'tag', required: true, positional: true }] },
  { type: 'add_to_project', group: 'organize', external: false, triggers: [], params: [{ name: 'project', kind: 'project', required: true, positional: true }] },
  { type: 'label', group: 'organize', external: false, triggers: MAIL, params: [{ name: 'add', kind: 'list', positional: true }, { name: 'remove', kind: 'list' }] },
  { type: 'forward', group: 'mail', external: true, triggers: MAIL, params: [{ name: 'to', kind: 'list', required: true, positional: true }] },
  { type: 'reply_template', group: 'mail', external: true, triggers: MAIL, params: [{ name: 'template', kind: 'template', required: true, positional: true }] },
  {
    type: 'notify',
    group: 'notify',
    external: false,
    triggers: [],
    params: [
      { name: 'channel', kind: 'channel', required: true, positional: true, options: ['desktop', 'push', 'signal', 'telegram', 'matrix', 'email'] },
      { name: 'text', kind: 'text', required: true, positional: true },
      { name: 'title', kind: 'text' },
    ],
  },
  { type: 'create_task', group: 'calendar', external: false, triggers: [], params: [{ name: 'title', kind: 'text', required: true, positional: true }, { name: 'due', kind: 'duration' }, { name: 'project', kind: 'project' }] },
  { type: 'create_event', group: 'calendar', external: false, triggers: [], params: [{ name: 'title', kind: 'text', required: true, positional: true }, { name: 'start', kind: 'string' }, { name: 'duration', kind: 'duration' }] },
  { type: 'archive_pdf', group: 'files', external: false, triggers: MAIL, params: [{ name: 'target', kind: 'string', required: true, positional: true, options: ['nextcloud', 'local'] }, { name: 'path', kind: 'path', required: true, positional: true }] },
  { type: 'upload_attachments', group: 'files', external: false, triggers: MAIL, params: [{ name: 'target', kind: 'string', required: true, positional: true, options: ['nextcloud', 'local'] }, { name: 'dir', kind: 'path', positional: true }, { name: 'min_size', kind: 'string' }, { name: 'replace_with_link', kind: 'bool' }, { name: 'share_expiry', kind: 'duration' }] },
  { type: 'webhook', group: 'advanced', external: true, triggers: [], params: [{ name: 'url', kind: 'url', required: true, positional: true }, { name: 'method', kind: 'string', options: ['POST', 'PUT'] }] },
  { type: 'set_var', group: 'advanced', external: false, triggers: [], params: [{ name: 'name', kind: 'string', required: true, positional: true }, { name: 'value', kind: 'text', required: true, positional: true }] },
  { type: 'run_workflow', group: 'advanced', external: false, triggers: [], params: [{ name: 'id', kind: 'string', required: true, positional: true }] },
  { type: 'vacation_reply', group: 'mail', external: true, triggers: ['mail.received'], params: [] },
  { type: 'remind_no_reply', group: 'calendar', external: false, triggers: ['mail.sent', 'manual'], params: [{ name: 'after', kind: 'duration', required: true, positional: true }] },
];

const f = (kind: WorkflowFieldKind, names: string[], aliases: Record<string, string> = {}): WorkflowFieldSpec[] => names.map((name) => ({ name, kind, alias: aliases[name] }));

const fields: WorkflowFieldSpec[] = [
  ...f(
    'string',
    ['mail.from', 'mail.fromName', 'mail.fromDomain', 'mail.subject', 'mail.body', 'mail.account', 'mail.folder', 'mail.category', 'mail.project', 'chat.channel', 'chat.chat', 'chat.sender', 'chat.text', 'calendar.summary', 'calendar.location', 'calendar.organizer', 'file.name', 'file.path', 'contact.displayName', 'time.clock'],
    { 'mail.from': 'from', 'mail.fromName': 'name', 'mail.fromDomain': 'domain', 'mail.subject': 'subject', 'mail.body': 'body', 'mail.account': 'account', 'mail.folder': 'folder', 'mail.category': 'category', 'mail.project': 'project', 'chat.channel': 'channel', 'chat.chat': 'chat', 'chat.sender': 'sender', 'chat.text': 'text', 'file.name': 'filename' },
  ),
  ...f('list', ['mail.to', 'mail.cc', 'mail.recipients', 'mail.attachmentNames', 'mail.flags', 'mail.labels', 'mail.tags', 'calendar.attendees'], { 'mail.to': 'to', 'mail.cc': 'cc', 'mail.attachmentNames': 'attachment', 'mail.labels': 'label', 'mail.tags': 'tag' }),
  ...f('number', ['mail.size', 'mail.attachmentCount', 'mail.maxAttachmentSize', 'file.size', 'time.hour', 'time.weekday', 'noreply.daysWaited'], { 'mail.size': 'size', 'mail.attachmentCount': 'attachments' }),
  ...f('bool', ['mail.hasAttachments', 'mail.isInvoice', 'mail.pgp.encrypted', 'mail.pgp.signed', 'chat.group', 'chat.hasMedia', 'time.weekend', 'time.workday']),
];

export const DEFAULT_SCHEMA: WorkflowSchema = {
  triggers: [
    { event: 'mail.received', group: 'mail', filters: ['accounts', 'folders'] },
    { event: 'mail.sent', group: 'mail', filters: ['accounts'] },
    { event: 'mail.moved', group: 'mail', filters: ['accounts', 'folders'] },
    { event: 'mail.flagged', group: 'mail', filters: ['accounts', 'folders'] },
    { event: 'mail.before_send', group: 'mail', filters: ['accounts'] },
    { event: 'chat.received', group: 'chat', filters: ['accounts', 'channels'] },
    { event: 'calendar.event_starting', group: 'calendar', filters: [] },
    { event: 'calendar.invite_answered', group: 'calendar', filters: [] },
    { event: 'contact.created', group: 'contact', filters: [] },
    { event: 'file.created', group: 'file', filters: ['accounts'] },
    { event: 'schedule.tick', group: 'time', filters: ['schedule'] },
    { event: 'noreply.timeout', group: 'mail', filters: ['accounts'] },
    { event: 'manual', group: 'other', filters: [] },
    { event: 'webhook', group: 'other', filters: ['name'] },
  ],
  fields,
  operators: {
    string: ['eq', 'ne', 'contains', 'not_contains', 'starts_with', 'ends_with', 'matches', 'in', 'not_in', 'empty', 'not_empty', 'domain_is'],
    list: ['contains', 'not_contains', 'eq', 'ne', 'starts_with', 'ends_with', 'matches', 'domain_is', 'empty', 'not_empty', 'count_gt', 'count_lt'],
    number: ['eq', 'ne', 'gt', 'gte', 'lt', 'lte', 'in'],
    bool: ['is_true', 'is_false', 'eq'],
  },
  actions,
  variables: ['absender', 'absender_email', 'betreff', 'datum', 'uhrzeit', 'jahr', 'monat', 'konto', 'ordner', 'chat', 'kanal', 'text', 'projekt', 'empfaenger'],
};

/** Shorthands of the rule syntax: `has:attachment`, `is:invoice` ... -> bool fields. */
export const FLAG_ALIASES: Record<string, Record<string, string>> = {
  has: { attachment: 'mail.hasAttachments', attachments: 'mail.hasAttachments', media: 'chat.hasMedia' },
  is: { invoice: 'mail.isInvoice', encrypted: 'mail.pgp.encrypted', signed: 'mail.pgp.signed', group: 'chat.group', weekend: 'time.weekend', workday: 'time.workday' },
};

/** Operators without a value. */
export const UNARY_OPS = new Set(['is_true', 'is_false', 'empty', 'not_empty']);

export const DYNAMIC_FIELD_PREFIXES = ['mail.headers.', 'vars.', 'data.'];

export function fieldSpec(schema: WorkflowSchema, nameOrAlias: string): WorkflowFieldSpec | undefined {
  const n = nameOrAlias;
  const hit = schema.fields.find((x) => x.name === n) ?? schema.fields.find((x) => x.alias === n.toLowerCase());
  if (hit) return hit;
  if (DYNAMIC_FIELD_PREFIXES.some((p) => n.startsWith(p) && n.length > p.length)) return { name: n, kind: 'string' };
  return undefined;
}

export function actionSpec(schema: WorkflowSchema, type: string): WorkflowActionSpec | undefined {
  return schema.actions.find((a) => a.type === type);
}

export function hasExternalEffects(schema: WorkflowSchema, types: string[]): boolean {
  return types.some((t) => !!actionSpec(schema, t)?.external);
}

/** Names the editors resolve values against (accounts by name, folders, tags ...). */
export interface WorkflowContext {
  accounts: { id: number; name: string }[];
  folders: string[];
  tags: string[];
  projects: string[];
  templates: string[];
}

export const EMPTY_CONTEXT: WorkflowContext = { accounts: [], folders: [], tags: [], projects: [], templates: [] };
