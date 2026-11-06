// SPDX-License-Identifier: AGPL-3.0-or-later
//
// In-memory comfort API for the demo/mock transport: derives insights
// (category, spam score, newsletter, e-invoice, S/MIME) from the mock mails.

import type { MessageSummary, Workflow } from '../types';
import { ApiError } from './api';
import { folders, mails, persons, type MailSpec } from './mockData';
import type {
  CategoryId,
  CategoryInfo,
  ComfortApi,
  ComfortSettings,
  MergeJob,
  MergeJobInput,
  MergeRecipient,
  MergeRendered,
  MergeTable,
  MeetingLink,
  MessageInsights,
  SmimeCertificate,
  PgpKey,
  Subscription,
  UnsubscribeResult,
} from './comfort';
import { CATEGORIES } from './comfort';

const DAY = 86400_000;

function email(personId: number): string {
  return persons.find((p) => p.id === personId)?.handles.find((h) => h.kind === 'email')?.value ?? '';
}
function name(personId: number): string {
  return persons.find((p) => p.id === personId)?.displayName ?? '';
}
function when(m: MailSpec): string {
  const [days, hm] = m.at;
  const d = new Date(Date.now() - days * DAY);
  const [h, mi] = hm.split(':').map(Number);
  d.setHours(h, mi, 0, 0);
  return d.toISOString();
}

export function mockCategory(m: MailSpec): CategoryId {
  const from = email(m.from);
  const s = (m.subject + ' ' + m.text).toLowerCase();
  const atts = (m.attachments ?? []).map((a) => a[0].toLowerCase());
  if (from.startsWith('newsletter@') || s.includes('wochenrückblick')) return 'newsletter';
  if (atts.some((a) => a.endsWith('.ics')) || /termin|meeting|einladung/.test(m.subject.toLowerCase())) return 'appointment';
  if (/rechnung|bestellung|buchung|invoice/.test(s) || atts.some((a) => /rechnung|invoice/.test(a))) return 'invoice';
  if (/noreply|no-reply|^it@/.test(from) || /sendung|zertifikat/.test(s)) return 'notification';
  return 'personal';
}

const NEWSLETTER = { http: 'https://www.heise.de/newsletter/abmelden?id=demo', mailto: 'mailto:abmelden@heise.de', oneClick: true, listId: 'wochenrueckblick.heise.de', method: 'one_click' as const };

function insightFor(m: MailSpec, overrides: Map<number, CategoryId>, spamOverride: Map<number, boolean>): MessageInsights {
  const from = email(m.from);
  const spamMail = from.endsWith('.biz');
  const sp = spamOverride.get(m.id) ?? spamMail;
  const out: MessageInsights = {
    messageId: m.id,
    spam: sp ? { score: 97, level: 'spam', server: true, reasons: ['server score 11.2/5.0', 'dmarc=fail'] } : { score: from.endsWith('.biz') ? 40 : 3, level: 'ham' },
    category: overrides.get(m.id) ?? mockCategory(m),
    categorySource: overrides.has(m.id) ? 'user' : 'rule',
    sender: from,
    senderName: name(m.from),
  };
  if (from === 'newsletter@heise.de') out.unsubscribe = { ...NEWSLETTER };
  if (m.id === 29) {
    out.invoice = {
      format: 'facturx', number: 'RE-2026-11-4711', issued: new Date(Date.now() - DAY).toISOString(), due: new Date(Date.now() + 12 * DAY).toISOString(),
      amount: '87.40', currency: 'EUR', seller: 'SWM Versorgungs GmbH', iban: 'DE89370400440532013000', reference: '4711081542', source: 'Rechnung_2026-11.pdf',
    };
  }
  if (m.id === 4) out.smime = { present: true, signed: true, encrypted: false, decrypted: false, signature: 'trusted', signerEmail: from, signerName: name(m.from), issuer: 'D-TRUST Mail CA 2-2 2023' };
  if (m.id === 7) out.pgp = { present: true, encrypted: true, decrypted: true, signed: true, signature: 'valid', signerTrust: 'tofu', signerFingerprint: 'C5AA1F0E7B3D42189E60D1A4F3B27C9E1D80A6F4' };
  if (m.id === 11) out.smime = { present: true, signed: true, encrypted: true, decrypted: true, signature: 'untrusted', signerEmail: from, signerName: name(m.from), issuer: 'Kanzlei Sommer interne CA' };
  return out;
}

