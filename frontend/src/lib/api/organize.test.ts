// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { MockTransport } from './mock';

const api = () => new MockTransport({ latencyMs: 0, autoReply: false });

describe('tags', () => {
  it('lists hierarchical tags with counts', async () => {
    const tags = await api().listTags();
    const kunde = tags.find((t) => t.tag.path === 'Kunde')!;
    const mueller = tags.find((t) => t.tag.path === 'Kunde/Müller')!;
    expect(mueller.tag.parentId).toBe(kunde.tag.id);
    expect(mueller.direct).toBe(4); // 3 mails + the WhatsApp chat
    expect(kunde.total).toBeGreaterThan(mueller.total);
  });

  it('creates parents, renames subtrees, merges and assigns', async () => {
    const a = api();
    const t = await a.saveTag({ path: 'Projekt/Alpha/Intern', color: '#123456' });
    expect(t.path).toBe('Projekt/Alpha/Intern');
    const all = await a.listTags();
    const alpha = all.find((x) => x.tag.path === 'Projekt/Alpha')!.tag;
    await a.saveTag({ id: alpha.id, path: 'Projekt/Beta', color: '#000000' });
    expect((await a.listTags()).map((x) => x.tag.path)).toContain('Projekt/Beta/Intern');
    await expect(a.saveTag({ path: 'wichtig', color: '#fff' })).rejects.toMatchObject({ code: 'tag_exists' });

    await a.setItemTags([{ kind: 'message', id: 12 }], [t.id], []);
    expect((await a.getMessage(12)).tagIds).toContain(t.id);
    const page = await a.listMessages({ scope: { kind: 'tag', tagId: alpha.id }, offset: 0, limit: 50 });
    expect(page.items.map((m) => m.id)).toEqual([12]);

    const wichtig = (await a.listTags()).find((x) => x.tag.path === 'Wichtig')!.tag;
    await a.mergeTags([t.id], wichtig.id);
    expect((await a.getMessage(12)).tagIds).toContain(wichtig.id);
    expect((await a.listTags()).some((x) => x.tag.id === t.id)).toBe(false);
  });

  it('filters chats by tag (incl. chat-level tags)', async () => {
    const a = api();
    const fam = (await a.listTags()).find((x) => x.tag.path === 'Privat')!.tag;
    const page = await a.listConversations({ scope: { kind: 'all' }, grouping: 'person', offset: 0, limit: 100, tagId: fam.id });
    expect(page.items.map((c) => c.title)).toEqual(expect.arrayContaining(['Familie Becker']));
  });
});

describe('hybrid view scopes', () => {
  it('lists conversations of a folder and marks items inside it', async () => {
    const a = api();
    const convs = await a.listConversations({ scope: { kind: 'folder', folderId: 17 }, grouping: 'thread', offset: 0, limit: 50 });
    expect(convs.items.map((c) => c.title)).toEqual(['Termin Bodenplatte']);
    const items = await a.listConversationItems(convs.items[0].id, { scope: { kind: 'folder', folderId: 17 } });
    expect(items.filter((i) => i.inScope).map((i) => i.id)).toEqual(['m:20']);
    expect(items.find((i) => i.id === 'm:22')?.folderIds).toEqual([29]);
    const unified = await a.listConversationItems(convs.items[0].id, { scope: { kind: 'unified', role: 'inbox' } });
    expect(unified.filter((i) => i.inScope).map((i) => i.id)).toEqual(['m:24']);
  });
});

describe('projects', () => {
  it('lists projects with progress and counts, builds the timeline', async () => {
    const a = api();
    const ps = await a.listProjects({ status: ['active'] });
    expect(ps.map((p) => p.key)).toEqual(['HB-26', 'MUE-Q4', 'SK-117']);
    const hb = ps[0];
    expect(hb.progress).toBe(0.5);
    expect(hb.counts).toMatchObject({ mail: 5, task: 4, event: 3, file: 4 });
    const tl = await a.projectTimeline(hb.id);
    expect(tl.length).toBe(16);
    expect([...tl].map((e) => e.date)).toEqual([...tl].map((e) => e.date).sort().reverse());
    expect(tl.find((e) => e.kind === 'mail')?.conversationId).toBe(10);
  });

  it('creates from a template (binding workflows), links items and applies rules', async () => {
    const a = api();
    const tpl = (await a.listProjectTemplates()).find((t) => t.id === 'legal')!;
    const p = await a.saveProject({
      key: 'GR-1',
      name: 'Grillfest-Akte',
      status: 'active',
      color: tpl.color,
      description: '',
      customFields: tpl.fields,
      memberPersonIds: [],
      notes: '',
      tagIds: [],
      rules: { enabled: true, query: 'subject:Grillfest' },
      templateId: tpl.id,
    });
    expect((await a.listWorkflows({ projectId: p.id })).map((w) => w.templateId)).toEqual(['project-autolink', 'deadline-task']);
    const r = await a.applyProjectRules(p.id);
    expect(r.added).toBe(4);
    await a.addToProject(p.id, [{ kind: 'message', id: 12 }]);
    expect((await a.getProject(p.id)).counts.mail).toBe(5);
    expect((await a.getMessage(12)).projectIds).toContain(p.id);
    await a.removeFromProject(p.id, [{ kind: 'message', id: 12 }]);
    expect((await a.getProject(p.id)).counts.mail).toBe(4);
  });

  it('toggles tasks', async () => {
    const a = api();
    const tl = await a.projectTimeline(1);
    const open = tl.find((e) => e.kind === 'task' && !e.done)!;
    await a.setTaskDone(open.item.id, true);
    expect((await a.getProject(1)).progress).toBe(0.75);
  });
});

