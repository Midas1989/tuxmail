// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Mock implementation of tags, projects, search folders and workflows. Kept
// apart from mock.ts; MockTransport delegates to it. Like the rest of the
// mock it documents the semantics the Go core implements.

import { ApiError } from './api';
import type {
  Account,
  Folder,
  ItemRef,
  MessageDetail,
  Person,
  Project,
  ProjectInput,
  ProjectStatus,
  ProjectTemplate,
  ProjectTimelineEntry,
  SearchFolder,
  SearchFolderInput,
  Tag,
  TagCount,
  TagInput,
  Workflow,
  WorkflowCompletion,
  WorkflowDryRunResult,
  WorkflowPlannedAction,
  WorkflowRun,
  WorkflowTemplate,
  WorkflowValidation,
} from '../types';
import { evaluate, type SearchDoc } from '../search/engine';
import { parse } from '../search/query';
import { DEFAULT_SCHEMA, hasExternalEffects, actionSpec, type WorkflowContext } from '../workflow/schema';
import { completeSource, validateSource } from '../workflow/lang';
import { evalRule, fillTemplate } from '../workflow/eval';

const SEEN = '\\Seen';

export interface OrgChatMsg {
  id: number;
  chatId: number;
  from: number;
  sentAt: string;
  text: string;
  seen: boolean;
  attachments: { filename: string; size: number }[];
}

export interface OrgHost {
  messages(): Map<number, MessageDetail>;
  chatMsgs(): OrgChatMsg[];
  chatTitle(chatId: number): string;
  chatChannel(chatId: number): 'whatsapp' | 'signal' | 'telegram';
  /** Conversation id of a chat for the default grouping. */
  chatConversation(chatId: number): number;
  threadOf(messageId: number): number;
  folders(): Folder[];
  accounts(): Account[];
  persons(): Person[];
  docs(): SearchDoc[];
  isHidden(m: MessageDetail): boolean;
  notify(kind: 'tags' | 'project' | 'searchfolders', id?: number): void;
  now(): Date;
}

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const key = (r: ItemRef) => `${r.kind}:${r.id}`;

function rel(now: Date, days: number, hm = '09:00'): string {
  const d = new Date(now);
  d.setDate(d.getDate() + days);
  const [h, m] = hm.split(':').map(Number);
  d.setHours(h, m, 0, 0);
  return d.toISOString();
}
const dateOnly = (iso: string) => iso.slice(0, 10);

interface TaskRec {
  id: number;
  projectId: number;
  title: string;
  done: boolean;
  due?: string;
  created: string;
}
interface EventRec {
  id: number;
  projectId: number;
  title: string;
  start: string;
  end: string;
  location?: string;
}
interface FileRec {
  id: number;
  projectId: number;
  name: string;
  path: string;
  size: number;
  date: string;
  personId?: number;
}

export class MockOrganizer {
  private tags: Tag[];
  private assign = new Map<string, Set<number>>();
  private projects: Project[] = [];
  private links = new Map<number, { item: ItemRef; source: 'manual' | 'rule' | 'workflow' }[]>();
  private tasks: TaskRec[] = [];
  private events: EventRec[] = [];
  private files: FileRec[] = [];
  private searchFolders: Omit<SearchFolder, 'total' | 'unread'>[];
  private workflows: Workflow[] = [];
  private runs = new Map<string, WorkflowRun[]>();
  private seq = 100;

