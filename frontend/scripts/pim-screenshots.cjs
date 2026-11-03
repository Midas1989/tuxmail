// SPDX-License-Identifier: AGPL-3.0-or-later
// Screenshots of calendar, tasks and contacts in mock mode (KONZEPT 9, 10).
//   npm run build && npx vite preview --port 4174 &
//   NODE_PATH=$(npm root -g) node scripts/pim-screenshots.cjs docs/screenshots
const { chromium } = require('playwright');
const base = process.env.TUXMAIL_URL ?? 'http://127.0.0.1:4174';
const out = process.argv[2] ?? 'docs/screenshots';

async function context(browser, opts = {}) {
  const ctx = await browser.newContext({
    viewport: opts.mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: opts.mobile ? 2 : 1,
    locale: opts.lang === 'en' ? 'en-US' : 'de-DE',
    timezoneId: 'Europe/Berlin',
    colorScheme: opts.dark ? 'dark' : 'light',
  });
  await ctx.addInitScript(
    ([lang, view, secondary, dark]) => {
      try {
        localStorage.setItem(
          'tuxmail.prefs.v2',
          JSON.stringify({ locale: lang, theme: dark ? 'dark' : 'light', region: { timeZone: 'Europe/Berlin', secondaryTimeZone: secondary } }),
        );
        localStorage.setItem('tuxmail.mock.preferences.v1', JSON.stringify({ locale: lang, region: { timeZone: 'Europe/Berlin', secondaryTimeZone: secondary } }));
        if (view) localStorage.setItem('tuxmail.calendar.view', view);
      } catch {}
    },
    [opts.lang ?? 'de', opts.view ?? '', opts.secondary ?? '', !!opts.dark],
  );
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
  return { ctx, page, errors };
}

async function shot(page, name) {
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${out}/${name}.jpg`, quality: 85, type: 'jpeg' });
  console.log('screenshot', name);
}

(async () => {
  const browser = await chromium.launch();
  const all = [];

  // Week view with drag targets, recurring events, invitation and all-day row.
  let s = await context(browser, { view: 'week', secondary: 'America/New_York' });
  await s.page.goto(base + '/?api=mock#/calendar');
  await s.page.waitForSelector('.tg .ev');
  await shot(s.page, 'calendar-week');
  // Event dialog of a recurring event with attendees.
  await s.page.click('.tg .ev:has-text("Mandantentermin")');
  await s.page.waitForSelector('[role="dialog"]');
  await shot(s.page, 'calendar-event-dialog');
  await s.page.keyboard.press('Escape');
  // New event with recurrence editor (custom).
  await s.page.click('button:has-text("Neuer Termin")');
  await s.page.fill('[role="dialog"] input.big', 'Sprint-Review');
  await s.page.selectOption('#rec-preset', 'custom');
  await s.page.fill('[role="dialog"] input[placeholder^="Teilnehmer"]', 'max');
  await s.page.waitForTimeout(400);
  await s.page.keyboard.press('Enter');
  await shot(s.page, 'calendar-new-recurring');
  await s.page.keyboard.press('Escape');
  // Quick entry with natural language.
  await s.page.fill('form.quick input', 'Zahnarzt morgen 15 Uhr');
  await shot(s.page, 'calendar-quick-entry');
  await s.page.press('form.quick input', 'Enter');
  // Month and agenda.
  await s.page.click('[role="radio"]:has-text("Monat")');
  await s.page.waitForSelector('.mv');
  await shot(s.page, 'calendar-month');
  await s.page.click('[role="radio"]:has-text("Agenda")');
  await shot(s.page, 'calendar-agenda');
  await s.page.click('[role="radio"]:has-text("Aufgaben")');
  await s.page.waitForSelector('.tv .list');
  await shot(s.page, 'calendar-tasks');
  all.push(...s.errors);
  await s.ctx.close();

  // Day view, English, dark.
  s = await context(browser, { view: 'day', lang: 'en', dark: true, secondary: 'Asia/Tokyo' });
  await s.page.goto(base + '/?api=mock#/calendar');
  await s.page.waitForSelector('.tg');
  await shot(s.page, 'calendar-day-dark-en');
  all.push(...s.errors);
  await s.ctx.close();

  // Invitation card in the reading pane (classic view).
  s = await context(browser, {});
  await s.page.addInitScript(() => {
    try {
      const p = JSON.parse(localStorage.getItem('tuxmail.prefs.v2') || '{}');
      localStorage.setItem('tuxmail.prefs.v2', JSON.stringify({ ...p, viewMode: 'classic' }));
    } catch {}
  });
  await s.page.goto(base + '/?api=mock#/mail');
  await s.page.click('text="Teammeeting Donnerstag 10 Uhr"');
  await s.page.waitForSelector('section.inv', { timeout: 8000 }).catch(async () => {
    await shot(s.page, 'debug-invitation');
    throw new Error('invitation card missing');
  });
  await shot(s.page, 'calendar-invitation');
  await s.page.click('section.inv button:has-text("Zusagen")');
  await s.page.waitForTimeout(400);
  await shot(s.page, 'calendar-invitation-accepted');
  all.push(...s.errors);
  await s.ctx.close();

  // Contacts.
  s = await context(browser, {});
  await s.page.goto(base + '/?api=mock#/contacts');
  await s.page.waitForSelector('.list .row');
  await s.page.click('.list .row:has-text("Anna Müller")');
  await shot(s.page, 'contacts');
  await s.page.click('.detail button:has-text("Bearbeiten")');
  await s.page.waitForSelector('[role="dialog"]');
  await shot(s.page, 'contacts-edit');
  await s.page.keyboard.press('Escape');
  await s.page.click('button:has-text("Duplikate finden")');
  await s.page.waitForSelector('[role="dialog"] .grp');
  await shot(s.page, 'contacts-merge');
  all.push(...s.errors);
  await s.ctx.close();

  // Mobile.
  s = await context(browser, { mobile: true, view: 'day' });
  await s.page.goto(base + '/?api=mock#/calendar');
  await s.page.waitForSelector('.tg');
  await shot(s.page, 'calendar-mobile');
  await s.page.goto(base + '/?api=mock#/contacts');
  await s.page.waitForSelector('.list .row');
  await s.page.click('.list .row:has-text("Paul Richter")');
  await shot(s.page, 'contacts-mobile');
  all.push(...s.errors);
  await s.ctx.close();

  await browser.close();
  console.log('errors:', JSON.stringify(all));
})();
