// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Renders the PWA icons (PNG) from inline SVG with a headless Chromium.
// Maintainer tool - the generated files are committed to public/icons/.
// Requires Playwright (not a project dependency):  node scripts/generate-icons.mjs

import { createRequire } from 'node:module';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// createRequire so a globally installed Playwright (NODE_PATH) works too.
const { chromium } = createRequire(import.meta.url)('playwright');

const out = join(dirname(fileURLToPath(import.meta.url)), '..', 'public', 'icons');
mkdirSync(out, { recursive: true });

const ENVELOPE = (stroke) =>
  `<path d="M15 22h34v21H15z M15 22l17 12 17-12" fill="none" stroke="${stroke}" stroke-width="4" stroke-linejoin="round" stroke-linecap="round"/>`;

// Standard icon: rounded square.
const icon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#2563eb"/>${ENVELOPE('#fff')}</svg>`;
// Maskable: full-bleed background, glyph inside the 80% safe zone.
const maskable = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#2563eb"/><g transform="translate(9.6 9.6) scale(0.7)">${ENVELOPE('#fff')}</g></svg>`;
// iOS: no transparency, iOS rounds corners itself.
const apple = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#2563eb"/><g transform="translate(4.8 4.8) scale(0.85)">${ENVELOPE('#fff')}</g></svg>`;
// Android status-bar badge: white glyph on transparent.
const badge = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">${ENVELOPE('#fff')}</svg>`;

writeFileSync(join(out, 'icon.svg'), icon + '\n');
writeFileSync(join(out, 'icon-maskable.svg'), maskable + '\n');

const jobs = [
  ['icon-192.png', icon, 192],
  ['icon-512.png', icon, 512],
  ['icon-maskable-512.png', maskable, 512],
  ['apple-touch-icon.png', apple, 180],
  ['badge-72.png', badge, 72],
];

const browser = await chromium.launch();
const page = await browser.newPage({ deviceScaleFactor: 1 });
for (const [name, svg, size] of jobs) {
  await page.setViewportSize({ width: size, height: size });
  await page.setContent(
    `<html><body style="margin:0;background:transparent">${svg.replace('<svg ', `<svg width="${size}" height="${size}" `)}</body></html>`,
  );
  await page.screenshot({ path: join(out, name), omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } });
  console.log('wrote', name);
}
await browser.close();