describe('search folders', () => {
  it('evaluates queries incl. tag: and project: and serves them as virtual folders', async () => {
    const a = api();
    const sfs = await a.listSearchFolders();
    const invoices = sfs.find((s) => s.name === 'Rechnungen')!;
    expect(invoices.total).toBeGreaterThanOrEqual(3);
    const page = await a.listMessages({ scope: { kind: 'searchFolder', searchFolderId: invoices.id }, offset: 0, limit: 50 });
    expect(page.items.every((m) => /Rechnung/i.test(m.subject) || m.tagIds?.length)).toBe(true);
    const boden = sfs.find((s) => s.projectId === 1)!;
    expect(boden.total).toBe(5);
    const saved = await a.saveSearchFolder({ name: 'Grill', query: 'subject:Grillfest', useAsChatFolder: true });
    expect(saved.total).toBe(4);
    const chatFolders = await a.listChatFolders();
    expect(chatFolders.some((f) => f.searchFolderId === saved.id)).toBe(true);
    const chats = await a.listConversations({ scope: { kind: 'all' }, grouping: 'thread', offset: 0, limit: 50, folderId: 'sf:' + saved.id });
    expect(chats.items.map((c) => c.title)).toEqual(['Grillfest am Samstag']);
    await expect(a.saveSearchFolder({ name: 'x', query: 'from:(', useAsChatFolder: false })).rejects.toMatchObject({ code: 'bad_query' });
  });
});

describe('workflows', () => {
  it('lists workflows with run logs, validates, completes and dry-runs', async () => {
    const a = api();
    const ws = await a.listWorkflows();
    const k = ws.find((w) => w.id === 'kundenmails-mueller')!;
    expect(k.enabled).toBe(true);
    expect(k.externalEffects).toBe(false);
    expect((await a.listWorkflowRuns(k.id)).length).toBe(4);
    expect(ws.find((w) => w.id === 'invoices-to-nextcloud')?.lastRun?.status).toBe('failed');

    const v = await a.validateWorkflow('WHEN mail.received IN account:"Kanzlei Sommer" IF from:*@mueller-gmbh.de THEN tag Kunde, forward chef@example.org', 'rule');
    expect(v.diagnostics).toEqual([]);
    expect(v.workflow?.externalEffects).toBe(true);
    expect(v.workflow?.trigger.accounts).toEqual([2]);
    const bad = await a.validateWorkflow('actions:\n  - type: mvoe\n', 'yaml');
    expect(bad.diagnostics.map((d) => d.code)).toEqual(['missing_name', 'missing_trigger', 'unknown_action']);
    const c = await a.completeWorkflow('WHEN mail.received THEN tag ', 26 + 4, 'rule');
    expect(c.items.map((i) => i.label)).toContain('Kunde/Müller');

    const dry = await a.dryRunWorkflow(k);
    expect(dry.checked).toBeGreaterThan(0);
    const hit = dry.samples.find((s) => s.matched);
    expect(hit?.item.title).toContain('Angebot');
    expect(hit?.planned.map((p) => p.type)).toEqual(['tag', 'move', 'notify']);
    expect(hit?.planned[2].params?.text).toMatch(/^Neue Kundenmail: (Re: )?Angebot Q4$/);
  });

  it('saves, toggles and deletes', async () => {
    const a = api();
    const w = await a.saveWorkflow({ id: '', name: 'Test', enabled: false, trigger: { event: 'manual' }, actions: [{ type: 'archive', params: {} }] });
    expect(w.id).toMatch(/^test-/);
    expect((await a.setWorkflowEnabled(w.id, true)).enabled).toBe(true);
    await expect(a.saveWorkflow({ ...w, actions: [{ type: 'move', params: {} }] })).rejects.toMatchObject({ code: 'invalid_workflow' });
    await a.deleteWorkflow(w.id);
    await expect(a.getWorkflow(w.id)).rejects.toMatchObject({ code: 'not_found' });
  });

  it('needs the explicit confirmation for workflows with external effects', async () => {
    const a = api();
    const fwd = { id: '', name: 'Spiegel', enabled: true, trigger: { event: 'mail.received' }, actions: [{ type: 'forward', params: { to: ['boss@example.org'] } }] };
    await expect(a.saveWorkflow(fwd)).rejects.toMatchObject({ code: 'confirmation_required', status: 409 });
    const w = await a.saveWorkflow({ ...fwd, confirmExternal: true });
    expect(w.externalEffects).toBe(true);
    expect(w.confirmExternal).toBeUndefined();
    await a.setWorkflowEnabled(w.id, false);
    await expect(a.setWorkflowEnabled(w.id, true)).rejects.toMatchObject({ code: 'confirmation_required' });
    expect((await a.setWorkflowEnabled(w.id, true, true)).enabled).toBe(true);
  });
});
