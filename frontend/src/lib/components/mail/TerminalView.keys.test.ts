// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
//
// The terminal view is used with the keyboard only (vim / aerc keys). Keys are sent to the window
// like a real user would type them.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, until } from '../security/mountUtil';
import { app } from '../../state';
import TerminalView from './TerminalView.svelte';
import { FLAG_SEEN } from '../../types';

const msg = (id: number, seen = true) => ({ id, accountId: 1, folderId: 10, from: [{ email: `a${id}@example.org` }], to: [], subject: `Betreff ${id}`, date: '2026-11-09T10:00:00Z', flags: seen ? [FLAG_SEEN] : [], hasAttachments: false });

function key(k: string, init: KeyboardEventInit = {}) {
  window.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...init }));
}
const keys = (s: string) => [...s].forEach((c) => key(c, c >= 'A' && c <= 'Z' ? { shiftKey: true } : {}));

let select: ReturnType<typeof vi.spyOn>;
let trash: ReturnType<typeof vi.spyOn>;
let compose: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  app.messages = [msg(1), msg(2, false), msg(3), msg(4, false), msg(5)] as never;
  app.messagesTotal = 5;
  app.selectedMessageId = 1;
  app.message = null;
  app.checked = [];
  app.search = '';
  select = vi.spyOn(app, 'selectMessage').mockImplementation(async (id) => {
    app.selectedMessageId = id as number | null;
  });
  trash = vi.spyOn(app, 'moveToRole').mockResolvedValue();
  compose = vi.spyOn(app, 'openCompose').mockImplementation(() => {});
  vi.spyOn(app, 'loadMoreMessages').mockResolvedValue();
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const lastSelected = () => select.mock.calls.at(-1)?.[0];

describe('terminal view: keyboard only', () => {
  it('opens full screen with a status line and a hint for the help', () => {
    const root = render(TerminalView, {});
    expect(root.querySelector('.term')).toBeTruthy();
    expect(root.textContent).toContain('TuxMail');
    expect(root.querySelector('.cmd')?.textContent).toContain('?');
  });

  it('j and k move through the list, with counts', async () => {
    render(TerminalView, {});
    key('j');
    expect(lastSelected()).toBe(2);
    key('3');
    key('j');
    expect(lastSelected()).toBe(5); // from message 2, three down
  });

  it('gg goes to the top, G to the end, 3G to line 3', () => {
    render(TerminalView, {});
    keys('G');
    expect(lastSelected()).toBe(5);
    keys('gg');
    expect(lastSelected()).toBe(1);
    keys('3G');
    expect(lastSelected()).toBe(3);
  });

  it('dd moves the message to the trash, a single d does nothing', () => {
    render(TerminalView, {});
    key('d');
    expect(trash).not.toHaveBeenCalled();
    key('d');
    expect(trash).toHaveBeenCalledWith(1, 'trash');
  });

  it('n jumps to the next unread message', () => {
    render(TerminalView, {});
    key('n');
    expect(lastSelected()).toBe(2);
  });

  it('c opens the composer, e archives', () => {
    render(TerminalView, {});
    key('c');
    expect(compose).toHaveBeenCalled();
    key('e');
    expect(trash).toHaveBeenCalledWith(1, 'archive');
  });

  it('x marks and moves on; actions then use all marked messages', () => {
    render(TerminalView, {});
    key('x');
    expect(app.checked).toEqual([1]);
    expect(lastSelected()).toBe(2);
    key('X', { shiftKey: true });
    expect(app.checked).toEqual([]);
  });

  it(': opens the command line, Escape leaves it again', async () => {
    const root = render(TerminalView, {});
    key(':');
    const input = root.querySelector<HTMLInputElement>('#term-cmd')!;
    await until(() => expect(document.activeElement).toBe(input));
    expect(root.querySelector('.prompt')?.textContent).toBe(':');
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    await until(() => expect(root.querySelector('.prompt')).toBeNull());
  });

  it('/ opens the search line with the active search', async () => {
    const root = render(TerminalView, {});
    key('/');
    await until(() => expect(root.querySelector('.prompt')?.textContent).toBe('/'));
  });

  it('? shows the key list, Escape closes it; keys do nothing while it is open', async () => {
    const root = render(TerminalView, {});
    key('?', { shiftKey: true });
    await until(() => expect(root.querySelector('.help')).toBeTruthy());
    select.mockClear();
    key('j');
    expect(select).not.toHaveBeenCalled();
    key('Escape');
    await until(() => expect(root.querySelector('.help')).toBeNull());
  });

  it('keeps browser shortcuts and dialogs alone', () => {
    render(TerminalView, {});
    const ev = new KeyboardEvent('keydown', { key: 'r', ctrlKey: true, bubbles: true, cancelable: true });
    window.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(false); // Ctrl+R reloads the page as usual
    const dlg = document.createElement('div');
    dlg.setAttribute('aria-modal', 'true');
    document.body.append(dlg);
    select.mockClear();
    key('j');
    expect(select).not.toHaveBeenCalled(); // the composer gets its keys
    dlg.remove();
  });

  it('does not let classic single-key shortcuts through (t would open the tag dialog)', () => {
    render(TerminalView, {});
    const ev = new KeyboardEvent('keydown', { key: 't', bubbles: true, cancelable: true });
    window.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
  });
});
