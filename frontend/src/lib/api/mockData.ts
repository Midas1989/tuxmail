// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Demo data for the MockTransport: two mail accounts (private IMAP, work EAS),
// WhatsApp and Signal, nested folders, ~40 mails in reply/forward threads and
// messenger chats with the same people (for the mixed person timeline).
// All names, addresses and phone numbers are fictitious.

import type { Account, Folder, Identity, Person, Signature } from '../types';

export const ME = 1;

export const accounts: Account[] = [
  {
    id: 1,
    kind: 'imap',
    displayName: 'Privat',
    color: '#2563eb',
    incoming: { host: 'posteo.de', port: 993, tls: 'tls', auth: 'password', username: 'jonas.becker@posteo.de' },
    outgoing: { host: 'posteo.de', port: 465, tls: 'tls', auth: 'password', username: 'jonas.becker@posteo.de' },
    extra: { address: 'jonas.becker@posteo.de' },
    enabled: true,
    sortOrder: 0,
  },
  {
    id: 2,
    kind: 'eas',
    displayName: 'Kanzlei Sommer',
    color: '#9333ea',
    incoming: { host: 'mail.kanzlei-sommer.de', port: 443, tls: 'tls', auth: 'password', username: 'j.becker' },
    extra: { address: 'j.becker@kanzlei-sommer.de' },
    enabled: true,
    sortOrder: 1,
  },
  {
    id: 3,
    kind: 'whatsapp',
    displayName: 'WhatsApp',
    color: '#16a34a',
    extra: { address: '+49 151 2345678' },
    enabled: true,
    sortOrder: 2,
  },
  {
    id: 4,
    kind: 'signal',
    displayName: 'Signal',
    color: '#3a76f0',
    extra: { address: '+49 151 2345678' },
    enabled: true,
    sortOrder: 3,
  },
  {
    id: 5,
    kind: 'telegram',
    displayName: 'Telegram',
    color: '#229ed9',
    extra: { address: '@jonasbecker' },
    enabled: true,
    sortOrder: 4,
  },
];

export const signatures: Signature[] = [
  { id: 1, name: 'Privat', html: '<p>Viele Grüße<br>Jonas</p>', plain: 'Viele Grüße\nJonas', placement: 'below_reply' },
  { id: 2, name: 'Familie', html: '<p>Liebe Grüße, Jonas</p>', plain: 'Liebe Grüße, Jonas', placement: 'below_reply' },
  {
    id: 3,
    name: 'Kanzlei',
    html: '<p>Mit freundlichen Grüßen</p><p><b>Jonas Becker</b><br>Rechtsanwalt<br>Kanzlei Sommer &amp; Partner<br>Tel. +49 89 123456-12</p>',
    plain: 'Mit freundlichen Grüßen\n\nJonas Becker\nRechtsanwalt\nKanzlei Sommer & Partner\nTel. +49 89 123456-12',
    placement: 'below_reply',
  },
  { id: 4, name: 'Kurz (Kanzlei)', html: '<p>Jonas Becker · Kanzlei Sommer · +49 89 123456-12</p>', plain: 'Jonas Becker · Kanzlei Sommer · +49 89 123456-12', placement: 'below_reply' },
  { id: 5, name: 'Kurz (Privat)', html: '<p>Jonas · 0151 2345678</p>', plain: 'Jonas · 0151 2345678', placement: 'below_reply' },
];

export const identities: Identity[] = [
  { id: 1, accountId: 1, name: 'Jonas Becker', email: 'jonas.becker@posteo.de', bccSelf: false, defaultSignatureId: 1, replySignatureId: 1, isDefault: true, chatSignatureMode: 'full', chatSignatureId: 5 },
  { id: 2, accountId: 1, name: 'Jonas Becker', email: 'jonas@becker-familie.de', bccSelf: false, defaultSignatureId: 2, replySignatureId: 2, isDefault: false, chatSignatureMode: 'full' },
  { id: 3, accountId: 2, name: 'Jonas Becker', email: 'j.becker@kanzlei-sommer.de', bccSelf: false, defaultSignatureId: 3, replySignatureId: 3, isDefault: true, chatSignatureMode: 'short', chatSignatureId: 4 },
  { id: 4, accountId: 2, name: 'Kanzlei Sommer – Info', email: 'info@kanzlei-sommer.de', bccSelf: true, defaultSignatureId: 3, isDefault: false },
];

type F = Omit<Folder, 'unread' | 'total' | 'subscribed' | 'remoteId'> & { remoteId?: string };
const folderSpecs: F[] = [
  { id: 10, accountId: 1, name: 'Posteingang', role: 'inbox' },
  { id: 11, accountId: 1, name: 'Gesendet', role: 'sent' },
  { id: 12, accountId: 1, name: 'Entwürfe', role: 'drafts' },
  { id: 13, accountId: 1, name: 'Archiv', role: 'archive' },
  { id: 29, accountId: 1, name: '2026', role: '', parentId: 13 },
  { id: 16, accountId: 1, name: 'Projekte', role: '' },
  { id: 17, accountId: 1, name: 'Hausbau', role: '', parentId: 16 },
  { id: 18, accountId: 1, name: 'Verein', role: '', parentId: 16 },
  { id: 19, accountId: 1, name: 'Rechnungen', role: '' },
  { id: 14, accountId: 1, name: 'Spam', role: 'junk' },
  { id: 15, accountId: 1, name: 'Papierkorb', role: 'trash' },
  { id: 20, accountId: 2, name: 'Posteingang', role: 'inbox' },
  { id: 21, accountId: 2, name: 'Gesendete Elemente', role: 'sent' },
  { id: 22, accountId: 2, name: 'Entwürfe', role: 'drafts' },
  { id: 23, accountId: 2, name: 'Archiv', role: 'archive' },
  { id: 25, accountId: 2, name: 'Mandanten', role: '' },
  { id: 26, accountId: 2, name: 'Müller GmbH', role: '', parentId: 25 },
  { id: 27, accountId: 2, name: 'Schneider KG', role: '', parentId: 25 },
  { id: 28, accountId: 2, name: 'Intern', role: '' },
  { id: 24, accountId: 2, name: 'Gelöschte Elemente', role: 'trash' },
];