  constructor(private host: OrgHost) {
    const now = host.now();
    this.tags = [
      { id: 1, name: 'Kunde', path: 'Kunde', color: '#2563eb', icon: 'briefcase', sortOrder: 0 },
      { id: 2, parentId: 1, name: 'Müller', path: 'Kunde/Müller', color: '#0ea5e9', sortOrder: 0 },
      { id: 3, parentId: 1, name: 'Schneider', path: 'Kunde/Schneider', color: '#7c3aed', sortOrder: 1 },
      { id: 4, name: 'Privat', path: 'Privat', color: '#16a34a', icon: 'house', sortOrder: 1 },
      { id: 5, parentId: 4, name: 'Familie', path: 'Privat/Familie', color: '#22c55e', icon: 'heart', sortOrder: 0 },
      { id: 6, name: 'Wichtig', path: 'Wichtig', color: '#dc2626', icon: 'flame', sortOrder: 2 },
      { id: 7, name: 'Rechnung', path: 'Rechnung', color: '#d97706', icon: 'receipt', sortOrder: 3 },
      { id: 8, name: 'Hausbau', path: 'Hausbau', color: '#a16207', icon: 'hammer', sortOrder: 4 },
    ];
    const tagMsgs = (tagId: number, ids: number[]) => ids.forEach((id) => this.addTag({ kind: 'message', id }, tagId));
    tagMsgs(2, [1, 2, 3]);
    tagMsgs(6, [3, 9]);
    tagMsgs(3, [4, 5, 6, 7, 8]);
    tagMsgs(7, [13, 29, 30]);
    tagMsgs(8, [20, 21, 22, 23, 24]);
    tagMsgs(4, [25, 26, 27, 28]);
    this.addTag({ kind: 'chat', id: 102 }, 5);
    this.addTag({ kind: 'chat', id: 103 }, 5);
    this.addTag({ kind: 'chat', id: 101 }, 2);

    // ---- projects ----------------------------------------------------------
    const mk = (p: Omit<Project, 'progress' | 'counts' | 'createdAt' | 'updatedAt'>): void => {
      this.projects.push({ ...p, progress: 0, counts: { mail: 0, chat: 0, event: 0, task: 0, file: 0 }, createdAt: rel(now, -60), updatedAt: rel(now, -1) });
    };
    mk({
      id: 1,
      key: 'HB-26',
      name: 'Hausbau 2026',
      status: 'active',
      color: '#a16207',
      description: 'Neubau Einfamilienhaus in Oberhaching – Rohbau bis Jahresende, Bezug Sommer 2027.',
      customer: 'Wagner Bau GmbH',
      customerPersonId: 8,
      startDate: dateOnly(rel(now, -120)),
      dueDate: dateOnly(rel(now, 45)),
      budget: { amount: 385000, currency: 'EUR', spent: 142300 },
      customFields: [
        { key: 'permit', label: 'Bauantrag-Nr.', type: 'text', value: 'BA-2026-0417' },
        { key: 'plot', label: 'Grundstück', type: 'text', value: 'Flurstück 112/4, Oberhaching' },
        { key: 'architect', label: 'Architekt', type: 'text', value: 'Büro Lindner' },
        { key: 'phase', label: 'Bauphase', type: 'select', value: 'Rohbau', options: ['Planung', 'Rohbau', 'Innenausbau', 'Abnahme'] },
      ],
      memberPersonIds: [8, 4],
      cloudFolder: { provider: 'nextcloud', path: '/Projekte/Hausbau 2026', url: 'https://cloud.example.org/apps/files/?dir=/Projekte/Hausbau%202026' },
      notes: 'Bodenplatte nach Frostfreigabe gießen. Fliesenauswahl bis Ende des Monats an Wagner Bau melden.',
      tagIds: [8],
      rules: { enabled: true, query: 'in:Hausbau OR subject:Bodenplatte' },
      templateId: 'construction',
    });
    mk({
      id: 2,
      key: 'MUE-Q4',
      name: 'Angebot Q4 – Müller GmbH',
      status: 'active',
      color: '#0ea5e9',
      description: 'Rahmenvertrag und Angebot für das vierte Quartal.',
      customer: 'Müller GmbH',
      customerPersonId: 2,
      startDate: dateOnly(rel(now, -10)),
      dueDate: dateOnly(rel(now, 9)),
      budget: { amount: 48000, currency: 'EUR', spent: 6200 },
      customFields: [
        { key: 'offer', label: 'Angebotsnummer', type: 'text', value: 'A-2026-118' },
        { key: 'customerNo', label: 'Kundennummer', type: 'text', value: 'K-1042' },
      ],
      memberPersonIds: [2, 3],
      notes: '',
      tagIds: [2],
      rules: { enabled: true, query: 'from:mueller-gmbh.de OR to:mueller-gmbh.de' },
      templateId: 'client',
    });
    mk({
      id: 3,
      key: 'SK-117',
      name: 'Schneider ./. Krause',
      status: 'active',
      color: '#7c3aed',
      description: 'Klageverfahren vor dem LG Hamburg, Fristverlängerung beantragt.',
      customer: 'Schneider KG',
      customerPersonId: 6,
      startDate: dateOnly(rel(now, -40)),
      dueDate: dateOnly(rel(now, 21)),
      customFields: [
        { key: 'file', label: 'Aktenzeichen', type: 'text', value: '117/26' },
        { key: 'court', label: 'Gericht', type: 'text', value: 'LG Hamburg' },
        { key: 'opponent', label: 'Gegner', type: 'text', value: 'Krause GmbH' },
      ],
      memberPersonIds: [6, 3],
      notes: 'Mandantin wünscht Rückruf vor Einreichung.',
      tagIds: [3],
      rules: { enabled: false, query: 'subject:"Schneider ./. Krause"' },
      templateId: 'legal',
    });
    mk({ id: 4, key: 'VF-26', name: 'Vereinsfest', status: 'done', color: '#16a34a', description: 'Grillfest des Sportvereins.', customFields: [], memberPersonIds: [5, 7], notes: '', tagIds: [4], rules: { enabled: false, query: '' } });
    mk({ id: 5, key: 'WEB', name: 'Website-Relaunch', status: 'planned', color: '#db2777', description: 'Neue Kanzlei-Website mit Terminbuchung.', startDate: dateOnly(rel(now, 30)), customFields: [], memberPersonIds: [12], notes: '', tagIds: [], rules: { enabled: false, query: '' } });

    const link = (pid: number, kind: ItemRef['kind'], ids: number[], source: 'manual' | 'rule' | 'workflow' = 'manual') => {
      const l = this.links.get(pid) ?? [];
      for (const id of ids) l.push({ item: { kind, id }, source });
      this.links.set(pid, l);
    };
    link(1, 'message', [20, 22], 'rule');
    link(1, 'message', [21, 23, 24]);
    link(2, 'message', [1, 2, 3]);
    link(2, 'chat_message', host.chatMsgs().filter((c) => c.chatId === 101).map((c) => c.id));
    link(3, 'message', [4, 5, 6, 7, 8]);
    link(4, 'message', [25, 26, 27, 28]);
    let tid = 1;
    const task = (projectId: number, title: string, done: boolean, dueDays?: number) => {
      this.tasks.push({ id: tid++, projectId, title, done, due: dueDays != null ? rel(now, dueDays, '17:00') : undefined, created: rel(now, -30 + tid) });
    };
    task(1, 'Fliesen aussuchen', true, -3);
    task(1, 'Bauzeitenplan aktualisieren', true, -8);
    task(1, 'KfW-Förderantrag einreichen', false, 10);
    task(1, 'Angebote Elektriker vergleichen', false, 3);
    task(2, 'Kalkulation prüfen', true, -2);
    task(2, 'Angebot finalisieren und senden', false, 2);
    task(3, 'Schriftsatz entwerfen', false, 7);
    task(3, 'Mandantin anrufen', true, -1);
    task(4, 'Grill organisieren', true, -6);
    let eid = 1;
    const ev = (projectId: number, title: string, days: number, from: string, to: string, location?: string) => {
      this.events.push({ id: eid++, projectId, title, start: rel(now, days, from), end: rel(now, days, to), location });
    };
    ev(1, 'Baubesprechung mit Wagner Bau', 2, '16:00', '17:00', 'Baustelle Oberhaching');
    ev(1, 'Bodenplatte gießen', 5, '07:00', '12:00', 'Baustelle Oberhaching');
    ev(1, 'Notartermin Grundschuld', -20, '10:30', '11:30', 'Notariat Dr. Huber');
    ev(2, 'Call mit Anna Müller', 1, '10:00', '10:30');
    ev(3, 'Fristablauf Klageerwiderung', 21, '00:00', '23:59', 'LG Hamburg');
    let fid = 1;
    const file = (projectId: number, name: string, size: number, days: number, personId?: number) => {
      const dir = this.projects.find((p) => p.id === projectId)?.cloudFolder?.path ?? '/Projekte';
      this.files.push({ id: fid++, projectId, name, path: `${dir}/${name}`, size, date: rel(now, days, '12:00'), personId });
    };
    file(1, 'Grundriss_EG.pdf', 1_240_000, -40, 8);
    file(1, 'Statik_Bodenplatte.pdf', 3_820_000, -9, 8);
    file(1, 'Angebot_Wagner_Bau.pdf', 412_000, -60, 8);
    file(1, 'Baustelle_2026-11-01.jpg', 2_150_000, -2);
    file(2, 'Angebot_Q4_Entwurf.docx', 88_000, -3);

    // ---- search folders ------------------------------------------------------
    this.searchFolders = [
      { id: 1, name: 'Rechnungen', query: 'subject:Rechnung OR tag:Rechnung', useAsChatFolder: false, icon: 'receipt' },
      { id: 2, name: 'Ungelesen & wichtig', query: 'is:unread AND (is:flagged OR tag:Wichtig)', useAsChatFolder: true },
      { id: 3, name: 'Kundenmails mit Anhang', query: 'tag:Kunde AND has:attachment', accountId: 2, useAsChatFolder: false },
      { id: 4, name: 'Bodenplatte', query: 'project:HB-26 AND (subject:Bodenplatte OR body:Bodenplatte)', projectId: 1, useAsChatFolder: false },
    ];

    // ---- workflows -------------------------------------------------------------
    for (const t of this.templates()) if (['invoices-to-nextcloud', 'newsletter-auto-archive', 'vip-mail-signal', 'waiting-for-reply'].includes(t.id)) this.workflows.push({ ...clone(t.workflow), templateId: t.id });
    this.workflows.find((w) => w.id === 'invoices-to-nextcloud')!.enabled = true;
    this.workflows.find((w) => w.id === 'newsletter-auto-archive')!.enabled = true;
    this.workflows.unshift({
      id: 'kundenmails-mueller',
      name: 'Kundenmails Müller GmbH einsortieren',
      description: 'Mails von Müller GmbH mit Anhang taggen, einsortieren und per Signal melden.',
      enabled: true,
      projectId: 2,
      trigger: { event: 'mail.received', accounts: [2] },
      condition: { rule: { all: [{ field: 'mail.from', op: 'ends_with', value: '@mueller-gmbh.de' }, { field: 'mail.hasAttachments', op: 'is_true' }] } },
      actions: [
        { type: 'tag', params: { tags: ['Kunde/Müller'] } },
        { type: 'move', params: { folder: 'Mandanten/Müller GmbH' } },
        { type: 'notify', params: { channel: 'signal', text: 'Neue Kundenmail: {{betreff}}' } },
      ],
    });
    this.workflows.push({
      id: 'hausbau-projekt',
      name: 'Hausbau-Mails dem Projekt zuordnen',
      enabled: true,
      projectId: 1,
      trigger: { event: 'mail.received', accounts: [1] },
      condition: { rule: { any: [{ field: 'mail.fromDomain', op: 'eq', value: 'wagner-bau.de' }, { field: 'mail.subject', op: 'contains', value: 'Bodenplatte' }] } },
      actions: [
        { type: 'add_to_project', params: { project: 'Hausbau 2026' } },
        { type: 'tag', params: { tags: ['Hausbau'] } },
      ],
    });
    for (const w of this.workflows) {
      w.externalEffects = hasExternalEffects(DEFAULT_SCHEMA, w.actions.map((a) => a.type));
      w.updatedAt = rel(now, -3);
      if (!w.enabled) continue;
      const runs: WorkflowRun[] = [];
      const mails = [...host.messages().values()].filter((m) => !host.isHidden(m)).sort((a, b) => b.date.localeCompare(a.date));
      for (let i = 0; i < 4; i++) {
        const m = mails[(i * 3 + w.id.length) % mails.length];
        const failed = w.id === 'invoices-to-nextcloud' && i === 0;
        const started = new Date(now.getTime() - (i * 7 + 2) * 3600_000).toISOString();
        runs.push({
          id: `r-${w.id}-${i}`,
          workflowId: w.id,
          startedAt: started,
          finishedAt: new Date(Date.parse(started) + 840).toISOString(),
          status: failed ? 'failed' : i === 3 ? 'skipped' : 'ok',
          message: failed ? 'Nextcloud nicht erreichbar (HTTP 503) – wird erneut versucht' : i === 3 ? 'Bedingung nicht erfüllt' : undefined,
          trigger: w.trigger.event,
          item: { kind: 'message', id: m.id, title: m.subject },
          steps: i === 3 ? [] : w.actions.map((a, k) => ({ action: a.type, status: failed && k === 0 ? 'failed' : failed ? 'skipped' : 'ok', summary: this.planned(a, m).summary, durationMs: 40 + k * 90 })),
        });
      }
      this.runs.set(w.id, runs);
      w.lastRun = { id: runs[0].id, startedAt: runs[0].startedAt, status: runs[0].status, message: runs[0].message };
    }
  }

