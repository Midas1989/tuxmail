// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render } from './security/mountUtil';

// A phone held sideways: every query matches.
vi.hoisted(() => {
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as never;
});

const h = vi.hoisted(() => ({ compose: vi.fn() }));
vi.mock('../state', () => ({
  app: { route: 'mail', unifiedUnread: 0, messageListView: true, selectedConversationId: null, navigate: () => {}, setViewMode: async () => {}, openCompose: h.compose },
  prefs: { viewMode: 'classic' },
}));

import ActivityBar from './ActivityBar.svelte';

afterEach(cleanup);

describe('ActivityBar on a phone held sideways', () => {
  it('holds the compose button between the destinations and "More"', () => {
    const root = render(ActivityBar, {});
    const names = [...root.querySelectorAll<HTMLElement>('nav.rail button')].map((b) => (b.getAttribute('aria-label') || b.textContent || '').trim());
    expect(names).toEqual(['Mail', 'Chats', 'Calendar', 'Contacts', 'New email', 'More']);
    root.querySelector<HTMLButtonElement>('button.compose')!.click();
    expect(h.compose).toHaveBeenCalled();
  });
});
