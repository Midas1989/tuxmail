// SPDX-License-Identifier: AGPL-3.0-or-later
//
// MockTransport: in-memory implementation of the Api with realistic German
// demo data. Used for UI development, demos and the screenshots. It also
// serves as an executable specification of the semantics the Go core has to
// provide (conversation grouping, person views, selection mapping, events).

import type { Api, ConversationQuery, ConversationRef, ItemsQuery, MessageQuery, TransportKind, Unsubscribe } from './api';
import { ApiError, EventHub } from './api';
import { chatItemId, mailItemId, normalizeSubject, splitMail, splitQuoted } from '../conversation';
import type {
Account,
  Address,
  ApiEvent,
  Attachment,
  Channel,
  Conversation,
  ConversationGrouping,
  ConversationItem,
  DeliveryStatus,
  ContactCard,
  ConversationPatch,
  Reaction,
  LinkPreview,
  DeviceAuthorization,
  DeviceTokenResult,
  DiscoveryResult,
  Flag,
  Folder,
  Identity,
  LocaleInfo,
  MessageDetail,
  MessageSummary,
  NewAccountInput,
  NotificationSettings,
  OutgoingMessage,
  Page,
  Person,
  ProvisioningImport,
  ProvisioningProfile,
  PushSubscriptionInput,
  SearchHistoryEntry,
  SearchProfile,
  SearchQuery,
  SearchResult,
  SendResult,
  ServerLoginInput,
  Signature,
  UserPreferences,
} from '../types';
import * as data from './mockData';
import { runSearch, type SearchDoc } from '../search/engine';
import { DEFAULT_REGION } from '../format';

const SEEN: Flag = '\\Seen';
const ANSWERED: Flag = '\\Answered';
const FLAGGED: Flag = '\\Flagged';
const DRAFT: Flag = '\\Draft';
const FORWARDED: Flag = '$Forwarded';

interface MailMeta {
  thread: number;
  fromPerson: number;
  personIds: number[];
  relation?: 'reply' | 'forward';
}

interface ChatRecord {
  id: number;
  chatId: number;
  from: number;
  sentAt: string;
  text: string;
  seen: boolean;
  attachments: Attachment[];
  reactions?: Reaction[];
  status?: DeliveryStatus;
  replyTo?: number;
  link?: LinkPreview;
  forwarded?: boolean;
}

interface ConvFlags {
  pinned?: boolean;
  muted?: boolean;
  archived?: boolean;
  favorite?: boolean;
  markedUnread?: boolean;
  blocked?: boolean;
}

/** Placeholder "photo" (SVG landscape) for the demo data. */
export function photoDataUrl(skyHue: number, groundHue: number): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 300"><defs><linearGradient id="s" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="hsl(${skyHue} 70% 72%)"/><stop offset="1" stop-color="hsl(${(skyHue + 30) % 360} 80% 88%)"/></linearGradient></defs><rect width="400" height="300" fill="url(#s)"/><circle cx="300" cy="80" r="34" fill="hsl(45 95% 70%)"/><path d="M0 230 L90 120 L160 200 L240 100 L330 190 L400 140 L400 300 L0 300Z" fill="hsl(${groundHue} 35% 42%)"/><path d="M0 260 L120 200 L220 250 L320 210 L400 240 L400 300 L0 300Z" fill="hsl(${groundHue} 40% 30%)"/></svg>`;
  return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
}

function waveform(seed: number, n = 40): number[] {
  let x = seed;
  return Array.from({ length: n }, () => {
    x = (x * 9301 + 49297) % 233280;
    return 0.2 + (x / 233280) * 0.8;
  });
}

export interface MockOptions {
  latencyMs?: number;
  now?: Date;
  /** Let a new messenger message arrive after a while (demo of live events). */
  simulateIncoming?: boolean;
  /** Auto-reply in direct chats after the user sends something. */
  autoReply?: boolean;
}

/** Day 0 of the demo data. Before 13:00 local time we use yesterday so no item lies in the future. */
export function anchorDay(now: Date): Date {
  const d = new Date(now);
  d.setHours(0, 0, 0, 0);
  if (now.getHours() < 13) d.setDate(d.getDate() - 1);
  return d;
}

function whenToIso(anchor: Date, [days, hm]: data.When): string {
  const d = new Date(anchor);
  d.setDate(d.getDate() - days);
  const [h, m] = hm.split(':').map(Number);
  d.setHours(h, m, 0, 0);
  return d.toISOString();
}

function fmtQuoteDate(iso: string): string {
  const d = new Date(iso);
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getDate())}.${p(d.getMonth() + 1)}.${d.getFullYear()} um ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function preview(text: string): string {
  return splitQuoted(text).body.replace(/\s+/g, ' ').slice(0, 160);
}

