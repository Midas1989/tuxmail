// SPDX-License-Identifier: AGPL-3.0-or-later
//
// End-to-end test of the core mail journey against real protocol servers
// (docs/ENTWICKLUNG.md). Normally started by scripts/e2e.sh, which runs
// `tuxmail devserver` (IMAP/SMTP/autoconfig) and `tuxmail serve`.
//
//   TUXMAIL_URL       TuxMail web UI            (default http://127.0.0.1:18080)
//   TUXMAIL_USER      TuxMail login             (default anna)
//   TUXMAIL_PASSWORD  TuxMail password          (default "correct horse battery staple")
//   DEVSERVER_URL     devserver control API     (default http://127.0.0.1:1080)
//   MAIL_ADDRESS      mail account (wizard)     (default anna@tuxmail.test)
//   MAIL_PASSWORD     mail password             (default tuxmail-dev)
//   PEER_ADDRESS      second mailbox            (default ben@tuxmail.test)
//   SCREENSHOTS       screenshot directory      (default docs/screenshots; files e2e-*.jpg)
//
// Every step is reported as PASS/FAIL; the exit code is the number of
// failed steps (0 = all passed).
const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');

const base = process.env.TUXMAIL_URL ?? 'http://127.0.0.1:18080';
const loginUser = process.env.TUXMAIL_USER ?? 'anna';
const loginPassword = process.env.TUXMAIL_PASSWORD ?? 'correct horse battery staple';
const dev = process.env.DEVSERVER_URL ?? 'http://127.0.0.1:1080';
const mailAddr = process.env.MAIL_ADDRESS ?? 'anna@tuxmail.test';
const mailPassword = process.env.MAIL_PASSWORD ?? 'tuxmail-dev';
const peerAddr = process.env.PEER_ADDRESS ?? 'ben@tuxmail.test';
const out = process.env.SCREENSHOTS ?? 'docs/screenshots';
const deadline = (ms) => Date.now() + ms;

const results = [];
const apiErrors = [];
let currentPage = null; // screenshot target on failure

async function step(name, fn) {
  const t0 = Date.now();
  try {
    await fn();
    results.push({ name, ok: true, ms: Date.now() - t0 });
    console.log(`PASS  ${name} (${Date.now() - t0} ms)`);
  } catch (e) {
    results.push({ name, ok: false, ms: Date.now() - t0, error: String(e?.message ?? e).split('\n')[0] });
    console.log(`FAIL  ${name}: ${String(e?.message ?? e).split('\n').slice(0, 3).join(' | ')}`);
    if (currentPage) await currentPage.screenshot({ path: path.join(out, `fail-${name.split(' ')[0]}.jpg`), type: 'jpeg' }).catch(() => {});
  }
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

async function shot(page, name) {
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(out, `e2e-${name}.jpg`), quality: 82, type: 'jpeg' });
}

// --- devserver control API (real IMAP/SMTP behind it) ---
async function devGet(p) {
  const r = await fetch(dev + p);
  if (!r.ok) throw new Error(`devserver ${p}: ${r.status} ${await r.text()}`);
  return (r.headers.get('content-type') ?? '').includes('json') ? r.json() : r.text();
}
async function devPost(p, body) {
  const r = await fetch(dev + p, { method: 'POST', body: typeof body === 'string' ? body : JSON.stringify(body) });
  if (!r.ok) throw new Error(`devserver ${p}: ${r.status} ${await r.text()}`);
  return r.json();
}
const serverMessages = (user, folder) => devGet(`/api/messages?user=${encodeURIComponent(user)}&folder=${encodeURIComponent(folder)}`);
const rawMessage = (user, folder, uid) => devGet(`/api/raw?user=${encodeURIComponent(user)}&folder=${encodeURIComponent(folder)}&uid=${uid}`);
async function findOnServer(user, folder, pred, ms = 15000) {
  const end = deadline(ms);
  for (;;) {
    const list = await serverMessages(user, folder);
    const m = list.find(pred);
    if (m) return m;
    if (Date.now() > end) throw new Error(`no matching message in ${user}/${folder} (${list.length} messages)`);
    await new Promise((r) => setTimeout(r, 500));
  }
}
async function waitFor(fn, ms, what) {
  const end = deadline(ms);
  let last;
  for (;;) {
    try {
      const v = await fn();
      if (v) return v;
    } catch (e) {
      last = e;
    }
    if (Date.now() > end) throw new Error(`timeout waiting for ${what}${last ? ': ' + last.message : ''}`);
    await new Promise((r) => setTimeout(r, 400));
  }
}

