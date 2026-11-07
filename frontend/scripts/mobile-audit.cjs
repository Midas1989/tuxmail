// SPDX-License-Identifier: AGPL-3.0-or-later
// Automatic phone-layout audit (mock mode, Android emulation): renders the main screens at
// 360x800 and 412x915 (deviceScaleFactor 2.625, touch) with html.tm-native and system-bar insets
// (top 32 px, bottom 24 px), light and dark, and fails on regressions:
//   - horizontal overflow (document, or elements leaving the viewport outside scroll containers)
//   - touch targets below 44x44 CSS px, icon-only buttons with icons below 20 px
//   - text fields below 16 px, operating text (buttons, links) below 14 px
//   - fixed/sticky controls inside the status-bar or navigation-bar area
//   npm run build && npx vite preview --port 4176 &
//   TUXMAIL_URL=http://127.0.0.1:4176 node scripts/mobile-audit.cjs [outDir] [--shots] [--screens=a,b]
// Exit code 1 when something is found. Screenshots (with --shots) go to outDir.
const { chromium } = (() => {
  try {
    return require('playwright');
  } catch {}
  try {
    const root = require('child_process').execSync('npm root -g', { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    return require(require('path').join(root, 'playwright'));
  } catch {}
  console.error('mobile-audit: playwright not found (neither local nor global).');
  console.error('Install it without touching package.json:  npm i --no-save playwright && npx playwright install chromium');
  process.exit(2);
})();
const fs = require('fs');
const path = require('path');

const base = process.env.TUXMAIL_URL ?? 'http://127.0.0.1:4176';
const args = process.argv.slice(2);
const out = args.find((a) => !a.startsWith('--')) ?? 'docs/screenshots/mobile';
const shots = args.includes('--shots');
const only = (args.find((a) => a.startsWith('--screens=')) ?? '').slice(10).split(',').filter(Boolean);
const nativeCss = fs.readFileSync(path.join(__dirname, '../src/lib/native/native.css'), 'utf8');
const INSET = { top: 32, bottom: 24 };
const SIZES = [
  { n: '360', w: 360, h: 800 },
  { n: '412', w: 412, h: 915 },
];

const SCREENS = [
  ['mail-list', '#/mail'],
  ['mail-read', '#/mail', async (p) => { await p.locator('[data-testid="message-row"], .row, li button').first().click().catch(() => {}); }],
  ['compose', '#/mail', async (p) => { await p.keyboard.press('c').catch(() => {}); }],
  ['drawer', '#/mail', async (p) => { await p.locator('button.menu').first().click(); }],
  ['more-sheet', '#/mail', async (p) => { await p.locator('button.more').click(); }],
  ['chats', '#/mail', async (p) => { await p.locator('nav.rail button', { hasText: 'Chats' }).click(); }],
  ['chat-open', '#/mail', async (p) => { await p.locator('nav.rail button', { hasText: 'Chats' }).click(); await p.waitForTimeout(400); await p.getByText('Anna Müller').first().click(); }],
  ['chat-bubble-sheet', '#/mail', async (p) => { await p.locator('nav.rail button', { hasText: 'Chats' }).click(); await p.waitForTimeout(400); await p.getByText('Anna Müller').first().click(); await p.waitForTimeout(400); await p.getByText('Hallo Herr Becker').first().click(); }],
  ['chat-filter-sheet', '#/mail', async (p) => { await p.locator('nav.rail button', { hasText: 'Chats' }).click(); await p.waitForTimeout(400); await p.getByRole('button', { name: /^Filter/ }).click(); }],
  ['calendar', '#/calendar'],
  ['calendar-month', '#/calendar', async (p) => { await p.getByText('Monat', { exact: true }).first().click({ timeout: 4000 }); }],
  ['calendar-week', '#/calendar', async (p) => { await p.getByText('3 Tage', { exact: true }).first().click({ timeout: 4000 }); }],
  ['calendar-day', '#/calendar', async (p) => { await p.getByText('Tag', { exact: true }).first().click({ timeout: 4000 }); }],
  ['calendar-event', '#/calendar', async (p) => { await p.locator('button.row').first().click(); }],
  ['tasks', '#/calendar', async (p) => { await p.getByText('Aufgaben', { exact: true }).first().click({ timeout: 4000 }); }],
  ['contacts', '#/contacts'],
  ['contact-open', '#/contacts', async (p) => { await p.locator('main li button, main [role=listitem], main .row').first().click(); }],
  ['projects', '#/projects'],
  ['project-open', '#/projects', async (p) => { await p.locator('main button.card, main a.card, main li button, main .card').first().click(); }],
  ['files', '#/files'],
  ['search', '#/search'],
  ['settings', '#/settings'],
  ['settings-general', '#/settings/general'],
  ['settings-accounts', '#/settings/accounts'],
  ['settings-account-add', '#/settings/accounts/list', async (p) => { await p.getByRole('button', { name: /Konto hinzufügen/ }).first().click(); }],
  ['settings-mail', '#/settings/mail'],
  ['settings-chats', '#/settings/chats'],
  ['settings-pim', '#/settings/pim'],
  ['settings-security', '#/settings/security'],
  ['settings-data', '#/settings/data'],
  ['settings-automation', '#/settings/automation'],
  ['native-settings', '#/native-settings'],
];

/* Runs in the page; returns problems per category. */
function measure({ top, bottom }) {
  const vw = document.documentElement.clientWidth;
  const vh = window.innerHeight;
  const vis = (el) => {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' && s.opacity !== '0';
  };
  const label = (el) => {
    const txt = (el.getAttribute('aria-label') || el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 28);
    const cls = typeof el.className === 'string' ? el.className.trim().split(/\s+/).filter((c) => !c.startsWith('svelte-')).slice(0, 2).join('.') : '';
    return `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${cls ? '.' + cls : ''} "${txt}"`;
  };
  const res = { overflow: [], target: [], icon: [], field: [], text: [], bars: [] };
  const dw = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - vw;
  if (dw > 1) res.overflow.push(`document scrolls horizontally by ${dw}px`);
  const interactive = 'button, a[href], [role=button], [role=tab], [role=menuitem], [role=radio], input:not([type=hidden]), select, textarea, summary';
  for (const el of document.querySelectorAll('body *')) {
    if (!vis(el)) continue;
    if (el.closest('.skip, [data-audit-skip]')) continue;
    const r = el.getBoundingClientRect();
    let clippedByScroller = false;
    for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
      const o = getComputedStyle(a).overflowX;
      if (o === 'auto' || o === 'scroll' || o === 'hidden') {
        const ar = a.getBoundingClientRect();
        if (r.right > ar.right + 1 || r.left < ar.left - 1) { clippedByScroller = true; break; }
      }
    }
    if (!clippedByScroller && (r.right > vw + 1 || r.left < -1) && getComputedStyle(el).position !== 'fixed') res.overflow.push(`${label(el)} L${Math.round(r.left)} R${Math.round(r.right)}`);
    if (!el.matches(interactive)) continue;
    if (el.disabled && el.tagName !== 'BUTTON') continue;
    // a control cut off by an overflow:hidden ancestor is unreachable
    for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.overflowX === 'auto' || cs.overflowX === 'scroll') break; // scrolls on purpose (tab rows, lists)
      if (cs.overflowX === 'hidden' || cs.overflowX === 'clip') {
        const ar = a.getBoundingClientRect();
        if (r.right > ar.right + 2 || r.left < ar.left - 2) { res.overflow.push(`${label(el)} is cut off by ${label(a)}`); break; }
      }
    }
    const type = el.getAttribute('type');
    const wrapped = el.closest('label');
    const inBox = (type === 'checkbox' || type === 'radio') && wrapped && wrapped.getBoundingClientRect().height >= 44;
    const inline = el.tagName === 'A' && el.closest('p, li > span, .hint, .intro, small');
    // a bare text input inside a taller field box (search bar, chip input) counts through its box
    const box = false;
    const bareCheck = (type === 'checkbox' || type === 'radio') && r.width >= 23.5 && r.height >= 23.5;
    if (!inBox && !bareCheck && !inline && !box && !el.closest('[data-touch-compact]') && (r.width < 43.5 || r.height < 43.5)) res.target.push(`${label(el)} ${Math.round(r.width)}x${Math.round(r.height)}`);
    if (el.matches('button, a, [role=button]') && !(el.textContent || '').trim()) {
      for (const svg of el.querySelectorAll('svg')) {
        const sr = svg.getBoundingClientRect();
        if (sr.width && sr.width < 19.5) res.icon.push(`${label(el)} icon ${Math.round(sr.width)}px`);
      }
    }
    const fs = parseFloat(getComputedStyle(el).fontSize);
    if (el.matches('input:not([type=checkbox]):not([type=radio]):not([type=range]):not([type=file]), select, textarea')) {
      if (fs < 15.9) res.field.push(`${label(el)} font ${fs}px`);
    } else if (el.matches('button, a[href], summary') && (el.textContent || '').trim() && fs < 13.9 && !el.closest('nav.rail, [data-touch-compact]')) res.text.push(`${label(el)} font ${fs}px`);
    // controls that stay in place (fixed/sticky) must not sit under the system bars
    let pinned = false;
    for (let a = el; a && a !== document.body; a = a.parentElement) { const p = getComputedStyle(a).position; if (p === 'fixed' || p === 'sticky') { pinned = true; break; } }
    // visible part = own box clipped by every scrolling/clipping ancestor
    let vt = r.top;
    let vb = r.bottom;
    for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
      const o = getComputedStyle(a).overflowY;
      if (o === 'auto' || o === 'scroll' || o === 'hidden') { const ar = a.getBoundingClientRect(); vt = Math.max(vt, ar.top); vb = Math.min(vb, ar.bottom); }
    }
    if (pinned && vb > vt && vt < top - 0.5 && vb > 0.5) res.bars.push(`${label(el)} top ${Math.round(vt)}`);
    if (pinned && vb > vt && vb > vh - bottom + 0.5 && vt < vh) res.bars.push(`${label(el)} bottom ${Math.round(vb)}/${vh - bottom}`);
  }
  for (const k of Object.keys(res)) res[k] = [...new Set(res[k])].slice(0, 30);
  return res;
}