export const folders: Folder[] = folderSpecs.map((f) => ({
  ...f,
  remoteId: f.remoteId ?? f.name,
  delimiter: '/',
  subscribed: true,
  unread: 0,
  total: 0,
}));

export const persons: Person[] = [
  {
    id: ME,
    displayName: 'Jonas Becker',
    isMe: true,
    handles: [
      { kind: 'email', value: 'jonas.becker@posteo.de' },
      { kind: 'email', value: 'jonas@becker-familie.de' },
      { kind: 'email', value: 'j.becker@kanzlei-sommer.de' },
      { kind: 'whatsapp', value: '+491512345678' },
      { kind: 'signal', value: '+491512345678' },
    ],
  },
  { id: 2, displayName: 'Anna Müller', isMe: false, lastSeen: 'rel:-20m', handles: [{ kind: 'email', value: 'anna.mueller@mueller-gmbh.de' }, { kind: 'whatsapp', value: '+491701112233' }] },
  { id: 3, displayName: 'Max Weber', isMe: false, lastSeen: 'rel:-10m', handles: [{ kind: 'email', value: 'max.weber@kanzlei-sommer.de' }, { kind: 'signal', value: '+491604445566' }] },
  { id: 4, displayName: 'Sabine Becker', isMe: false, lastSeen: 'rel:-45m', handles: [{ kind: 'email', value: 'sabine.becker@t-online.de' }, { kind: 'whatsapp', value: '+491717778899' }] },
  { id: 5, displayName: 'Thomas Klein', isMe: false, handles: [{ kind: 'email', value: 't.klein@tsv-gruenwald.de' }] },
  { id: 6, displayName: 'Lisa Schneider', isMe: false, handles: [{ kind: 'email', value: 'lisa.schneider@schneider-kg.de' }] },
  {
    id: 7,
    displayName: 'Paul Richter',
    isMe: false,
    handles: [
      { kind: 'email', value: 'paul.richter@mailbox.org' },
      { kind: 'signal', value: '+491529990001' },
      { kind: 'whatsapp', value: '+491529990001' },
      { kind: 'telegram', value: '@paulrichter' },
    ],
    lastSeen: 'rel:-3m',
  },
  { id: 8, displayName: 'Kerstin Wagner (Wagner Bau)', isMe: false, handles: [{ kind: 'email', value: 'k.wagner@wagner-bau.de' }] },
  { id: 9, displayName: 'Stadtwerke München', isMe: false, handles: [{ kind: 'email', value: 'rechnung@swm-kundenservice.de' }] },
  { id: 10, displayName: 'heise online', isMe: false, handles: [{ kind: 'email', value: 'newsletter@heise.de' }] },
  { id: 11, displayName: 'Deutsche Bahn', isMe: false, handles: [{ kind: 'email', value: 'buchungsbestaetigung@bahn.de' }] },
  { id: 12, displayName: 'Claudia Sommer', isMe: false, handles: [{ kind: 'email', value: 'c.sommer@kanzlei-sommer.de' }] },
  { id: 13, displayName: 'Lena Becker', isMe: false, lastSeen: 'rel:-2h', handles: [{ kind: 'email', value: 'lena.becker@gmx.de' }, { kind: 'whatsapp', value: '+491763334455' }, { kind: 'telegram', value: '@lenab' }] },
  { id: 14, displayName: 'DHL Paket', isMe: false, handles: [{ kind: 'email', value: 'noreply@dhl.de' }] },
  { id: 15, displayName: 'IT-Service Kanzlei', isMe: false, handles: [{ kind: 'email', value: 'it@kanzlei-sommer.de' }] },
  { id: 16, displayName: 'Büromarkt Böttcher', isMe: false, handles: [{ kind: 'email', value: 'service@bueromarkt-boettcher.de' }] },
  { id: 17, displayName: 'Gewinnspiel-Team', isMe: false, handles: [{ kind: 'email', value: 'win@gluecks-gewinn.biz' }] },
  { id: 18, displayName: 'Fitness First', isMe: false, handles: [{ kind: 'email', value: 'kuendigung@fitness-studio.example' }] },
  { id: 19, displayName: 'Online-Shop', isMe: false, handles: [{ kind: 'email', value: 'bestellung@shop.example' }] },
  { id: 20, displayName: 'Jana Vogel', isMe: false, lastSeen: 'rel:-1m', handles: [{ kind: 'telegram', value: '@janavogel' }, { kind: 'phone', value: '+491578887766' }] },
  { id: 21, displayName: 'Felix Braun', isMe: false, lastSeen: 'rel:-1d', handles: [{ kind: 'telegram', value: '@felixb' }] },
];