async function newPage(browser, viewMode) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1, locale: 'de-DE', acceptDownloads: true });
  await ctx.addInitScript((mode) => {
    try {
      const k = 'tuxmail.prefs.v2';
      const cur = JSON.parse(localStorage.getItem(k) ?? '{}');
      if (!sessionStorage.getItem('e2e.mode')) {
        localStorage.setItem(k, JSON.stringify({ ...cur, viewMode: mode, locale: 'de' }));
        sessionStorage.setItem('e2e.mode', '1');
      }
    } catch {}
  }, viewMode);
  const page = await ctx.newPage();
  page.on('pageerror', (e) => apiErrors.push(`pageerror: ${e}`));
  page.on('response', (r) => {
    if (r.url().includes('/api/') && r.status() >= 400) apiErrors.push(`${r.status()} ${r.request().method()} ${r.url()}`);
  });
  return { ctx, page };
}

async function login(page) {
  await page.goto(base + '/');
  await page.waitForURL(/\/login/);
  await page.fill('#u', loginUser);
  await page.fill('#p', loginPassword);
  await Promise.all([page.waitForURL((u) => !u.pathname.startsWith('/login')), page.click('#submit')]);
}

/** Selects a folder of the (only) account in the sidebar by its label. */
async function openFolder(page, label) {
  await page.getByRole('treeitem', { name: new RegExp('^' + label) }).last().click();
  await page.waitForTimeout(600);
}

/** Switches the mail view (the messenger layout has no segmented toggle). */
async function setView(page, mode) {
  const names = { classic: 'Klassisch', hybrid: 'Hybrid', chat: 'Chat' };
  const radio = page.getByRole('radio', { name: names[mode], exact: true });
  if (await radio.count()) return radio.click();
  await page.getByRole('button', { name: 'Klassisch', exact: true }).click();
  if (mode !== 'classic') await page.getByRole('radio', { name: names[mode], exact: true }).click();
}

async function openMessage(page, subject) {
  await page.locator('[role=option]', { hasText: subject }).first().click();
  await page.waitForSelector(`h2.subject:has-text("${subject}")`, { timeout: 10000 });
}

