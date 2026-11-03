// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { EMPTY_RULES, detectTrigger, fillSnippet, fuzzy, isFocusActive, isVip, matchesFolder, timePresets } from './chatfeatures';
import { MockTransport } from './api/mock';
import type { Conversation, FocusSettings } from './types';

const conv = (o: Partial<Conversation>): Conversation => ({
  id: 1,
  kind: 'person_view',
  title: 'X',
  lastActivity: '2026-11-03T10:00:00Z',
  unreadCount: 0,
  pinned: false,
  muted: false,
  archived: false,
  favorite: false,
  markedUnread: false,
  lastIsOwn: false,
  channels: ['email'],
  participantIds: [2],
  accountIds: [1],
  snippet: '',
  isGroup: false,
  ...o,
});

describe('chat folder rules', () => {
  it('matches explicit chats and persons', () => {
    expect(matchesFolder(conv({ id: 7 }), { ...EMPTY_RULES, includeConversationIds: [7] })).toBe(true);
    expect(matchesFolder(conv({ participantIds: [4] }), { ...EMPTY_RULES, includePersonIds: [4] })).toBe(true);
    expect(matchesFolder(conv({}), { ...EMPTY_RULES })).toBe(false);
  });
  it('combines criteria with AND and honours exclusions', () => {
    const rules = { ...EMPTY_RULES, channels: ['whatsapp' as const], groups: 'only' as const };
    expect(matchesFolder(conv({ channels: ['whatsapp'], isGroup: true }), rules)).toBe(true);
    expect(matchesFolder(conv({ channels: ['whatsapp'], isGroup: false }), rules)).toBe(false);
    expect(matchesFolder(conv({ id: 3, channels: ['whatsapp'], isGroup: true }), { ...rules, excludeConversationIds: [3] })).toBe(false);
    expect(matchesFolder(conv({ accountIds: [2], muted: true }), { ...EMPTY_RULES, accountIds: [2], excludeMuted: true })).toBe(false);
    expect(matchesFolder(conv({ unreadCount: 2 }), { ...EMPTY_RULES, unreadOnly: true })).toBe(true);
    expect(matchesFolder(conv({ archived: true, id: 7 }), { ...EMPTY_RULES, includeConversationIds: [7] })).toBe(false);
  });
});

describe('time presets', () => {
  it('computes 1 h, evening, tomorrow and Monday', () => {
    const now = new Date(2026, 10, 3, 14, 20); // Tuesday
    const p = Object.fromEntries(timePresets(now).map((x) => [x.id, x.at]));
    expect(p.hour.getHours()).toBe(15);
    expect([p.evening.getDate(), p.evening.getHours()]).toEqual([3, 18]);
    expect([p.tomorrow.getDate(), p.tomorrow.getHours()]).toEqual([4, 8]);
    expect([p.monday.getDay(), p.monday.getDate()]).toEqual([1, 9]);
    const late = Object.fromEntries(timePresets(new Date(2026, 10, 3, 19, 0)).map((x) => [x.id, x.at]));
    expect(late.evening.getDate()).toBe(4);
  });
});

describe('focus mode', () => {
  const base: FocusSettings = { enabled: true, vipPersonIds: [2], vipConversationIds: [99], includeFrequent: true, schedule: { enabled: true, start: '09:00', end: '12:00', days: [1, 2, 3, 4, 5] } };
  it('respects the schedule (time and weekday)', () => {
    expect(isFocusActive(base, new Date(2026, 10, 3, 10, 0))).toBe(true);
    expect(isFocusActive(base, new Date(2026, 10, 3, 12, 0))).toBe(false);
    expect(isFocusActive(base, new Date(2026, 10, 7, 10, 0))).toBe(false); // Saturday
    expect(isFocusActive({ ...base, schedule: { ...base.schedule!, start: '22:00', end: '07:00', days: [] } }, new Date(2026, 10, 3, 23, 0))).toBe(true);
    expect(isFocusActive({ ...base, enabled: false }, new Date(2026, 10, 3, 10, 0))).toBe(false);
  });
  it('classifies VIP chats incl. frequent contacts', () => {
    expect(isVip(conv({ participantIds: [2] }), base)).toBe(true);
    expect(isVip(conv({ id: 99, isGroup: true, participantIds: [5, 6] }), base)).toBe(true);
    expect(isVip(conv({ participantIds: [7] }), base, [7])).toBe(true);
    expect(isVip(conv({ participantIds: [7] }), { ...base, includeFrequent: false }, [7])).toBe(false);
  });
});

