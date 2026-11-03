// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { buildTimeline, parseSignature, splitMail } from './conversation';
import { highlight, isEmojiOnly, linkify } from './richtext';
import { MockTransport } from './api/mock';
import type { ConversationItem } from './types';

function item(id: string, sentAt: string, extra: Partial<ConversationItem> = {}): ConversationItem {
  return { id, conversationId: 1, channel: 'email', accountId: 1, senderPersonId: 2, senderName: 'Anna', isOwn: false, sentAt, body: 'x', attachments: [], ...extra };
}

describe('splitMail', () => {
  it('separates new text, signature and quoted history', () => {
    const text = 'Passt so.\n\nViele Grüße\nAnna\n\n-- \nAnna Müller\nMüller GmbH\nTel. +49 89 555123-40\n\nAm 03.11.2026 um 09:12 schrieb Jonas:\n> Hallo';
    const p = splitMail(text);
    expect(p.body).toBe('Passt so.\n\nViele Grüße\nAnna');
    expect(p.signature).toBe('Anna Müller\nMüller GmbH\nTel. +49 89 555123-40');
    expect(p.quoted).toMatch(/^Am 03\.11\.2026/);
  });

  it('works without signature', () => {
    expect(splitMail('Hallo\n> zitat')).toEqual({ body: 'Hallo', signature: '', quoted: '> zitat' });
  });
});

describe('parseSignature', () => {
  it('extracts a business card', () => {
    const c = parseSignature('Anna Müller\nKey Account Management\nMüller GmbH\nIndustriestraße 5, 80939 München\nTel. +49 89 555123-40\nMobil +49 170 1112233\nwww.mueller-gmbh.de');
    expect(c).toMatchObject({
      name: 'Anna Müller',
      title: 'Key Account Management',
      company: 'Müller GmbH',
      address: 'Industriestraße 5, 80939 München',
      website: 'www.mueller-gmbh.de',
    });
    expect(c.phones).toEqual(['+49 89 555123-40', '+49 170 1112233']);
  });

  it('skips greetings and finds e-mail addresses', () => {
    const c = parseSignature('Mit freundlichen Grüßen\nJonas Becker · Kanzlei Sommer\nj.becker@kanzlei-sommer.de');
    expect(c.emails).toEqual(['j.becker@kanzlei-sommer.de']);
    expect(c.company).toBe('Jonas Becker · Kanzlei Sommer');
  });
});

describe('buildTimeline (messenger)', () => {
  const items = [
    item('m:1', '2026-11-02T09:00:00.000Z', { subject: 'Angebot Q4' }),
    item('m:2', '2026-11-02T09:01:00.000Z', { subject: 'Re: Angebot Q4', replyTo: { itemId: 'm:1', senderName: 'Anna', excerpt: 'x' } }),
    item('c:3', '2026-11-02T09:02:00.000Z', { channel: 'whatsapp', replyTo: { itemId: 'm:1', senderName: 'Anna', excerpt: 'x' } }),
    item('m:4', '2026-11-02T10:00:00.000Z', { subject: 'Rechnung', unread: true }),
  ];

  it('adds subject system lines only when the subject changes', () => {
    const subj = buildTimeline(items).filter((e) => e.type === 'subject');
    expect(subj.map((e) => e.type === 'subject' && [e.subject, e.changed])).toEqual([
      ['Angebot Q4', false],
      ['Rechnung', true],
    ]);
  });

  it('marks channel changes and hides redundant reply quotes', () => {
    const its = buildTimeline(items).filter((e) => e.type === 'item');
    const byId = Object.fromEntries(its.map((e) => [e.type === 'item' && e.item.id, e]));
    expect(byId['c:3']).toMatchObject({ channelChanged: true, showReplyQuote: true });
    expect(byId['m:2']).toMatchObject({ channelChanged: false, showReplyQuote: false }); // answers the bubble right above
    expect(byId['m:1']).toMatchObject({ groupStart: true, groupEnd: false });
  });

  it('inserts the unread marker before the first unread item', () => {
    const tl = buildTimeline(items, { unreadFromId: 'm:4' });
    const idx = tl.findIndex((e) => e.type === 'unread');
    expect(tl[idx]).toMatchObject({ count: 1 });
    expect(tl.slice(idx).find((e) => e.type === 'item')).toMatchObject({ key: 'm:4' });
  });
});