(async () => {
  const browser = await chromium.launch(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {});
  let failures = 0;
  const summary = [];
  if (shots) fs.mkdirSync(out, { recursive: true });
  for (const theme of ['light', 'dark']) {
    for (const sz of SIZES) {
      for (const [name, hash, act] of SCREENS) {
        if (only.length && !only.includes(name)) continue;
        const ctx = await browser.newContext({ viewport: { width: sz.w, height: sz.h }, deviceScaleFactor: 2.625, isMobile: true, hasTouch: true, locale: 'de-DE', timezoneId: 'Europe/Berlin', colorScheme: theme });
        await ctx.addInitScript((th) => {
          try { localStorage.setItem('tuxmail.prefs.v2', JSON.stringify({ locale: 'de', theme: th })); } catch {}
        }, theme);
        const page = await ctx.newPage();
        const errors = [];
        page.on('pageerror', (e) => errors.push(String(e)));
        await page.goto(`${base}/?api=mock${hash}`);
        await page.addStyleTag({ content: `${nativeCss}\nhtml{--safe-area-inset-top:${INSET.top}px;--safe-area-inset-bottom:${INSET.bottom}px;--safe-area-inset-left:0px;--safe-area-inset-right:0px}` });
        await page.evaluate(() => document.documentElement.classList.add('tm-native'));
        await page.waitForTimeout(900);
        let actError = '';
        if (act) { try { await act(page); } catch (e) { actError = `screen "${name}" could not be opened: ${String(e).split('\n')[0]}`; } await page.waitForTimeout(500); }
        const res = await page.evaluate(measure, INSET);
        if (actError) res.overflow.push(actError);
        if (errors.length) res.overflow.push(...errors.map((e) => 'page error: ' + e));
        if (shots) await page.screenshot({ path: path.join(out, `${theme}-${sz.n}-${name}.png`) });
        const bad = Object.entries(res).filter(([, v]) => v.length);
        if (bad.length) {
          failures += bad.reduce((n, [, v]) => n + v.length, 0);
          console.log(`\n[${theme} ${sz.n} ${name}]`);
          for (const [k, v] of bad) for (const x of v) console.log(`  ${k}: ${x}`);
        }
        summary.push(`${theme}/${sz.n}/${name}: ${bad.length ? 'FAIL' : 'ok'}`);
        await ctx.close();
      }
    }
  }
  await browser.close();
  console.log(`\n${summary.filter((s) => s.endsWith('ok')).length}/${summary.length} screens clean, ${failures} problems`);
  process.exit(failures ? 1 : 0);
})();
