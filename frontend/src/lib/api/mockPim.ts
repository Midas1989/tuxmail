// SPDX-License-Identifier: AGPL-3.0-or-later
//
// In-memory calendars, tasks and contacts for the mock transport (demo,
// screenshots, tests). Times are seeded relative to "now" in the system
// time zone; recurring events are expanded with lib/calendar/recurrence.

import { addDays, dayKeyIn, diffDays, minutesIn, startOfWeek, zonedInstant, type DayKey } from '../calendar/datemath';
import { expandDays } from '../calendar/recurrence';
import { systemTimeZone } from '../format';
import type {
  AddressSuggestion,
  CalendarEvent,
  CalendarInput,
  ContactGroup,
  DuplicateGroup,
  EventInput,
  EventQuery,
  FreeBusy,
  Invitation,
  InvitationResponse,
  Person,
  PimAddressBook,
  PimCalendar,
  PimContact,
  PimTask,
  RecurrenceRule,
  RecurrenceScope,
  TaskInput,
  TaskQuery,
} from '../types';
import { ApiError } from './api';

interface MockEvent {
  objectId: number;
  calendarId: number;
  uid: string;
  title: string;
  description?: string;
  location?: string;
  meetingUrl?: string;
  allDay: boolean;
  /** Local wall clock start (timed) or start day (all-day). */
  day: DayKey;
  startMin: number;
  durationMin: number;
  /** All-day length in days. */
  days: number;
  recurrence?: RecurrenceRule;
  exdates: DayKey[];
  overrides: Record<DayKey, Partial<MockEvent>>;
  attendees: CalendarEvent['attendees'];
  organizer?: CalendarEvent['organizer'];
  alarms: CalendarEvent['alarms'];
  busy: boolean;
  categories: string[];
  status?: string;
}

const ME_MAIL = 'j.becker@kanzlei-sommer.de';

function emptyContact(): PimContact {
  return {
    id: 0,
    addressBookId: 1,
    displayName: '',
    hasPhoto: false,
    channels: [],
    uid: '',
    fn: '',
    n: {},
    emails: [],
    phones: [],
    addresses: [],
    urls: [],
    impp: [],
    social: [],
    categories: [],
    custom: [],
  };
}

export class MockPim {
  private tz = systemTimeZone();
  private calendars: PimCalendar[];
  private events: MockEvent[] = [];
  private tasks: PimTask[] = [];
  private books: PimAddressBook[];
  private contacts: PimContact[] = [];
  private invitations: Invitation[] = [];
  private seq = 100;
  private readonly now: () => Date;
  private readonly persons: () => Person[];
  private readonly notify: (kind: 'calendar' | 'contacts') => void;

  constructor(opts: { now: () => Date; persons: () => Person[]; notify: (kind: 'calendar' | 'contacts') => void }) {
    this.now = opts.now;
    this.persons = opts.persons;
    this.notify = opts.notify;
    this.calendars = [
      { id: 1, name: 'Persönlich', color: '#2563eb', kind: 'local', readOnly: false, visible: true, isDefault: true, components: ['VEVENT', 'VTODO'] },
      {
        id: 2,
        accountId: 2,
        accountName: 'Kanzlei Sommer',
        name: 'Kanzlei',
        color: '#9333ea',
        kind: 'caldav',
        readOnly: false,
        visible: true,
        isDefault: false,
        components: ['VEVENT', 'VTODO'],
        url: 'https://cloud.kanzlei-sommer.de/remote.php/dav/calendars/jbecker/kanzlei/',
        lastSync: this.now().toISOString(),
      },
      {
        id: 3,
        name: 'Feiertage Bayern',
        color: '#16a34a',
        kind: 'subscription',
        readOnly: true,
        visible: true,
        isDefault: false,
        components: ['VEVENT'],
        url: 'webcal://feiertage.example/bayern.ics',
        refreshMinutes: 1440,
      },
      { id: 4, name: 'Geburtstage', color: '#ec4899', kind: 'birthdays', readOnly: true, visible: true, isDefault: false, components: ['VEVENT'] },
    ];
    this.books = [
      { id: 1, name: 'Persönlich', kind: 'local', isDefault: true, count: 0 },
      { id: 2, accountId: 2, accountName: 'Kanzlei Sommer', name: 'Kanzlei', kind: 'carddav', isDefault: false, count: 0, lastSync: this.now().toISOString() },
    ];
    this.seed();
  }

  private today(): DayKey {
    return dayKeyIn(this.now(), this.tz);
  }

