// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import {
  buildTimeline,
  classicSelectionFromChat,
  mailItemId,
  messageIdFromItemId,
  normalizeSubject,
  pickFolderForMessage,
  replySubject,
  splitQuoted,
} from './conversation';
import { MockTransport } from './api/mock';
import type { ConversationItem, Folder } from './types';

function item(id: string, sentAt: string, extra: Partial<ConversationItem> = {}): ConversationItem {
  return {
    id,
    conversationId: 1,
    channel: id.startsWith('m:') ? 'email' : 'whatsapp',
    accountId: 1,
    senderPersonId: 2,
    senderName: 'Anna',
    isOwn: false,
    sentAt,
    body: 'x',
    attachments: [],
    messageId: messageIdFromItemId(id) ?? undefined,
    ...extra,
  };
}

describe('subjects', () => {
  it('normalizes reply/forward prefixes in German and English', () => {
    expect(normalizeSubject('AW: WG: Re: Angebot Q4')).toBe('Angebot Q4');
    expect(normalizeSubject('Fwd: Re[2]: Test')).toBe('Test');
    expect(replySubject('AW: Angebot')).toBe('Re: Angebot');
  });
});

describe('splitQuoted', () => {
  it('cuts at a German attribution line', () => {
    const r = splitQuoted('Danke!\n\nAm 03.11.2026 um 09:12 schrieb Anna Müller <a@b.de>:\n> Hallo');
    expect(r.body).toBe('Danke!');
    expect(r.quoted).toMatch(/^Am 03\.11\.2026/);
  });

  it('cuts at the signature delimiter', () => {
    const r = splitQuoted('Text\n\n-- \nJonas Becker\nRechtsanwalt');
    expect(r.body).toBe('Text');
    expect(r.quoted).toContain('Rechtsanwalt');
  });

  it('cuts at forwarded and Outlook header blocks', () => {
    expect(splitQuoted('FYI\n---------- Forwarded message ----------\nFrom: x').body).toBe('FYI');
    expect(splitQuoted('Siehe unten\n\nVon: Max Weber\nGesendet: Montag\nAn: Jonas').body).toBe('Siehe unten');
    // "Von:" without an Outlook block below is normal text
    expect(splitQuoted('Von: mir, für dich').quoted).toBe('');
  });

  it('keeps unquoted text untouched', () => {
    expect(splitQuoted('Hallo\nWelt')).toEqual({ body: 'Hallo\nWelt', quoted: '' });
  });
});

describe('buildTimeline', () => {
  it('inserts day separators and groups consecutive bubbles of one sender', () => {
    const items = [
      item('c:1', '2026-11-02T09:00:00.000Z'),
      item('c:2', '2026-11-02T09:02:00.000Z'),
      item('c:3', '2026-11-02T09:30:00.000Z'),
      item('m:4', '2026-11-03T10:00:00.000Z', { isOwn: true, senderPersonId: 1 }),
    ];
    const tl = buildTimeline(items);
    const kinds = tl.map((e) => e.type);
    expect(kinds.filter((k) => k === 'day').length).toBeGreaterThanOrEqual(1);
    const bubbles = tl.filter((e) => e.type === 'item');
    expect(bubbles.map((b) => [b.groupStart, b.groupEnd])).toEqual([
      [true, false],
      [false, true],
      [true, true],
      [true, true],
    ]);
  });
});

describe('subject lines', () => {
  it('appear only in two-way conversations, newsletters carry the subject in the bubble', () => {
    const oneWay = [
      item('m:1', '2026-11-02T09:00:00.000Z', { subject: 'Sale 1' }),
      item('m:2', '2026-11-03T09:00:00.000Z', { subject: 'Sale 2' }),
      item('m:3', '2026-11-04T09:00:00.000Z', { subject: 'Sale 3' }),
    ];
    const tl = buildTimeline(oneWay);
    expect(tl.filter((e) => e.type === 'subject')).toEqual([]);
    expect(tl.filter((e) => e.type === 'item').every((e) => e.type === 'item' && e.showSubject)).toBe(true);

    const twoWay = [
      item('m:1', '2026-11-02T09:00:00.000Z', { subject: 'Angebot' }),
      item('m:2', '2026-11-02T10:00:00.000Z', { subject: 'Re: Angebot', isOwn: true, senderPersonId: 1 }),
      item('m:3', '2026-11-02T11:00:00.000Z', { subject: 'Rechnung' }),
    ];
    const subj = buildTimeline(twoWay).filter((e) => e.type === 'subject');
    expect(subj.map((e) => e.type === 'subject' && [e.subject, e.changed])).toEqual([
      ['Angebot', false],
      ['Rechnung', true],
    ]);
  });
});