function summary(m: MailSpec): MessageSummary {
  return {
    id: m.id, accountId: m.account, folderIds: m.folders, conversationId: 1000 + m.thread, messageId: `${m.id}@mock`, subject: m.subject,
    from: [{ name: name(m.from), email: email(m.from) }], to: m.to.map((p) => ({ name: name(p), email: email(p) })), cc: [], date: when(m),
    preview: m.text.slice(0, 120), flags: m.seen ? ['\\Seen'] : [], hasAttachments: !!m.attachments?.length, size: m.text.length,
  };
}

function render(tpl: string, r: MergeRecipient, missing: Set<string>, html = false): string {
  return tpl.replace(/\{\{\s*([^{}|]+?)\s*(?:\|\s*([^{}]*?)\s*)?\}\}/g, (_m, k: string, def?: string) => {
    const key = k.toLowerCase();
    const f = r.fields ?? {};
    let v = f[key] ?? (key === 'email' ? r.email : key === 'name' ? r.name : key === 'vorname' ? r.name?.split(' ')[0] : undefined);
    if (!v) {
      if (def !== undefined) v = def;
      else missing.add(key);
    }
    v = v ?? '';
    return html ? v.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!) : v;
  });
}

const DICT_WRONG: Record<string, string[]> = {
  rechnug: ['Rechnung'], vieleicht: ['vielleicht'], morgn: ['morgen', 'Morgen'], grüse: ['Grüße'], teh: ['the'], recieve: ['receive'],
  wiederhohlen: ['wiederholen'], standart: ['Standard'], seperat: ['separat'], definately: ['definitely'],
};