  private seed(): void {
    const mon = startOfWeek(this.today(), 1);
    const ev = (p: Partial<MockEvent> & { title: string; day: DayKey }): void => {
      const id = ++this.seq;
      this.events.push({
        objectId: id,
        calendarId: 1,
        uid: `mock-${id}@tuxmail`,
        allDay: false,
        startMin: 9 * 60,
        durationMin: 60,
        days: 1,
        exdates: [],
        overrides: {},
        attendees: [],
        alarms: [{ minutesBefore: 15 }],
        busy: true,
        categories: [],
        ...p,
      });
    };
    ev({ title: 'Bodenplatte – Wagner Bau', day: mon, startMin: 8 * 60, durationMin: 90, location: 'Baustelle Grünwald, Eichenweg 4' });
    ev({
      title: 'Jour fixe Kanzlei',
      calendarId: 2,
      day: addDays(mon, -14),
      startMin: 9 * 60 + 30,
      durationMin: 30,
      recurrence: { freq: 'WEEKLY', byDay: ['MO', 'WE'] },
      exdates: [addDays(mon, 2)],
      meetingUrl: 'https://meet.jit.si/kanzlei-sommer-jourfixe',
      organizer: { email: 'c.sommer@kanzlei-sommer.de', name: 'Claudia Sommer' },
      attendees: [
        { email: ME_MAIL, name: 'Jonas Becker', partStat: 'ACCEPTED' },
        { email: 'max.weber@kanzlei-sommer.de', name: 'Max Weber', partStat: 'ACCEPTED' },
      ],
    });
    ev({
      title: 'Mandantentermin Schneider KG',
      calendarId: 2,
      day: addDays(mon, 1),
      startMin: 14 * 60,
      durationMin: 90,
      location: 'Besprechungsraum 2',
      organizer: { email: ME_MAIL, name: 'Jonas Becker' },
      attendees: [
        { email: 'lisa.schneider@schneider-kg.de', name: 'Lisa Schneider', partStat: 'ACCEPTED' },
        { email: 'max.weber@kanzlei-sommer.de', name: 'Max Weber', partStat: 'TENTATIVE' },
      ],
    });
    ev({ title: 'Fristablauf Krause', calendarId: 2, day: addDays(mon, 1), startMin: 14 * 60 + 30, durationMin: 30, categories: ['Frist'] });
    ev({ title: 'Zahnarzt Dr. Huber', day: addDays(mon, 3), startMin: 15 * 60, durationMin: 45, location: 'Leopoldstr. 12' });
    ev({ title: 'Mittagessen mit Max', day: addDays(mon, 2), startMin: 12 * 60, durationMin: 60, busy: false });
    ev({ title: 'Grillfest TSV', day: addDays(mon, 5), startMin: 16 * 60, durationMin: 180, location: 'Sportplatz TSV Grünwald' });
    ev({ title: 'Lauftreff', day: addDays(mon, -21), startMin: 18 * 60 + 30, durationMin: 60, recurrence: { freq: 'WEEKLY', byDay: ['TU', 'TH'] }, alarms: [] });
    ev({ title: 'Urlaub Lena', day: addDays(mon, 4), allDay: true, days: 3, busy: false, alarms: [] });
    ev({ title: 'Allerheiligen', calendarId: 3, day: this.firstOfNovember(), allDay: true, days: 1, busy: false, alarms: [] });
    ev({ title: 'Buß- und Bettag (Schulen)', calendarId: 3, day: addDays(mon, 16), allDay: true, days: 1, busy: false, alarms: [] });
    ev({ title: 'Quartalsabschluss', calendarId: 2, day: addDays(mon, 10), startMin: 10 * 60, durationMin: 120, categories: ['Buchhaltung'] });

    const task = (p: Partial<PimTask> & { title: string }): PimTask => {
      const t: PimTask = {
        id: ++this.seq,
        calendarId: 1,
        uid: `task-${this.seq}`,
        priority: 0,
        progress: 0,
        status: 'NEEDS-ACTION',
        done: false,
        categories: [],
        ...p,
      };
      this.tasks.push(t);
      return t;
    };
    const today = this.today();
    task({ title: 'Angebot Müller GmbH prüfen', due: today, dueAllDay: true, priority: 1, calendarId: 2 });
    task({ title: 'Schriftsatz Krause entwerfen', due: addDays(today, -1), dueAllDay: true, priority: 1, calendarId: 2, progress: 40, status: 'IN-PROCESS' });
    const parent = task({ title: 'Umzug Büro vorbereiten', due: addDays(today, 6), dueAllDay: true, priority: 5, calendarId: 2 });
    task({ title: 'Umzugskartons bestellen', parentId: parent.id, parentUid: parent.uid, calendarId: 2, done: true, status: 'COMPLETED', progress: 100 });
    task({ title: 'IT-Umzug mit IT-Service abstimmen', parentId: parent.id, parentUid: parent.uid, calendarId: 2, due: addDays(today, 3), dueAllDay: true });
    task({ title: 'Geschenk für Mama', due: addDays(today, 4), dueAllDay: true, categories: ['Familie'] });
    task({ title: 'Steuerunterlagen sortieren', priority: 9 });
    task({ title: 'Reifenwechsel buchen', done: true, status: 'COMPLETED', progress: 100, completed: this.now().toISOString() });

    const c = (p: Partial<PimContact> & { displayName: string }): void => {
      const id = ++this.seq;
      const [given, ...rest] = p.displayName.split(' ');
      this.contacts.push({ ...emptyContact(), id, uid: `contact-${id}`, fn: p.displayName, n: { given, family: rest.join(' ') }, ...p });
    };
    c({
      displayName: 'Anna Müller',
      personId: 2,
      org: 'Müller GmbH',
      title: 'Key Account Management',
      emails: [{ value: 'anna.mueller@mueller-gmbh.de', type: 'work', pref: true }],
      phones: [
        { value: '+49 89 555123-40', type: 'work' },
        { value: '+49 170 1112233', type: 'cell' },
      ],
      addresses: [{ type: 'work', street: 'Industriestraße 5', postalCode: '80939', locality: 'München', country: 'Deutschland' }],
      urls: [{ value: 'https://www.mueller-gmbh.de', type: 'work' }],
      impp: [{ value: 'whatsapp:+491701112233' }],
      birthday: '1985-' + today.slice(5, 7) + '-' + String(Math.min(28, Number(today.slice(8, 10)) + 2)).padStart(2, '0'),
      categories: ['Kunden'],
      addressBookId: 2,
    });
    c({ displayName: 'Max Weber', personId: 3, org: 'Kanzlei Sommer', title: 'Rechtsanwalt', emails: [{ value: 'max.weber@kanzlei-sommer.de', type: 'work' }], phones: [{ value: '+49 160 4445566', type: 'cell' }], categories: ['Kanzlei'], addressBookId: 2 });
    c({ displayName: 'Sabine Becker', personId: 4, emails: [{ value: 'sabine.becker@t-online.de', type: 'home' }], phones: [{ value: '+49 171 7778899', type: 'cell' }], birthday: '--03-14', categories: ['Familie'], note: 'Mama' });
    c({ displayName: 'Lena Becker', personId: 13, emails: [{ value: 'lena.becker@gmx.de', type: 'home' }], phones: [{ value: '+49 176 3334455', type: 'cell' }], impp: [{ value: 'telegram:@lenab' }], birthday: '1996-07-21', categories: ['Familie'] });
    c({ displayName: 'Thomas Klein', personId: 5, org: 'TSV Grünwald', emails: [{ value: 't.klein@tsv-gruenwald.de' }], categories: ['Verein'] });
    c({ displayName: 'Lisa Schneider', personId: 6, org: 'Schneider KG', title: 'Geschäftsführerin', emails: [{ value: 'lisa.schneider@schneider-kg.de', type: 'work' }], phones: [{ value: '+49 40 987654-0', type: 'work' }], categories: ['Kunden'], addressBookId: 2 });
    c({ displayName: 'Paul Richter', personId: 7, emails: [{ value: 'paul.richter@mailbox.org', type: 'home' }], phones: [{ value: '+49 152 9990001', type: 'cell' }], impp: [{ value: 'signal:+491529990001' }, { value: 'telegram:@paulrichter' }] });
    c({ displayName: 'Kerstin Wagner', personId: 8, org: 'Wagner Bau GmbH', title: 'Bauleitung', emails: [{ value: 'k.wagner@wagner-bau.de', type: 'work' }], phones: [{ value: '+49 89 6131-220', type: 'work' }] });
    c({ displayName: 'Claudia Sommer', personId: 12, org: 'Kanzlei Sommer', title: 'Partnerin', emails: [{ value: 'c.sommer@kanzlei-sommer.de', type: 'work' }], categories: ['Kanzlei'], addressBookId: 2 });
    // Duplicate of Paul (same phone, different spelling).
    c({ displayName: 'Richter, Paul', phones: [{ value: '0152 9990001', type: 'cell' }], org: 'Fotoclub München', emails: [{ value: 'p.richter@fotoclub-muenchen.de', type: 'work' }] });

    this.invitations.push({
      id: 1,
      messageId: 9,
      method: 'REQUEST',
      status: 'pending',
      uid: 'teammeeting-kw@kanzlei-sommer.de',
      title: 'Teammeeting',
      location: 'Konferenzraum 1',
      meetingUrl: 'https://meet.jit.si/kanzlei-sommer-team',
      start: zonedInstant(addDays(mon, 3), 10 * 60, this.tz).toISOString(),
      end: zonedInstant(addDays(mon, 3), 11 * 60, this.tz).toISOString(),
      allDay: false,
      recurring: false,
      organizer: { email: 'c.sommer@kanzlei-sommer.de', name: 'Claudia Sommer' },
      attendees: [
        { email: ME_MAIL, name: 'Jonas Becker', partStat: 'NEEDS-ACTION', rsvp: true },
        { email: 'max.weber@kanzlei-sommer.de', name: 'Max Weber', partStat: 'ACCEPTED' },
      ],
      myPartStat: 'NEEDS-ACTION',
      sender: 'c.sommer@kanzlei-sommer.de',
      conflicts: [],
      canRespond: true,
    });
  }