describe('selection mapping', () => {
  const items = [
    item('m:1', '2026-11-01T08:00:00.000Z'),
    item('c:10', '2026-11-01T09:00:00.000Z'),
    item('m:2', '2026-11-01T10:00:00.000Z'),
    item('c:11', '2026-11-01T11:00:00.000Z'),
  ];

  it('maps a mail bubble to its message', () => {
    expect(classicSelectionFromChat(items, 'm:2')).toBe(2);
  });

  it('maps a messenger bubble to the closest earlier mail', () => {
    expect(classicSelectionFromChat(items, 'c:10')).toBe(1);
    expect(classicSelectionFromChat(items, 'c:11')).toBe(2);
  });

  it('falls back to the next mail, then the newest, then null', () => {
    const onlyLater = [item('c:5', '2026-11-01T07:00:00.000Z'), item('m:9', '2026-11-01T08:00:00.000Z')];
    expect(classicSelectionFromChat(onlyLater, 'c:5')).toBe(9);
    expect(classicSelectionFromChat(items, null)).toBe(2);
    expect(classicSelectionFromChat([item('c:1', '2026-11-01T07:00:00.000Z')], 'c:1')).toBeNull();
  });

  it('item ids round-trip', () => {
    expect(messageIdFromItemId(mailItemId(17))).toBe(17);
    expect(messageIdFromItemId('c:17')).toBeNull();
  });

  it('picks the most sensible folder to reveal a message', () => {
    const folders = [
      { id: 1, role: 'inbox' },
      { id: 2, role: 'sent' },
      { id: 3, role: '' },
      { id: 4, role: 'trash' },
    ] as Folder[];
    expect(pickFolderForMessage([2, 3], folders, 2)).toBe(2); // current folder wins
    expect(pickFolderForMessage([2, 3], folders, 1)).toBe(3); // custom folder before sent
    expect(pickFolderForMessage([4, 1], folders, null)).toBe(1);
  });
});

describe('MockTransport conversation semantics', () => {
  const api = new MockTransport({ latencyMs: 0, now: new Date('2026-11-03T15:00:00') });

  it('maps a mail to its thread and to its person view', async () => {
    const thread = await api.conversationForMessage(3, 'thread');
    expect(thread).toEqual({ conversationId: 1, itemId: 'm:3' });
    const items = await api.listConversationItems(thread.conversationId);
    expect(items.map((i) => i.id)).toEqual(['m:1', 'm:2', 'm:3']);

    const person = await api.conversationForMessage(3, 'person');
    const mixed = await api.listConversationItems(person.conversationId);
    // Anna: mails + WhatsApp in one timeline, chronologically sorted
    expect(new Set(mixed.map((i) => i.channel))).toEqual(new Set(['email', 'whatsapp']));
    expect(mixed.some((i) => i.id === 'm:3')).toBe(true);
    const times = mixed.map((i) => i.sentAt);
    expect([...times].sort()).toEqual(times);
  });

  it('strips quotes from mail bubbles but keeps them collapsible', async () => {
    const items = await api.listConversationItems(1);
    const reply = items.find((i) => i.id === 'm:2')!;
    expect(reply.isOwn).toBe(true);
    expect(reply.body).not.toContain('schrieb');
    expect(reply.quoted).toContain('schrieb');
  });

  it('keeps the conversation when a mail is moved (KONZEPT 6.5)', async () => {
    const before = await api.conversationForMessage(3, 'thread');
    await api.move([3], 23);
    const after = await api.conversationForMessage(3, 'thread');
    expect(after).toEqual(before);
    const msg = await api.getMessage(3);
    expect(msg.folderIds).toEqual([23]);
  });

  it('a reply sent from the chat lands in the same thread', async () => {
    const res = await api.send({ channel: 'email', identityId: 3, to: [{ email: 'anna.mueller@mueller-gmbh.de' }], cc: [], bcc: [], subject: 'Re: Angebot Q4', html: '', text: 'Passt.', inReplyToMessageId: 3, attachments: [] });
    expect(res.conversationId).toBe(1);
    const items = await api.listConversationItems(1);
    expect(items[items.length - 1].id).toBe(res.itemId);
  });
});
