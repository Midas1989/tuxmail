// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render } from './security/mountUtil';
import { flushSync } from 'svelte';

// Phone viewport for the shared MediaQuery (max-width: 639px).
vi.hoisted(() => {
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as never;
});

const nav = vi.hoisted(() => ({ calls: [] as string[] }));
vi.mock('../state', () => ({
  app: {
    route: 'mail',
    unifiedUnread: 0,
    navigate: (r: string) => nav.calls.push(r),
    setViewMode: async () => {},
  },
  prefs: { viewMode: 'classic' },
}));

import ActivityBar from './ActivityBar.svelte';

afterEach(() => {
  cleanup();
  nav.calls.length = 0;
});

const labels = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('nav.rail button')].map((b) => (b.getAttribute('aria-label') || b.textContent || '').trim());

describe('ActivityBar on phones', () => {
  it('shows four destinations plus "More" instead of eight entries', () => {
    const root = render(ActivityBar, {});
    expect(labels(root)).toEqual(['Mail', 'Chats', 'Calendar', 'Contacts', 'More']);
    expect(root.querySelector('[role="dialog"]')).toBeNull();
  });

  it('opens the sheet with search, projects, files and settings', () => {
    const root = render(ActivityBar, {});
    root.querySelector<HTMLButtonElement>('button.more')!.click();
    flushSync();
    const sheet = root.querySelector('[role="dialog"]')!;
    expect(sheet.getAttribute('aria-modal')).toBe('true');
    expect([...sheet.querySelectorAll('button')].map((b) => b.textContent!.trim())).toEqual(['Search', 'Projects', 'Files', 'Settings']);
  });

  it('the Android back button (Escape on the dialog) closes the sheet', () => {
    const root = render(ActivityBar, {});
    root.querySelector<HTMLButtonElement>('button.more')!.click();
    flushSync();
    root.querySelector('[role="dialog"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    flushSync();
    expect(root.querySelector('[role="dialog"]')).toBeNull();
  });

  it('choosing an entry navigates and closes the sheet', () => {
    const root = render(ActivityBar, {});
    root.querySelector<HTMLButtonElement>('button.more')!.click();
    flushSync();
    [...root.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) => b.textContent!.trim() === 'Settings')!.click();
    flushSync();
    expect(nav.calls).toEqual(['settings']);
    expect(root.querySelector('[role="dialog"]')).toBeNull();
  });

  it('moves focus into the sheet, keeps Tab inside and returns focus to "More" on close', () => {
    const root = render(ActivityBar, {});
    const more = root.querySelector<HTMLButtonElement>('button.more')!;
    more.focus();
    more.click();
    flushSync();
    const buttons = [...root.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')];
    expect(document.activeElement).toBe(buttons[0]);
    // Tab on the last entry wraps to the first, Shift+Tab on the first wraps to the last.
    buttons[buttons.length - 1].focus();
    buttons[buttons.length - 1].dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(buttons[0]);
    buttons[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(buttons[buttons.length - 1]);
    root.querySelector('[role="dialog"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    flushSync();
    expect(document.activeElement).toBe(more);
  });
});
