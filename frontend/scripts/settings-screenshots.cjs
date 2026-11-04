// SPDX-License-Identifier: AGPL-3.0-or-later
// Screenshots of the settings screen (two-pane layout, search, phone list → page) in mock mode.
//   npm run build && npx vite preview --port 4175 &
//   node scripts/settings-screenshots.cjs docs/screenshots   (playwright: local or global install)
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
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${out}/${name}.jpg`, quality: 85, type: 'jpeg' });
  console.log('screenshot', name);
}

async function open(browser, hash, opts = {}) {
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
  await page.goto(base + '/?api=mock' + hash);
  await page.waitForSelector('.settings nav .nav-item');
  return { ctx, page, errors };
}

(async () => {
  const browser = await chromium.launch();
  const all = [];
  const run = async (hash, opts, fn) => {
    const s = await open(browser, hash, opts);
    await fn(s.page);
    all.push(...s.errors);
    await s.ctx.close();
  };

  for (const dark of [false, true]) {
    const sfx = dark ? '-dark' : '';
    await run('#/settings', { dark }, (p) => shot(p, 'settings-overview' + sfx));
    await run('#/settings/accounts', { dark }, async (p) => {
      await p.waitForSelector('.acc-row');
      await shot(p, 'settings-accounts' + sfx);
    });
    await run('#/settings/mail', { dark }, (p) => shot(p, 'settings-mail' + sfx));
    await run('#/settings/security', { dark }, (p) => shot(p, 'settings-security' + sfx));
    await run('#/settings', { dark, mobile: true }, (p) => shot(p, 'settings-mobile-list' + sfx));
    await run('#/settings', { dark, mobile: true }, async (p) => {
      await p.click('.nav-item[data-cat="mail"]');
      await p.waitForSelector('[data-testid="settings-page"][data-category="mail"]');
      await shot(p, 'settings-mobile-page' + sfx);
    });
  }

  // Search: "signatur" → jumps to Konten › Signaturen and highlights the section.
  await run('#/settings', {}, async (p) => {
    await p.fill('[data-testid="settings-search"]', 'Signatur');
    await shot(p, 'settings-search');
    await p.keyboard.press('Enter');
    await p.waitForSelector('#settings-accounts-signatures.flash');
    await p.waitForTimeout(150);
    await p.screenshot({ path: `${out}/settings-search-jump.jpg`, quality: 85, type: 'jpeg' });
    console.log('screenshot', 'settings-search-jump');
  });

  await browser.close();
  if (all.length) {
    console.error('page errors:\n' + all.join('\n'));
    process.exit(1);
  }
})();