  // -------------------------------------------------------------------------
  // Tags
  // -------------------------------------------------------------------------

  private addTag(item: ItemRef, tagId: number) {
    const k = key(item);
    const s = this.assign.get(k) ?? new Set<number>();
    s.add(tagId);
    this.assign.set(k, s);
  }

  tagIds(item: ItemRef): number[] {
    return [...(this.assign.get(key(item)) ?? [])].sort((a, b) => a - b);
  }

  /** Tags of a chat message incl. the chat's own tags. */
  chatItemTags(chatMsgId: number, chatId: number): number[] {
    return [...new Set([...this.tagIds({ kind: 'chat_message', id: chatMsgId }), ...this.tagIds({ kind: 'chat', id: chatId })])].sort((a, b) => a - b);
  }

  /** The tag and all descendants. */
  subtree(tagId: number): number[] {
    const out = [tagId];
    for (let i = 0; i < out.length; i++) for (const t of this.tags) if (t.parentId === out[i]) out.push(t.id);
    return out;
  }

  hasTag(ids: number[] | undefined, tagId: number): boolean {
    const sub = this.subtree(tagId);
    return !!ids?.some((x) => sub.includes(x));
  }

  tagPaths(ids: number[]): string[] {
    return ids.map((id) => this.tags.find((t) => t.id === id)?.path).filter((p): p is string => !!p);
  }