  private firstOfNovember(): DayKey {
    return this.today().slice(0, 4) + '-11-01';
  }

  private fail(msg: string, code = 'invalid_input'): never {
    throw new ApiError(msg, 400, code);
  }

  private cal(id: number): PimCalendar {
    const c = this.calendars.find((x) => x.id === id);
    if (!c) throw new ApiError('not found', 404, 'not_found');
    return c;
  }

  // -------------------------------------------------------------------------
  // Calendars
  // -------------------------------------------------------------------------

  listCalendars(): PimCalendar[] {
    return this.calendars;
  }

  saveCalendar(input: CalendarInput): PimCalendar {
    if (input.id) {
      const c = this.cal(input.id);
      if (input.name) c.name = input.name;
      if (input.color) c.color = input.color;
      if (input.visible !== undefined) c.visible = input.visible;
      if (input.isDefault) for (const x of this.calendars) x.isDefault = x.id === c.id;
      if (input.url && c.kind === 'subscription') c.url = input.url;
      if (input.refreshMinutes) c.refreshMinutes = input.refreshMinutes;
      this.notify('calendar');
      return c;
    }
    const kind = input.kind ?? 'local';
    if (kind === 'subscription' && !input.url) this.fail('subscription URL required');
    const c: PimCalendar = {
      id: ++this.seq,
      name: input.name || input.url || '',
      color: input.color || '#0891b2',
      kind,
      readOnly: kind === 'subscription',
      visible: true,
      isDefault: false,
      components: kind === 'subscription' ? ['VEVENT'] : ['VEVENT', 'VTODO'],
      url: input.url,
      refreshMinutes: kind === 'subscription' ? (input.refreshMinutes ?? 360) : undefined,
      lastSync: kind === 'subscription' ? this.now().toISOString() : undefined,
    };
    this.calendars.push(c);
    this.notify('calendar');
    return c;
  }

