// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest';

const queries = vi.hoisted(() => [] as string[]);
vi.hoisted(() => {
  window.matchMedia = ((q: string) => {
    queries.push(q);
    return { matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false };
  }) as never;
});

import './phone.svelte';

// All sources (components, CSS, modules) as raw text.
const sources = import.meta.glob(['../**/*.svelte', '../**/*.css', '../**/*.ts', '!../**/*.test.ts'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>;

describe('phone breakpoint', () => {
  it('covers a phone held sideways (touch, not tall), not only narrow screens', () => {
    expect(queries.some((q) => q.includes('max-width: 639px') && q.includes('(pointer: coarse) and (max-height: 500px)'))).toBe(true);
  });

  it('every phone media query in the sources has the landscape clause', () => {
    const bad: string[] = [];
    expect(Object.keys(sources).length).toBeGreaterThan(100);
    for (const [f, text] of Object.entries(sources)) {
      for (const line of text.split('\n')) {
        if (/@media[^{]*max-width: 639px/.test(line) && !line.includes('(pointer: coarse) and (max-height: 500px)')) bad.push(`${f}: ${line.trim()}`);
        if (/matchMedia\('\(max-width: 639px\)'\)/.test(line)) bad.push(`${f}: ${line.trim()}`);
      }
    }
    expect(bad).toEqual([]);
  });
});