  listTags(): TagCount[] {
    const msgs = this.host.messages();
    const tags = [...this.tags].sort((a, b) => a.path.localeCompare(b.path, 'de'));
    return tags.map((tag) => {
      let direct = 0;
      let unread = 0;
      const sub = new Set(this.subtree(tag.id));
      const totalSet = new Set<string>();
      for (const [k, set] of this.assign) {
        if (set.has(tag.id)) {
          direct++;
          if (k.startsWith('message:')) {
            const m = msgs.get(Number(k.slice(8)));
            if (m && !m.flags.includes(SEEN)) unread++;
          }
        }
        if ([...set].some((x) => sub.has(x))) totalSet.add(k);
      }
      return { tag: clone(tag), direct, total: totalSet.size, unread };
    });
  }

  saveTag(input: TagInput): Tag {
    const path = input.path
      .split('/')
      .map((s) => s.trim())
      .filter(Boolean)
      .join('/');
    if (!path) throw new ApiError('Tag-Name fehlt', 400, 'invalid');
    const clash = this.tags.find((t) => t.path.toLowerCase() === path.toLowerCase() && t.id !== input.id);
    if (clash) throw new ApiError('Tag existiert bereits', 409, 'tag_exists');
    const ensureParent = (parentPath: string): number | undefined => {
      if (!parentPath) return undefined;
      const hit = this.tags.find((t) => t.path === parentPath);
      if (hit) return hit.id;
      const pp = parentPath.split('/');
      const parentId = ensureParent(pp.slice(0, -1).join('/'));
      const t: Tag = { id: ++this.seq, parentId, name: pp[pp.length - 1], path: parentPath, color: input.color, sortOrder: this.tags.length };
      this.tags.push(t);
      return t.id;
    };
    const parts = path.split('/');
    const parentId = ensureParent(parts.slice(0, -1).join('/'));
    let tag: Tag;
    if (input.id) {
      const t = this.tags.find((x) => x.id === input.id);
      if (!t) throw new ApiError('Tag nicht gefunden', 404, 'not_found');
      const old = t.path;
      t.path = path;
      t.name = parts[parts.length - 1];
      t.parentId = parentId;
      t.color = input.color;
      t.icon = input.icon;
      for (const d of this.tags) if (d.path.startsWith(old + '/')) d.path = path + d.path.slice(old.length);
      tag = t;
    } else {
      tag = { id: ++this.seq, parentId, name: parts[parts.length - 1], path, color: input.color, icon: input.icon, sortOrder: this.tags.length };
      this.tags.push(tag);
    }
    this.host.notify('tags');
    return clone(tag);
  }

  deleteTag(id: number): void {
    const sub = this.subtree(id);
    this.tags = this.tags.filter((t) => !sub.includes(t.id));
    for (const s of this.assign.values()) sub.forEach((x) => s.delete(x));
    this.host.notify('tags');
  }

  mergeTags(sourceIds: number[], targetId: number): void {
    if (!this.tags.some((t) => t.id === targetId)) throw new ApiError('Tag nicht gefunden', 404, 'not_found');
    for (const s of this.assign.values()) {
      if (sourceIds.some((x) => s.has(x))) {
        sourceIds.forEach((x) => s.delete(x));
        s.add(targetId);
      }
    }
    for (const sid of sourceIds) {
      // children move under the target
      const src = this.tags.find((t) => t.id === sid);
      const target = this.tags.find((t) => t.id === targetId)!;
      if (!src) continue;
      for (const d of this.tags) {
        if (d.parentId === sid) d.parentId = targetId;
        if (d.path.startsWith(src.path + '/')) d.path = target.path + d.path.slice(src.path.length);
      }
    }
    this.tags = this.tags.filter((t) => !sourceIds.includes(t.id) || t.id === targetId);
    this.host.notify('tags');
  }

  setItemTags(items: ItemRef[], add: number[], remove: number[]): void {
    for (const it of items) {
      const k = key(it);
      const s = this.assign.get(k) ?? new Set<number>();
      add.forEach((x) => s.add(x));
      remove.forEach((x) => s.delete(x));
      this.assign.set(k, s);
    }
    this.host.notify('tags');
  }

  // -------------------------------------------------------------------------
  // Projects
  // -------------------------------------------------------------------------

  projectIdsFor(item: ItemRef): number[] {
    const out: number[] = [];
    for (const [pid, l] of this.links) if (l.some((x) => key(x.item) === key(item))) out.push(pid);
    return out;
  }

  private computed(p: Project): Project {
    const l = this.links.get(p.id) ?? [];
    const tasks = this.tasks.filter((t) => t.projectId === p.id);
    const done = tasks.filter((t) => t.done).length;
    return {
      ...clone(p),
      progress: tasks.length ? done / tasks.length : p.status === 'done' ? 1 : 0,
      counts: {
        mail: l.filter((x) => x.item.kind === 'message').length,
        chat: l.filter((x) => x.item.kind === 'chat_message').length,
        event: this.events.filter((e) => e.projectId === p.id).length,
        task: tasks.length,
        file: this.files.filter((f) => f.projectId === p.id).length,
      },
    };
  }

  listProjects(status?: ProjectStatus[]): Project[] {
    return this.projects.filter((p) => !status?.length || status.includes(p.status)).map((p) => this.computed(p));
  }

  getProject(id: number): Project {
    const p = this.projects.find((x) => x.id === id);
    if (!p) throw new ApiError('Projekt nicht gefunden', 404, 'not_found');
    return this.computed(p);
  }