  deleteCalendar(id: number): void {
    const c = this.cal(id);
    if (c.kind === 'caldav' || c.kind === 'birthdays') this.fail('cannot delete');
    this.calendars = this.calendars.filter((x) => x.id !== id);
    this.events = this.events.filter((e) => e.calendarId !== id);
    this.notify('calendar');
  }

  // -------------------------------------------------------------------------
  // Events
  // -------------------------------------------------------------------------

  private toEvent(e: MockEvent, day: DayKey, recurring: boolean): CalendarEvent {
    const ov = e.overrides[day] ?? {};
    const m = { ...e, ...ov };
    const cal = this.cal(e.calendarId);
    const startDay = ov.day ?? day;
    const start = m.allDay ? startDay : zonedInstant(startDay, m.startMin, this.tz).toISOString();
    const end = m.allDay ? addDays(startDay, m.days) : new Date(new Date(start).getTime() + m.durationMin * 60_000).toISOString();
    const me = m.attendees.find((a) => a.email === ME_MAIL);
    return {
      id: recurring ? `${e.objectId}/${day.replaceAll('-', '')}` : String(e.objectId),
      objectId: e.objectId,
      calendarId: e.calendarId,
      uid: e.uid,
      recurrenceId: recurring ? day.replaceAll('-', '') : undefined,
      kind: 'event',
      title: m.title,
      description: m.description,
      location: m.location,
      meetingUrl: m.meetingUrl,
      start,
      end,
      allDay: m.allDay,
      recurrence: e.recurrence,
      recurring,
      exception: !!e.overrides[day],
      organizer: m.organizer,
      attendees: m.attendees,
      alarms: m.alarms,
      isOrganizer: !m.organizer || m.organizer.email === ME_MAIL,
      myPartStat: me?.partStat,
      status: m.status,
      busy: m.busy,
      categories: m.categories,
      readOnly: cal.readOnly,
    };
  }

  private occurrences(e: MockEvent, from: DayKey, to: DayKey): CalendarEvent[] {
    if (!e.recurrence) {
      const endDay = e.allDay ? addDays(e.day, e.days - 1) : e.day;
      return e.day <= to && endDay >= from ? [this.toEvent(e, e.day, false)] : [];
    }
    return expandDays(e.recurrence, e.day, addDays(from, -7), to, e.exdates).map((d) => this.toEvent(e, d, true));
  }

