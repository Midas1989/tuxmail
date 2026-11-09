// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { KeyParser, type Action, type KeyInput } from './keymap';

function run(keys: (string | KeyInput)[]): (Action | 'pending' | null)[] {
  const p = new KeyParser();
  return keys.map((k) => p.feed(typeof k === 'string' ? { key: k } : k));
}
const last = (keys: (string | KeyInput)[]) => run(keys).at(-1);

describe('terminal key map (vim)', () => {
  it('moves with j/k and the arrow keys', () => {
    expect(last(['j'])).toMatchObject({ name: 'down', count: 1, hasCount: false });
    expect(last(['k'])).toMatchObject({ name: 'up' });
    expect(last(['ArrowDown'])).toMatchObject({ name: 'down' });
    expect(last(['ArrowUp'])).toMatchObject({ name: 'up' });
  });

  it('takes counts: 5j, 12G, 100k', () => {
    expect(last(['5', 'j'])).toMatchObject({ name: 'down', count: 5, hasCount: true });
    expect(last(['1', '2', { key: 'G', shift: true }])).toMatchObject({ name: 'bottom', count: 12, hasCount: true });
    expect(last(['1', '0', '0', 'k'])).toMatchObject({ name: 'up', count: 100 });
    // A leading 0 is no count.
    expect(last(['0'])).toBeNull();
  });

  it('gg goes to the top, G to the end', () => {
    expect(run(['g', 'g'])).toEqual(['pending', expect.objectContaining({ name: 'top' })]);
    expect(last([{ key: 'G', shift: true }])).toMatchObject({ name: 'bottom', hasCount: false });
  });

  it('g + letter goes to a folder, gr refreshes', () => {
    expect(last(['g', 'i'])).toMatchObject({ name: 'goto', arg: 'inbox' });
    expect(last(['g', 's'])).toMatchObject({ name: 'goto', arg: 'sent' });
    expect(last(['g', 'd'])).toMatchObject({ name: 'goto', arg: 'drafts' });
    expect(last(['g', 'a'])).toMatchObject({ name: 'goto', arg: 'archive' });
    expect(last(['g', 't'])).toMatchObject({ name: 'goto', arg: 'trash' });
    expect(last(['g', 'j'])).toMatchObject({ name: 'goto', arg: 'junk' });
    expect(last(['g', 'r'])).toMatchObject({ name: 'refresh' });
  });

  it('dd deletes (a single d does nothing), also with a count', () => {
    expect(run(['d'])).toEqual(['pending']);
    expect(last(['d', 'd'])).toMatchObject({ name: 'trash' });
    expect(last(['d', 'x'])).toBeNull();
    expect(run(['d', 'x', 'j']).at(-1)).toMatchObject({ name: 'down' }); // the dropped prefix is gone
  });

  it('Shift alone does not end a sequence or a count', () => {
    expect(run(['3', { key: 'Shift', shift: true }, { key: 'J', shift: true }]).at(-1)).toMatchObject({ name: 'nextMessage', count: 3 });
  });

  it('scrolls like vim: Ctrl-d/u half page, Ctrl-f/b page, space page', () => {
    expect(last([{ key: 'd', ctrl: true }])).toMatchObject({ name: 'halfDown' });
    expect(last([{ key: 'u', ctrl: true }])).toMatchObject({ name: 'halfUp' });
    expect(last([{ key: 'f', ctrl: true }])).toMatchObject({ name: 'pageDown' });
    expect(last([{ key: 'b', ctrl: true }])).toMatchObject({ name: 'pageUp' });
    expect(last([' '])).toMatchObject({ name: 'pageDown' });
  });

  it('switches panes with h/l and Tab', () => {
    expect(last(['h'])).toMatchObject({ name: 'paneLeft' });
    expect(last(['l'])).toMatchObject({ name: 'paneRight' });
    expect(last(['Tab'])).toMatchObject({ name: 'cyclePane' });
    expect(last([{ key: 'Tab', shift: true }])).toMatchObject({ name: 'cyclePaneBack' });
  });

  it('mail actions', () => {
    const names: Record<string, string> = { c: 'compose', r: 'reply', f: 'forward', e: 'archive', s: 'flag', u: 'toggleRead', m: 'move', x: 'mark', n: 'nextUnread', Enter: 'open', '/': 'search', ':': 'command', '?': 'help', '#': 'trash' };
    for (const [key, name] of Object.entries(names)) expect(last([key]), key).toMatchObject({ name });
    expect(last([{ key: 'R', shift: true }])).toMatchObject({ name: 'replyAll' });
    expect(last([{ key: 'X', shift: true }])).toMatchObject({ name: 'unmarkAll' });
    expect(last([{ key: 'N', shift: true }])).toMatchObject({ name: 'prevUnread' });
    expect(last([{ key: 'K', shift: true }])).toMatchObject({ name: 'prevMessage' });
  });

  it('Escape drops what is typed first, then goes back', () => {
    expect(run(['2', 'Escape']).at(-1)).toBeNull();
    expect(last(['Escape'])).toMatchObject({ name: 'back' });
  });

  it('leaves browser shortcuts alone: Alt/Meta combinations and unknown Ctrl keys mean nothing', () => {
    expect(last([{ key: 'j', alt: true }])).toBeNull();
    expect(last([{ key: 'j', meta: true }])).toBeNull();
    expect(last([{ key: 'w', ctrl: true }])).toBeNull();
    expect(last(['F5'])).toBeNull();
  });
});