describe('richtext', () => {
  it('linkifies URLs without trailing punctuation', () => {
    expect(linkify('Siehe https://example.org/a?b=1. Danke')).toEqual([{ text: 'Siehe ' }, { text: 'https://example.org/a?b=1', href: 'https://example.org/a?b=1' }, { text: '. Danke' }]);
  });
  it('highlights search hits case-insensitively', () => {
    expect(highlight([{ text: 'Angebot und ANGEBOT' }], 'angebot').filter((s) => s.hit).length).toBe(2);
  });
  it('detects emoji-only messages', () => {
    expect(isEmojiOnly('👍')).toBe(true);
    expect(isEmojiOnly('❤️😂')).toBe(true);
    expect(isEmojiOnly('ok 👍')).toBe(false);
  });
});

describe('MockTransport messenger features', () => {
  const base = { scope: { kind: 'all' as const }, grouping: 'person' as const, offset: 0, limit: 500 };

  it('aggregates folders, excludes spam/trash and drafts, and hides folders from chats', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const all = await api.listConversations(base);
    expect(all.items.some((c) => c.title === 'Gewinnspiel-Team')).toBe(false); // spam
    expect(all.items.some((c) => c.title === 'Fitness First')).toBe(false); // draft only
    const anna = all.items.find((c) => c.title === 'Anna Müller')!;
    const items = await api.listConversationItems(anna.id);
    expect(new Set(items.map((i) => i.channel))).toEqual(new Set(['email', 'whatsapp']));
  });

  it('filters by pill and lists archived chats separately', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const tg = await api.listConversations({ ...base, filter: 'telegram' });
    expect(tg.items.length).toBeGreaterThan(0);
    expect(tg.items.every((c) => c.channels.includes('telegram'))).toBe(true);
    const groups = await api.listConversations({ ...base, filter: 'groups' });
    expect(groups.items.every((c) => c.isGroup)).toBe(true);
    const archived = await api.listConversations({ ...base, archived: true });
    expect(archived.items.every((c) => c.archived)).toBe(true);
    expect(archived.total).toBeGreaterThan(0);
  });

  it('archiving a chat archives its inbox mails; unarchiving restores them', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const ref = await api.conversationForMessage(3, 'person'); // Anna, mail 3 is in the inbox
    await api.updateConversation(ref.conversationId, { archived: true });
    expect((await api.getMessage(3)).folderIds).toEqual([23]); // Kanzlei archive
    await api.updateConversation(ref.conversationId, { archived: false });
    expect((await api.getMessage(3)).folderIds).toEqual([20]);
  });

  it('deleting a chat moves its mails to the trash', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const ref = await api.conversationForMessage(37, 'person'); // Lena
    await api.deleteConversation(ref.conversationId);
    expect((await api.getMessage(37)).folderIds).toEqual([15]);
  });

  it('toggles reactions (mail reactions are flagged as RFC 9078)', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const r1 = await api.reactToItem(1002, 'm:3', '👍');
    expect(r1).toMatchObject({ reactions: [{ emoji: '👍', count: 1, mine: true }], sentAsRfc9078: true });
    const r2 = await api.reactToItem(1002, 'm:3', '👍');
    expect(r2.reactions).toEqual([]);
    const r3 = await api.reactToItem(102, 'c:1105', '😂');
    expect(r3.reactions.find((r) => r.emoji === '😂')).toMatchObject({ count: 2, mine: true });
    expect(r3.reactions.find((r) => r.emoji === '❤️')?.count).toBe(1); // my previous ❤️ moved to 😂
  });

  it('exposes incoming signatures and replies', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const items = await api.listConversationItems(1002);
    const m3 = items.find((i) => i.id === 'm:3')!;
    expect(m3.signature).toContain('Müller GmbH');
    expect(m3.body).not.toContain('Industriestraße');
    expect(m3.replyTo?.itemId).toBe('m:2');
    const group = await api.listConversationItems(106);
    expect(group.find((i) => i.id === 'c:1507')?.replyTo?.itemId).toBe('c:1503');
    expect(group.find((i) => i.id === 'c:1503')?.linkPreview?.siteName).toBe('alpenverein.de');
  });

  it('forwards a bubble into another chat', async () => {
    const api = new MockTransport({ latencyMs: 0, autoReply: false });
    const res = await api.forwardItem('c:1104', 105);
    const items = await api.listConversationItems(105);
    expect(items.at(-1)).toMatchObject({ id: res.itemId, relation: 'forward', isOwn: true });
  });
});