  listEvents(q: EventQuery): CalendarEvent[] {
    const from = dayKeyIn(new Date(q.from), this.tz);
    const to = dayKeyIn(new Date(new Date(q.to).getTime() - 1), this.tz);
    const visible = new Set(this.calendars.filter((c) => (q.calendarIds?.length ? q.calendarIds.includes(c.id) : c.visible || q.includeHidden)).map((c) => c.id));
    const out: CalendarEvent[] = [];
    for (const e of this.events) {
      if (!visible.has(e.calendarId)) continue;
      for (const o of this.occurrences(e, from, to)) {
        const s = o.allDay ? o.start : dayKeyIn(new Date(o.start), this.tz);
        const en = o.allDay ? addDays(o.end, -1) : dayKeyIn(new Date(new Date(o.end).getTime() - 1), this.tz);
        if (s <= to && en >= from) out.push(o);
      }
    }
    if (visible.has(4)) {
      for (const c of this.contacts) {
        if (!c.birthday) continue;
        const md = c.birthday.startsWith('--') ? c.birthday.slice(2) : c.birthday.slice(5);
        for (let y = Number(from.slice(0, 4)); y <= Number(to.slice(0, 4)); y++) {
          const k = `${y}-${md}`;
          if (k < from || k > to) continue;
          out.push({
            id: `bday/${c.id}/${y}`,
            objectId: 0,
            calendarId: 4,
            uid: c.uid,
            kind: 'birthday',
            title: c.displayName,
            start: k,
            end: addDays(k, 1),
            allDay: true,
            recurring: true,
            attendees: [],
            alarms: [],
            busy: false,
            categories: [],
            readOnly: true,
            birthYear: c.birthday.startsWith('--') ? undefined : Number(c.birthday.slice(0, 4)),
          });
        }
      }
    }
    if (!q.calendarIds?.length) {
      for (const inv of this.invitations) {
        if (inv.method !== 'REQUEST' || inv.status !== 'pending') continue;
        const k = dayKeyIn(new Date(inv.start), this.tz);
        if (k < from || k > to) continue;
        out.push({
          id: `inv/${inv.id}`,
          objectId: 0,
          calendarId: 0,
          uid: inv.uid,
          kind: 'invitation',
          title: inv.title,
          location: inv.location,
          meetingUrl: inv.meetingUrl,
          start: inv.start,
          end: inv.end,
          allDay: inv.allDay,
          recurring: false,
          organizer: inv.organizer,
          attendees: inv.attendees,
          alarms: [],
          busy: true,
          categories: [],
          readOnly: true,
          myPartStat: 'NEEDS-ACTION',
          invitationId: inv.id,
        });
      }
    }
    return out.sort((a, b) => (a.allDay === b.allDay ? (a.allDay ? a.start.localeCompare(b.start) : Date.parse(a.start) - Date.parse(b.start)) : a.allDay ? -1 : 1));
  }

  private find(id: string): { e: MockEvent; day: DayKey | null } {
    const [oid, inst] = id.split('/');
    const e = this.events.find((x) => x.objectId === Number(oid));
    if (!e) throw new ApiError('not found', 404, 'not_found');
    const day = inst ? `${inst.slice(0, 4)}-${inst.slice(4, 6)}-${inst.slice(6, 8)}` : null;
    return { e, day };
  }

  getEvent(id: string): CalendarEvent {
    if (id.startsWith('inv/')) {
      const ev = this.listEvents({ from: new Date(0).toISOString(), to: new Date(Date.now() + 400 * 86_400_000).toISOString() }).find((x) => x.id === id);
      if (!ev) throw new ApiError('not found', 404, 'not_found');
      return ev;
    }
    const { e, day } = this.find(id);
    return this.toEvent(e, day ?? e.day, !!e.recurrence);
  }

  private apply(target: Partial<MockEvent>, input: EventInput): void {
    target.title = input.title;
    target.description = input.description;
    target.location = input.location;
    target.meetingUrl = input.meetingUrl;
    target.allDay = input.allDay;
    if (input.allDay) {
      target.day = input.start.slice(0, 10);
      target.days = Math.max(1, diffDays(target.day, (input.end || addDays(target.day, 1)).slice(0, 10)));
    } else {
      const s = new Date(input.start);
      target.day = dayKeyIn(s, this.tz);
      target.startMin = minutesIn(s, this.tz);
      target.durationMin = Math.max(15, Math.round((new Date(input.end).getTime() - s.getTime()) / 60_000));
    }
    if (input.attendees) target.attendees = input.attendees.map((a) => ({ partStat: 'NEEDS-ACTION', ...a }));
    if (input.alarms) target.alarms = input.alarms;
    if (input.busy !== undefined) target.busy = input.busy;
    if (input.categories) target.categories = input.categories;
  }

  createEvent(input: EventInput): CalendarEvent {
    const cal = this.cal(input.calendarId || this.calendars.find((c) => c.isDefault)!.id);
    if (cal.readOnly) this.fail('read-only calendar', 'read_only');
    if (!input.title.trim()) this.fail('title required');
    const e: MockEvent = {
      objectId: ++this.seq,
      calendarId: cal.id,
      uid: `mock-${this.seq}@tuxmail`,
      title: '',
      allDay: false,
      day: this.today(),
      startMin: 0,
      durationMin: 60,
      days: 1,
      exdates: [],
      overrides: {},
      attendees: [],
      alarms: [],
      busy: true,
      categories: [],
    };
    this.apply(e, input);
    if (input.recurrence?.freq) e.recurrence = input.recurrence;
    if (e.attendees.length) e.organizer = { email: ME_MAIL, name: 'Jonas Becker' };
    this.events.push(e);
    this.notify('calendar');
    return this.toEvent(e, e.day, !!e.recurrence);
  }

