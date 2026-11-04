// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { createSwitchableApi, HttpTeamClient, matchesView, MockTeamClient, roleAllows, teamPath, type TeamApiEvent } from './team';
import { HttpTransport } from './http';
import { MockTransport } from './mock';
import { activeMention, insertMention, mentionSegments } from '../components/team/mentions';
import { subscribeBody } from '../native/push';

describe('team paths', () => {
  it('maps personal routes to the team prefix, forces thread grouping and adds the view', () => {
    expect(teamPath(3, '', '/api/accounts')).toBe('/api/team/3/accounts');
    expect(teamPath(3, 'mine', '/api/conversations?scope=all&grouping=person&limit=50')).toBe('/api/team/3/conversations?scope=all&grouping=thread&limit=50&view=mine');
    expect(teamPath(3, 'done', '/api/conversations')).toBe('/api/team/3/conversations?view=done');
    expect(teamPath(3, 'done', '/api/conversations/7/items')).toBe('/api/team/3/conversations/7/items');
    expect(teamPath(3, '', '/api/messages/9/conversation?grouping=person')).toBe('/api/team/3/messages/9/conversation?grouping=thread');
  });

  it('routes the scoped HttpTransport through the team prefix', async () => {
    const calls: string[] = [];
    const fetchImpl = vi.fn(async (url: RequestInfo | URL) => {
      calls.push(String(url));
      return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as unknown as typeof fetch;
    const c = new HttpTeamClient({ fetchImpl });
    const api = c.scoped(5, 'unassigned');
    await api.listAccounts();
    await api.listConversations({ scope: { kind: 'all' }, grouping: 'person', offset: 0, limit: 20 });
    expect(calls[0]).toBe('/api/team/5/accounts');
    expect(calls[1]).toContain('/api/team/5/conversations?');
    expect(calls[1]).toContain('grouping=thread');
    expect(calls[1]).toContain('view=unassigned');
    await c.updateAssignment(5, 7, { status: 'done' });
    expect(calls[2]).toBe('/api/team/5/conversations/7/assignment');
  });
});

describe('switchable api', () => {
  it('sends mailbox methods to the team mailbox and keeps the rest on the root', async () => {
    const root = new MockTransport({ latencyMs: 0 });
    const scoped = new HttpTransport({ fetchImpl: (async () => new Response('[{"id":99}]', { status: 200 })) as unknown as typeof fetch });
    const ws = createSwitchableApi(root);
    expect((await ws.api.listAccounts()).length).toBeGreaterThan(1);
    ws.setTarget(scoped);
    expect(await ws.api.listAccounts()).toEqual([{ id: 99 }]);
    // Preferences and events stay personal.
    expect(ws.api.kind).toBe('mock');
    expect(await ws.api.getPreferences()).toBeTruthy();
    ws.setTarget(null);
    expect((await ws.api.listAccounts()).length).toBeGreaterThan(1);
  });
});

describe('roles and views', () => {
  it('orders roles', () => {
    expect(roleAllows('assign', 'reply')).toBe(true);
    expect(roleAllows('read', 'reply')).toBe(false);
    expect(roleAllows(null, 'read')).toBe(false);
  });
  it('filters views like the server', () => {
    const base = { mailboxId: 1, conversationId: 1, priority: 'normal' as const, openedAt: '', updatedAt: '', comments: 0 };
    expect(matchesView('unassigned', undefined)).toBe(true);
    expect(matchesView('mine', { ...base, status: 'in_progress', assigneeId: 1 }, 1)).toBe(true);
    expect(matchesView('mine', { ...base, status: 'done', assigneeId: 1 }, 1)).toBe(false);
    expect(matchesView('done', { ...base, status: 'done' })).toBe(true);
  });
});

describe('mock team client', () => {
  it('assigns, comments with mentions and filters views', async () => {
    const events: TeamApiEvent[] = [];
    const c = new MockTeamClient((e) => events.push(e));
    const mbs = await c.listMailboxes();
    expect(mbs[0].name).toBe('Sekretariat');
    const api = c.scoped(1, '');
    const accs = await api.listAccounts();
    expect(accs).toHaveLength(1);
    expect(accs[0].displayName).toBe('Sekretariat');
    const all = await api.listConversations({ scope: { kind: 'unified', role: 'inbox' }, grouping: 'person', offset: 0, limit: 50 });
    expect(all.items.length).toBeGreaterThan(0);
    const free = all.items.find((x) => !(x as { team?: { assigneeId?: number } }).team?.assigneeId)!;
    await c.updateAssignment(1, free.id, { assigneeId: 1 });
    expect(events.at(-1)?.type).toBe('team.changed');
    const mine = await c.scoped(1, 'mine').listConversations({ scope: { kind: 'all' }, grouping: 'thread', offset: 0, limit: 50 });
    expect(mine.items.some((x) => x.id === free.id)).toBe(true);
    const cm = await c.addComment(1, free.id, 'Bitte @anna prüfen, danke @jonas');
    expect(cm.mentions).toEqual([2]); // own mention ignored
    const detail = await c.conversation(1, free.id);
    expect(detail.team.assigneeId).toBe(1);
    expect(detail.team.comments).toBe(1);
  });
});

describe('mentions', () => {
  it('detects and inserts the mention at the caret', () => {
    const text = 'Hallo @an';
    const m = activeMention(text, text.length)!;
    expect(m).toEqual({ start: 6, query: 'an' });
    expect(insertMention(text, m, 'anna')).toEqual({ text: 'Hallo @anna ', caret: 12 });
    expect(activeMention('mail@an', 7)).toBeNull();
  });
  it('splits comments for highlighting', () => {
    expect(mentionSegments('@anna bitte, cc @ben.')).toEqual([
      { text: 'anna', mention: true },
      { text: ' bitte, cc ', mention: false },
      { text: 'ben', mention: true },
      { text: '.', mention: false },
    ]);
    expect(mentionSegments('x@y.de')).toEqual([{ text: 'x@y.de', mention: false }]);
  });
});

describe('android push registration', () => {
  it('builds the FCM body the server accepts', () => {
    expect(subscribeBody({ provider: 'fcm', token: 'tok' } as never, 'Pixel', 'org.tuxmail.app')).toEqual({ type: 'fcm', token: 'tok', deviceName: 'Pixel', appId: 'org.tuxmail.app' });
  });
});
