// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render } from '../security/mountUtil';

vi.hoisted(() => {
  (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as never;
});

const h = vi.hoisted(() => ({
  checked: [] as number[],
  toggleChecked: vi.fn(),
  moveToRole: vi.fn(),
  toggleRead: vi.fn(),
  openCompose: vi.fn(),
  selectMessage: vi.fn(),
}));

vi.mock('../../state', () => {
  const msg = (id: number) => ({ id, accountId: 1, subject: `Betreff ${id}`, preview: 'Vorschau', date: '2026-11-06T10:00:00Z', flags: [], from: [{ name: `Absender ${id}`, email: `a${id}@example.org` }], to: [], cc: [], hasAttachments: false, tagIds: [] });
  const app = {
    messages: [msg(1), msg(2), msg(3)],
    messagesTotal: 3,
    messagesLoading: false,
    listSync: null,
    search: '',
    sel: { kind: 'unified', role: 'inbox' },
    selectedMessageId: null as number | null,
    listScrollTick: 0,
    get checked() {
      return h.checked;
    },
    set checked(v: number[]) {
      h.checked = v;
    },
    account: () => ({ id: 1, displayName: 'Privat', color: '#2563eb' }),
    folder: () => undefined,
    loadMoreMessages: async () => {},
    toggleChecked: h.toggleChecked,
    moveToRole: h.moveToRole,
    toggleRead: h.toggleRead,
    toggleFlag: () => {},
    openCompose: h.openCompose,
    selectMessage: h.selectMessage,
    openMailWindow: () => {},
    bulk: () => {},
  };
  const comfort = { filterMessages: (m: unknown[]) => m, category: 'all', settings: { categories: { enabled: false } }, counts: [], loadSettings: async () => {}, loadCounts: async () => {}, loadCategories: async () => {} };
  return { app, prefs: { density: 'normal' }, comfort };
});

import MessageList from './MessageList.svelte';

function pointer(el: Element, type: string, x: number, y: number) {
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 0 });
  Object.defineProperty(e, 'pointerType', { value: 'touch' });
  Object.defineProperty(e, 'pointerId', { value: 3 });
  el.dispatchEvent(e);
}

describe('MessageList on phones', () => {
  beforeEach(() => {
    h.checked = [];
    Object.values(h).forEach((f) => typeof f === 'function' && (f as ReturnType<typeof vi.fn>).mockClear());
    Element.prototype.setPointerCapture = () => {};
  });
  afterEach(cleanup);

  it('long press starts a selection', () => {
    const root = render(MessageList, {});
    const row = root.querySelector('#msg-2')!;
    row.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }));
    expect(h.toggleChecked).toHaveBeenCalledWith(2, true);
    expect(root.querySelector('[role="menu"]')).toBeNull();
  });

  it('while rows are selected a tap adds or removes a row instead of opening it', () => {
    h.checked = [1];
    const root = render(MessageList, {});
    root.querySelector('#msg-3')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(h.toggleChecked).toHaveBeenCalledWith(3);
    expect(h.selectMessage).not.toHaveBeenCalled();
  });

  it('the selection bar keeps read, archive and delete; tags and project move behind "more"', () => {
    h.checked = [1, 2];
    const root = render(MessageList, {});
    const bar = root.querySelector('.bulk')!;
    expect(bar.querySelector('.bulk-more')).toBeTruthy();
    expect(bar.querySelectorAll('.bulk-rare').length).toBe(2);
    expect(bar.querySelectorAll('button').length).toBe(7);
  });

  it('swiping a row left deletes, right toggles read; a short drag does nothing', () => {
    const root = render(MessageList, {});
    const row = root.querySelector('#msg-1')!;
    pointer(row, 'pointerdown', 200, 100);
    pointer(row, 'pointermove', 90, 104);
    pointer(row, 'pointerup', 90, 104);
    expect(h.moveToRole).toHaveBeenCalledWith(1, 'trash');
    const row2 = root.querySelector('#msg-2')!;
    pointer(row2, 'pointerdown', 100, 100);
    pointer(row2, 'pointermove', 210, 100);
    pointer(row2, 'pointerup', 210, 100);
    expect(h.toggleRead).toHaveBeenCalledWith(2);
    h.moveToRole.mockClear();
    const row3 = root.querySelector('#msg-3')!;
    pointer(row3, 'pointerdown', 100, 100);
    pointer(row3, 'pointermove', 140, 100);
    pointer(row3, 'pointerup', 140, 100);
    expect(h.moveToRole).not.toHaveBeenCalled();
  });

  it('the floating compose button starts a new mail', () => {
    const root = render(MessageList, {});
    root.querySelector<HTMLButtonElement>('button.fab')!.click();
    expect(h.openCompose).toHaveBeenCalled();
  });
});