  updateEvent(id: string, input: EventInput, scope: RecurrenceScope): CalendarEvent {
    const { e, day } = this.find(id);
    if (this.cal(e.calendarId).readOnly) this.fail('read-only calendar', 'read_only');
    if (e.recurrence && day && scope === 'this') {
      const ov: Partial<MockEvent> = {};
      this.apply(ov, input);
      e.overrides[day] = ov;
      this.notify('calendar');
      return this.toEvent(e, day, true);
    }
    if (e.recurrence && day && scope === 'following' && day > e.day) {
      e.recurrence = { ...e.recurrence, count: undefined, until: addDays(day, -1) };
      const created = this.createEvent({ ...input, calendarId: e.calendarId, recurrence: input.recurrence ?? { ...e.recurrence, until: undefined } });
      return created;
    }
    if (e.recurrence && day && day !== e.day && !input.allDay) {
      // Drag of an occurrence with scope "all": shift the series.
      const s = new Date(input.start);
      const delta = diffDays(day, dayKeyIn(s, this.tz));
      const len = new Date(input.end).getTime() - s.getTime();
      const ns = zonedInstant(addDays(e.day, delta), minutesIn(s, this.tz), this.tz);
      input = { ...input, start: ns.toISOString(), end: new Date(ns.getTime() + len).toISOString() };
    }
    this.apply(e, input);
    if (input.recurrence !== undefined && input.recurrence !== null) e.recurrence = input.recurrence.freq ? input.recurrence : undefined;
    if (input.calendarId && input.calendarId !== e.calendarId) e.calendarId = input.calendarId;
    this.notify('calendar');
    return this.toEvent(e, e.day, !!e.recurrence);
  }

  deleteEvent(id: string, scope: RecurrenceScope): void {
    if (id.startsWith('inv/')) {
      const inv = this.invitations.find((i) => `inv/${i.id}` === id);
      if (inv) inv.status = 'obsolete';
      this.notify('calendar');
      return;
    }
    const { e, day } = this.find(id);
    if (e.recurrence && day && scope === 'this') e.exdates.push(day);
    else if (e.recurrence && day && scope === 'following' && day > e.day) e.recurrence = { ...e.recurrence, count: undefined, until: addDays(day, -1) };
    else this.events = this.events.filter((x) => x !== e);
    this.notify('calendar');
  }

  freeBusy(q: { attendees: string[]; from: string; to: string }): FreeBusy[] {
    const evs = this.listEvents({ from: q.from, to: q.to, calendarIds: [1, 2] }).filter((e) => e.busy && !e.allDay);
    return q.attendees.map((a) => {
      if (a === ME_MAIL || this.persons().some((p) => p.isMe && p.handles.some((h) => h.value === a))) {
        return { email: a, source: 'local', busy: evs.map((e) => ({ start: e.start, end: e.end })) };
      }
      if (a.endsWith('@kanzlei-sommer.de')) {
        const base = zonedInstant(dayKeyIn(new Date(q.from), this.tz), 0, this.tz).getTime();
        const busy = [0, 1, 2].map((d) => ({ start: new Date(base + (d * 24 + 11) * 3_600_000).toISOString(), end: new Date(base + (d * 24 + 12.5) * 3_600_000).toISOString() }));
        return { email: a, source: 'caldav', busy };
      }
      return { email: a, source: 'unknown', busy: [] };
    });
  }

  // -------------------------------------------------------------------------
  // Tasks
  // -------------------------------------------------------------------------

  listTasks(q: TaskQuery): PimTask[] {
    const today = this.today();
    const s = q.search?.toLowerCase() ?? '';
    const withFlags = this.tasks.map((t) => ({ ...t, overdue: !t.done && !!t.due && t.due.slice(0, 10) < today, color: this.cal(t.calendarId).color }));
    return withFlags
      .filter((t) => !q.calendarIds?.length || q.calendarIds.includes(t.calendarId))
      .filter((t) => !s || t.title.toLowerCase().includes(s))
      .filter((t) => {
        const due = t.due?.slice(0, 10);
        switch (q.filter ?? 'open') {
          case 'all':
            return true;
          case 'completed':
            return t.done;
          case 'today':
            return !t.done && !!due && due <= today;
          case 'overdue':
            return !!t.overdue;
          case 'upcoming':
            return !t.done && !!due && due >= today;
          default:
            return !t.done;
        }
      })
      .sort((a, b) => (a.due ?? '9999').localeCompare(b.due ?? '9999') || (a.priority || 10) - (b.priority || 10) || a.title.localeCompare(b.title));
  }

  saveTask(input: TaskInput): PimTask {
    if (!input.title.trim()) this.fail('title required');
    let t = input.id ? this.tasks.find((x) => x.id === input.id) : undefined;
    if (input.id && !t) throw new ApiError('not found', 404, 'not_found');
    if (!t) {
      t = { id: ++this.seq, calendarId: input.calendarId || 1, uid: `task-${this.seq}`, title: '', priority: 0, progress: 0, status: 'NEEDS-ACTION', done: false, categories: [] };
      this.tasks.push(t);
    }
    const parent = input.parentId ? this.tasks.find((x) => x.id === input.parentId) : undefined;
    Object.assign(t, {
      title: input.title,
      description: input.description,
      due: input.due || undefined,
      dueAllDay: input.dueAllDay,
      priority: input.priority ?? 0,
      categories: input.categories ?? [],
      parentId: parent?.id,
      parentUid: parent?.uid,
      calendarId: input.calendarId || t.calendarId,
      recurrence: input.recurrence ?? undefined,
    });
    this.setDone(t, !!input.done, input.progress ?? 0);
    this.notify('calendar');
    return t;
  }

