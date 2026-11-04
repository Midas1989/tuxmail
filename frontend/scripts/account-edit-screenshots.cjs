// SPDX-License-Identifier: AGPL-3.0-or-later
// Screenshots of the account edit dialog ("Konto bearbeiten") in mock mode.
//   npm run build && npx vite preview --port 4175 &
//   node scripts/account-edit-screenshots.cjs docs/screenshots   (playwright: local or global install)
const { chromium } = (() => {
  try {
    return require('playwright');
  } catch {
    const root = require('child_process').execSync('npm root -g').toString().trim();
    return require(require('path').join(root, 'playwright'));
  }
})();
const base = process.env.TUXMAIL_URL ?? 'http://127.0.0.1:4175';
const out = process.argv[2] ?? 'docs/screenshots';

async function shot(page, name) {
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${out}/${name}.jpg`, quality: 85, type: 'jpeg' });
  console.log('screenshot', name);
}

async function open(browser, opts = {}) {
  const ctx = await browser.newContext({
    viewport: opts.mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: opts.mobile ? 2 : 1,
    locale: 'de-DE',
    timezoneId: 'Europe/Berlin',
    colorScheme: opts.dark ? 'dark' : 'light',
  });
  await ctx.addInitScript((dark) => {
    try {
      localStorage.setItem('tuxmail.prefs.v2', JSON.stringify({ locale: 'de', theme: dark ? 'dark' : 'light' }));
    } catch {}
  }, !!opts.dark);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
  await page.goto(base + '/?api=mock#/settings');
  await page.waitForSelector('.acc-row');
  return { ctx, page, errors };
}

async function tab(page, name) {
  await page.click(`[role="dialog"] [role="tab"]:has-text("${name}")`);
  await page.waitForTimeout(300);
}

(async () => {
  const browser = await chromium.launch();
  const all = [];

  // IMAP account: general, server with connection test, identities, folders, advanced.
  let s = await open(browser);
  await s.page.click('.acc-row:has-text("Privat")');
  await s.page.waitForSelector('[role="dialog"] [role="tab"]');
  await shot(s.page, 'account-edit-general');
  await tab(s.page, 'Server');
  await s.page.click('[role="dialog"] summary');
  await s.page.click('[role="dialog"] button:has-text("Verbindung testen")');
  await s.page.waitForSelector('[role="dialog"] .tests li');
  await s.page.locator('[role="dialog"] .tests').scrollIntoViewIfNeeded();
  await shot(s.page, 'account-edit-server');
  await tab(s.page, 'Identitäten');
  await s.page.click('[role="dialog"] button:has-text("Identität hinzufügen")');
  await s.page.fill('[role="dialog"] .edit input[type="email"] >> nth=0', 'info@becker.example');
  await shot(s.page, 'account-edit-identities');
  await s.page.click('[role="dialog"] .edit button:has-text("Abbrechen")');
  await tab(s.page, 'Ordner');
  await s.page.waitForSelector('[role="dialog"] .table');
  await shot(s.page, 'account-edit-folders');
  await tab(s.page, 'Erweitert');
  await shot(s.page, 'account-edit-advanced');
  await s.page.keyboard.press('Escape');
  all.push(...s.errors);
  await s.ctx.close();

  // Exchange account with a login error, "Erneut versuchen" and removal confirmation (dark).
  s = await open(browser, { dark: true });
  await s.page.click('.acc-row:has-text("Kanzlei Sommer")');
  await s.page.waitForSelector('[role="dialog"] .errbox');
  await s.page.click('[role="dialog"] button:has-text("Konto entfernen")');
  await shot(s.page, 'account-edit-error-dark');
  await tab(s.page, 'Server');
  await s.page.fill('[role="dialog"] input[type="password"]', 'wrong');
  await s.page.click('[role="dialog"] button:has-text("Passwort ändern")');
  await s.page.waitForSelector('[role="dialog"] p.error');
  await shot(s.page, 'account-edit-password-dark');
  all.push(...s.errors);
  await s.ctx.close();

  // Mobile.
  s = await open(browser, { mobile: true });
  await s.page.click('.acc-row:has-text("Privat")');
  await s.page.waitForSelector('[role="dialog"] [role="tab"]');
  await shot(s.page, 'account-edit-mobile');
  all.push(...s.errors);
  await s.ctx.close();

  await browser.close();
  if (all.length) {
    console.error('page errors:\n' + all.join('\n'));
    process.exit(1);
  }
})();