describe('composer triggers', () => {
  it('detects "/" only at line start and "@" after whitespace', () => {
    expect(detectTrigger('/dan', 4)).toEqual({ kind: 'slash', query: 'dan', from: 0, to: 4 });
    expect(detectTrigger('Hallo\n/ib', 9)).toMatchObject({ kind: 'slash', query: 'ib', from: 6 });
    expect(detectTrigger('a/b', 3)).toBeNull();
    expect(detectTrigger('Hi @Li', 6)).toEqual({ kind: 'mention', query: 'Li', from: 3, to: 6 });
    expect(detectTrigger('mail@host', 9)).toBeNull();
  });
  it('fills snippet placeholders', () => {
    const now = new Date(2026, 10, 3, 9, 5);
    expect(fillSnippet('Danke, {vorname}! Gruß {meinname}, {datum} {uhrzeit} {unknown}', { firstName: 'Anna', myName: 'Jonas', now, locale: 'de-DE' })).toBe('Danke, Anna! Gruß Jonas, 03.11.2026 09:05 {unknown}');
  });
  it('fuzzy-matches', () => {
    expect(fuzzy('danke', 'da')).toBeGreaterThan(fuzzy('adresse', 'da'));
    expect(fuzzy('termin', 'tmn')).toBeGreaterThanOrEqual(0);
    expect(fuzzy('iban', 'x')).toBe(-1);
  });
});