  saveProject(input: ProjectInput): Project {
    const now = this.host.now().toISOString();
    if (!input.name.trim()) throw new ApiError('Name fehlt', 400, 'invalid');
    if (input.id) {
      const i = this.projects.findIndex((p) => p.id === input.id);
      if (i < 0) throw new ApiError('Projekt nicht gefunden', 404, 'not_found');
      this.projects[i] = { ...this.projects[i], ...clone(input), id: input.id, updatedAt: now };
      this.host.notify('project', input.id);
      return this.computed(this.projects[i]);
    }
    const id = Math.max(0, ...this.projects.map((p) => p.id)) + 1;
    const p: Project = { ...clone(input), id, progress: 0, counts: { mail: 0, chat: 0, event: 0, task: 0, file: 0 }, createdAt: now, updatedAt: now };
    if (!p.key) p.key = p.name.replace(/[^A-Za-zÄÖÜäöü0-9]/g, '').slice(0, 4).toUpperCase() + '-' + id;
    this.projects.push(p);
    const tpl = this.projectTemplates().find((t) => t.id === input.templateId);
    for (const wid of tpl?.workflowTemplateIds ?? []) {
      const wt = this.templates().find((t) => t.id === wid);
      if (wt) this.workflows.push({ ...clone(wt.workflow), id: `${wid}-${p.key.toLowerCase()}`, name: `${wt.workflow.name} (${p.name})`, projectId: id, templateId: wid, enabled: false, actions: wt.workflow.actions.map((a) => (a.type === 'add_to_project' ? { ...a, params: { project: p.name } } : a)) });
    }
    this.host.notify('project', id);
    return this.computed(p);
  }

  deleteProject(id: number): void {
    this.projects = this.projects.filter((p) => p.id !== id);
    this.links.delete(id);
    this.workflows = this.workflows.map((w) => (w.projectId === id ? { ...w, projectId: undefined } : w));
    this.host.notify('project', id);
  }

  projectTemplates(): ProjectTemplate[] {
    return [
      { id: 'blank', name: 'Leeres Projekt', description: 'Ohne Vorgaben.', color: '#64748b', fields: [], workflowTemplateIds: [] },
      {
        id: 'client',
        name: 'Kundenprojekt',
        description: 'Angebot, Auftrag, Kommunikation mit einem Kunden.',
        color: '#2563eb',
        fields: [
          { key: 'customerNo', label: 'Kundennummer', type: 'text', value: '' },
          { key: 'offer', label: 'Angebotsnummer', type: 'text', value: '' },
          { key: 'volume', label: 'Auftragsvolumen', type: 'number', value: '' },
        ],
        workflowTemplateIds: ['project-autolink'],
        ruleQuery: 'project:{key}',
      },
      {
        id: 'construction',
        name: 'Bauprojekt',
        description: 'Bauantrag, Gewerke, Termine auf der Baustelle.',
        color: '#a16207',
        fields: [
          { key: 'permit', label: 'Bauantrag-Nr.', type: 'text', value: '' },
          { key: 'plot', label: 'Grundstück', type: 'text', value: '' },
          { key: 'phase', label: 'Bauphase', type: 'select', value: 'Planung', options: ['Planung', 'Rohbau', 'Innenausbau', 'Abnahme'] },
        ],
        workflowTemplateIds: ['project-autolink'],
      },
      {
        id: 'legal',
        name: 'Mandat / Akte',
        description: 'Aktenzeichen, Gericht, Fristen.',
        color: '#7c3aed',
        fields: [
          { key: 'file', label: 'Aktenzeichen', type: 'text', value: '' },
          { key: 'court', label: 'Gericht', type: 'text', value: '' },
          { key: 'deadline', label: 'Nächste Frist', type: 'date', value: '' },
        ],
        workflowTemplateIds: ['project-autolink', 'deadline-task'],
      },
    ];
  }

  addToProject(projectId: number, items: ItemRef[], source: 'manual' | 'rule' | 'workflow' = 'manual'): number {
    this.getProject(projectId);
    const l = this.links.get(projectId) ?? [];
    let added = 0;
    for (const it of items) {
      if (l.some((x) => key(x.item) === key(it))) continue;
      l.push({ item: it, source });
      added++;
    }
    this.links.set(projectId, l);
    this.host.notify('project', projectId);
    return added;
  }

  removeFromProject(projectId: number, items: ItemRef[]): void {
    const ks = new Set(items.map(key));
    this.links.set(projectId, (this.links.get(projectId) ?? []).filter((x) => !ks.has(key(x.item))));
    if (items.some((i) => i.kind === 'task')) this.tasks = this.tasks.filter((t) => !(t.projectId === projectId && ks.has(`task:${t.id}`)));
    this.host.notify('project', projectId);
  }

  timeline(projectId: number): ProjectTimelineEntry[] {
    this.getProject(projectId);
    const out: ProjectTimelineEntry[] = [];
    const msgs = this.host.messages();
    const persons = this.host.persons();
    const personByEmail = (e: string) => persons.find((p) => p.handles.some((h) => h.kind === 'email' && h.value === e.toLowerCase()));
    for (const { item, source } of this.links.get(projectId) ?? []) {
      if (item.kind === 'message') {
        const m = msgs.get(item.id);
        if (!m) continue;
        const from = m.from[0];
        out.push({ item, kind: 'mail', date: m.date, title: m.subject, snippet: m.preview, personId: from ? personByEmail(from.email)?.id : undefined, actorName: from?.name || from?.email, channel: 'email', conversationId: this.host.threadOf(m.id), messageId: m.id, itemId: `m:${m.id}`, source });
      } else if (item.kind === 'chat_message') {
        const c = this.host.chatMsgs().find((x) => x.id === item.id);
        if (!c) continue;
        out.push({ item, kind: 'chat', date: c.sentAt, title: this.host.chatTitle(c.chatId), snippet: c.text || c.attachments[0]?.filename, personId: c.from, actorName: persons.find((p) => p.id === c.from)?.displayName, channel: this.host.chatChannel(c.chatId), conversationId: this.host.chatConversation(c.chatId), itemId: `c:${c.id}`, source });
      }
    }
    for (const e of this.events.filter((x) => x.projectId === projectId)) out.push({ item: { kind: 'event', id: e.id }, kind: 'event', date: e.start, end: e.end, title: e.title, location: e.location, source: 'manual' });
    for (const t of this.tasks.filter((x) => x.projectId === projectId)) out.push({ item: { kind: 'task', id: t.id }, kind: 'task', date: t.due ?? t.created, due: t.due, title: t.title, done: t.done, source: 'manual' });
    for (const f of this.files.filter((x) => x.projectId === projectId)) out.push({ item: { kind: 'file', id: f.id }, kind: 'file', date: f.date, title: f.name, path: f.path, size: f.size, personId: f.personId, actorName: persons.find((p) => p.id === f.personId)?.displayName, source: 'manual' });
    return out.sort((a, b) => b.date.localeCompare(a.date));
  }