/** [daysAgo, "HH:MM"]. Day-0 times stay before 13:00 (see anchorDate in mock.ts). */
export type When = [number, string];

export interface MailSpec {
  id: number;
  account: 1 | 2;
  folders: number[];
  thread: number;
  from: number;
  to: number[];
  cc?: number[];
  /** Override "me" address (alias). */
  meEmail?: string;
  subject: string;
  at: When;
  text: string;
  html?: string;
  attachments?: [string, string, number][];
  seen?: boolean;
  flagged?: boolean;
  draft?: boolean;
  replyTo?: number;
  forwardOf?: number;
  /** Signature id appended for own messages. */
  sig?: number;
  /** Signature of an external sender (appended after "-- "). */
  senderSig?: string;
  remote?: boolean;
}

const newsletterHtml = (week: string) => `<!doctype html><html><body style="margin:0;font-family:Arial,Helvetica,sans-serif;background:#f3f4f6">
<table width="100%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:24px">
<table width="600" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:8px;overflow:hidden">
<tr><td style="background:#d4002a;color:#fff;padding:18px 24px;font-size:22px;font-weight:bold">heise online – Wochenrückblick ${week}</td></tr>
<tr><td style="padding:8px 24px"><img src="https://heise.cloudimg.io/banner.jpg" width="552" height="120" alt="Banner (externer Inhalt)" style="display:block;background:#e5e7eb"></td></tr>
<tr><td style="padding:16px 24px;color:#111827;font-size:15px;line-height:1.5">
<h2 style="margin:0 0 8px;font-size:18px">Linux 6.18: Was der neue Kernel bringt</h2>
<p style="margin:0 0 16px">Der neue Long-Term-Kernel bringt Verbesserungen für Dateisysteme, neue Treiber und mehr Rust-Code.</p>
<h2 style="margin:0 0 8px;font-size:18px">Datenschutz: Messenger im Vergleich</h2>
<p style="margin:0 0 16px">Signal, WhatsApp, Threema &amp; Co. – wir haben Verschlüsselung und Metadaten unter die Lupe genommen.</p>
<p style="margin:0"><a href="https://www.heise.de/" style="color:#d4002a">Alle Artikel lesen →</a></p>
</td></tr>
<tr><td style="padding:16px 24px;color:#6b7280;font-size:12px;border-top:1px solid #e5e7eb">Sie erhalten diesen Newsletter, weil Sie ihn abonniert haben. <a href="https://www.heise.de/newsletter/abmelden" style="color:#6b7280">Abmelden</a></td></tr>
</table></td></tr></table></body></html>`;

const invoiceHtml = (month: string, amount: string) => `<!doctype html><html><body style="font-family:Segoe UI,Arial,sans-serif;color:#1f2937;background:#ffffff;margin:0;padding:24px">
<div style="max-width:560px;margin:0 auto;border:1px solid #e5e7eb;border-radius:10px;padding:24px">
<div style="font-size:20px;font-weight:600;color:#005ea8;margin-bottom:12px">SWM Stadtwerke München</div>
<p>Sehr geehrter Herr Becker,</p>
<p>Ihre Rechnung für <b>${month}</b> steht bereit. Der Betrag von <b>${amount}</b> wird am 15. des Monats per SEPA-Lastschrift von Ihrem Konto abgebucht.</p>
<table style="width:100%;border-collapse:collapse;margin:16px 0;font-size:14px">
<tr><td style="padding:6px 0;border-bottom:1px solid #f3f4f6">Strom (M-Ökostrom)</td><td style="text-align:right;border-bottom:1px solid #f3f4f6">${amount}</td></tr>
<tr><td style="padding:6px 0">Kundennummer</td><td style="text-align:right">4711 0815 42</td></tr>
</table>
<p style="font-size:13px;color:#6b7280">Dies ist eine automatisch erstellte Nachricht. Bitte antworten Sie nicht auf diese E-Mail.</p>
</div></body></html>`;

const dbHtml = `<!doctype html><html><body style="font-family:Arial,sans-serif;margin:0;padding:24px;background:#fff;color:#111">
<div style="max-width:560px;margin:0 auto">
<div style="background:#ec0016;color:#fff;padding:14px 18px;font-weight:bold;font-size:18px;border-radius:6px 6px 0 0">Deutsche Bahn</div>
<div style="border:1px solid #ddd;border-top:none;padding:18px;border-radius:0 0 6px 6px">
<p>Vielen Dank für Ihre Buchung!</p>
<p><b>München Hbf → Berlin Hbf</b><br>ICE 1006 · Abfahrt 07:57 · Ankunft 11:58<br>Wagen 9, Platz 64 (Fenster)</p>
<p>Auftragsnummer: <b>Q7XK2M</b></p>
<p style="font-size:12px;color:#666">Ihr Online-Ticket finden Sie im Anhang. Bitte führen Sie Ihren Ausweis mit.</p>
</div></div></body></html>`;