export function comfortMock(): ComfortApi {
  let settings: ComfortSettings = {
    spam: { enabled: true, unsure: 50, spam: 90, moveToJunk: false, serverLearn: true },
    categories: { enabled: true, aiRefine: false },
    spell: { enabled: true, language: 'auto' },
    meeting: { defaultProvider: 'jitsi', jitsiBaseUrl: 'https://meet.jit.si' },
    merge: { perMinute: 30 },
    smime: { signByDefault: false, encryptAutomatically: true, prefer: 'pgp' },
    pgp: { signByDefault: false, sendAutocrypt: true, preferEncrypt: false },
    pdf: { useChromium: true, nameTemplate: '{{datum}}_{{absender}}_{{betreff}}.pdf' },
    newsletter: { showBar: true },
  };
  const overrides = new Map<number, CategoryId>();
  const spamOverride = new Map<number, boolean>();
  const words: string[] = ['Tuxmail'];
  const unsubscribed = new Set<string>();
  const autoArchive = new Set<string>();
  const jobs = new Map<string, MergeJob>();
  let pgpKeys: PgpKey[] = [];
  let certs: SmimeCertificate[] = [
    { id: 1, email: 'jonas.becker@posteo.de', subject: 'Jonas Becker', issuer: 'Actalis Client Authentication CA G3', fingerprint: '3F:9A:…:C1', notBefore: new Date(Date.now() - 90 * DAY).toISOString(), notAfter: new Date(Date.now() + 275 * DAY).toISOString(), own: true, trusted: true, source: 'own', expired: false, canEncrypt: true },
    { id: 2, email: 'l.schneider@schneider-kg.de', subject: 'Lisa Schneider', issuer: 'D-TRUST Mail CA 2-2 2023', fingerprint: '8B:12:…:7E', notBefore: new Date(Date.now() - 200 * DAY).toISOString(), notAfter: new Date(Date.now() + 165 * DAY).toISOString(), own: false, trusted: false, source: 'collected', expired: false, canEncrypt: true },
  ];
  const wait = <T>(v: T) => new Promise<T>((r) => setTimeout(() => r(v), 40));
  const live = () => mails.filter((m) => !m.draft);
  const byId = (id: number) => mails.find((m) => m.id === id);
  const inbox = new Set(folders.filter((f) => f.role === 'inbox').map((f) => f.id));

  const api: ComfortApi = {
    getSettings: () => wait(structuredClone(settings)),
    saveSettings: (s) => wait((settings = structuredClone(s))),
    insights: (id) => {
      const m = byId(id);
      if (!m) return Promise.reject(new ApiError('not found', 404, 'not_found'));
      const ins = insightFor(m, overrides, spamOverride);
      if (ins.unsubscribe && unsubscribed.has(ins.unsubscribe.listId ?? '')) ins.unsubscribed = true;
      return wait(ins);
    },
    markSpam: async (ids, spam) => {
      for (const id of ids) spamOverride.set(id, spam);
    },
    setCategory: async (ids, c) => {
      for (const id of ids) overrides.set(id, c);
    },
    listCategories: async (role) => {
      const list = live().filter((m) => !role || (role === 'inbox' && m.folders.some((f) => inbox.has(f))));
      return CATEGORIES.map((id): CategoryInfo => {
        const ms = list.filter((m) => (overrides.get(m.id) ?? mockCategory(m)) === id);
        return { id, total: ms.length, unread: ms.filter((m) => !m.seen).length };
      });
    },
    categoryMessages: async (c, role) =>
      live()
        .filter((m) => (!role || (role === 'inbox' && m.folders.some((f) => inbox.has(f)))) && (overrides.get(m.id) ?? mockCategory(m)) === c)
        .map(summary),
    messageCategories: async (ids) => {
      const out: Record<number, CategoryId> = {};
      for (const id of ids) {
        const m = byId(id);
        if (m) out[id] = overrides.get(id) ?? mockCategory(m);
      }
      return out;
    },
    listSubscriptions: async (): Promise<Subscription[]> => {
      const heise = live().filter((m) => email(m.from) === 'newsletter@heise.de');
      const shop: Subscription = {
        key: 'news.bueromarkt-boettcher.de', sender: 'service@bueromarkt-boettcher.de', name: 'Büromarkt Böttcher', count: 14, unread: 3, inInbox: 2, perMonth: 4.3,
        firstMail: new Date(Date.now() - 95 * DAY).toISOString(), lastMail: new Date(Date.now() - 2 * DAY).toISOString(),
        unsubscribe: { mailto: 'mailto:abmelden@bueromarkt-boettcher.de', oneClick: false, method: 'mailto' }, autoArchive: autoArchive.has('news.bueromarkt-boettcher.de'),
      };
      const out: Subscription[] = [
        {
          key: NEWSLETTER.listId, sender: 'newsletter@heise.de', name: 'heise online', count: heise.length + 22, unread: heise.filter((m) => !m.seen).length, inInbox: heise.length,
          perMonth: 4.2, firstMail: new Date(Date.now() - 180 * DAY).toISOString(), lastMail: heise.map(when).sort().pop() ?? new Date().toISOString(),
          unsubscribe: { ...NEWSLETTER }, autoArchive: autoArchive.has(NEWSLETTER.listId),
        },
        shop,
        {
          key: 'angebote.shop.example', sender: 'bestellung@shop.example', name: 'Online-Shop', count: 31, unread: 0, inInbox: 0, perMonth: 10.3,
          firstMail: new Date(Date.now() - 90 * DAY).toISOString(), lastMail: new Date(Date.now() - 12 * DAY).toISOString(),
          unsubscribe: { http: 'https://shop.example/newsletter/off', oneClick: true, method: 'one_click' }, autoArchive: autoArchive.has('angebote.shop.example'),
        },
      ];
      for (const s of out) if (unsubscribed.has(s.key)) s.unsubscribed = new Date().toISOString();
      return out;
    },
    unsubscribe: async (key): Promise<UnsubscribeResult> => {
      unsubscribed.add(key);
      return { method: 'one_click', done: true, status: 200 };
    },
    unsubscribeMessage: async (id) => {
      const ins = await api.insights(id);
      if (!ins.unsubscribe) throw new ApiError('the mail has no unsubscribe link', 400, 'invalid_input');
      unsubscribed.add(ins.unsubscribe.listId ?? '');
      return { method: ins.unsubscribe.method, done: true, status: 200 };
    },
    archiveSubscription: async () => 2,
    autoArchiveSubscription: async (key) => {
      autoArchive.add(key);
      return { id: 'wf-archive-' + key, name: 'Newsletter archivieren: ' + key, enabled: true, trigger: { event: 'mail.received' }, condition: { expr: `mail.headers["list-id"] contains "${key}"` }, actions: [{ type: 'archive', params: {} }] } as unknown as Workflow;
    },
    createInvoiceTask: async () => ({ id: 'task-' + Math.random().toString(36).slice(2, 8) }),
    spellCheck: async (text, language) => {
      const misspellings = [];
      const re = /[\p{L}][\p{L}'’-]*/gu;
      for (const m of text.matchAll(re)) {
        const w = m[0];
        const sug = DICT_WRONG[w.toLowerCase()];
        if (sug && !words.includes(w)) misspellings.push({ word: w, start: m.index!, end: m.index! + w.length, byteStart: m.index!, suggestions: sug });
      }
      const lang = language && language !== 'auto' ? language : /\b(the|and|you|is)\b/i.test(text) ? 'en' : 'de';
      return { language: lang, detected: !language || language === 'auto', misspellings };
    },
    spellWords: async () => [...words],
    addSpellWord: async (w) => void words.push(w),
    deleteSpellWord: async (w) => void words.splice(words.indexOf(w), 1),
    printUrl: (o) => `data:text/html;charset=utf-8,${encodeURIComponent(mockPrint(o.messageId ?? 0))}`,
    pdfUrl: () => '#',
    exportPdfs: async () => ({ blob: new Blob(['PK'], { type: 'application/zip' }) }),
    parseMergeTable: async (file): Promise<MergeTable> => {
      const text = await file.text();
      const [head, ...rows] = text.split(/\r?\n/).filter(Boolean);
      const sep = (head.match(/;/g)?.length ?? 0) > (head.match(/,/g)?.length ?? 0) ? ';' : ',';
      const cols = head.split(sep).map((c) => c.trim().toLowerCase());
      const ei = Math.max(0, cols.findIndex((c) => /mail/.test(c)));
      return {
        columns: cols,
        recipients: rows.map((r) => {
          const v = r.split(sep).map((x) => x.trim());
          return { email: v[ei], fields: Object.fromEntries(cols.map((c, i) => [c, v[i] ?? ''])) };
        }),
      };
    },
    mergeFromGroup: async (): Promise<MergeTable> => ({
      columns: ['vorname', 'nachname', 'email', 'firma'],
      recipients: [
        { email: 'anna.mueller@mueller-gmbh.de', name: 'Anna Müller', fields: { vorname: 'Anna', nachname: 'Müller', email: 'anna.mueller@mueller-gmbh.de', firma: 'Müller GmbH' } },
        { email: 'l.schneider@schneider-kg.de', name: 'Lisa Schneider', fields: { vorname: 'Lisa', nachname: 'Schneider', email: 'l.schneider@schneider-kg.de', firma: 'Schneider KG' } },
        { email: 'm.krause@example.org', name: 'Martin Krause', fields: { vorname: 'Martin', nachname: 'Krause', email: 'm.krause@example.org', firma: '' } },
      ],
    }),
    previewMerge: async (input: MergeJobInput): Promise<MergeRendered[]> =>
      input.recipients.slice(0, 10).map((r) => {
        const missing = new Set<string>();
        return { recipient: r, subject: render(input.template.subject, r, missing), text: render(input.template.text ?? '', r, missing), html: render(input.template.html ?? '', r, missing, true), missing: [...missing] };
      }),
    startMerge: async (input) => {
      const job: MergeJob = {
        id: Math.random().toString(36).slice(2, 10), identityId: input.identityId, template: input.template, recipients: input.recipients, perMinute: input.perMinute ?? 30,
        log: input.recipients.map((r, i) => ({ index: i, email: r.email, status: 'pending', attempts: 0 })), state: 'running', created: new Date().toISOString(), sent: 0, failed: 0, pending: input.recipients.length,
      };
      jobs.set(job.id, job);
      const tick = () => {
        const e = job.log.find((l) => l.status === 'pending');
        if (!e || job.state !== 'running') {
          if (job.state === 'running') job.state = 'done';
          return;
        }
        e.attempts++;
        if (e.email.endsWith('example.org') && e.attempts === 1) {
          e.status = 'failed';
          e.error = '550 mailbox unavailable';
        } else {
          e.status = 'sent';
          e.sentAt = new Date().toISOString();
        }
        job.sent = job.log.filter((l) => l.status === 'sent').length;
        job.failed = job.log.filter((l) => l.status === 'failed').length;
        job.pending = job.log.filter((l) => l.status === 'pending').length;
        setTimeout(tick, 400);
      };
      setTimeout(tick, 300);
      return structuredClone(job);
    },
    listMergeJobs: async () => [...jobs.values()].map((j) => structuredClone(j)),
    getMergeJob: async (id) => {
      const j = jobs.get(id);
      if (!j) throw new ApiError('not found', 404, 'not_found');
      return structuredClone(j);
    },
    retryMergeJob: async (id) => {
      const j = jobs.get(id);
      if (!j) throw new ApiError('not found', 404, 'not_found');
      for (const e of j.log) if (e.status === 'failed') ((e.status = 'sent'), (e.attempts += 1), (e.error = undefined));
      j.sent = j.log.filter((l) => l.status === 'sent').length;
      j.failed = 0;
      return structuredClone(j);
    },
    cancelMergeJob: async (id) => {
      const j = jobs.get(id);
      if (j) j.state = 'cancelled';
    },
    createMeeting: async (input): Promise<MeetingLink> => {
      const p = input.provider ?? settings.meeting.defaultProvider;
      const slug = (input.title || 'TuxMail').replace(/[^A-Za-z0-9]/g, '').slice(0, 24) || 'TuxMail';
      const rnd = Math.random().toString(36).slice(2, 10) + Math.random().toString(36).slice(2, 10);
      if (p === 'teams') throw new ApiError('teams: provider not configured', 501, 'not_configured');
      const link =
        p === 'nextcloud_talk'
          ? { provider: p, url: `https://cloud.becker-familie.de/call/${rnd.slice(0, 8)}`, label: 'Nextcloud Talk', password: rnd.slice(8, 20) }
          : { provider: p, url: `${settings.meeting.jitsiBaseUrl.replace(/\/$/, '')}/${slug}-${rnd.slice(0, 16)}`, label: 'Jitsi Meet' };
      const de = (input.lang ?? 'de').startsWith('de');
      const description = `${de ? 'Online-Meeting beitreten' : 'Join the online meeting'} (${link.label}): ${link.url}${link.password ? `\n${de ? 'Passwort' : 'Password'}: ${link.password}` : ''}`;
      return { ...link, description, conference: `CONFERENCE;VALUE=URI;FEATURE=AUDIO,VIDEO;LABEL=${link.label}:${link.url}` } as MeetingLink;
    },
    listCertificates: async () => structuredClone(certs),
    importCertificate: async (file, password) => {
      const own = /\.(p12|pfx)$/i.test(file.name) || !!password;
      const c: SmimeCertificate = {
        id: certs.length + 1, email: own ? 'j.becker@kanzlei-sommer.de' : file.name.replace(/\.[^.]+$/, '') + '@example.org', subject: own ? 'Jonas Becker' : file.name, issuer: 'Demo CA',
        fingerprint: 'AA:BB:…:' + certs.length, notBefore: new Date().toISOString(), notAfter: new Date(Date.now() + 365 * DAY).toISOString(), own, trusted: true, source: own ? 'own' : 'imported', expired: false, canEncrypt: true,
      };
      certs.push(c);
      return c;
    },
    trustCertificate: async (id, trusted) => {
      certs = certs.map((c) => (c.id === id ? { ...c, trusted } : c));
    },
    deleteCertificate: async (id) => {
      certs = certs.filter((c) => c.id !== id);
    },
    listPgpKeys: async () => structuredClone(pgpKeys),
    unlockPgpKey: async (id) => {
      pgpKeys = pgpKeys.map((k) => (k.id === id ? { ...k, locked: false } : k));
      return structuredClone(pgpKeys.find((k) => k.id === id)!);
    },
    changePgpPassphrase: async (id, _current, next) => {
      pgpKeys = pgpKeys.map((k) => (k.id === id ? { ...k, protected: !!next, locked: !!next } : k));
      return structuredClone(pgpKeys.find((k) => k.id === id)!);
    },
    lockPgpKeys: async () => {
      pgpKeys = pgpKeys.map((k) => ({ ...k, locked: k.protected }));
    },
    generatePgpKey: async ({ name: n, email, passphrase }) => {
      const k: PgpKey = { protected: !!passphrase, locked: !!passphrase, id: pgpKeys.length + 1, fingerprint: 'A1B2C3D4E5F60718293A4B5C6D7E8F9012345678', email, userId: `${n} <${email}>`, algorithm: 'Ed25519', created: new Date().toISOString(), status: 'valid', own: true, canEncrypt: true, canSign: true, source: 'own', trust: 'verified' };
      pgpKeys.push(k);
      return k;
    },
    importPgpKeys: async (file) => {
      const email = file.name.replace(/\.[^.]+$/, '') + '@example.org';
      const k: PgpKey = { protected: false, locked: false, id: pgpKeys.length + 1, fingerprint: 'F0E1D2C3B4A5968778695A4B3C2D1E0F12345678', email, userId: email, algorithm: 'Ed25519', created: new Date().toISOString(), status: 'valid', own: false, canEncrypt: true, canSign: false, source: 'manual', trust: 'unknown' };
      pgpKeys.push(k);
      return [k];
    },
    exportPgpPublicKey: async () => '-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nDemo\n-----END PGP PUBLIC KEY BLOCK-----\n',
    exportPgpSecretKey: async () => '-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nDemo\n-----END PGP PRIVATE KEY BLOCK-----\n',
    verifyPgpKey: async (id, verified) => {
      pgpKeys = pgpKeys.map((k) => (k.id === id ? { ...k, trust: verified ? 'verified' : 'unknown' } : k));
    },
    deletePgpKey: async (id) => {
      pgpKeys = pgpKeys.filter((k) => k.id !== id);
    },
    lookupPgpKey: async (email) => {
      throw new ApiError(`no key for ${email}`, 404, 'pgp_key_not_found');
    },
    listPgpPeers: async () => [],
    verifyPgpPeer: async () => {
      throw new ApiError('not found', 404, 'not_found');
    },
    deletePgpPeer: async () => {},
    acceptPgpPeer: async () => {},
    failedSends: async () => [],
    dismissFailedSend: async () => {},
    pgpStatus: async (from, recipients) => {
      const canSign = pgpKeys.some((k) => k.own && k.email === from.toLowerCase());
      const missing = recipients.filter((r) => !pgpKeys.some((k) => k.email === r.toLowerCase()));
      return { canSign, canEncrypt: canSign && recipients.length > 0 && missing.length === 0, missing };
    },
    smimeStatus: async (from, recipients) => {
      const canSign = certs.some((c) => c.own && c.email === from.toLowerCase());
      const missing = recipients.filter((r) => !certs.some((c) => !c.own && c.email === r.toLowerCase()));
      return { canSign, canEncrypt: canSign && recipients.length > 0 && missing.length === 0, missing };
    },
  };
  return api;
}

function mockPrint(id: number): string {
  const m = mails.find((x) => x.id === id);
  if (!m) return '<p>—</p>';
  const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!);
  return `<!doctype html><html><head><meta charset="utf-8"><style>body{font:14px system-ui;margin:24px}th{text-align:left;color:#555;padding-right:12px}pre{white-space:pre-wrap;font:inherit}</style></head><body>
<h1>${esc(m.subject)}</h1><table><tr><th>Von</th><td>${esc(name(m.from))} &lt;${esc(email(m.from))}&gt;</td></tr><tr><th>Datum</th><td>${esc(new Date(when(m)).toLocaleString('de-DE'))}</td></tr></table><hr><pre>${esc(m.text)}</pre></body></html>`;
}