  applyProjectRules(projectId: number): number {
    const p = this.getProject(projectId);
    if (!p.rules.query.trim()) return 0;
    const r = parse(p.rules.query);
    if (!r.ok) throw new ApiError(r.error.message, 400, 'bad_query');
    const now = this.host.now();
    const items: ItemRef[] = [];
    for (const d of this.host.docs()) {
      if (d.inTrashOrSpam || !evaluate(r.ast, d, now)) continue;
      if (d.kind === 'mail' && d.messageId) items.push({ kind: 'message', id: d.messageId });
      else if (d.kind === 'chat' && d.itemId) items.push({ kind: 'chat_message', id: Number(d.itemId.slice(2)) });
    }
    return this.addToProject(projectId, items, 'rule');
  }

  setTaskDone(taskId: number, done: boolean): void {
    const t = this.tasks.find((x) => x.id === taskId);
    if (!t) throw new ApiError('Aufgabe nicht gefunden', 404, 'not_found');
    t.done = done;
    this.host.notify('project', t.projectId);
  }

  projectLabels(item: ItemRef): string[] {
    return this.projectIdsFor(item).flatMap((id) => {
      const p = this.projects.find((x) => x.id === id);
      return p ? [p.key, p.name] : [];
    });
  }

  // -------------------------------------------------------------------------
  // Search folders
  // -------------------------------------------------------------------------

  /** Item ids ("m:1", "c:1503") matching a search folder. */
  searchFolderMatches(id: number): Set<string> {
    const sf = this.searchFolders.find((s) => s.id === id);
    if (!sf) throw new ApiError('Suchordner nicht gefunden', 404, 'not_found');
    const r = parse(sf.query);
    const out = new Set<string>();
    if (!r.ok) return out;
    const now = this.host.now();
    for (const d of this.host.docs()) {
      if ((d.kind !== 'mail' && d.kind !== 'chat') || d.inTrashOrSpam) continue;
      if (sf.accountId && d.accountId !== sf.accountId) continue;
      if (evaluate(r.ast, d, now) && d.itemId) out.add(d.itemId);
    }
    return out;
  }

  listSearchFolders(): SearchFolder[] {
    const msgs = this.host.messages();
    const chats = this.host.chatMsgs();
    return this.searchFolders.map((sf) => {
      const ids = this.searchFolderMatches(sf.id);
      let unread = 0;
      for (const id of ids) {
        if (id.startsWith('m:')) unread += msgs.get(Number(id.slice(2)))?.flags.includes(SEEN) ? 0 : 1;
        else unread += chats.find((c) => c.id === Number(id.slice(2)))?.seen ? 0 : 1;
      }
      return { ...clone(sf), total: ids.size, unread };
    });
  }

  saveSearchFolder(input: SearchFolderInput): SearchFolder {
    if (!input.name.trim()) throw new ApiError('Name fehlt', 400, 'invalid');
    const r = parse(input.query);
    if (!r.ok) throw new ApiError(r.error.message, 400, 'bad_query');
    const rec = { ...clone(input), id: input.id ?? Math.max(0, ...this.searchFolders.map((s) => s.id)) + 1 };
    this.searchFolders = [...this.searchFolders.filter((s) => s.id !== rec.id), rec].sort((a, b) => a.id - b.id);
    this.host.notify('searchfolders');
    return this.listSearchFolders().find((s) => s.id === rec.id)!;
  }

  deleteSearchFolder(id: number): void {
    this.searchFolders = this.searchFolders.filter((s) => s.id !== id);
    this.host.notify('searchfolders');
  }

  chatFolderSearchFolders(): SearchFolder[] {
    return this.listSearchFolders().filter((s) => s.useAsChatFolder);
  }

  // -------------------------------------------------------------------------
  // Workflows
  // -------------------------------------------------------------------------