  private setDone(t: PimTask, done: boolean, progress: number): void {
    t.done = done;
    t.progress = done ? 100 : progress;
    t.status = done ? 'COMPLETED' : progress > 0 ? 'IN-PROCESS' : 'NEEDS-ACTION';
    t.completed = done ? this.now().toISOString() : undefined;
  }

  setTaskDone(id: number, done: boolean): boolean {
    const t = this.tasks.find((x) => x.id === id);
    if (!t) return false;
    this.setDone(t, done, 0);
    this.notify('calendar');
    return true;
  }

  deleteTask(id: number): void {
    this.tasks = this.tasks.filter((t) => t.id !== id);
    this.notify('calendar');
  }

  // -------------------------------------------------------------------------
  // Invitations
  // -------------------------------------------------------------------------

  messageInvitations(messageId: number): Invitation[] {
    return this.invitations.filter((i) => i.messageId === messageId);
  }

  listInvitations(): Invitation[] {
    return this.invitations.filter((i) => i.status === 'pending');
  }

  respond(id: number, r: InvitationResponse): Invitation {
    const inv = this.invitations.find((i) => i.id === id);
    if (!inv) throw new ApiError('not found', 404, 'not_found');
    if (r.proposeStart) return inv;
    const status = { ACCEPTED: 'accepted', TENTATIVE: 'tentative', DECLINED: 'declined' }[r.partStat];
    inv.status = status;
    inv.myPartStat = r.partStat;
    inv.attendees = inv.attendees.map((a) => (a.email === ME_MAIL ? { ...a, partStat: r.partStat } : a));
    if (r.partStat !== 'DECLINED' && !inv.eventId) {
      const ev = this.createEvent({
        calendarId: r.calendarId || 2,
        title: inv.title,
        location: inv.location,
        meetingUrl: inv.meetingUrl,
        start: inv.start,
        end: inv.end,
        allDay: inv.allDay,
        alarms: [{ minutesBefore: 15 }],
      });
      const e = this.events.find((x) => x.objectId === ev.objectId)!;
      e.organizer = inv.organizer;
      e.attendees = inv.attendees;
      inv.eventId = ev.id;
    }
    this.notify('calendar');
    return inv;
  }

  // -------------------------------------------------------------------------
  // Contacts
  // -------------------------------------------------------------------------

  private channelsOf(c: PimContact): PimContact['channels'] {
    const p = this.persons().find((x) => x.id === c.personId);
    return p ? p.handles : [];
  }

  private view(c: PimContact): PimContact {
    return { ...c, channels: this.channelsOf(c) };
  }

  listBooks(): PimAddressBook[] {
    return this.books.map((b) => ({ ...b, count: this.contacts.filter((c) => c.addressBookId === b.id).length }));
  }

  saveBook(input: { id?: number; name: string; isDefault?: boolean }): PimAddressBook {
    let b = input.id ? this.books.find((x) => x.id === input.id) : undefined;
    if (!b) {
      b = { id: ++this.seq, name: input.name, kind: 'local', isDefault: false, count: 0 };
      this.books.push(b);
    }
    if (input.name) b.name = input.name;
    if (input.isDefault) for (const x of this.books) x.isDefault = x === b;
    this.notify('contacts');
    return b;
  }

  deleteBook(id: number): void {
    if (this.books.find((b) => b.id === id)?.kind !== 'local') this.fail('cannot delete');
    this.books = this.books.filter((b) => b.id !== id);
    this.contacts = this.contacts.filter((c) => c.addressBookId !== id);
    this.notify('contacts');
  }

  listContacts(q: { addressBookId?: number; search?: string; group?: string }): PimContact[] {
    const words = (q.search ?? '').toLowerCase().split(/\s+/).filter(Boolean);
    return this.contacts
      .filter((c) => !q.addressBookId || c.addressBookId === q.addressBookId)
      .filter((c) => !q.group || c.categories.includes(q.group))
      .filter((c) => {
        const hay = [c.displayName, c.org, c.title, ...c.emails.map((e) => e.value), ...c.phones.map((p) => p.value)].join(' ').toLowerCase();
        return words.every((w) => hay.includes(w));
      })
      .map((c) => this.view(c))
      .sort((a, b) => (a.n.family || a.displayName).localeCompare(b.n.family || b.displayName));
  }

  getContact(id: number): PimContact {
    const c = this.contacts.find((x) => x.id === id);
    if (!c) throw new ApiError('not found', 404, 'not_found');
    return this.view(c);
  }

  saveContact(input: PimContact): PimContact {
    const name = input.fn?.trim() || [input.n.prefix, input.n.given, input.n.additional, input.n.family, input.n.suffix].filter(Boolean).join(' ') || input.org || input.emails[0]?.value;
    if (!name) this.fail('name required');
    let c = input.id ? this.contacts.find((x) => x.id === input.id) : undefined;
    if (!c) {
      c = { ...emptyContact(), id: ++this.seq, uid: `contact-${this.seq}`, addressBookId: input.addressBookId || this.books.find((b) => b.isDefault)!.id };
      this.contacts.push(c);
    }
    const { photo, channels: _ch, ...rest } = input;
    Object.assign(c, rest, { id: c.id, uid: c.uid, displayName: name!, fn: input.fn || name! });
    if (photo === '-') {
      c.hasPhoto = false;
      c.photoUrl = undefined;
    } else if (photo?.startsWith('data:')) {
      c.hasPhoto = true;
      c.photoUrl = photo;
    }
    if (!c.personId) {
      const email = c.emails[0]?.value.toLowerCase();
      c.personId = this.persons().find((p) => p.handles.some((h) => h.value === email))?.id;
    }
    this.notify('contacts');
    return this.view(c);
  }

