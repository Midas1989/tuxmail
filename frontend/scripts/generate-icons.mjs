// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Generates the fallback PWA icons in public/icons/ from the brand mascot
// (assets/branding/tuxmail-penguin.png). Official icons placed in
// assets/branding/icons/ take precedence at build time (vite.config.ts).
// Maintainer tool - the generated files are committed.
// Requires Playwright (not a project dependency):  node scripts/generate-icons.mjs

import { createRequire } from 'node:module';
import { mkdirSync, readFileSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// createRequire so a globally installed Playwright (NODE_PATH) works too.
const { chromium } = createRequire(import.meta.url)('playwright');

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, '..', 'public', 'icons');
mkdirSync(out, { recursive: true });
for (const old of ['icon.svg', 'icon-maskable.svg']) rmSync(join(out, old), { force: true });

const penguin = 'data:image/png;base64,' + readFileSync(join(here, '..', '..', 'assets', 'branding', 'tuxmail-penguin.png')).toString('base64');
const BG = '#f3f6fa';

// [file, size, background, scale of the mascot relative to the canvas, rounded]
const jobs = [
  ['icon-32.png', 32, 'transparent', 0.96, false],
  ['icon-64.png', 64, 'transparent', 0.96, false],
  ['icon-192.png', 192, BG, 0.84, true],
  ['icon-512.png', 512, BG, 0.84, true],
  ['icon-maskable-512.png', 512, BG, 0.66, false], // safe zone = inner 80 %
  ['apple-touch-icon.png', 180, BG, 0.8, false], // iOS rounds itself, no alpha
];

const browser = await chromium.launch();
const page = await browser.newPage({ deviceScaleFactor: 1 });
for (const [name, size, bg, scale, rounded] of jobs) {
  await page.setViewportSize({ width: size, height: size });
  const radius = rounded ? Math.round(size * 0.22) : 0;
  await page.setContent(`<html><body style="margin:0;background:transparent">
    <div style="width:${size}px;height:${size}px;background:${bg};border-radius:${radius}px;display:flex;align-items:center;justify-content:center">
      <img src="${penguin}" style="max-width:${Math.round(size * scale)}px;max-height:${Math.round(size * scale)}px">
    </div></body></html>`);
  await page.waitForFunction(() => document.images[0]?.complete);
  await page.screenshot({ path: join(out, name), omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } });
  console.log('wrote', name);
}

// Android status-bar badge: white envelope on transparent (must be monochrome).
await page.setViewportSize({ width: 72, height: 72 });
await page.setContent(
  `<html><body style="margin:0;background:transparent"><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="72" height="72"><path d="M15 22h34v21H15z M15 22l17 12 17-12" fill="none" stroke="#fff" stroke-width="4" stroke-linejoin="round"/></svg></body></html>`,
);
await page.screenshot({ path: join(out, 'badge-72.png'), omitBackground: true, clip: { x: 0, y: 0, width: 72, height: 72 } });
console.log('wrote badge-72.png');
await browser.close();
