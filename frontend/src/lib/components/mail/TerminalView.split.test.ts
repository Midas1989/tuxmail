// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render } from '../security/mountUtil';
import TerminalView from './TerminalView.svelte';

afterEach(() => {
  cleanup();
  localStorage.clear();
});

describe('terminal view divider', () => {
  it('shares the width 50/50 by default and moves with the arrow keys, remembered per device', async () => {
    const root = render(TerminalView, {});
    const sep = root.querySelector('[role="separator"]') as HTMLElement;
    expect(sep.getAttribute('aria-valuenow')).toBe('50');
    expect((root.querySelector('.panes') as HTMLElement).style.getPropertyValue('--term-list')).toBe('50%');
    sep.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    await new Promise((r) => setTimeout(r, 10));
    expect(sep.getAttribute('aria-valuenow')).toBe('55');
    expect(localStorage.getItem('tuxmail.term.listPct')).toBe('55');
    for (let i = 0; i < 20; i++) sep.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
    await new Promise((r) => setTimeout(r, 10));
    expect(sep.getAttribute('aria-valuenow')).toBe('25');
  });
});