  templates(): WorkflowTemplate[] {
    const t = (workflow: Workflow, description: string): WorkflowTemplate => ({ id: workflow.id, name: workflow.name, description, workflow: { ...workflow, externalEffects: hasExternalEffects(DEFAULT_SCHEMA, workflow.actions.map((a) => a.type)) } });
    return [
      t(
        {
          id: 'invoices-to-nextcloud',
          name: 'Rechnungen als PDF in Nextcloud ablegen',
          enabled: false,
          trigger: { event: 'mail.received', folders: ['inbox'] },
          condition: { expr: 'mail.isInvoice || mail.category == "rechnung" || anyGlob(mail.attachmentNames, "*rechnung*.pdf")' },
          actions: [
            { type: 'archive_pdf', params: { target: 'nextcloud', path: '/Buchhaltung/{{jahr}}/{{monat}}/{{datum}}_{{absender}}_{{betreff}}.pdf' } },
            { type: 'label', params: { add: ['Rechnung', 'archiviert'] } },
          ],
          options: { loop_protection: { max_per_message: 1, window: '30d' } },
        },
        'Jede eingehende Rechnung als PDF/A in Nextcloud unter /Buchhaltung/Jahr/Monat ablegen.',
      ),
      t(
        {
          id: 'newsletter-auto-archive',
          name: 'Newsletter automatisch archivieren',
          enabled: false,
          trigger: { event: 'mail.received', folders: ['inbox'] },
          condition: { rule: { any: [{ field: 'mail.category', op: 'eq', value: 'newsletter' }, { field: 'mail.headers.List-Unsubscribe', op: 'not_empty' }] } },
          actions: [{ type: 'flag', params: { add: ['seen'] } }, { type: 'archive', params: {} }],
        },
        'Newsletter als gelesen markieren und archivieren.',
      ),
      t(
        {
          id: 'vip-mail-signal',
          name: 'VIP-Mails per Signal melden',
          enabled: false,
          trigger: { event: 'mail.received' },
          condition: { rule: { any: [{ field: 'mail.from', op: 'in', value: ['chefin@example.com', 'wichtig@example.org'] }, { field: 'mail.fromDomain', op: 'eq', value: 'vip-kunde.example' }] } },
          actions: [{ type: 'notify', params: { channel: 'signal:me', title: 'VIP-Mail von {{absender}}', text: '{{absender}}: {{betreff}}' } }],
          options: { rate_limit: { max: 20, per: '1h' } },
        },
        'Signal-Nachricht an mich, wenn eine Mail von VIP-Absendern eingeht.',
      ),
      t(
        {
          id: 'waiting-for-reply',
          name: 'Warten auf Antwort',
          enabled: false,
          trigger: { event: 'noreply.timeout' },
          actions: [
            { type: 'notify', params: { channel: 'desktop', title: 'Keine Antwort erhalten', text: 'Keine Antwort auf „{{betreff}}“ an {{empfaenger}} – nachfassen?' } },
            { type: 'flag', params: { add: ['flagged'] } },
          ],
        },
        'Erinnern und markieren, wenn auf eine gesendete Mail niemand antwortet.',
      ),
      t(
        {
          id: 'large-attachments-cloud-link',
          name: 'Anhänge über 10 MB als Cloud-Link',
          enabled: false,
          trigger: { event: 'mail.before_send' },
          condition: { rule: { field: 'mail.maxAttachmentSize', op: 'gt', value: 10 * 1024 * 1024 } },
          actions: [{ type: 'upload_attachments', params: { target: 'nextcloud', dir: '/Mail-Anhänge/{{jahr}}-{{monat}}', replace_with_link: true, share_expiry: '14d' } }],
        },
        'Große Anhänge vor dem Senden hochladen und durch einen Freigabelink ersetzen.',
      ),
      t(
        {
          id: 'out-of-office',
          name: 'Abwesenheitsnotiz',
          enabled: false,
          trigger: { event: 'mail.received', folders: ['inbox'] },
          actions: [{ type: 'vacation_reply', params: {} }],
          options: { loop_protection: { max_per_message: 1, window: '1d' } },
        },
        'Automatisch antworten, solange die Abwesenheit aktiv ist.',
      ),
      t(
        {
          id: 'project-autolink',
          name: 'Projektmails zuordnen und taggen',
          enabled: false,
          trigger: { event: 'mail.received' },
          condition: { rule: { field: 'mail.subject', op: 'contains', value: '{{projekt}}' } },
          actions: [{ type: 'add_to_project', params: { project: '' } }],
        },
        'Eingehende Mails mit dem Projektnamen im Betreff dem Projekt zuordnen.',
      ),
      t(
        {
          id: 'deadline-task',
          name: 'Fristen als Aufgabe anlegen',
          enabled: false,
          trigger: { event: 'mail.received' },
          condition: { rule: { any: [{ field: 'mail.subject', op: 'contains', value: 'Frist' }, { field: 'mail.body', op: 'contains', value: 'Frist' }] } },
          actions: [
            { type: 'create_task', params: { title: 'Frist prüfen: {{betreff}}', due: '3d' } },
            { type: 'tag', params: { tags: ['Wichtig'] } },
          ],
        },
        'Mails mit Fristen als Aufgabe übernehmen und als wichtig markieren.',
      ),
    ];
  }

  context(): WorkflowContext {
    const folders = this.host.folders();
    const path = (f: Folder): string => {
      const parent = folders.find((x) => x.id === f.parentId);
      return parent ? `${path(parent)}/${f.name}` : f.name;
    };
    return {
      accounts: this.host.accounts().map((a) => ({ id: a.id, name: a.displayName })),
      folders: ['inbox', ...new Set(folders.filter((f) => f.role !== 'junk' && f.role !== 'trash').map(path))],
      tags: this.tags.map((t) => t.path).sort((a, b) => a.localeCompare(b, 'de')),
      projects: this.projects.filter((p) => p.status !== 'archived').map((p) => p.name),
      templates: ['Danke', 'Eingangsbestätigung', 'Abwesend'],
    };
  }

  listWorkflows(projectId?: number): Workflow[] {
    return clone(this.workflows.filter((w) => projectId == null || w.projectId === projectId));
  }

  getWorkflow(id: string): Workflow {
    const w = this.workflows.find((x) => x.id === id);
    if (!w) throw new ApiError('Workflow nicht gefunden', 404, 'not_found');
    return clone(w);
  }

  saveWorkflow(w: Workflow): Workflow {
    const rec: Workflow = { ...clone(w), updatedAt: this.host.now().toISOString() };
    if (!rec.id) rec.id = (rec.name || 'workflow').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') + '-' + ++this.seq;
    rec.externalEffects = hasExternalEffects(DEFAULT_SCHEMA, rec.actions.map((a) => a.type));
    const errs = this.semanticErrors(rec);
    if (errs.length) throw new ApiError(errs[0], 400, 'invalid_workflow');
    const i = this.workflows.findIndex((x) => x.id === rec.id);
    if (i >= 0) {
      rec.lastRun = this.workflows[i].lastRun;
      this.workflows[i] = rec;
    } else this.workflows.push(rec);
    return clone(rec);
  }

  private semanticErrors(w: Workflow): string[] {
    const out: string[] = [];
    if (!DEFAULT_SCHEMA.triggers.some((t) => t.event === w.trigger.event)) out.push(`Unknown trigger "${w.trigger.event}"`);
    if (!w.actions.length) out.push('At least one action is required');
    for (const a of w.actions) {
      const spec = actionSpec(DEFAULT_SCHEMA, a.type);
      if (!spec) out.push(`Unknown action "${a.type}"`);
      else for (const p of spec.params) if (p.required && (a.params[p.name] === undefined || a.params[p.name] === '')) out.push(`${a.type}: missing "${p.name}"`);
    }
    return out;
  }

  deleteWorkflow(id: string): void {
    this.workflows = this.workflows.filter((w) => w.id !== id);
    this.runs.delete(id);
  }

  setWorkflowEnabled(id: string, enabled: boolean): Workflow {
    const w = this.workflows.find((x) => x.id === id);
    if (!w) throw new ApiError('Workflow nicht gefunden', 404, 'not_found');
    w.enabled = enabled;
    return clone(w);
  }

  listRuns(id: string, limit = 20): WorkflowRun[] {
    return clone((this.runs.get(id) ?? []).slice(0, limit));
  }

  validate(src: string, mode: 'rule' | 'yaml'): WorkflowValidation {
    return validateSource(src, mode, DEFAULT_SCHEMA, this.context());
  }