describe('MockTransport chat features', () => {
  const base = { scope: { kind: 'all' as const }, grouping: 'person' as const, offset: 0, limit: 500 };

  it('chat folders filter lists and count unread', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const folders = await api.listChatFolders();
    expect(folders.map((f) => f.name)).toEqual(['Arbeit', 'Familie', 'Kunden', 'Ungelesen & wichtig']);
    expect(folders.at(-1)?.searchFolderId).toBe(2);
    const fam = await api.listConversations({ ...base, folderId: 'family' });
    expect(fam.items.map((c) => c.title)).toEqual(expect.arrayContaining(['Familie Becker', 'Sabine Becker', 'Lena Becker']));
    expect(fam.items.every((c) => ['Familie Becker', 'Sabine Becker', 'Lena Becker'].includes(c.title))).toBe(true);
    expect(folders.find((f) => f.id === 'family')!.unreadCount).toBeGreaterThan(0);
  });

  it('stars and pins (max. 3) messages', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    await api.setItemStarred(106, 'c:1503', true);
    expect((await api.listStarredItems()).map((s) => s.item.id)).toEqual(['c:1503']);
    for (const id of ['c:1501', 'c:1502', 'c:1503']) await api.setItemPinned(106, id, true);
    await expect(api.setItemPinned(106, 'c:1504', true)).rejects.toMatchObject({ code: 'pin_limit' });
    expect((await api.getConversation(106)).pinnedItemIds).toEqual(['c:1501', 'c:1502', 'c:1503']);
  });

  it('snoozes chats and brings them back on top', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    await api.updateConversation(1007, { snoozedUntil: new Date(Date.now() + 3600_000).toISOString() });
    expect((await api.listConversations(base)).items.some((c) => c.id === 1007)).toBe(false);
    await api.updateConversation(1007, { snoozedUntil: null });
    const list = (await api.listConversations(base)).items;
    expect(list.find((c) => c.id === 1007)?.returnedFromSnooze).toBe(true);
    expect(list.findIndex((c) => c.id === 1007)).toBeLessThan(3);
  });

  it('focus mode splits VIP and waiting chats', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const vip = await api.listConversations({ ...base, focus: 'vip' });
    const waiting = await api.listConversations({ ...base, focus: 'waiting' });
    const all = await api.listConversations(base);
    expect(vip.total + waiting.total).toBe(all.total);
    expect(vip.items.some((c) => c.title === 'Anna Müller')).toBe(true);
  });

  it('keeps drafts and schedules, edits and cancels messages', async () => {
    const api = new MockTransport({ latencyMs: 0, autoReply: false });
    await api.saveDraft(105, { text: 'Halb fertig…' });
    expect((await api.getConversation(105)).draft).toBe('Halb fertig…');
    const sendAt = new Date(Date.now() + 86400_000).toISOString();
    const res = await api.send({ channel: 'signal', accountId: 4, conversationId: 105, to: [], cc: [], bcc: [], subject: '', text: 'Morgen!', html: '', attachments: [], sendAt });
    expect(res.scheduledId).toBeTruthy();
    const items = await api.listConversationItems(105);
    expect(items.at(-1)).toMatchObject({ scheduledFor: sendAt, body: 'Morgen!' });
    await api.updateScheduled(res.scheduledId!, { text: 'Morgen früh!' });
    expect((await api.listScheduled())[0].text).toBe('Morgen früh!');
    await api.cancelScheduled(res.scheduledId!);
    expect(await api.listScheduled()).toEqual([]);
  });

  it('AI requires a provider, then returns canned results', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    await expect(api.aiSuggestReplies(1002)).rejects.toMatchObject({ code: 'ai_not_configured' });
    const s = await api.saveAiSettings({ provider: 'ollama', baseUrl: 'http://localhost:11434', model: 'llama3.1:8b', apiKeySet: false, accountPermissions: {}, maskPersonalData: true, apiKey: 'secret' });
    expect(s).not.toHaveProperty('apiKey');
    expect(s.apiKeySet).toBe(true);
    expect((await api.aiSuggestReplies(1002)).length).toBe(3);
    expect((await api.aiTranslate(1002, 'c:1003', 'en')).text).toBe('Did you see the e-mail with the correction? 🙂');
    expect((await api.aiSummarize(1002)).bullets.length).toBeGreaterThan(0);
  });

  it('meeting proposal: free slots, send, accept creates a system line', async () => {
    const api = new MockTransport({ latencyMs: 0, autoReply: false });
    const from = new Date(Date.now() + 86400_000);
    const to = new Date(Date.now() + 8 * 86400_000);
    const slots = await api.findFreeSlots({ from: from.toISOString(), to: to.toISOString(), durationMin: 30 });
    expect(slots.length).toBeGreaterThan(3);
    expect(slots.every((s) => new Date(s.start).getDay() >= 1 && new Date(s.start).getDay() <= 5)).toBe(true);
    const res = await api.proposeMeeting(105, { title: 'Wanderung planen', durationMin: 30, slots: slots.slice(0, 3), channel: 'signal', accountId: 4 });
    let items = await api.listConversationItems(105);
    const proposal = items.find((i) => i.id === res.itemId)!;
    expect(proposal.meeting?.slots.length).toBe(3);
    await api.acceptMeetingSlot(105, proposal.meeting!.id, slots[1].start);
    items = await api.listConversationItems(105);
    expect(items.find((i) => i.id === res.itemId)?.meeting).toMatchObject({ status: 'booked', chosen: slots[1].start });
    expect(items.some((i) => i.system?.kind === 'eventCreated')).toBe(true);
    // the booked slot is no longer free
    const again = await api.findFreeSlots({ from: from.toISOString(), to: to.toISOString(), durationMin: 30 });
    expect(again.some((s) => s.start === slots[1].start)).toBe(false);
  });

  it('own and incoming mail signatures are separated from the body', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const items = await api.listConversationItems(1002);
    const own = items.find((i) => i.id === 'm:2')!;
    expect(own.signature).toContain('Rechtsanwalt');
    expect(own.body).not.toContain('Rechtsanwalt');
  });
});
