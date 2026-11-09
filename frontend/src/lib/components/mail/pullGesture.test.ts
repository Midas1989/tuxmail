// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { PULL_TRIGGER, PullGesture } from './pullGesture';

describe('PullGesture', () => {
  it('a long downward pull from the top triggers, a short one only shows the indicator', () => {
    const g = new PullGesture();
    g.down(100, 100, true, true);
    expect(g.move(101, 150, true)).toBeGreaterThan(0);
    expect(g.move(101, 150, true)).toBeLessThan(PULL_TRIGGER);
    expect(g.up()).toBe(false);
    g.down(100, 100, true, true);
    g.move(101, 150, true);
    expect(g.move(103, 260, true)).toBeGreaterThanOrEqual(PULL_TRIGGER);
    expect(g.up()).toBe(true);
  });

  it('does not start when the list is not at the top or a refresh is running', () => {
    const g = new PullGesture();
    g.down(100, 100, false, true);
    expect(g.move(100, 300, true)).toBe(0);
    expect(g.up()).toBe(false);
    g.down(100, 100, true, false);
    expect(g.move(100, 300, true)).toBe(0);
    expect(g.up()).toBe(false);
  });

  it('a mostly sideways move (row swipe) ends the pull for this touch', () => {
    const g = new PullGesture();
    g.down(200, 100, true, true);
    expect(g.move(170, 112, true)).toBe(0);
    expect(g.move(170, 300, true)).toBe(0);
    expect(g.up()).toBe(false);
  });

  it('a diagonal move with as much sideways as downward movement does not pull', () => {
    const g = new PullGesture();
    g.down(100, 100, true, true);
    expect(g.move(130, 140, true)).toBe(0);
    expect(g.up()).toBe(false);
  });

  it('scrolling up first, or the list leaving the top, ends the pull', () => {
    const g = new PullGesture();
    g.down(100, 100, true, true);
    expect(g.move(100, 80, true)).toBe(0);
    expect(g.move(100, 300, true)).toBe(0);
    expect(g.up()).toBe(false);
    g.down(100, 100, true, true);
    g.move(100, 200, true);
    expect(g.move(100, 300, false)).toBe(0);
    expect(g.up()).toBe(false);
  });
});