const vpnHtml = `<!doctype html><html><body style="font-family:Calibri,Arial,sans-serif;font-size:14px;color:#222;padding:16px">
<p>Liebe Kolleginnen und Kollegen,</p>
<p>am <b>Freitag ab 18:00 Uhr</b> tauschen wir das VPN-Zertifikat aus. Bitte installieren Sie bis dahin das neue Profil über das Self-Service-Portal.</p>
<ol><li>Portal öffnen</li><li>„VPN 2027" auswählen</li><li>Installieren und Rechner neu starten</li></ol>
<p>Bei Fragen: Durchwahl -99.</p>
<p>Ihr IT-Service</p></body></html>`;

export const mails: MailSpec[] = [
  // ---- Work: Angebot Q4 (Anna Müller) -------------------------------------
  {
    id: 1, account: 2, folders: [26], thread: 1, from: 2, to: [ME], subject: 'Angebot Q4', at: [3, '09:12'], seen: true, senderSig: 'Anna Müller\nKey Account Management\nMüller GmbH\nIndustriestraße 5, 80939 München\nTel. +49 89 555123-40\nMobil +49 170 1112233\nwww.mueller-gmbh.de',
    text: 'Hallo Herr Becker,\n\nanbei wie besprochen unser Angebot für die Rahmenvereinbarung im vierten Quartal. Die Konditionen gelten bis Ende November.\n\nFür Rückfragen stehe ich Ihnen gern zur Verfügung.\n\nViele Grüße\nAnna Müller',
    attachments: [['Angebot_Q4.pdf', 'application/pdf', 248_312]],
  },
  {
    id: 2, account: 2, folders: [21], thread: 1, from: ME, to: [2], subject: 'Re: Angebot Q4', at: [3, '10:05'], seen: true, replyTo: 1, sig: 3,
    text: 'Hallo Frau Müller,\n\nvielen Dank – ich prüfe das Angebot und melde mich bis Freitag.',
  },
  {
    id: 3, account: 2, folders: [20], thread: 1, from: 2, to: [ME], subject: 'Re: Angebot Q4', at: [0, '08:41'], replyTo: 2, flagged: true, senderSig: 'Anna Müller\nKey Account Management\nMüller GmbH\nIndustriestraße 5, 80939 München\nTel. +49 89 555123-40\nMobil +49 170 1112233\nwww.mueller-gmbh.de',
    text: 'Hallo Herr Becker,\n\nkurze Ergänzung: In Position 4 hat sich ein Tippfehler eingeschlichen, der Stundensatz beträgt 145 € statt 154 €. Die korrigierte Fassung hängt an.\n\nViele Grüße\nAnna Müller',
    attachments: [['Angebot_Q4_korrigiert.pdf', 'application/pdf', 251_004]],
  },
  // ---- Work: Fristverlängerung (Lisa Schneider, Max Weber) ----------------
  {
    id: 4, account: 2, folders: [27], thread: 2, from: 6, to: [ME], cc: [3], subject: 'Fristverlängerung Schneider ./. Krause', at: [2, '14:20'], seen: true, senderSig: 'Lisa Schneider\nGeschäftsführerin\nSchneider KG\nHafenstraße 12, 20457 Hamburg\nTel. +49 40 987654-0\nwww.schneider-kg.de',
    text: 'Sehr geehrter Herr Becker,\n\ndas Gericht hat die Frist zur Stellungnahme bis zum 24.11. verlängert. Anbei unser Entwurf für den Schriftsatz – wir würden uns über Ihre Anmerkungen freuen.\n\nMit freundlichen Grüßen\nLisa Schneider',
    attachments: [['Schriftsatz_Entwurf.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', 84_221]],
  },
  {
    id: 5, account: 2, folders: [21], thread: 2, from: ME, to: [6], cc: [3], subject: 'AW: Fristverlängerung Schneider ./. Krause', at: [2, '16:02'], seen: true, replyTo: 4, sig: 3,
    text: 'Sehr geehrte Frau Schneider,\n\nvielen Dank. Ich sehe mir den Entwurf bis Mittwoch an. Herr Weber notiert die neue Frist.',
  },
  {
    id: 6, account: 2, folders: [21], thread: 2, from: ME, to: [3], subject: 'WG: Fristverlängerung Schneider ./. Krause', at: [2, '16:10'], seen: true, forwardOf: 4, sig: 3,
    text: 'Max, zur Info – kannst du die Frist bitte im Kalender eintragen?',
    attachments: [['Schriftsatz_Entwurf.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', 84_221]],
  },
  {
    id: 7, account: 2, folders: [20], thread: 2, from: 3, to: [ME, 6], subject: 'AW: Fristverlängerung Schneider ./. Krause', at: [1, '08:15'], seen: true, replyTo: 5,
    text: 'Ich übernehme die Fristnotierung, Vorfrist ist der 17.11.\n\nMax',
  },
  {
    id: 8, account: 2, folders: [20], thread: 2, from: 6, to: [ME], cc: [3], subject: 'AW: Fristverlängerung Schneider ./. Krause', at: [1, '11:30'], seen: true, replyTo: 7,
    text: 'Vielen Dank für die schnelle Rückmeldung!\n\nLisa Schneider',
  },
  // ---- Work: Teammeeting --------------------------------------------------
  {
    id: 9, account: 2, folders: [20], thread: 3, from: 12, to: [ME, 3], subject: 'Teammeeting Donnerstag 10 Uhr', at: [0, '07:55'],
    text: 'Hallo zusammen,\n\nam Donnerstag um 10 Uhr besprechen wir die Quartalszahlen und die Urlaubsplanung. Die Einladung hängt an.\n\nBeste Grüße\nClaudia',
    attachments: [['Einladung.ics', 'text/calendar', 1_912]],
  },
  {
    id: 10, account: 2, folders: [20], thread: 3, from: 3, to: [12, ME], subject: 'AW: Teammeeting Donnerstag 10 Uhr', at: [0, '08:03'], replyTo: 9,
    text: 'Ich bin dabei und bringe die Zahlen aus dem Controlling mit.\n\nMax',
  },
  // ---- Work: misc ---------------------------------------------------------
  {
    id: 11, account: 2, folders: [28], thread: 4, from: 15, to: [ME], subject: 'Neues VPN-Zertifikat ab Freitag', at: [5, '15:40'], seen: true,
    text: 'Liebe Kolleginnen und Kollegen,\n\nam Freitag ab 18:00 Uhr tauschen wir das VPN-Zertifikat aus. Bitte installieren Sie das neue Profil.\n\nIhr IT-Service', html: vpnHtml,
  },
  {
    id: 12, account: 2, folders: [20], thread: 5, from: 3, to: [ME], subject: 'Mittagessen?', at: [0, '11:50'],
    text: 'Gehen wir heute zum Italiener um die Ecke? 12:30?',
  },
  {
    id: 13, account: 2, folders: [23], thread: 6, from: 16, to: [ME], subject: 'Ihre Bestellung Nr. 58213 – Rechnung', at: [7, '10:00'], seen: true,
    text: 'Sehr geehrter Kunde,\n\nanbei erhalten Sie die Rechnung zu Ihrer Bestellung (Druckerpapier, Ordner, Toner).\n\nIhr Büromarkt Böttcher',
    attachments: [['Rechnung_58213.pdf', 'application/pdf', 96_120]],
  },
  {
    id: 14, account: 2, folders: [22], thread: 7, from: ME, to: [12], subject: 'Mandanten-Newsletter Dezember (Entwurf)', at: [1, '17:45'], seen: true, draft: true,
    text: 'Hallo Claudia,\n\nhier mein Entwurf für den Dezember-Newsletter. Themen: Mietrecht-Novelle, Fristen zum Jahresende …',
  },
  // ---- Private: Hausbau -----------------------------------------------------
  {
    id: 20, account: 1, folders: [17], thread: 10, from: 8, to: [ME], subject: 'Termin Bodenplatte', at: [6, '09:30'], seen: true, senderSig: 'Kerstin Wagner\nBauleitung\nWagner Bau GmbH\nAm Werkhof 3, 82041 Oberhaching\nTel. +49 89 6131-220\nwww.wagner-bau.de',
    text: 'Guten Tag Herr Becker,\n\nwir können die Bodenplatte am Dienstag nächster Woche gießen, sofern das Wetter mitspielt. Den aktualisierten Bauzeitenplan finden Sie im Anhang.\n\nMit freundlichen Grüßen\nKerstin Wagner',
    attachments: [['Bauzeitenplan_v3.pdf', 'application/pdf', 1_245_880]],
  },
  {
    id: 21, account: 1, folders: [11], thread: 10, from: ME, to: [8], subject: 'Re: Termin Bodenplatte', at: [6, '12:14'], seen: true, replyTo: 20, sig: 1,
    text: 'Hallo Frau Wagner,\n\ndas passt uns gut. Brauchen Sie an dem Tag Zugang zum Grundstück über die Nachbarseite?',
  },
  {
    id: 22, account: 1, folders: [29], thread: 10, from: 8, to: [ME], subject: 'Re: Termin Bodenplatte', at: [4, '08:05'], seen: true, replyTo: 21,
    text: 'Ja, das wäre hilfreich – der Betonmischer kommt gegen 7 Uhr. Vielen Dank!\n\nKerstin Wagner',
  },
  {
    id: 23, account: 1, folders: [11], thread: 10, from: ME, to: [4], meEmail: 'jonas@becker-familie.de', subject: 'Fwd: Termin Bodenplatte', at: [4, '19:20'], seen: true, forwardOf: 20, sig: 2,
    text: 'Hallo Mama, schau mal – nächste Woche geht es endlich richtig los! 🏗️',
    attachments: [['Bauzeitenplan_v3.pdf', 'application/pdf', 1_245_880]],
  },
  {
    id: 24, account: 1, folders: [10], thread: 10, from: 4, to: [ME], subject: 'Re: Fwd: Termin Bodenplatte', at: [3, '07:48'], seen: true, replyTo: 23,
    text: 'Toll! Papa und ich kommen am Dienstag gerne vorbei und bringen Kaffee und Kuchen mit.\n\nMama',
  },
  // ---- Private: Verein Grillfest -------------------------------------------
  {
    id: 25, account: 1, folders: [18], thread: 11, from: 5, to: [ME, 7], subject: 'Grillfest am Samstag', at: [2, '18:00'], seen: true,
    text: 'Hallo zusammen,\n\nam Samstag ab 16 Uhr steigt unser Saisonabschluss-Grillfest am Vereinsheim. Bitte gebt kurz Bescheid, was ihr mitbringt.\n\nSportliche Grüße\nThomas',
  },
  {
    id: 26, account: 1, folders: [11], thread: 11, from: ME, to: [5], cc: [7], subject: 'Re: Grillfest am Samstag', at: [2, '19:02'], seen: true, replyTo: 25, sig: 1,
    text: 'Ich bin dabei und bringe Würstchen und Getränke mit.',
  },
  {
    id: 27, account: 1, folders: [10], thread: 11, from: 7, to: [5, ME], subject: 'Re: Grillfest am Samstag', at: [1, '09:12'], seen: true, replyTo: 26,
    text: 'Super, dann bringe ich Nudelsalat mit! 🥗\n\nPaul',
  },
  {
    id: 28, account: 1, folders: [10], thread: 11, from: 5, to: [ME, 7], subject: 'Re: Grillfest am Samstag', at: [1, '20:10'], replyTo: 27,
    text: 'Update: Wir brauchen noch zwei Bierbänke. Hat jemand einen Anhänger?\n\nThomas',
  },
  // ---- Private: invoices / newsletters / transactional --------------------
  {
    id: 29, account: 1, folders: [10], thread: 12, from: 9, to: [ME], subject: 'Ihre Rechnung November 2026', at: [0, '06:30'], remote: false,
    text: 'Ihre Rechnung für November 2026 steht bereit. Betrag: 87,40 €.', html: invoiceHtml('November 2026', '87,40 €'),
    attachments: [['Rechnung_2026-11.pdf', 'application/pdf', 142_330]],
  },
  {
    id: 30, account: 1, folders: [19], thread: 13, from: 9, to: [ME], subject: 'Ihre Rechnung Oktober 2026', at: [33, '06:30'], seen: true,
    text: 'Ihre Rechnung für Oktober 2026 steht bereit. Betrag: 82,15 €.', html: invoiceHtml('Oktober 2026', '82,15 €'),
    attachments: [['Rechnung_2026-10.pdf', 'application/pdf', 140_918]],
  },
  {
    id: 31, account: 1, folders: [10], thread: 14, from: 10, to: [ME], subject: 'Wochenrückblick KW 45: Linux 6.18, Messenger-Vergleich', at: [0, '05:00'], remote: true,
    text: 'Linux 6.18: Was der neue Kernel bringt. Datenschutz: Messenger im Vergleich.', html: newsletterHtml('KW 45'),
  },
  {
    id: 32, account: 1, folders: [10], thread: 15, from: 10, to: [ME], subject: 'Wochenrückblick KW 44', at: [7, '05:00'], seen: true, remote: true,
    text: 'Die wichtigsten Meldungen der Woche.', html: newsletterHtml('KW 44'),
  },
  {
    id: 33, account: 1, folders: [13], thread: 16, from: 11, to: [ME], subject: 'Ihre Buchungsbestätigung (Auftrag Q7XK2M)', at: [8, '21:14'], seen: true,
    text: 'Vielen Dank für Ihre Buchung! München Hbf → Berlin Hbf, ICE 1006.', html: dbHtml,
    attachments: [['Online-Ticket_Q7XK2M.pdf', 'application/pdf', 312_004]],
  },
  {
    id: 34, account: 1, folders: [10], thread: 17, from: 14, to: [ME], subject: 'Ihre Sendung ist unterwegs', at: [1, '09:00'], seen: true,
    text: 'Guten Tag,\n\nIhre Sendung 00340434161094042557 wird heute zwischen 13:00 und 16:00 Uhr zugestellt.\n\nIhr DHL Team',
  },
  // ---- Private: Paul (photos) ---------------------------------------------
  {
    id: 35, account: 1, folders: [10], thread: 18, from: 7, to: [ME], subject: 'Fotos vom Wochenende', at: [5, '20:31'], seen: true,
    text: 'Hi Jonas,\n\nhier die Fotos von der Wanderung am Herzogstand. Das Panorama-Bild ist mein Favorit!\n\nPaul',
    attachments: [
      ['Herzogstand_01.jpg', 'image/jpeg', 2_310_442],
      ['Herzogstand_02.jpg', 'image/jpeg', 2_101_871],
      ['Panorama.jpg', 'image/jpeg', 4_882_101],
    ],
  },
  {
    id: 36, account: 1, folders: [11], thread: 18, from: ME, to: [7], subject: 'Re: Fotos vom Wochenende', at: [5, '21:05'], seen: true, replyTo: 35, sig: 1,
    text: 'Wow, die sind super geworden! Das Panorama hänge ich mir ins Büro.',
  },
  // ---- Private: Lena (alias) ------------------------------------------------
  {
    id: 37, account: 1, folders: [10], thread: 19, from: 13, to: [ME], meEmail: 'jonas@becker-familie.de', subject: 'Geschenkidee für Mama', at: [1, '18:22'], seen: true,
    text: 'Hey Bruderherz,\n\nMama hat doch bald Geburtstag. Was hältst du von einem Kochkurs für Papa und sie? Wir könnten uns die Kosten teilen.\n\nLena',
  },
  {
    id: 38, account: 1, folders: [11], thread: 19, from: ME, to: [13], meEmail: 'jonas@becker-familie.de', subject: 'Re: Geschenkidee für Mama', at: [1, '21:40'], seen: true, replyTo: 37, sig: 2,
    text: 'Gute Idee! Ich schaue mal, was die Volkshochschule anbietet. Lass uns morgen telefonieren.',
  },
  // ---- Private: Mama recipe -------------------------------------------------
  {
    id: 39, account: 1, folders: [10], thread: 20, from: 4, to: [ME], subject: 'Rezept Apfelkuchen', at: [10, '16:10'], seen: true,
    text: 'Hallo Jonas,\n\nhier das Rezept, nach dem du gefragt hast:\n\n- 250 g Mehl\n- 125 g Butter\n- 100 g Zucker\n- 1 Ei\n- 1 kg Äpfel\n- Zimt nach Geschmack\n\nBei 180 °C etwa 45 Minuten backen. Viel Spaß!\n\nMama',
  },
  {
    id: 40, account: 1, folders: [11], thread: 20, from: ME, to: [4], subject: 'Re: Rezept Apfelkuchen', at: [9, '19:30'], seen: true, replyTo: 39, sig: 2,
    text: 'Danke! Der Kuchen ist super geworden, alle waren begeistert.',
  },
  // ---- Private: spam / draft / trash ---------------------------------------
  {
    id: 41, account: 1, folders: [14], thread: 21, from: 17, to: [ME], subject: 'Sie haben gewonnen!!! Jetzt Gewinn abholen', at: [2, '03:12'],
    text: 'Herzlichen Glückwunsch! Sie wurden als Gewinner eines iPhone ausgewählt. Klicken Sie hier, um Ihren Gewinn abzuholen.',
  },
  {
    id: 42, account: 1, folders: [12], thread: 22, from: ME, to: [18], subject: 'Kündigung meiner Mitgliedschaft', at: [3, '22:15'], seen: true, draft: true,
    text: 'Sehr geehrte Damen und Herren,\n\nhiermit kündige ich meine Mitgliedschaft (Nr. 20391) fristgerecht zum nächstmöglichen Zeitpunkt.',
  },
  {
    id: 43, account: 1, folders: [15], thread: 23, from: 19, to: [ME], subject: 'Ihre Bestellung wurde versandt', at: [12, '11:00'], seen: true,
    text: 'Ihre Bestellung #A-77812 wurde versandt und ist in 2–3 Werktagen bei Ihnen.',
  },
];