/** "rel:-20m" / "rel:-2h" / "rel:-1d" -> ISO timestamp relative to now (demo data). */
function relTime(v: string | undefined, now: Date): string | undefined {
  const m = v ? /^rel:-(\d+)([mhd])$/.exec(v) : null;
  if (!m) return v;
  const ms = Number(m[1]) * { m: 60_000, h: 3_600_000, d: 86_400_000 }[m[2] as 'm' | 'h' | 'd'];
  return new Date(now.getTime() - ms).toISOString();
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const AUTO_REPLIES = ['👍', 'Alles klar, danke!', 'Super, bis später 🙂', 'Passt!', 'Ok, ich melde mich.'];

export class MockTransport implements Api {
  readonly kind: TransportKind = 'mock';
  private readonly hub = new EventHub();
  private readonly latency: number;
  private readonly opts: MockOptions;

  private accounts: Account[];
  private identities: Identity[];
  private signatures: Signature[];
  private persons: Person[];
  private folders: Folder[];
  private messages = new Map<number, MessageDetail>();
  private meta = new Map<number, MailMeta>();
  private chats: data.ChatSpec[];
  private chatMsgs: ChatRecord[] = [];
  private nextId = 10_000;
  private nextThread = 500;
  private incomingTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(opts: MockOptions = {}) {
    this.opts = opts;
    this.latency = opts.latencyMs ?? 60;
    this.accounts = structuredClone(data.accounts);
    this.identities = structuredClone(data.identities);
    this.signatures = structuredClone(data.signatures);
    this.persons = structuredClone(data.persons).map((p) => ({ ...p, lastSeen: relTime(p.lastSeen, opts.now ?? new Date()) }));
    this.folders = structuredClone(data.folders);
    this.chats = structuredClone(data.chats);
    this.seed(opts.now ?? new Date());
  }

  // -------------------------------------------------------------------------
  // Seeding
  // -------------------------------------------------------------------------

  private person(id: number): Person {
    const p = this.persons.find((x) => x.id === id);
    if (!p) throw new Error('unknown person ' + id);
    return p;
  }

  private addrOf(personId: number, accountId: number, meEmail?: string): Address {
    const p = this.person(personId);
    if (personId === data.ME) {
      return { name: p.displayName, email: meEmail ?? this.accounts.find((a) => a.id === accountId)?.extra?.address ?? '' };
    }
    return { name: p.displayName, email: p.handles.find((h) => h.kind === 'email')?.value ?? '' };
  }

  private seed(now: Date): void {
    const anchor = anchorDay(now);
    for (const s of data.mails) {
      const domain = s.account === 1 ? 'posteo.de' : 'kanzlei-sommer.de';
      const msgId = `tm-${s.id}.${s.thread}@${s.from === data.ME ? domain : this.addrOf(s.from, s.account).email.split('@')[1]}`;
      const date = whenToIso(anchor, s.at);
      let text = s.text;
      if (s.sig) {
        const sig = this.signatures.find((x) => x.id === s.sig);
        if (sig) text += `\n\n-- \n${sig.plain}`;
      }
      if (s.senderSig) text += `\n\n-- \n${s.senderSig}`;
      let inReplyTo: string[] = [];
      let references: string[] = [];
      const parentId = s.replyTo ?? s.forwardOf;
      if (parentId) {
        const parent = this.messages.get(parentId)!;
        references = [...parent.references, parent.messageId];
        if (s.replyTo) {
          inReplyTo = [parent.messageId];
          const from = parent.from[0];
          const quotedLines = (parent.text ?? '').split('\n').slice(0, 14).map((l) => '> ' + l);
          text += `\n\nAm ${fmtQuoteDate(parent.date)} schrieb ${from.name} <${from.email}>:\n${quotedLines.join('\n')}`;
        } else {
          const from = parent.from[0];
          text +=
            `\n\n---------- Weitergeleitete Nachricht ----------\nVon: ${from.name} <${from.email}>\nDatum: ${fmtQuoteDate(parent.date)}\n` +
            `Betreff: ${parent.subject}\nAn: ${parent.to.map((a) => a.email).join(', ')}\n\n${splitQuoted(parent.text ?? '').body}`;
        }
      }
      const flags: Flag[] = [];
      if (s.seen) flags.push(SEEN);
      if (s.flagged) flags.push(FLAGGED);
      if (s.draft) flags.push(DRAFT);
      const attachments: Attachment[] = (s.attachments ?? []).map(([filename, mimeType, size], i) => ({
        id: s.id * 10 + i,
        filename,
        mimeType,
        size,
      }));
      const detail: MessageDetail = {
        id: s.id,
        accountId: s.account,
        folderIds: [...s.folders],
        conversationId: s.thread,
        messageId: msgId,
        subject: s.subject,
        from: [this.addrOf(s.from, s.account, s.meEmail)],
        to: s.to.map((p) => this.addrOf(p, s.account, s.meEmail)),
        cc: (s.cc ?? []).map((p) => this.addrOf(p, s.account, s.meEmail)),
        date,
        preview: preview(text),
        flags,
        hasAttachments: attachments.length > 0,
        size: text.length + attachments.reduce((n, a) => n + a.size, 0),
        inReplyTo,
        references,
        text,
        html: s.html,
        attachments,
        hasRemoteContent: s.remote,
      };
      this.messages.set(s.id, detail);
      this.meta.set(s.id, {
        thread: s.thread,
        fromPerson: s.from,
        personIds: [...new Set([s.from, ...s.to, ...(s.cc ?? [])])],
        relation: s.replyTo ? 'reply' : s.forwardOf ? 'forward' : undefined,
      });
      if (s.replyTo) this.messages.get(s.replyTo)?.flags.push(ANSWERED);
      if (s.forwardOf) this.messages.get(s.forwardOf)?.flags.push(FORWARDED);
    }
    for (const c of data.chatMessages) {
      this.chatMsgs.push({
        id: c.id,
        chatId: c.chatId,
        from: c.from,
        sentAt: whenToIso(anchor, c.at),
        text: c.text,
        seen: c.seen ?? c.from === data.ME,
        attachments: (c.attachments ?? []).map((spec, i) => {
          const [filename, mimeType, size, extra] = spec as [string, string, number, { photo?: [number, number]; seconds?: number }?];
          const a: Attachment = { id: c.id * 10 + i, filename, mimeType, size };
          if (extra?.photo) Object.assign(a, { previewUrl: photoDataUrl(extra.photo[0], extra.photo[1]), width: 400, height: 300 });
          if (extra?.seconds) Object.assign(a, { durationSec: extra.seconds, waveform: waveform(c.id) });
          return a;
        }),
        reactions: c.reactions ? structuredClone(c.reactions) : undefined,
        status: c.from === data.ME ? 'read' : undefined,
        replyTo: c.replyTo,
        link: c.link,
      });
    }
  }

  // -------------------------------------------------------------------------
  // Helpers
  // -------------------------------------------------------------------------

  private async reply<T>(value: T): Promise<T> {
    if (this.latency > 0) await sleep(this.latency * (0.5 + Math.random()));
    return structuredClone(value);
  }

  private emit(e: ApiEvent): void {
    this.hub.emit(e);
  }

  private folder(id: number): Folder | undefined {
    return this.folders.find((f) => f.id === id);
  }

  private isHidden(m: MessageDetail): boolean {
    // Mails that only live in trash/junk do not show up in conversations.
    return m.folderIds.every((fid) => {
      const r = this.folder(fid)?.role;
      return r === 'trash' || r === 'junk';
    });
  }

  private summary(m: MessageDetail): MessageSummary {
    const { inReplyTo: _a, references: _b, html: _c, text: _d, attachments: _e, hasRemoteContent: _f, bcc: _g, replyTo: _h, ...rest } = m;
    return rest;
  }

  private personByEmail(email: string): Person | undefined {
    const e = email.toLowerCase();
    return this.persons.find((p) => p.handles.some((h) => h.kind === 'email' && h.value === e));
  }

  private ensurePerson(addr: Address): number {
    const p = this.personByEmail(addr.email);
    if (p) return p.id;
    const id = Math.max(...this.persons.map((x) => x.id)) + 1;
    this.persons.push({ id, displayName: addr.name || addr.email, isMe: false, handles: [{ kind: 'email', value: addr.email.toLowerCase() }] });
    return id;
  }

  private chatChannel(chat: data.ChatSpec): Channel {
    const kind = this.accounts.find((a) => a.id === chat.accountId)?.kind;
    return kind === 'signal' ? 'signal' : kind === 'telegram' ? 'telegram' : 'whatsapp';
  }

  private convFlags = new Map<number, ConvFlags>([
    [102, { pinned: true }],
    [1002, { pinned: true, favorite: true }],
    [1004, { favorite: true }],
    [106, { muted: true }],
    [1011, { archived: true }],
    [1016, { archived: true }],
  ]);
  private archivedMoves = new Map<number, number[]>();
  private mailReactions = new Map<string, Reaction[]>();
  private uploads = new Map<string, string>();
  private contactCards = new Map<number, ContactCard>();
  private createdFromItems: { id: string; itemId: string; kind: 'task' | 'event' }[] = [];

  private flags(id: number): ConvFlags {
    return this.convFlags.get(id) ?? {};
  }

  private excerpt(text: string, n = 90): string {
    const t = text.replace(/\s+/g, ' ').trim();
    return t.length > n ? t.slice(0, n - 1) + '…' : t;
  }

  private mailItem(m: MessageDetail, conversationId: number): ConversationItem {
    const meta = this.meta.get(m.id)!;
    const split = splitMail(m.text ?? '');
    const own = meta.fromPerson === data.ME;
    const parent = meta.relation === 'reply' && m.inReplyTo[0] ? [...this.messages.values()].find((x) => x.messageId === m.inReplyTo[0]) : undefined;
    const isCard = !!m.html;
    return {
      display: isCard ? 'card' : 'bubble',
      card: isCard ? { title: m.subject, excerpt: this.excerpt(split.body, 160) } : undefined,
      signature: !own && split.signature ? split.signature : undefined,
      replyTo: parent
        ? { itemId: mailItemId(parent.id), senderName: this.person(this.meta.get(parent.id)!.fromPerson).displayName, excerpt: this.excerpt(splitMail(parent.text ?? '').body) }
        : undefined,
      reactions: this.mailReactions.get(mailItemId(m.id)),
      id: mailItemId(m.id),
      conversationId,
      channel: 'email',
      accountId: m.accountId,
      senderPersonId: meta.fromPerson,
      senderName: this.person(meta.fromPerson).displayName,
      isOwn: meta.fromPerson === data.ME,
      sentAt: m.date,
      body: split.body,
      quoted: [split.signature && own ? '-- \n' + split.signature : '', split.quoted].filter(Boolean).join('\n\n') || undefined,
      subject: m.subject,
      relation: meta.relation,
      attachments: m.attachments,
      messageId: m.id,
      unread: !m.flags.includes(SEEN),
    };
  }

  private chatItem(c: ChatRecord, conversationId: number): ConversationItem {
    const chat = this.chats.find((x) => x.id === c.chatId)!;
    return {
      id: chatItemId(c.id),
      conversationId,
      channel: this.chatChannel(chat),
      accountId: chat.accountId,
      senderPersonId: c.from,
      senderName: this.person(c.from).displayName,
      isOwn: c.from === data.ME,
      sentAt: c.sentAt,
      body: c.text,
      attachments: c.attachments,
      status: c.status,
      reactions: c.reactions,
      unread: !c.seen,
      linkPreview: c.link,
      relation: c.forwarded ? 'forward' : undefined,
      replyTo: (() => {
        const r = c.replyTo != null ? this.chatMsgs.find((x) => x.id === c.replyTo) : undefined;
        return r ? { itemId: chatItemId(r.id), senderName: this.person(r.from).displayName, excerpt: this.excerpt(r.text || r.attachments[0]?.filename || '') } : undefined;
      })(),
    };
  }

  /** All conversations for a grouping, each with its items. */
  private buildConversations(grouping: ConversationGrouping): { conv: Conversation; items: ConversationItem[]; mailIds: number[] }[] {
    const out: { conv: Conversation; items: ConversationItem[]; mailIds: number[] }[] = [];
    // Chats aggregate all folders except spam/trash; drafts are not messages.
    const visibleMails = [...this.messages.values()].filter((m) => !this.isHidden(m) && !m.flags.includes(DRAFT));

    type Base = Omit<
      Conversation,
      'lastActivity' | 'unreadCount' | 'snippet' | 'lastSenderName' | 'channels' | 'pinned' | 'muted' | 'archived' | 'favorite' | 'markedUnread' | 'lastIsOwn' | 'lastStatus' | 'lastChannel'
    >;
    const finish = (conv: Base, items: ConversationItem[], mailIds: number[]) => {
      if (items.length === 0) return;
      items.sort((a, b) => a.sentAt.localeCompare(b.sentAt));
      const last = items[items.length - 1];
      const f = this.flags(conv.id);
      const media = last.attachments[0];
      out.push({
        conv: {
          ...conv,
          pinned: !!f.pinned,
          muted: !!f.muted,
          archived: !!f.archived,
          favorite: !!f.favorite,
          markedUnread: !!f.markedUnread,
          blocked: !!f.blocked,
          channels: [...new Set(items.map((i) => i.channel))],
          lastActivity: last.sentAt,
          unreadCount: items.filter((i) => i.unread).length,
          snippet: last.display === 'card' ? (last.card?.title ?? '') : last.body.replace(/\s+/g, ' ').slice(0, 120),
          lastMedia: !last.body && media ? (media.mimeType.startsWith('image/') ? 'photo' : media.mimeType.startsWith('audio/') ? 'voice' : 'file') : undefined,
          lastMediaName: media?.filename,
          lastSenderName: last.isOwn ? undefined : last.senderName.split(' ')[0],
          lastIsOwn: last.isOwn,
          lastStatus: last.isOwn ? (last.status ?? 'sent') : undefined,
          lastChannel: last.channel,
        },
        items,
        mailIds,
      });
    };

    const chatConv = (chat: data.ChatSpec) => {
      const items = this.chatMsgs.filter((c) => c.chatId === chat.id).map((c) => this.chatItem(c, chat.id));
      finish(
        {
          id: chat.id,
          kind: 'chat',
          title: chat.title ?? this.person(chat.memberIds[0]).displayName,
          participantIds: chat.memberIds,
          accountIds: [chat.accountId],
          isGroup: chat.kind === 'group',
        },
        items,
        [],
      );
    };

    if (grouping === 'thread') {
      const threads = new Map<number, MessageDetail[]>();
      for (const m of visibleMails) {
        const t = this.meta.get(m.id)!.thread;
        if (!threads.has(t)) threads.set(t, []);
        threads.get(t)!.push(m);
      }
      for (const [t, msgs] of threads) {
        msgs.sort((a, b) => a.date.localeCompare(b.date));
        const people = new Set<number>();
        msgs.forEach((m) => this.meta.get(m.id)!.personIds.forEach((p) => p !== data.ME && people.add(p)));
        finish(
          {
            id: t,
            kind: 'email_thread',
            title: normalizeSubject(msgs[0].subject),
            participantIds: [...people],
            accountIds: [...new Set(msgs.map((m) => m.accountId))],
            isGroup: people.size > 1,
          },
          msgs.map((m) => this.mailItem(m, t)),
          msgs.map((m) => m.id),
        );
      }
      this.chats.forEach(chatConv);
    } else {
      const ids = new Set<number>();
      for (const m of visibleMails) this.meta.get(m.id)!.personIds.forEach((p) => p !== data.ME && ids.add(p));
      for (const c of this.chats) if (c.kind === 'direct') ids.add(c.memberIds[0]);
      for (const pid of ids) {
        const cid = 1000 + pid;
        const mails = visibleMails.filter((m) => this.meta.get(m.id)!.personIds.includes(pid));
        const direct = this.chats.filter((c) => c.kind === 'direct' && c.memberIds[0] === pid);
        const items = [
          ...mails.map((m) => this.mailItem(m, cid)),
          ...this.chatMsgs.filter((c) => direct.some((d) => d.id === c.chatId)).map((c) => this.chatItem(c, cid)),
        ];
        finish(
          {
            id: cid,
            kind: 'person_view',
            title: this.person(pid).displayName,
            participantIds: [pid],
            accountIds: [...new Set([...mails.map((m) => m.accountId), ...direct.map((d) => d.accountId)])],
            isGroup: false,
          },
          items,
          mails.map((m) => m.id),
        );
      }
      this.chats.filter((c) => c.kind === 'group').forEach(chatConv);
    }
    return out;
  }

  private findConversation(id: number) {
    const grouping: ConversationGrouping = id >= 1000 ? 'person' : 'thread';
    const c = this.buildConversations(grouping).find((x) => x.conv.id === id);
    if (!c) throw new ApiError('Konversation nicht gefunden', 404, 'not_found');
    return c;
  }

  // -------------------------------------------------------------------------
  // Api
  // -------------------------------------------------------------------------

  listAccounts(): Promise<Account[]> {
    return this.reply(this.accounts);
  }
  listIdentities(): Promise<Identity[]> {
    return this.reply(this.identities);
  }
  listSignatures(): Promise<Signature[]> {
    return this.reply(this.signatures);
  }
  listPersons(): Promise<Person[]> {
    return this.reply(this.persons);
  }

  async discoverAccount(email: string, _password: string): Promise<DiscoveryResult> {
    await sleep(Math.max(this.latency * 8, 0));
    const domain = (email.split('@')[1] ?? '').toLowerCase();
    const known: Record<string, [string, string, number, number, 'tls' | 'starttls']> = {
      'posteo.de': ['posteo.de', 'posteo.de', 993, 465, 'tls'],
      'mailbox.org': ['imap.mailbox.org', 'smtp.mailbox.org', 993, 465, 'tls'],
      'gmx.de': ['imap.gmx.net', 'mail.gmx.net', 993, 587, 'starttls'],
      'web.de': ['imap.web.de', 'smtp.web.de', 993, 587, 'starttls'],
      't-online.de': ['secureimap.t-online.de', 'securesmtp.t-online.de', 993, 465, 'tls'],
      'gmail.com': ['imap.gmail.com', 'smtp.gmail.com', 993, 465, 'tls'],
    };
    const k = known[domain];
    if (!domain) return this.reply({ source: 'none', kind: 'imap', displayName: email });
    const [inHost, outHost, inPort, outPort, outTls] = k ?? [`imap.${domain}`, `smtp.${domain}`, 993, 587, 'starttls'];
    return this.reply({
      source: k ? 'autoconfig' : 'guess',
      kind: 'imap',
      displayName: domain,
      incoming: { host: inHost, port: inPort, tls: 'tls', auth: 'password', username: email },
      outgoing: { host: outHost, port: outPort, tls: outTls, auth: 'password', username: email },
      oauthAvailable: domain === 'gmail.com',
    });
  }

  async createAccount(input: NewAccountInput): Promise<Account> {
    const id = Math.max(...this.accounts.map((a) => a.id)) + 1;
    const acc: Account = {
      id,
      kind: input.kind,
      displayName: input.displayName || input.email,
      color: '#0891b2',
      incoming: input.incoming,
      outgoing: input.outgoing,
      extra: { address: input.email },
      enabled: true,
      sortOrder: this.accounts.length,
    };
    this.accounts.push(acc);
    this.identities.push({ id: Math.max(...this.identities.map((i) => i.id)) + 1, accountId: id, name: input.name, email: input.email, bccSelf: false, isDefault: true });
    const base = id * 100;
    (['inbox', 'sent', 'drafts', 'archive', 'trash'] as const).forEach((role, i) => {
      const names = { inbox: 'Posteingang', sent: 'Gesendet', drafts: 'Entwürfe', archive: 'Archiv', trash: 'Papierkorb' };
      this.folders.push({ id: base + i, accountId: id, name: names[role], remoteId: names[role], role, subscribed: true, unread: 0, total: 0 });
    });
    this.emit({ type: 'account.updated', accountId: id });
    return this.reply(acc);
  }

  listFolders(accountId: number): Promise<Folder[]> {
    const msgs = [...this.messages.values()];
    const result = this.folders
      .filter((f) => f.accountId === accountId)
      .map((f) => {
        const inF = msgs.filter((m) => m.folderIds.includes(f.id));
        return { ...f, total: inF.length, unread: inF.filter((m) => !m.flags.includes(SEEN)).length };
      });
    return this.reply(result);
  }

  listMessages(q: MessageQuery): Promise<Page<MessageSummary>> {
    let list = [...this.messages.values()];
    const s = q.scope;
    if (s.kind === 'folder') {
      list = list.filter((m) => m.folderIds.includes(s.folderId));
    } else {
      const roleOf = (m: MessageDetail) => m.folderIds.map((id) => this.folder(id)?.role ?? '');
      switch (s.role) {
        case 'flagged':
          list = list.filter((m) => m.flags.includes(FLAGGED) && !this.isHidden(m));
          break;
        case 'unread':
          list = list.filter((m) => !m.flags.includes(SEEN) && !this.isHidden(m));
          break;
        default:
          list = list.filter((m) => (roleOf(m) as string[]).includes(s.role));
      }
    }
    if (q.search) {
      const needle = q.search.toLowerCase();
      list = list.filter((m) =>
        [m.subject, m.preview, ...m.from.map((a) => `${a.name} ${a.email}`), ...m.to.map((a) => `${a.name} ${a.email}`)]
          .join(' ')
          .toLowerCase()
          .includes(needle),
      );
    }
    list.sort((a, b) => b.date.localeCompare(a.date));
    return this.reply({ items: list.slice(q.offset, q.offset + q.limit).map((m) => this.summary(m)), total: list.length, offset: q.offset });
  }

  getMessage(id: number): Promise<MessageDetail> {
    const m = this.messages.get(id);
    if (!m) return Promise.reject(new ApiError('Nachricht nicht gefunden', 404, 'not_found'));
    return this.reply(m);
  }

  listConversations(q: ConversationQuery): Promise<Page<Conversation>> {
    let all = this.buildConversations(q.grouping);
    const s = q.scope;
    all = all.filter((c) => !!c.conv.archived === !!q.archived);
    switch (q.filter) {
      case 'unread':
        all = all.filter((c) => c.conv.unreadCount > 0 || c.conv.markedUnread);
        break;
      case 'mail':
        all = all.filter((c) => c.conv.channels.includes('email'));
        break;
      case 'whatsapp':
      case 'signal':
      case 'telegram': {
        const ch = q.filter;
        all = all.filter((c) => c.conv.channels.includes(ch));
        break;
      }
      case 'groups':
        all = all.filter((c) => c.conv.isGroup);
        break;
      case 'favorites':
        all = all.filter((c) => c.conv.favorite);
        break;
    }
    if (s.kind === 'account') all = all.filter((c) => c.conv.accountIds.includes(s.accountId));
    if (s.kind === 'folder') all = all.filter((c) => c.mailIds.some((id) => this.messages.get(id)!.folderIds.includes(s.folderId)));
    if (q.search) {
      const needle = q.search.toLowerCase();
      all = all.filter((c) => (c.conv.title + ' ' + c.items.map((i) => i.body + ' ' + (i.subject ?? '')).join(' ')).toLowerCase().includes(needle));
    }
    const convs = all.map((c) => c.conv);
    convs.sort((a, b) => Number(b.pinned) - Number(a.pinned) || b.lastActivity.localeCompare(a.lastActivity));
    return this.reply({ items: convs.slice(q.offset, q.offset + q.limit), total: convs.length, offset: q.offset });
  }

  async getConversation(id: number): Promise<Conversation> {
    return this.reply(this.findConversation(id).conv);
  }

  async listConversationItems(conversationId: number, query: ItemsQuery = {}): Promise<ConversationItem[]> {
    let items = this.findConversation(conversationId).items;
    if (query.before) {
      const idx = items.findIndex((i) => i.id === query.before);
      if (idx >= 0) items = items.slice(0, idx);
    }
    if (query.limit) items = items.slice(-query.limit);
    return this.reply(items);
  }

  async conversationForMessage(messageId: number, grouping: ConversationGrouping): Promise<ConversationRef> {
    const m = this.messages.get(messageId);
    const meta = this.meta.get(messageId);
    if (!m || !meta) throw new ApiError('Nachricht nicht gefunden', 404, 'not_found');
    if (grouping === 'thread') return this.reply({ conversationId: meta.thread, itemId: mailItemId(messageId) });
    let counterpart = meta.fromPerson;
    if (counterpart === data.ME) counterpart = meta.personIds.find((p) => p !== data.ME) ?? data.ME;
    return this.reply({ conversationId: 1000 + counterpart, itemId: mailItemId(messageId) });
  }

  async markConversationRead(conversationId: number): Promise<void> {
    const c = this.findConversation(conversationId);
    const touched = new Set<number>();
    for (const item of c.items) {
      if (!item.unread) continue;
      if (item.messageId != null) {
        const m = this.messages.get(item.messageId)!;
        m.flags.push(SEEN);
        m.folderIds.forEach((f) => touched.add(f));
      } else {
        const rec = this.chatMsgs.find((r) => chatItemId(r.id) === item.id);
        if (rec) rec.seen = true;
      }
    }
    const f = this.convFlags.get(conversationId);
    if (f?.markedUnread) f.markedUnread = false;
    this.emit({ type: 'conversation.updated', conversationId });
    for (const f of touched) this.emit({ type: 'folder.updated', folderId: f, accountId: this.folder(f)?.accountId ?? 0 });
    await this.reply(null);
  }

  private roleFolder(accountId: number, role: string): Folder | undefined {
    return this.folders.find((f) => f.accountId === accountId && f.role === role);
  }

  async updateConversation(conversationId: number, patch: ConversationPatch): Promise<Conversation> {
    const c = this.findConversation(conversationId);
    const before = this.flags(conversationId);
    const next = { ...before, ...patch };
    this.convFlags.set(conversationId, next);
    // Archiving a chat archives the underlying mails (inbox -> archive), unarchiving restores them.
    if (patch.archived === true && !before.archived) {
      const moved: number[] = [];
      for (const id of c.mailIds) {
        const m = this.messages.get(id)!;
        const inbox = m.folderIds.find((fid) => this.folder(fid)?.role === 'inbox');
        const archive = this.roleFolder(m.accountId, 'archive');
        if (inbox != null && archive) {
          m.folderIds = [...m.folderIds.filter((fid) => fid !== inbox), archive.id];
          moved.push(id);
        }
      }
      this.archivedMoves.set(conversationId, moved);
    }
    if (patch.archived === false && before.archived) {
      for (const id of this.archivedMoves.get(conversationId) ?? []) {
        const m = this.messages.get(id)!;
        const archive = this.roleFolder(m.accountId, 'archive');
        const inbox = this.roleFolder(m.accountId, 'inbox');
        if (archive && inbox) m.folderIds = [...m.folderIds.filter((fid) => fid !== archive.id), inbox.id];
      }
      this.archivedMoves.delete(conversationId);
    }
    this.emit({ type: 'conversation.updated', conversationId });
    for (const a of this.accounts) this.emit({ type: 'folder.updated', folderId: 0, accountId: a.id });
    return this.reply(this.findConversation(conversationId).conv);
  }

  private chatsOf(conversationId: number): data.ChatSpec[] {
    if (conversationId < 1000) return this.chats.filter((c) => c.id === conversationId);
    return this.chats.filter((c) => c.kind === 'direct' && c.memberIds[0] === conversationId - 1000);
  }

  async deleteConversation(conversationId: number): Promise<void> {
    const c = this.findConversation(conversationId);
    for (const id of c.mailIds) {
      const m = this.messages.get(id)!;
      const trash = this.roleFolder(m.accountId, 'trash');
      if (trash) m.folderIds = [trash.id];
    }
    const chatIds = new Set(this.chatsOf(conversationId).map((x) => x.id));
    this.chatMsgs = this.chatMsgs.filter((r) => !chatIds.has(r.chatId));
    this.convFlags.delete(conversationId);
    this.emit({ type: 'conversation.updated', conversationId });
    for (const a of this.accounts) this.emit({ type: 'folder.updated', folderId: 0, accountId: a.id });
    await this.reply(null);
  }

  async reactToItem(conversationId: number, itemId: string, emoji: string): Promise<{ reactions: Reaction[]; sentAsRfc9078?: boolean }> {
    const toggle = (list: Reaction[] = []): Reaction[] => {
      const out = structuredClone(list);
      const mine = out.find((r) => r.mine);
      if (mine) {
        mine.count--;
        mine.mine = false;
      }
      if (!mine || mine.emoji !== emoji) {
        const r = out.find((x) => x.emoji === emoji);
        if (r) {
          r.count++;
          r.mine = true;
        } else out.push({ emoji, count: 1, mine: true });
      }
      return out.filter((r) => r.count > 0);
    };
    let reactions: Reaction[];
    let sentAsRfc9078: boolean | undefined;
    if (itemId.startsWith('m:')) {
      reactions = toggle(this.mailReactions.get(itemId));
      this.mailReactions.set(itemId, reactions);
      sentAsRfc9078 = true; // demo: pretend the server/recipient supports RFC 9078
    } else {
      const rec = this.chatMsgs.find((r) => chatItemId(r.id) === itemId);
      if (!rec) throw new ApiError('Nachricht nicht gefunden', 404, 'not_found');
      rec.reactions = toggle(rec.reactions);
      reactions = rec.reactions;
    }
    this.emit({ type: 'conversation.updated', conversationId });
    return this.reply({ reactions, sentAsRfc9078 });
  }

  async deleteItem(conversationId: number, itemId: string): Promise<void> {
    const mid = itemId.startsWith('m:') ? Number(itemId.slice(2)) : null;
    if (mid != null) {
      const m = this.messages.get(mid);
      const trash = m && this.roleFolder(m.accountId, 'trash');
      if (m && trash) m.folderIds = [trash.id];
    } else {
      this.chatMsgs = this.chatMsgs.filter((r) => chatItemId(r.id) !== itemId);
    }
    this.emit({ type: 'conversation.updated', conversationId });
    await this.reply(null);
  }

  async forwardItem(itemId: string, targetConversationId: number): Promise<SendResult> {
    let text = '';
    let attachments: Attachment[] = [];
    let subject = '';
    if (itemId.startsWith('m:')) {
      const m = this.messages.get(Number(itemId.slice(2)));
      text = splitMail(m?.text ?? '').body;
      attachments = m?.attachments ?? [];
      subject = m?.subject ?? '';
    } else {
      const r = this.chatMsgs.find((x) => chatItemId(x.id) === itemId);
      text = r?.text ?? '';
      attachments = r?.attachments ?? [];
    }
    const chat = this.chatsOf(targetConversationId)[0];
    if (chat) {
      const rec = this.pushChat(chat.id, data.ME, text, 'sent');
      rec.attachments = structuredClone(attachments);
      rec.forwarded = true;
      this.convIdsForChat(chat).forEach((c) => this.emit({ type: 'conversation.updated', conversationId: c, itemId: chatItemId(rec.id) }));
      return this.reply({ conversationId: targetConversationId, itemId: chatItemId(rec.id), queued: false });
    }
    const target = this.findConversation(targetConversationId);
    const to = target.conv.participantIds
      .map((id) => this.person(id))
      .map((p) => ({ name: p.displayName, email: p.handles.find((h) => h.kind === 'email')?.value ?? '' }))
      .filter((a) => a.email);
    return this.sendMail({ channel: 'email', identityId: this.identities[0].id, to, cc: [], bcc: [], subject: 'Fwd: ' + normalizeSubject(subject || text.slice(0, 40)), text, html: '', attachments: [] });
  }

  async createFromItem(_conversationId: number, itemId: string, kind: 'task' | 'event'): Promise<{ id: string }> {
    const id = `${kind}-${this.createdFromItems.length + 1}`;
    this.createdFromItems.push({ id, itemId, kind });
    return this.reply({ id });
  }

  async saveContactCard(personId: number, card: ContactCard): Promise<void> {
    this.contactCards.set(personId, structuredClone(card));
    const p = this.person(personId);
    for (const ph of card.phones) {
      const v = ph.replace(/[^\d+]/g, '');
      if (!p.handles.some((h) => h.value === v)) p.handles.push({ kind: 'phone', value: v });
    }
    await this.reply(null);
  }

  async updateIdentity(identity: Identity): Promise<Identity> {
    this.identities = this.identities.map((i) => (i.id === identity.id ? structuredClone(identity) : i));
    return this.reply(identity);
  }

  async uploadAttachment(file: Blob, filename: string): Promise<{ uploadToken: string }> {
    const token = `mock-upload-${Date.now()}-${encodeURIComponent(filename)}-${file.size}`;
    try {
      if (typeof URL !== 'undefined' && URL.createObjectURL) this.uploads.set(token, URL.createObjectURL(file));
    } catch {
      /* no object URLs (tests) */
    }
    return this.reply({ uploadToken: token });
  }

  async send(msg: OutgoingMessage): Promise<SendResult> {
    if (msg.channel === 'email') return this.sendMail(msg);
    return this.sendChat(msg);
  }

  private sendMail(msg: OutgoingMessage): Promise<SendResult> {
    const ident = this.identities.find((i) => i.id === msg.identityId) ?? this.identities[0];
    if (msg.to.length + msg.cc.length + msg.bcc.length === 0) return Promise.reject(new ApiError('Kein Empfänger angegeben', 400, 'no_recipient'));
    const sent = this.folders.find((f) => f.accountId === ident.accountId && f.role === 'sent');
    const parentId = msg.inReplyToMessageId ?? msg.forwardOfMessageId;
    const parent = parentId != null ? this.messages.get(parentId) : undefined;
    const thread = parent ? this.meta.get(parent.id)!.thread : this.nextThread++;
    const passwords: string[] = [];
    const sharePasswords: { filename: string; password: string }[] = [];
    const attachments: Attachment[] = msg.attachments.map((a, i) => {
      const base: Attachment = { id: this.nextId * 10 + i, filename: a.filename, mimeType: a.mimeType, size: a.size };
      if (a.delivery === 'attach') return base;
      const host = { nextcloud: 'cloud.becker-familie.de', opencloud: 'opencloud.kanzlei-sommer.de', vaultwarden: 'vault.becker-familie.de' }[a.delivery];
      const token = Math.random().toString(36).slice(2, 10);
      const pw = Math.random().toString(36).slice(2, 10) + 'K7#q';
      passwords.push(pw);
      sharePasswords.push({ filename: a.filename, password: pw });
      const url = a.delivery === 'vaultwarden' ? `https://${host}/#/send/${token}/${token.split('').reverse().join('')}` : `https://${host}/s/${token}`;
      return { ...base, cloudLink: { provider: a.delivery, url, expiresAt: new Date(Date.now() + 14 * 86400_000).toISOString() } };
    });
    const id = this.nextId++;
    const now = new Date().toISOString();
    const detail: MessageDetail = {
      id,
      accountId: ident.accountId,
      folderIds: sent ? [sent.id] : [],
      conversationId: thread,
      messageId: `tm-${id}.${thread}@${ident.email.split('@')[1]}`,
      subject: msg.subject,
      from: [{ name: ident.name, email: ident.email }],
      to: msg.to,
      cc: msg.cc,
      bcc: msg.bcc,
      date: now,
      preview: preview(msg.text),
      flags: [SEEN],
      hasAttachments: attachments.length > 0,
      size: msg.text.length,
      inReplyTo: msg.inReplyToMessageId != null && parent ? [parent.messageId] : [],
      references: parent ? [...parent.references, parent.messageId] : [],
      text: msg.text,
      html: msg.html || undefined,
      attachments,
    };
    this.messages.set(id, detail);
    const recipients = [...msg.to, ...msg.cc, ...msg.bcc].map((a) => this.ensurePerson(a));
    this.meta.set(id, {
      thread,
      fromPerson: data.ME,
      personIds: [...new Set([data.ME, ...recipients])],
      relation: msg.inReplyToMessageId != null ? 'reply' : msg.forwardOfMessageId != null ? 'forward' : undefined,
    });
    if (parent) parent.flags.push(msg.inReplyToMessageId != null ? ANSWERED : FORWARDED);

    // Password delivery for cloud links (KONZEPT 12.2).
    const pd = msg.passwordDelivery;
    if (passwords.length && pd && pd.mode === 'separate_mail') {
      const pid = this.nextId++;
      const text = `Hallo,\n\ndas Passwort für die Freigabe aus meiner vorherigen Mail lautet:\n\n${passwords.join('\n')}`;
      this.messages.set(pid, { ...structuredClone(detail), id: pid, subject: 'Passwort: ' + msg.subject, text, html: undefined, preview: preview(text), attachments: [], hasAttachments: false, messageId: `tm-${pid}@${ident.email.split('@')[1]}` });
      this.meta.set(pid, { ...this.meta.get(id)! });
    }
    if (passwords.length && pd && pd.mode === 'messenger' && pd.messengerAccountId != null) {
      const target = recipients[0];
      const chat = this.findOrCreateDirectChat(pd.messengerAccountId, target);
      this.pushChat(chat.id, data.ME, `Passwort für „${msg.subject}": ${passwords.join(', ')}`, 'sent');
    }

    this.emit({ type: 'message.added', messageId: id, conversationId: thread, folderIds: detail.folderIds });
    this.emit({ type: 'conversation.updated', conversationId: thread, itemId: mailItemId(id) });
    if (sent) this.emit({ type: 'folder.updated', folderId: sent.id, accountId: ident.accountId });
    return this.reply({ messageId: id, conversationId: thread, itemId: mailItemId(id), queued: false, sharePasswords: sharePasswords.length ? sharePasswords : undefined });
  }

  private findOrCreateDirectChat(accountId: number, personId: number): data.ChatSpec {
    let chat = this.chats.find((c) => c.accountId === accountId && c.kind === 'direct' && c.memberIds[0] === personId);
    if (!chat) {
      chat = { id: Math.max(...this.chats.map((c) => c.id)) + 1, accountId: accountId as 3 | 4 | 5, kind: 'direct', memberIds: [personId] };
      this.chats.push(chat);
    }
    return chat;
  }

  private pushChat(chatId: number, from: number, text: string, status?: DeliveryStatus): ChatRecord {
    const rec: ChatRecord = { id: this.nextId++, chatId, from, sentAt: new Date().toISOString(), text, seen: from === data.ME, attachments: [], status };
    this.chatMsgs.push(rec);
    return rec;
  }

  private convIdsForChat(chat: data.ChatSpec): number[] {
    return chat.kind === 'direct' ? [chat.id, 1000 + chat.memberIds[0]] : [chat.id];
  }

  private sendChat(msg: OutgoingMessage): Promise<SendResult> {
    const accountId = msg.accountId ?? (msg.channel === 'signal' ? 4 : msg.channel === 'telegram' ? 5 : 3);
    let chat: data.ChatSpec | undefined;
    if (msg.conversationId != null && msg.conversationId < 1000) {
      chat = this.chats.find((c) => c.id === msg.conversationId && c.accountId === accountId);
    }
    if (!chat) {
      const pid = msg.conversationId != null && msg.conversationId >= 1000 ? msg.conversationId - 1000 : undefined;
      if (pid == null) return Promise.reject(new ApiError('Kein Chat für diese Konversation', 400, 'no_chat'));
      chat = this.findOrCreateDirectChat(accountId, pid);
    }
    const rec = this.pushChat(chat.id, data.ME, msg.text, 'pending');
    rec.attachments = msg.attachments.map((a, i) => {
      const url = this.uploads.get(a.uploadToken);
      const att: Attachment = { id: rec.id * 10 + i, filename: a.filename, mimeType: a.mimeType, size: a.size, durationSec: a.durationSec };
      if (url && a.mimeType.startsWith('image/')) att.previewUrl = url;
      if (url && a.mimeType.startsWith('audio/')) att.url = url;
      if (a.durationSec) att.waveform = waveform(rec.id);
      return att;
    });
    if (msg.replyToItemId?.startsWith('c:')) rec.replyTo = Number(msg.replyToItemId.slice(2));
    const convs = this.convIdsForChat(chat);
    const itemId = chatItemId(rec.id);
    const notify = () => convs.forEach((c) => this.emit({ type: 'conversation.updated', conversationId: c, itemId }));
    notify();
    const steps: [number, DeliveryStatus][] = [
      [400, 'sent'],
      [1200, 'delivered'],
      [2600, 'read'],
    ];
    for (const [ms, st] of steps) {
      setTimeout(() => {
        rec.status = st;
        notify();
      }, ms);
    }
    if (this.opts.autoReply !== false && chat.kind === 'direct' && !this.flags(msg.conversationId ?? chat.id).muted) {
      const target = chat;
      setTimeout(() => {
        const r = this.pushChat(target.id, target.memberIds[0], AUTO_REPLIES[Math.floor(Math.random() * AUTO_REPLIES.length)]);
        this.convIdsForChat(target).forEach((c) => this.emit({ type: 'conversation.updated', conversationId: c, itemId: chatItemId(r.id) }));
      }, 4200);
    }
    return this.reply({ conversationId: msg.conversationId ?? chat.id, itemId, queued: false });
  }

  async move(messageIds: number[], toFolderId: number): Promise<void> {
    const target = this.folder(toFolderId);
    if (!target) throw new ApiError('Ordner nicht gefunden', 404, 'not_found');
    const touched = new Set<number>([toFolderId]);
    for (const id of messageIds) {
      const m = this.messages.get(id);
      if (!m) continue;
      // Only the location changes; identity, relations and conversation stay (KONZEPT 6.5).
      m.folderIds.forEach((f) => touched.add(f));
      m.folderIds = [...m.folderIds.filter((f) => this.folder(f)?.accountId !== target.accountId), toFolderId];
      this.emit({ type: 'message.updated', messageId: id });
    }
    for (const f of touched) this.emit({ type: 'folder.updated', folderId: f, accountId: this.folder(f)?.accountId ?? 0 });
    await this.reply(null);
  }

  async setFlags(messageIds: number[], add: Flag[], remove: Flag[]): Promise<void> {
    const touched = new Set<number>();
    for (const id of messageIds) {
      const m = this.messages.get(id);
      if (!m) continue;
      m.flags = [...new Set([...m.flags.filter((f) => !remove.includes(f)), ...add])];
      m.folderIds.forEach((f) => touched.add(f));
      this.emit({ type: 'message.updated', messageId: id });
    }
    for (const f of touched) this.emit({ type: 'folder.updated', folderId: f, accountId: this.folder(f)?.accountId ?? 0 });
    await this.reply(null);
  }

  // ---- push notifications --------------------------------------------------

  private notificationSettings: NotificationSettings = {
    privacy: 'full',
    quietHours: { enabled: true, start: '22:00', end: '07:00' },
    accounts: {},
    channels: { email: true, whatsapp: true, signal: true, telegram: true },
  };
  private pushSubscriptions: PushSubscriptionInput[] = [];

  getPushVapidKey(): Promise<string> {
    // A syntactically valid demo key (uncompressed P-256 point, base64url).
    return this.reply('BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U');
  }
  async subscribePush(subscription: PushSubscriptionInput): Promise<void> {
    this.pushSubscriptions = [...this.pushSubscriptions.filter((s) => s.endpoint !== subscription.endpoint), subscription];
    await this.reply(null);
  }
  async unsubscribePush(endpoint: string): Promise<void> {
    this.pushSubscriptions = this.pushSubscriptions.filter((s) => s.endpoint !== endpoint);
    await this.reply(null);
  }
  getNotificationSettings(): Promise<NotificationSettings> {
    return this.reply(this.notificationSettings);
  }
  saveNotificationSettings(settings: NotificationSettings): Promise<NotificationSettings> {
    this.notificationSettings = structuredClone(settings);
    return this.reply(this.notificationSettings);
  }
  async sendTestNotification(): Promise<void> {
    // No push server in the mock: show the notification locally via the SW (or the page).
    const title = 'TuxMail';
    const body = this.notificationSettings.privacy === 'generic' ? 'Neue Nachricht' : 'Anna Müller: Hast du die Mail mit der Korrektur gesehen? 🙂';
    const data = { type: 'test', title, body, url: './#/mail', tag: 'tuxmail-test' };
    if (typeof navigator !== 'undefined' && 'serviceWorker' in navigator) {
      const reg = await navigator.serviceWorker.getRegistration();
      if (reg) {
        await reg.showNotification(title, { body, tag: data.tag, data, icon: './icons/icon-192.png', badge: './icons/badge-72.png' });
        return;
      }
    }
    if (typeof Notification !== 'undefined' && Notification.permission === 'granted') new Notification(title, { body, tag: data.tag });
    else throw new ApiError('Benachrichtigungen sind nicht erlaubt', 400, 'permission');
  }

  // ---- connect to a TuxMail server (device flow) ------------------------------

  private devicePolls = new Map<string, number>();

  async deviceAuthorize(serverUrl: string): Promise<DeviceAuthorization> {
    const url = serverUrl.replace(/\/+$/, '');
    if (!/^https?:\/\/[^/]+/.test(url)) throw new ApiError('Ungültige Server-Adresse', 400, 'invalid_url');
    const deviceCode = 'dev-' + Math.random().toString(36).slice(2);
    this.devicePolls.set(deviceCode, 0);
    return this.reply({
      deviceCode,
      userCode: 'WDJB-MJHT',
      verificationUri: `${url}/device`,
      verificationUriComplete: `${url}/device?user_code=WDJB-MJHT`,
      expiresIn: 600,
      interval: 2,
    });
  }

  async deviceToken(serverUrl: string, deviceCode: string): Promise<DeviceTokenResult> {
    if (!this.devicePolls.has(deviceCode)) return this.reply({ status: 'expired' });
    const n = this.devicePolls.get(deviceCode)! + 1;
    this.devicePolls.set(deviceCode, n);
    if (n < 3) return this.reply({ status: 'pending' });
    return this.reply({ status: 'approved', profile: this.demoProfile(serverUrl) });
  }

  async serverLogin(serverUrl: string, c: ServerLoginInput): Promise<DeviceTokenResult> {
    await sleep(this.latency * 4);
    if (!c.username || !c.password) return this.reply({ status: 'denied', error: 'Benutzername oder Passwort fehlt' });
    if (!c.totp || !/^\d{6}$/.test(c.totp)) return this.reply({ status: 'denied', error: 'Bitte den 6-stelligen TOTP-Code eingeben' });
    return this.reply({ status: 'approved', profile: this.demoProfile(serverUrl) });
  }

  private demoProfile(serverUrl: string): ProvisioningProfile {
    return {
      server: { url: serverUrl.replace(/\/+$/, ''), name: 'TuxMail Familie Becker', version: '1.0.0' },
      user: { name: 'Jonas Becker', email: 'jonas.becker@posteo.de' },
      accounts: this.accounts,
      identities: this.identities,
      signatures: this.signatures,
      workflows: [
        { id: 'invoices', name: 'Rechnungen ablegen', description: 'PDF-Rechnungen nach Nextcloud /Dokumente/Rechnungen' },
        { id: 'newsletter', name: 'Newsletter bündeln', description: 'Newsletter täglich um 18 Uhr zusammenfassen' },
        { id: 'vip', name: 'VIP-Benachrichtigungen', description: 'Nur Familie und Mandanten außerhalb der Ruhezeiten' },
      ],
    };
  }

  async importProvisioning(input: ProvisioningImport): Promise<void> {
    await sleep(this.latency * 6);
    if (input.accountIds.length === 0 && input.identityIds.length === 0) throw new ApiError('Nichts zum Importieren ausgewählt', 400, 'empty');
    await this.reply(null);
  }

  // ---- search ------------------------------------------------------------------

  private searchProfiles: SearchProfile[] = [
    {
      id: 'buchhaltung',
      name: 'Buchhaltung',
      pinned: true,
      query: {
        text: '(rechnung OR filename:*.pdf) AND NOT from:newsletter',
        mode: 'expert',
        scopes: ['mail'],
        accountIds: [],
        folderIds: [],
        includeSubfolders: true,
        excludeTrashSpam: true,
        location: 'auto',
        sort: 'date',
      },
    },
  ];
  private searchHistory: SearchHistoryEntry[] = [];

  private folderLabel(f: Folder): string {
    const names: Record<string, string> = { inbox: 'Posteingang', sent: 'Gesendet', drafts: 'Entwürfe', archive: 'Archiv', trash: 'Papierkorb', junk: 'Spam' };
    return f.role ? names[f.role] : f.name;
  }

  private searchDocs(): SearchDoc[] {
    const docs: SearchDoc[] = [];
    const acc = (id: number) => this.accounts.find((a) => a.id === id);
    const fmt = (a: Address) => (a.name ? `${a.name} <${a.email}>` : a.email);
    for (const m of this.messages.values()) {
      const meta = this.meta.get(m.id)!;
      const folders = m.folderIds.map((id) => this.folder(id)).filter((f): f is Folder => !!f);
      const text = (m.text ?? '').replace(/<[^>]+>/g, ' ');
      const cats: string[] = [];
      if (/rechnung|invoice/i.test(m.subject + ' ' + m.attachments.map((a) => a.filename).join(' '))) cats.push('rechnung');
      if (/newsletter|wochenrückblick/i.test(m.subject + ' ' + m.from[0]?.email)) cats.push('newsletter');
      if (m.accountId === 2) cats.push('arbeit');
      docs.push({
        id: 'm:' + m.id,
        kind: 'mail',
        title: m.subject,
        body: splitQuoted(text).body || text,
        from: m.from.map(fmt).join(', '),
        to: m.to.map(fmt).join(', '),
        cc: m.cc.map(fmt).join(', '),
        filenames: m.attachments.map((a) => a.filename),
        accountId: m.accountId,
        accountLabel: acc(m.accountId)?.displayName ?? '',
        folderIds: m.folderIds,
        folderNames: folders.map((f) => this.folderLabel(f)),
        inTrashOrSpam: this.isHidden(m),
        channel: 'email',
        categories: cats,
        date: m.date,
        size: m.size,
        unread: !m.flags.includes(SEEN),
        flagged: m.flags.includes(FLAGGED),
        answered: m.flags.includes(ANSWERED),
        hasCalendar: m.attachments.some((a) => a.mimeType === 'text/calendar'),
        senderName: m.from[0]?.name ?? m.from[0]?.email ?? '',
        senderEmail: m.from[0]?.email ?? '',
        messageId: m.id,
        conversationId: meta.thread,
        itemId: mailItemId(m.id),
      });
      for (const a of m.attachments) {
        docs.push({
          ...docs[docs.length - 1],
          id: `f:${a.id}`,
          kind: 'file',
          title: a.filename,
          body: m.subject,
          filenames: [a.filename],
          size: a.size,
        });
      }
    }
    for (const c of this.chatMsgs) {
      const chat = this.chats.find((x) => x.id === c.chatId)!;
      const sender = this.person(c.from);
      const account = acc(chat.accountId);
      const handle = sender.handles.find((h) => h.kind === this.chatChannel(chat))?.value ?? '';
      docs.push({
        id: chatItemId(c.id),
        kind: 'chat',
        title: chat.title ?? this.person(chat.memberIds[0]).displayName,
        body: c.text,
        from: `${sender.displayName} <${handle}>`,
        to: chat.memberIds.map((id) => this.person(id).displayName).join(', '),
        cc: '',
        filenames: c.attachments.map((a) => a.filename),
        accountId: chat.accountId,
        accountLabel: account?.displayName ?? '',
        folderIds: [],
        folderNames: [],
        inTrashOrSpam: false,
        channel: this.chatChannel(chat),
        categories: [],
        date: c.sentAt,
        size: c.text.length + c.attachments.reduce((n, a) => n + a.size, 0),
        unread: !c.seen,
        flagged: false,
        answered: false,
        hasCalendar: false,
        senderName: sender.displayName,
        senderEmail: '',
        conversationId: chat.id,
        itemId: chatItemId(c.id),
      });
    }
    for (const p of this.persons) {
      if (p.isMe) continue;
      docs.push({
        id: 'p:' + p.id,
        kind: 'contact',
        title: p.displayName,
        body: p.handles.map((h) => h.value).join(' · '),
        from: `${p.displayName} <${p.handles.find((h) => h.kind === 'email')?.value ?? ''}>`,
        to: '',
        cc: '',
        filenames: [],
        accountLabel: '',
        folderIds: [],
        folderNames: [],
        inTrashOrSpam: false,
        categories: [],
        size: 0,
        unread: false,
        flagged: false,
        answered: false,
        hasCalendar: false,
        senderName: p.displayName,
        senderEmail: p.handles.find((h) => h.kind === 'email')?.value ?? '',
        personId: p.id,
      });
    }
    return docs;
  }

  async search(query: SearchQuery): Promise<SearchResult> {
    const result = runSearch(this.searchDocs(), query, {
      now: new Date(),
      descendants: (id) => {
        const out: number[] = [];
        const walk = (pid: number) => this.folders.filter((f) => f.parentId === pid).forEach((f) => (out.push(f.id), walk(f.id)));
        walk(id);
        return out;
      },
      monthLabel: (y, m) => new Date(y, m, 1).toLocaleDateString(undefined, { month: 'short', year: '2-digit' }),
    });
    if (query.text.trim() && !query.cursor) {
      this.searchHistory = [{ text: query.text, mode: query.mode, at: new Date().toISOString() }, ...this.searchHistory.filter((h) => h.text !== query.text)].slice(0, 20);
    }
    return this.reply(result);
  }
  listSearchProfiles(): Promise<SearchProfile[]> {
    return this.reply(this.searchProfiles);
  }
  async saveSearchProfile(profile: SearchProfile): Promise<SearchProfile> {
    const p = { ...structuredClone(profile), id: profile.id || 'p-' + Math.random().toString(36).slice(2, 8) };
    this.searchProfiles = [...this.searchProfiles.filter((x) => x.id !== p.id), p];
    return this.reply(p);
  }
  async deleteSearchProfile(id: string): Promise<void> {
    this.searchProfiles = this.searchProfiles.filter((p) => p.id !== id);
    await this.reply(null);
  }
  getSearchHistory(): Promise<SearchHistoryEntry[]> {
    return this.reply(this.searchHistory);
  }
  async clearSearchHistory(): Promise<void> {
    this.searchHistory = [];
    await this.reply(null);
  }

  // ---- preferences (in memory, mirrored to localStorage when available) --------

  private static PREFS_KEY = 'tuxmail.mock.preferences.v1';

  async getPreferences(): Promise<UserPreferences> {
    let stored: Partial<UserPreferences> = {};
    try {
      stored = JSON.parse(globalThis.localStorage?.getItem(MockTransport.PREFS_KEY) ?? '{}') as Partial<UserPreferences>;
    } catch {
      /* no storage */
    }
    const prefs: UserPreferences = { locale: stored.locale ?? 'system', region: { ...DEFAULT_REGION, ...(stored.region ?? {}) } };
    return this.reply(prefs);
  }
  async setPreferences(prefs: UserPreferences): Promise<UserPreferences> {
    try {
      globalThis.localStorage?.setItem(MockTransport.PREFS_KEY, JSON.stringify(prefs));
    } catch {
      /* no storage */
    }
    return this.reply(prefs);
  }

  // ---- extra languages (none in the demo) -------------------------------------

  listLocales(): Promise<LocaleInfo[]> {
    return this.reply([]);
  }
  getLocale(code: string): Promise<Record<string, unknown>> {
    return Promise.reject(new ApiError(`Locale ${code} not available`, 404, 'not_found'));
  }

  subscribe(handler: (event: ApiEvent) => void): Unsubscribe {
    const off = this.hub.add(handler);
    if (this.opts.simulateIncoming && !this.incomingTimer) {
      this.incomingTimer = setTimeout(() => {
        const r = this.pushChat(102, 13, 'Bin um 18 Uhr da – soll ich noch was mitbringen?');
        this.emit({ type: 'conversation.updated', conversationId: 102, itemId: chatItemId(r.id) });
      }, 90_000);
    }
    return () => {
      off();
      if (this.hub.size === 0 && this.incomingTimer) {
        clearTimeout(this.incomingTimer);
        this.incomingTimer = null;
      }
    };
  }
}
