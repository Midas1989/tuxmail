// SPDX-License-Identifier: AGPL-3.0-or-later
// Screenshots of the Windows setup window (cmd/tuxmail-setup/ui) in a
// browser with the mock bridge (cmd/tuxmail-setup/ui-dev/mock.js).
//   NODE_PATH=$(npm root -g) node scripts/setup-screenshots.cjs docs/screenshots
const http = require('http');
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');

const out = path.resolve(process.argv[2] ?? 'docs/screenshots');
const root = path.resolve(__dirname, '../../cmd/tuxmail-setup');
const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png' };

function serve() {
  const srv = http.createServer((req, res) => {
    const u = new URL(req.url, 'http://x');
    const file = path.join(root, 'ui', path.normalize(u.pathname === '/' ? 'index.html' : u.pathname));
    if (!file.startsWith(path.join(root, 'ui')) || !fs.existsSync(file)) {
      res.writeHead(404);
      return res.end();
    }
    res.writeHead(200, { 'content-type': types[path.extname(file)] || 'application/octet-stream' });
    fs.createReadStream(file).pipe(res);
  });
  return new Promise((r) => srv.listen(0, '127.0.0.1', () => r(srv)));
}

(async () => {
  const srv = await serve();
  const base = `http://127.0.0.1:${srv.address().port}/`;
  const browser = await chromium.launch();
  const errors = [];

  async function open(dark, query = '') {
    const ctx = await browser.newContext({ viewport: { width: 860, height: 580 }, deviceScaleFactor: 1, locale: 'de-DE', colorScheme: dark ? 'dark' : 'light' });
    await ctx.addInitScript({ path: path.join(root, 'ui-dev', 'mock.js') });
    const page = await ctx.newPage();
    page.on('pageerror', (e) => errors.push(String(e)));
    page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
    await page.goto(base + '?lang=de' + query);
    await page.waitForSelector('.footer .btn');
    return { ctx, page };
  }
  const shot = async (page, name, dark) => {
    await page.waitForTimeout(500);
    const file = `${out}/setup-${name}${dark ? '-dark' : ''}.jpg`;
    await page.screenshot({ path: file, quality: 86, type: 'jpeg' });
    console.log('screenshot', path.basename(file));
  };
  const next = (page) => page.click('.footer [data-action="next"]');

  for (const dark of [false, true]) {
    // Install: welcome -> type -> destination -> options -> running dialog -> progress -> done
    let { ctx, page } = await open(dark, '&running=1');
    await shot(page, 'welcome', dark);
    await next(page);
    await page.click('[data-scope="machine"]');
    await shot(page, 'type', dark);
    await next(page);
    await page.waitForSelector('#dir');
    await shot(page, 'dir', dark);
    await page.fill('#dir', 'C:\\Windows\\TuxMail');
    await page.waitForSelector('.notice.error');
    await shot(page, 'dir-error', dark);
    await page.fill('#dir', 'C:\\Program Files\\TuxMail');
    await page.waitForSelector('.footer [data-action="next"]:not([disabled])');
    await next(page);
    await page.click('[data-opt="desktop"]');
    await page.click('[data-opt="defaultMail"]');
    await shot(page, 'options', dark);
    await page.click('.footer [data-action="install"]');
    await page.waitForSelector('.dialog');
    await shot(page, 'running', dark);
    await page.click('[data-m="yes"]');
    await page.waitForSelector('.footer [data-action="launch"]', { timeout: 15000 });
    await page.waitForTimeout(900);
    await shot(page, 'done', dark);
    await ctx.close();

    // Progress (frozen at 58 %).
    ({ ctx, page } = await open(dark, '&hold=58'));
    await next(page);
    await next(page);
    await page.waitForSelector('#dir');
    await next(page);
    await page.click('.footer [data-action="install"]');
    await page.waitForFunction(() => parseFloat(document.getElementById('bar')?.style.width || '0') >= 50);
    await shot(page, 'progress', dark);
    await ctx.close();

    // Update of an existing installation (welcome notice).
    ({ ctx, page } = await open(dark, '&existing=upgrade'));
    await shot(page, 'update', dark);
    await ctx.close();

    // Portable done page.
    ({ ctx, page } = await open(dark));
    await next(page);
    await page.click('[data-scope="portable"]');
    await next(page);
    await page.waitForSelector('#dir');
    await page.click('.footer [data-action="install"]');
    await page.waitForSelector('.footer [data-action="openfolder"]', { timeout: 15000 });
    await page.waitForTimeout(900);
    await shot(page, 'portable-done', dark);
    await ctx.close();

    // Error page.
    ({ ctx, page } = await open(dark, '&fail=1'));
    await next(page);
    await next(page);
    await page.waitForSelector('#dir');
    await next(page);
    await page.click('.footer [data-action="install"]');
    await page.waitForSelector('.error-box', { timeout: 15000 });
    await shot(page, 'error', dark);
    await ctx.close();

    // Uninstall: page, "remove data" warning, done.
    ({ ctx, page } = await open(dark, '&mode=uninstall'));
    await shot(page, 'uninstall', dark);
    await page.click('[data-action="keepdata"]');
    await shot(page, 'uninstall-remove-data', dark);
    await page.click('[data-action="keepdata"]');
    await page.click('.footer [data-action="uninstall"]');
    await page.waitForSelector('.footer .btn-primary[data-action="quit"]', { timeout: 15000 });
    await page.waitForTimeout(700);
    await shot(page, 'uninstall-done', dark);
    await ctx.close();
  }

  await browser.close();
  srv.close();
  if (errors.length) {
    console.error('page errors:\n' + errors.join('\n'));
    process.exit(1);
  }
})();