  complete(src: string, offset: number, mode: 'rule' | 'yaml'): WorkflowCompletion {
    return completeSource(src, offset, mode, DEFAULT_SCHEMA, this.context());
  }

  private mailVars(m: MessageDetail): Record<string, string> {
    const d = new Date(m.date);
    const p = (n: number) => String(n).padStart(2, '0');
    const acc = this.host.accounts().find((a) => a.id === m.accountId);
    const folder = this.host.folders().find((f) => m.folderIds.includes(f.id));
    return {
      absender: m.from[0]?.name || m.from[0]?.email || '',
      absender_email: m.from[0]?.email ?? '',
      betreff: m.subject,
      datum: `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`,
      uhrzeit: `${p(d.getHours())}:${p(d.getMinutes())}`,
      jahr: String(d.getFullYear()),
      monat: p(d.getMonth() + 1),
      konto: acc?.displayName ?? '',
      ordner: folder?.name ?? '',
      empfaenger: m.to.map((a) => a.name || a.email).join(', '),
    };
  }

  private mailField(m: MessageDetail, field: string): unknown {
    const from = m.from[0]?.email ?? '';
    const folders = this.host.folders().filter((f) => m.folderIds.includes(f.id));
    const isInvoice = /rechnung|invoice/i.test(m.subject + ' ' + m.attachments.map((a) => a.filename).join(' '));
    switch (field) {
      case 'mail.from':
        return from;
      case 'mail.fromName':
        return m.from[0]?.name ?? '';
      case 'mail.fromDomain':
        return from.split('@')[1] ?? '';
      case 'mail.subject':
        return m.subject;
      case 'mail.body':
        return m.text ?? '';
      case 'mail.account':
        return this.host.accounts().find((a) => a.id === m.accountId)?.displayName ?? '';
      case 'mail.folder':
        return folders[0]?.name ?? '';
      case 'mail.category':
        return isInvoice ? 'rechnung' : /newsletter|wochenrückblick/i.test(m.subject) ? 'newsletter' : '';
      case 'mail.project':
        return this.projectLabels({ kind: 'message', id: m.id })[1] ?? '';
      case 'mail.to':
        return m.to.map((a) => a.email);
      case 'mail.cc':
        return m.cc.map((a) => a.email);
      case 'mail.recipients':
        return [...m.to, ...m.cc].map((a) => a.email);
      case 'mail.attachmentNames':
        return m.attachments.map((a) => a.filename);
      case 'mail.flags':
        return m.flags;
      case 'mail.labels':
      case 'mail.tags':
        return this.tagPaths(this.tagIds({ kind: 'message', id: m.id }));
      case 'mail.size':
        return m.size;
      case 'mail.attachmentCount':
        return m.attachments.length;
      case 'mail.maxAttachmentSize':
        return Math.max(0, ...m.attachments.map((a) => a.size));
      case 'mail.hasAttachments':
        return m.attachments.length > 0;
      case 'mail.isInvoice':
        return isInvoice;
      default:
        if (field.startsWith('mail.headers.list-') || field.startsWith('mail.headers.List-')) return /newsletter|wochenrückblick/i.test(m.subject) ? 'yes' : '';
        return '';
    }
  }

  /** Very small expr subset for the demo: disjunctions of known predicates. */
  private evalExpr(expr: string, m: MessageDetail): boolean {
    return expr.split('||').some((part) => {
      const p = part.trim();
      if (p === 'mail.isInvoice') return this.mailField(m, 'mail.isInvoice') === true;
      let x = /^mail\.(\w+)\s*==\s*"([^"]*)"$/.exec(p);
      if (x) return String(this.mailField(m, 'mail.' + x[1])).toLowerCase() === x[2].toLowerCase();
      x = /^anyGlob\(mail\.attachmentNames,\s*"([^"]*)"\)$/.exec(p);
      if (x) {
        const re = new RegExp('^' + x[1].replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*') + '$', 'i');
        return m.attachments.some((a) => re.test(a.filename));
      }
      return false;
    });
  }

  private planned(a: Workflow['actions'][number], m: MessageDetail): WorkflowPlannedAction {
    const vars = this.mailVars(m);
    const params: Record<string, string> = {};
    for (const [k, v] of Object.entries(a.params)) params[k] = Array.isArray(v) ? v.join(', ') : fillTemplate(String(v), vars);
    const spec = actionSpec(DEFAULT_SCHEMA, a.type);
    return { type: a.type, summary: `${a.type} ${Object.values(params).join(' · ')}`.trim(), external: !!spec?.external, params };
  }

  dryRun(w: Workflow, limit = 8): WorkflowDryRunResult {
    const folders = this.host.folders();
    const mails = [...this.host.messages().values()]
      .filter((m) => !this.host.isHidden(m) && !m.flags.includes('\\Draft'))
      .filter((m) => (w.trigger.event === 'mail.sent' ? m.folderIds.some((id) => folders.find((f) => f.id === id)?.role === 'sent') : !m.folderIds.some((id) => folders.find((f) => f.id === id)?.role === 'sent')))
      .sort((a, b) => b.date.localeCompare(a.date))
      .slice(0, limit);
    const samples = mails.map((m) => {
      let matched = true;
      if (w.trigger.accounts?.length && !w.trigger.accounts.includes(m.accountId)) matched = false;
      if (matched && w.trigger.folders?.length) {
        const fs = folders.filter((f) => m.folderIds.includes(f.id));
        matched = w.trigger.folders.some((name) => fs.some((f) => f.name.toLowerCase() === name.toLowerCase() || (name.toLowerCase() === 'inbox' && f.role === 'inbox')));
      }
      if (matched && w.condition?.rule) matched = evalRule(w.condition.rule, (f) => this.mailField(m, f));
      if (matched && w.condition?.expr) matched = this.evalExpr(w.condition.expr, m);
      return {
        item: { kind: 'message' as const, id: m.id, title: m.subject, date: m.date, from: m.from[0]?.name || m.from[0]?.email },
        matched,
        planned: matched ? w.actions.map((a) => this.planned(a, m)) : [],
      };
    });
    return { samples, matched: samples.filter((s) => s.matched).length, checked: samples.length };
  }
}
