// SPDX-License-Identifier: AGPL-3.0-or-later
// E2E against a real server (KONZEPT 4.5). Setup:
//   tuxmail user add --config C --username anna --password-stdin && tuxmail demo seed --config C --user anna
//   tuxmail serve --config C   (listen 127.0.0.1:18080, frontend embedded via go generate ./web)
//   NODE_PATH=$(npm root -g) node scripts/e2e-real.cjs docs/screenshots
const { chromium } = require('playwright');
const base = process.env.TUXMAIL_URL ?? 'http://127.0.0.1:18080';
const password = process.env.TUXMAIL_PASSWORD ?? 'correct horse battery staple';
const out = process.argv[2] ?? 'docs/screenshots';

async function shot(page, name) {
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/real-${name}.jpg`, quality: 85, type: 'jpeg' });
  console.log('screenshot', name);
}

async function session(browser, viewMode) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'de-DE' });
  await ctx.addInitScript((mode) => {
    try {
      localStorage.setItem('tuxmail.prefs.v2', JSON.stringify({ viewMode: mode, grouping: 'person', locale: 'de' }));
    } catch {}
  }, viewMode);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('response', (r) => {
    if (r.url().includes('/api/') && r.status() >= 400) errors.push(`${r.status()} ${r.url()}`);
  });
  await page.goto(base + '/');
  await page.waitForURL(/\/login/);
  await page.fill('#u', 'anna');
  await page.fill('#p', password);
  await Promise.all([page.waitForURL((u) => !u.pathname.startsWith('/login')), page.click('#submit')]);
  return { ctx, page, errors };
}

(async () => {
  const browser = await chromium.launch();
  // Classic mail view
  let s = await session(browser, 'classic');
  await s.page.waitForSelector('text=Angebot Website-Relaunch', { timeout: 15000 });
  await shot(s.page, 'mailbox');
  await s.page.click('text=Ihre Rechnung Oktober 2026');
  await s.page.waitForSelector('text=RE-2026-1042', { timeout: 10000 });
  await shot(s.page, 'message');
  // Search
  await s.page.goto(base + '/#/search');
  await s.page.waitForTimeout(1000);
  const input = await s.page.$('input[type="search"]');
  if (input) {
    await input.fill('Angebot');
    await input.press('Enter');
    await s.page.waitForSelector('text=Angebot-2026-117.pdf, text=Angebot Website-Relaunch', { timeout: 10000 }).catch(() => {});
  }
  await shot(s.page, 'search');
  console.log('errors (classic):', JSON.stringify(s.errors));
  await s.ctx.close();

  // Chat view (person grouping)
  s = await session(browser, 'chat');
  await s.page.waitForSelector('text=Maria Sommer', { timeout: 15000 });
  await s.page.click('text=Maria Sommer');
  await s.page.waitForSelector('text=das Logo habe ich noch etwas angepasst', { timeout: 10000 });
  await shot(s.page, 'conversation');
  console.log('errors (chat):', JSON.stringify(s.errors));
  await s.ctx.close();

  // Hybrid view
  s = await session(browser, 'hybrid');
  await s.page.waitForSelector('text=Angebot Website-Relaunch', { timeout: 15000 });
  await s.page.click('text=Angebot Website-Relaunch');
  await s.page.waitForTimeout(1500);
  await shot(s.page, 'hybrid');
  console.log('errors (hybrid):', JSON.stringify(s.errors));
  await s.ctx.close();
  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