  deleteContact(id: number): void {
    this.contacts = this.contacts.filter((c) => c.id !== id);
    this.notify('contacts');
  }

  groups(): ContactGroup[] {
    const m = new Map<string, number>();
    for (const c of this.contacts) for (const g of c.categories) m.set(g, (m.get(g) ?? 0) + 1);
    return [...m].map(([name, count]) => ({ name, count })).sort((a, b) => a.name.localeCompare(b.name));
  }

  private keys(c: PimContact): string[] {
    const phone = (v: string) => {
      let d = v.replace(/[^\d+]/g, '');
      if (d.startsWith('00')) d = '+' + d.slice(2);
      if (d.startsWith('0')) d = '+49' + d.slice(1);
      return d;
    };
    const name = c.displayName.toLowerCase().replace(/[^\p{L}\s]/gu, ' ').split(/\s+/).filter(Boolean).sort().join(' ');
    return [...c.emails.map((e) => 'e:' + e.value.toLowerCase()), ...c.phones.map((p) => 'p:' + phone(p.value)), ...(name.includes(' ') ? ['n:' + name] : [])];
  }

  merged(list: PimContact[]): PimContact {
    const [first, ...rest] = list.map((c) => structuredClone({ ...c }));
    const m = { ...first };
    for (const o of rest) {
      m.org ||= o.org;
      m.title ||= o.title;
      m.birthday ||= o.birthday;
      m.note = [m.note, o.note].filter(Boolean).join('\n\n') || undefined;
      const norm = (v: string) => (/^[+\d\s()/-]+$/.test(v) ? this.keys({ ...emptyContact(), phones: [{ value: v }] })[0] : v.toLowerCase());
      const uniq = <V extends { value: string }>(a: V[], b: V[]) => [...a, ...b.filter((x) => !a.some((y) => norm(y.value) === norm(x.value)))];
      m.emails = uniq(m.emails, o.emails);
      m.phones = uniq(m.phones, o.phones);
      m.urls = uniq(m.urls, o.urls);
      m.impp = uniq(m.impp, o.impp);
      m.addresses = [...m.addresses, ...o.addresses.filter((a) => !m.addresses.some((x) => x.street === a.street))];
      m.categories = [...new Set([...m.categories, ...o.categories])];
      if (!m.hasPhoto && o.hasPhoto) Object.assign(m, { hasPhoto: true, photoUrl: o.photoUrl });
    }
    return m;
  }

  duplicates(): DuplicateGroup[] {
    const groups: PimContact[][] = [];
    const used = new Set<number>();
    for (const c of this.contacts) {
      if (used.has(c.id)) continue;
      const k = new Set(this.keys(c));
      const g = this.contacts.filter((o) => o.id !== c.id && !used.has(o.id) && this.keys(o).some((x) => k.has(x)));
      if (g.length) {
        const all = [c, ...g];
        all.forEach((x) => used.add(x.id));
        groups.push(all);
      }
    }
    return groups.map((g) => ({ contacts: g.map((c) => this.view(c)), preview: this.view(this.merged(g)) }));
  }

  merge(ids: number[], merged?: PimContact): PimContact {
    const list = ids.map((id) => this.contacts.find((c) => c.id === id)).filter((c): c is PimContact => !!c);
    if (list.length < 2) this.fail('at least two contacts');
    const result = merged ? { ...merged, id: list[0].id } : this.merged(list);
    this.contacts = this.contacts.filter((c) => !ids.slice(1).includes(c.id));
    Object.assign(list[0], result, { id: list[0].id });
    this.notify('contacts');
    return this.view(list[0]);
  }

  autocomplete(q: string, limit: number): AddressSuggestion[] {
    const s = q.trim().toLowerCase();
    if (!s) return [];
    const out: AddressSuggestion[] = [];
    for (const c of this.contacts) {
      for (const e of c.emails) {
        if (c.displayName.toLowerCase().includes(s) || e.value.toLowerCase().includes(s)) out.push({ name: c.displayName, email: e.value, contactId: c.id, personId: c.personId, source: 'contact' });
      }
    }
    for (const p of this.persons()) {
      if (p.isMe) continue;
      for (const h of p.handles) {
        if (h.kind === 'email' && !out.some((o) => o.email === h.value) && (p.displayName.toLowerCase().includes(s) || h.value.includes(s))) {
          out.push({ name: p.displayName, email: h.value, personId: p.id, source: 'recent' });
        }
      }
    }
    return out.slice(0, limit);
  }
}
