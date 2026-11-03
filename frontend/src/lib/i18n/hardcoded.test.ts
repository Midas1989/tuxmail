// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Guard against hard-coded user-visible strings in component markup: text
// nodes and the attributes placeholder/title/aria-label/alt must come from
// t(). Product names, key names and syntax examples are allow-listed.

import { describe, expect, it } from 'vitest';

const files = import.meta.glob('/src/**/*.svelte', { query: '?raw', import: 'default', eager: true }) as Record<string, string>;

const ALLOW = new Set(['TuxMail', 'SMTP', 'Cc', 'Bcc', 'Ctrl', 'K', 'J', 'Esc', 'AND', 'OR', 'NOT', 'from', 'anna', 'max', 'subject', 'angebot', 'date', 'now', '30d', 'https', 'mail', 'example', 'de']);

function stripExpressions(s: string): string {
  let prev = '';
  while (prev !== s) {
    prev = s;
    s = s.replace(/\{[^{}]*\}/g, ' ');
  }
  return s;
}

function visibleText(src: string): { text: string[]; attrs: string[] } {
  let s = src
    .replace(/<script[\s\S]*?<\/script>/g, ' ')
    .replace(/<style[\s\S]*?<\/style>/g, ' ')
    .replace(/<!--[\s\S]*?-->/g, ' ')
    .replace(/&[a-z]+;/g, ' ')
    .replace(/<(code|kbd)[^>]*>[\s\S]*?<\/\1>/g, ' ');
  const attrs = [...s.matchAll(/\s(?:placeholder|title|aria-label|alt)="([^"{][^"]*)"/g)].map((m) => m[1]);
  s = stripExpressions(s).replace(/<[^>]*>/g, ' ');
  const text = s
    .split(/\s+/)
    .map((w) => w.trim())
    .filter((w) => /\p{L}{2,}/u.test(w));
  return { text, attrs };
}

describe('no hard-coded UI strings in components', () => {
  for (const [path, src] of Object.entries(files)) {
    it(path, () => {
      const { text, attrs } = visibleText(src);
      const words = text.flatMap((w) => w.split(/[^\p{L}\p{N}]+/u)).filter((w) => /\p{L}{2,}/u.test(w) && !ALLOW.has(w));
      expect(words, `text in ${path}`).toEqual([]);
      const isShortcut = (a: string) => /^((Ctrl|Shift|Alt|Cmd|Esc|Enter|[A-Z])\+?)+$/.test(a);
      const badAttrs = attrs.filter((a) => /\p{L}{3,}/u.test(a) && !/^https?:\/\//.test(a) && !ALLOW.has(a) && !isShortcut(a));
      expect(badAttrs, `attributes in ${path}`).toEqual([]);
    });
  }
});