export interface ChatSpec {
  id: number;
  accountId: 3 | 4 | 5;
  /** Remote chat kind. */
  kind: 'direct' | 'group';
  title?: string;
  memberIds: number[];
  pinned?: boolean;
}

export const chats: ChatSpec[] = [
  { id: 101, accountId: 3, kind: 'direct', memberIds: [2] },
  { id: 102, accountId: 3, kind: 'group', title: 'Familie Becker', memberIds: [4, 13], pinned: true },
  { id: 103, accountId: 3, kind: 'direct', memberIds: [4] },
  { id: 104, accountId: 4, kind: 'direct', memberIds: [3] },
  { id: 105, accountId: 4, kind: 'direct', memberIds: [7] },
  { id: 106, accountId: 5, kind: 'group', title: 'Wandergruppe ⛰️', memberIds: [7, 20, 21, 13] },
  { id: 107, accountId: 5, kind: 'direct', memberIds: [20] },
  { id: 108, accountId: 4, kind: 'group', title: 'Kanzlei-Team', memberIds: [3, 12] },
];

/** [filename, mime, size, extra] - extra: photo hues or voice duration. */
export type MediaSpec = [string, string, number] | [string, string, number, { photo?: [number, number]; seconds?: number }];

export interface ChatMessageSpec {
  id: number;
  chatId: number;
  from: number;
  at: When;
  text: string;
  seen?: boolean;
  attachments?: MediaSpec[];
  reactions?: { emoji: string; count: number; mine?: boolean }[];
  /** Answers this chat message id. */
  replyTo?: number;
  link?: { url: string; title: string; description?: string; siteName?: string };
}