(async () => {
  fs.mkdirSync(out, { recursive: true });
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser, 'classic');
  currentPage = page;

  await step('1 login via /login', async () => {
    await login(page);
    await page.waitForSelector('text=Neue E-Mail', { timeout: 10000 });
  });

  await step('2 account wizard: discovery (autoconfig/ISPDB) + create account', async () => {
    await page.goto(base + '/#/settings');
    await page.getByRole('button', { name: 'Konto hinzufügen' }).first().click();
    const dlg = page.locator('[role=dialog]');
    await dlg.locator('input[autocomplete=name]').fill('Anna Berger');
    await dlg.locator('input[type=email]').fill(mailAddr);
    await dlg.locator('input[type=password]').fill(mailPassword);
    await shot(page, 'wizard-credentials');
    await dlg.locator('button[type=submit]').click();
    await dlg.locator('text=Einstellungen über Autoconfig gefunden').waitFor({ timeout: 40000 });
    const insecure = dlg.locator('.warn input[type=checkbox]');
    if (await insecure.count()) await insecure.check();
    await shot(page, 'wizard-discovered');
    await dlg.getByRole('button', { name: 'Konto hinzufügen' }).click();
    await dlg.locator('text=wurde hinzugefügt').waitFor({ timeout: 15000 });
    await dlg.locator('.done .btn-primary').click();
    await dlg.waitFor({ state: 'detached', timeout: 5000 });
  });

  await step('2b manual server form validates and is reachable', async () => {
    await page.locator('.card-head').getByRole('button', { name: 'Konto hinzufügen' }).click();
    const dlg = page.locator('[role=dialog]');
    await dlg.locator('input[type=email]').fill(mailAddr);
    await dlg.getByRole('button', { name: 'Manuell' }).click();
    await dlg.locator('legend', { hasText: 'Posteingangsserver' }).waitFor();
    const host = await dlg.locator('fieldset').first().locator('input').first().inputValue();
    assert(host === 'imap.tuxmail.test', `manual form prefilled host ${host}`);
    await shot(page, 'wizard-manual');
    await page.keyboard.press('Escape');
    await dlg.waitFor({ state: 'detached', timeout: 5000 });
  });

  await step('3 sync: folder tree with real folders and counts', async () => {
    await page.goto(base + '/#/mail');
    await page.waitForSelector('[role=option]:has-text("Mittagessen morgen?")', { timeout: 30000 });
    for (const f of ['Posteingang', 'Gesendet', 'Archiv', 'Entwürfe', 'Papierkorb', 'Spam']) {
      await page.getByRole('treeitem', { name: new RegExp('^' + f) }).last().waitFor({ timeout: 5000 });
    }
    const inbox = await serverMessages(mailAddr, 'INBOX');
    const unseen = inbox.filter((m) => !m.flags.includes('\\Seen')).length;
    const badge = await page.getByRole('treeitem', { name: /^Posteingang/ }).last().innerText();
    assert(badge.includes(String(unseen)), `inbox badge "${badge}" != server unseen ${unseen}`);
    await shot(page, 'mailbox');
  });

  await step('4 reading pane: sanitized HTML, remote images blocked banner', async () => {
    await openMessage(page, 'Herbst-Angebote');
    await page.waitForSelector('text=Externe Inhalte wurden', { timeout: 10000 });
    const frame = page.frameLocator('.body iframe');
    await frame.locator('text=unsere neue Herbstkollektion').waitFor({ timeout: 10000 });
    const html = await frame.locator('body').innerHTML();
    assert(!/<script/i.test(html), 'script tag not removed');
    assert(!/track\.shop\.example\.com/.test(html) || !/src="https:\/\/track/.test(html), 'tracking pixel loaded directly');
    await shot(page, 'reading-html');
  });

  await step('5 attachment download', async () => {
    await openMessage(page, 'Fwd: Rechnung Oktober 2026');
    const dl = page.locator('a.dl', { has: page.locator('svg') }).first();
    await dl.waitFor({ timeout: 10000 });
    const [download] = await Promise.all([page.waitForEvent('download', { timeout: 10000 }), dl.click()]);
    assert(download.suggestedFilename() === 'Rechnung-RE-2026-1042.pdf', `filename ${download.suggestedFilename()}`);
    const p = await download.path();
    const head = fs.readFileSync(p).subarray(0, 8).toString();
    assert(head.startsWith('%PDF-1.4'), `downloaded content starts with ${head}`);
    await shot(page, 'attachment');
  });


  await step('6 conversation grouping across Inbox/Sent (hybrid, highlighted bubbles)', async () => {
    await setView(page, 'hybrid');
    await page.locator('.hl [role=option]', { hasText: 'Angebot Website-Relaunch' }).first().click();
    const bubbles = page.locator('.bubble');
    await waitFor(async () => (await bubbles.count()) >= 3, 15000, '3 bubbles of the Angebot thread');
    // Two mails from Maria (INBOX) and Anna's reply (Sent) form one conversation.
    const own = await page.locator('.row.own .bubble').count();
    assert(own >= 1, 'own reply from Sent missing in the conversation');
    const inScope = await page.locator('.row.scope-in').count();
    const outScope = await page.locator('.row.scope-out').count();
    assert(inScope >= 2 && outScope >= 1, `hybrid highlight in=${inScope} out=${outScope}`);
    await shot(page, 'hybrid');
  });

  await step('7 chat view: bubbles with quotes and signatures collapsed', async () => {
    await setView(page, 'chat');
    await page.locator('[role=option]', { hasText: 'Maria Sommer' }).first().click();
    const last = page.locator('.bubble', { hasText: 'das Logo habe ich noch etwas angepasst' }).last();
    await last.waitFor({ timeout: 15000 });
    const text = await last.locator('.text').innerText();
    assert(!text.includes('schrieb Anna Berger'), 'quoted history visible in bubble');
    assert(!text.includes('Tel. +49 89'), 'signature visible in bubble');
    assert(await last.locator('.sig-toggle').count() === 1, 'signature toggle missing');
    await shot(page, 'chat');
  });


  await step('8 settings: create a signature for the identity', async () => {
    await page.goto(base + '/#/settings');
    await page.getByRole('button', { name: 'Neue Signatur' }).click();
    const ta = page.getByRole('textbox', { name: 'Signaturtext' }).first();
    await ta.waitFor({ timeout: 5000 });
    await ta.fill('Anna Berger\nTuxMail GmbH · Projektleitung');
    await ta.blur();
    await page.waitForTimeout(500);
    const sel = page.getByRole('combobox', { name: `Signatur für neue Mails von ${mailAddr}` });
    await waitFor(async () => (await sel.inputValue()) !== '', 5000, 'signature assigned to identity');
    await page.locator('#set-signatures').scrollIntoViewIfNeeded();
    await shot(page, 'signature');
  });

  await step('9 search: simple and expert (from:… AND has:attachment) with facets', async () => {
    await page.goto(base + '/#/search');
    const input = page.locator('input[data-search-input]');
    await input.fill('Relaunch');
    await input.press('Enter');
    await page.locator('.hit', { hasText: 'Angebot Website-Relaunch' }).first().waitFor({ timeout: 10000 });
    await page.getByRole('radio', { name: 'Experte' }).click();
    const q = page.locator('.q-input');
    // The simple-mode text stays as a chip; the expert terms are added to it.
    await q.click();
    await q.pressSequentially('from:maria.sommer@example.org AND has:attachment');
    await page.keyboard.press('Escape');
    await page.locator('button.go').click();
    await page.locator('.hit', { hasText: 'Angebot Website-Relaunch' }).first().waitFor({ timeout: 10000 });
    const titles = await page.locator('.hit .hit-title').allInnerTexts();
    assert(titles.every((t) => !/Mittagessen|Herbst|Projektplan/.test(t)), `unexpected hits: ${titles.join(' | ')}`);
    assert(titles.length >= 2, `expected both Maria mails with attachment, got ${titles.length}`);
    await page.locator('aside.facets').waitFor({ timeout: 5000 });
    const facets = await page.locator('aside.facets').innerText();
    assert(/Maria|example\.org/.test(facets), `facets without sender/domain: ${facets.slice(0, 200)}`);
    await shot(page, 'search');
  });

  await step('10 tag a mail -> IMAP keyword on the server', async () => {
    await page.goto(base + '/#/mail');
    await setView(page, 'classic');
    await openFolder(page, 'Posteingang');
    await openMessage(page, 'Grüße aus München');
    await page.locator('section.pane').getByRole('button', { name: 'Tags …' }).click();
    const dlg = page.locator('[role=dialog]');
    await dlg.locator('input[role=combobox]').fill('Privat');
    await dlg.getByRole('button', { name: /Privat/ }).first().click();
    await dlg.getByRole('button', { name: 'Übernehmen' }).click();
    await dlg.waitFor({ state: 'detached', timeout: 5000 });
    const m = await findOnServer(mailAddr, 'INBOX', (x) => x.subject === 'Grüße aus München' && x.flags.some((f) => /privat/i.test(f)));
    console.log('      keyword on server:', m.flags.join(' '));
    await shot(page, 'tag');
  });

  await step('11 move a mail to Archive -> moved on the server', async () => {
    await openMessage(page, 'Grüße aus München');
    await page.locator('section.pane').getByRole('button', { name: 'Verschieben' }).click();
    await page.getByRole('menuitem', { name: 'Archiv' }).click();
    await findOnServer(mailAddr, 'Archive', (x) => x.subject === 'Grüße aus München');
    const inbox = await serverMessages(mailAddr, 'INBOX');
    assert(!inbox.some((x) => x.subject === 'Grüße aus München'), 'still in INBOX on the server');
  });

  await step('12 read/unread both directions (UI -> IMAP, IMAP -> UI live)', async () => {
    await openFolder(page, 'Posteingang');
    await openMessage(page, 'Mittagessen morgen?');
    await findOnServer(mailAddr, 'INBOX', (x) => x.subject === 'Mittagessen morgen?' && x.flags.includes('\\Seen'));
    await page.locator('section.pane').getByRole('button', { name: 'Als ungelesen markieren' }).click();
    const m = await findOnServer(mailAddr, 'INBOX', (x) => x.subject === 'Mittagessen morgen?' && !x.flags.includes('\\Seen'));
    // Another client marks the newsletter as read: the UI updates without reload.
    const news = (await serverMessages(mailAddr, 'INBOX')).find((x) => x.subject.startsWith('Herbst-Angebote'));
    assert(news && !news.flags.includes('\\Seen'), 'newsletter should be unread');
    const row = page.locator('[role=option]', { hasText: 'Herbst-Angebote' }).first();
    await devPost('/api/flags', { user: mailAddr, folder: 'INBOX', uid: news.uid, add: ['\\Seen'] });
    await waitFor(async () => !(await row.evaluate((el) => el.classList.contains('unread'))), 20000, 'newsletter row marked read live');
    await devPost('/api/flags', { user: mailAddr, folder: 'INBOX', uid: m.uid, add: ['\\Flagged'] });
    await page.locator('[role=option]', { hasText: 'Mittagessen morgen?' }).first().locator('svg.star').waitFor({ timeout: 20000 });
    await shot(page, 'flags-live');
  });

  let replyMsgId = '';
  await step('13 reply from the full composer -> arrives at the peer with In-Reply-To/References + signature, in Sent', async () => {
    await openMessage(page, 'Mittagessen morgen?');
    await page.locator('section.pane').getByRole('button', { name: 'Antworten', exact: true }).click();
    const dlg = page.locator('[role=dialog]');
    const ed = dlg.locator('[contenteditable=true]');
    await ed.waitFor({ timeout: 5000 });
    await ed.click();
    await page.keyboard.press('Control+Home');
    await page.keyboard.type('Gerne, 12:30 passt mir gut!');
    await shot(page, 'composer');
    await dlg.getByRole('button', { name: 'Senden' }).last().click();
    await dlg.waitFor({ state: 'detached', timeout: 15000 });
    const got = await findOnServer(peerAddr, 'INBOX', (x) => x.subject === 'Re: Mittagessen morgen?');
    assert(got.inReplyTo === '<ben-mittagessen@tuxmail.test>', `In-Reply-To ${got.inReplyTo}`);
    assert(got.references.includes('<ben-mittagessen@tuxmail.test>'), `References ${got.references}`);
    const raw = await rawMessage(peerAddr, 'INBOX', got.uid);
    assert(raw.includes('Gerne, 12:30 passt mir gut!'), 'text missing');
    assert(raw.includes('TuxMail GmbH'), 'signature missing');
    await findOnServer(mailAddr, 'Sent', (x) => x.messageId === got.messageId);
    replyMsgId = got.messageId;
  });

  await step('14 send check: forgotten attachment warning before sending, then "send anyway"', async () => {
    await openMessage(page, 'Fwd: Rechnung Oktober 2026');
    await page.locator('section.pane').getByRole('button', { name: 'Antworten', exact: true }).click();
    const dlg = page.locator('[role=dialog]');
    const ed = dlg.locator('[contenteditable=true]');
    await ed.waitFor({ timeout: 5000 });
    await ed.click();
    await page.keyboard.press('Control+Home');
    await page.keyboard.type('Freigegeben, anbei die unterschriebene Rechnung.');
    await dlg.getByRole('button', { name: 'Senden' }).last().click();
    const checks = dlg.locator('.checks');
    await checks.waitFor({ timeout: 10000 });
    assert(await checks.locator('[data-check=forgotten_attachment]').count() === 1, 'forgotten_attachment finding missing');
    const before = (await serverMessages(peerAddr, 'INBOX')).length;
    assert(!(await serverMessages(peerAddr, 'INBOX')).some((x) => /^Re: (Fwd: )?Rechnung Oktober 2026$/.test(x.subject)), 'sent before confirmation');
    await shot(page, 'sendcheck');
    await checks.getByRole('button', { name: 'Trotzdem senden' }).click();
    await dlg.waitFor({ state: 'detached', timeout: 15000 });
    await findOnServer(peerAddr, 'INBOX', (x) => /^Re: (Fwd: )?Rechnung Oktober 2026$/.test(x.subject));
    assert((await serverMessages(peerAddr, 'INBOX')).length === before + 1, 'not exactly one mail sent');
  });

  await step('15 reply from the chat composer -> threaded mail with signature at the peer', async () => {
    await setView(page, 'chat');
    await page.locator('[role=option]', { hasText: 'Ben Krüger' }).first().click();
    const ta = page.locator('.composer textarea');
    await ta.waitFor({ timeout: 10000 });
    await ta.fill('Bis morgen beim Italiener 🍝');
    await ta.press('Enter');
    await page.locator('.row.own .bubble', { hasText: 'Bis morgen beim Italiener' }).waitFor({ timeout: 15000 });
    const got = await findOnServer(peerAddr, 'INBOX', (x) => (x.subject ?? '').startsWith('Re:') && x.messageId !== replyMsgId && /Mittagessen|Rechnung/.test(x.subject) && x.inReplyTo !== '' && x.date, 15000);
    const all = await serverMessages(peerAddr, 'INBOX');
    const chat = all[all.length - 1];
    const raw = await rawMessage(peerAddr, 'INBOX', chat.uid);
    assert(raw.includes('Bis morgen beim Italiener'), 'chat text not at the peer');
    assert(/^In-Reply-To: <.+>/m.test(raw), 'chat reply without In-Reply-To');
    assert(raw.includes('TuxMail GmbH'), 'chat reply without signature');
    void got;
    await page.waitForTimeout(1500);
    const row = await page.locator('[role=option]', { hasText: 'Ben Krüger' }).first().innerText();
    assert(!row.includes('Entwurf'), `sent text came back as draft: ${row}`);
    await shot(page, 'chat-reply');
  });

  await step('16 workflow (rule) created in the UI', async () => {
    await page.goto(base + '/#/settings');
    await page.getByRole('button', { name: 'Neuer Workflow' }).click();
    const dlg = page.locator('[role=dialog]');
    await dlg.locator('.meta input').first().fill('Kunden markieren');
    await dlg.getByRole('radio', { name: /Regel/ }).click();
    const code = dlg.locator('textarea');
    await code.fill('WHEN mail.received IF from:*@example.org THEN tag "Kunde"');
    await page.waitForTimeout(800);
    // New workflows start disabled (dry run first); enable it.
    await dlg.locator('label.switch').click();
    await dlg.locator('text=Aktiv').first().waitFor({ timeout: 3000 }).catch(() => {});
    await shot(page, 'workflow');
    await dlg.getByRole('button', { name: 'Speichern' }).click();
    await dlg.waitFor({ state: 'detached', timeout: 10000 });
    await page.locator('text=Kunden markieren').first().waitFor({ timeout: 5000 });
  });

  await step('17 new incoming mail appears live (IDLE + WebSocket) in the right conversation; workflow tags it', async () => {
    await page.goto(base + '/#/mail');
    await setView(page, 'chat');
    await page.locator('[role=option]', { hasText: 'Maria Sommer' }).first().click();
    await page.locator('.bubble', { hasText: 'das Logo habe ich noch etwas angepasst' }).last().waitFor({ timeout: 10000 });
    await devPost('/api/send', {
      from: 'Maria Sommer <maria.sommer@example.org>', to: [mailAddr], subject: 'Re: Angebot Website-Relaunch',
      text: 'Hallo Anna,\n\ndie Auftragsbestätigung kommt morgen.\n\nViele Grüße\nMaria',
      inReplyTo: '<maria-re-re-angebot@example.org>', references: '<angebot-117@example.org> <anna-re-angebot@tuxmail.test> <maria-re-re-angebot@example.org>',
    });
    // No reload: the bubble must appear through IDLE -> sync -> WebSocket.
    await page.locator('.bubble', { hasText: 'die Auftragsbestätigung kommt morgen' }).waitFor({ timeout: 30000 });
    const m = await findOnServer(mailAddr, 'INBOX', (x) => x.subject === 'Re: Angebot Website-Relaunch' && x.flags.some((f) => /kunde/i.test(f)), 30000);
    console.log('      workflow keyword on server:', m.flags.join(' '));
    await shot(page, 'live-incoming');
  });

  console.log('\nAPI/page errors:', JSON.stringify(apiErrors, null, 1));
  await ctx.close();
  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} steps passed`);
  for (const f of failed) console.log(`  FAILED: ${f.name}: ${f.error}`);
  process.exit(failed.length);
})().catch((e) => {
  console.error(e);
  process.exit(100);
});