export const chatMessages: ChatMessageSpec[] = [
  // Anna (WhatsApp)
  { id: 1001, chatId: 101, from: 2, at: [3, '09:14'], text: 'Guten Morgen Herr Becker, ich habe Ihnen eben das Angebot geschickt.', seen: true },
  { id: 1002, chatId: 101, from: ME, at: [3, '09:20'], text: 'Danke, ist angekommen!', seen: true },
  { id: 1003, chatId: 101, from: 2, at: [0, '10:42'], text: 'Hast du die Mail mit der Korrektur gesehen? 🙂' },
  // Family group (WhatsApp)
  { id: 1101, chatId: 102, from: 4, at: [2, '18:30'], text: 'Wer kommt am Sonntag zum Essen? 🍲', seen: true },
  { id: 1102, chatId: 102, from: 13, at: [2, '18:41'], text: 'Ich! Bringe Nachtisch mit.', seen: true },
  { id: 1103, chatId: 102, from: ME, at: [2, '19:05'], text: 'Wir sind auch dabei 👍', seen: true, reactions: [{ emoji: '❤️', count: 2 }] },
  { id: 1104, chatId: 102, from: 4, at: [0, '09:15'], text: 'Bild vom neuen Gartenhaus', attachments: [['IMG_2041.jpg', 'image/jpeg', 1_802_331, { photo: [28, 140] }]] },
  { id: 1105, chatId: 102, from: 4, at: [0, '09:16'], text: 'Papa ist ganz stolz 😄', reactions: [{ emoji: '😂', count: 1 }, { emoji: '❤️', count: 2, mine: true }] },
  { id: 1106, chatId: 102, from: 13, at: [0, '09:31'], text: '', attachments: [['Sprachnachricht.ogg', 'audio/ogg', 48_210, { seconds: 14 }]] },
  { id: 1107, chatId: 102, from: ME, at: [0, '09:40'], text: 'Sieht toll aus! Am Sonntag schauen wir es uns an 🙂', replyTo: 1104 },
  // Mama direct (WhatsApp)
  { id: 1201, chatId: 103, from: 4, at: [4, '20:02'], text: 'Danke für die Mail mit dem Bauzeitenplan!', seen: true },
  { id: 1202, chatId: 103, from: ME, at: [4, '20:10'], text: 'Gerne! Ihr könnt jederzeit vorbeikommen.', seen: true },
  { id: 1203, chatId: 103, from: 4, at: [1, '12:30'], text: 'Denkst du an Lenas Geburtstagsgeschenk?', seen: true },
  // Max (Signal)
  { id: 1301, chatId: 104, from: 3, at: [1, '07:40'], text: 'Kommst du morgen ins Büro?', seen: true },
  { id: 1302, chatId: 104, from: ME, at: [1, '07:52'], text: 'Ja, ab 9 Uhr.', seen: true },
  { id: 1303, chatId: 104, from: 3, at: [1, '07:53'], text: 'Perfekt, dann besprechen wir Schneider ./. Krause.', seen: true },
  // Paul (Signal)
  { id: 1401, chatId: 105, from: 7, at: [5, '20:35'], text: 'Hab dir die Fotos gemailt 📸', seen: true },
  { id: 1402, chatId: 105, from: ME, at: [5, '21:06'], text: 'Gerade gesehen, mega!', seen: true },
  { id: 1403, chatId: 105, from: 7, at: [1, '09:20'], text: 'Kannst du am Samstag einen Anhänger organisieren? Thomas braucht noch Bierbänke.', seen: true },
  { id: 1404, chatId: 105, from: 7, at: [0, '12:05'], text: 'Und: Nächstes Wochenende wieder wandern? ⛰️' },
  // Wandergruppe (Telegram group)
  { id: 1501, chatId: 106, from: 7, at: [1, '18:02'], text: 'Wer ist Samstag dabei? 🥾', seen: true },
  { id: 1502, chatId: 106, from: 20, at: [1, '18:05'], text: 'Ich! Welche Route?', seen: true },
  {
    id: 1503, chatId: 106, from: 7, at: [1, '18:07'], seen: true,
    text: 'Herzogstand – Heimgarten, Gratwanderung: https://www.alpenverein.de/touren/herzogstand-heimgarten',
    link: { url: 'https://www.alpenverein.de/touren/herzogstand-heimgarten', siteName: 'alpenverein.de', title: 'Gratwanderung Herzogstand – Heimgarten', description: 'Klassische Überschreitung mit Blick auf Walchensee und Kochelsee, ca. 5 Std.' },
  },
  { id: 1504, chatId: 106, from: 21, at: [1, '18:20'], text: 'Fotos von letztem Mal 📸', seen: true, attachments: [
    ['Gipfel.jpg', 'image/jpeg', 2_210_000, { photo: [200, 30] }],
    ['Walchensee.jpg', 'image/jpeg', 1_980_000, { photo: [190, 160] }],
    ['Brotzeit.jpg', 'image/jpeg', 1_640_000, { photo: [35, 90] }],
  ] },
  { id: 1505, chatId: 106, from: ME, at: [1, '18:31'], text: 'Bin dabei 👍', seen: true, reactions: [{ emoji: '🎉', count: 2 }] },
  { id: 1506, chatId: 106, from: 13, at: [1, '19:02'], text: 'Ich auch, nehme den Zug um 7:12', seen: true },
  { id: 1507, chatId: 106, from: 20, at: [0, '08:15'], text: 'Sieht super aus, freu mich!', replyTo: 1503 },
  { id: 1508, chatId: 106, from: 7, at: [0, '08:17'], text: 'Treffpunkt 7:45 am Parkplatz Kesselberg' },
  // Jana (Telegram)
  { id: 1601, chatId: 107, from: 20, at: [2, '20:11'], text: 'Hi Jonas! Danke nochmal für den Tipp mit der Hütte 😊', seen: true },
  { id: 1602, chatId: 107, from: ME, at: [2, '20:15'], text: 'Gern! War sie gut?', seen: true },
  { id: 1603, chatId: 107, from: 20, at: [2, '20:16'], text: '', seen: true, attachments: [['Sprachnachricht.ogg', 'audio/ogg', 36_500, { seconds: 9 }]] },
  { id: 1604, chatId: 107, from: 20, at: [2, '20:17'], text: 'Sonnenuntergang von der Terrasse', seen: true, attachments: [['Huette.jpg', 'image/jpeg', 2_900_000, { photo: [15, 280] }]], reactions: [{ emoji: '😍', count: 1, mine: true }] },
  // Kanzlei-Team (Signal group)
  { id: 1701, chatId: 108, from: 12, at: [0, '07:58'], text: 'Kurze Info: Donnerstag 10 Uhr Teammeeting im großen Besprechungsraum.' },
  { id: 1702, chatId: 108, from: 3, at: [0, '08:01'], text: '👍' },
];
