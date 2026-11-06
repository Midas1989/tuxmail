// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Helpers for the component tests of the security pages (jsdom): mount a
// Svelte component, wait for async UI, type into fields, click buttons by
// their visible label. Test support only.

import { flushSync, mount, unmount } from 'svelte';
import type { Component, ComponentProps } from 'svelte';
import { expect, vi } from 'vitest';

// jsdom has no Web Animations API; Svelte's transitions (Modal) call element.animate().
if (typeof Element !== 'undefined' && !Element.prototype.animate) {
  Element.prototype.animate = function () {
    const a = { onfinish: null as null | (() => void), cancel() {}, finish() { a.onfinish?.(); }, currentTime: 0, effect: null, pause() {}, play() {}, reverse() {} };
    queueMicrotask(() => a.onfinish?.());
    return a as unknown as Animation;
  };
}

const mounted: { cmp: Record<string, unknown>; target: HTMLElement }[] = [];

export function render<C extends Component<any>>(component: C, props: ComponentProps<C>): HTMLElement {
  const target = document.body.appendChild(document.createElement('div'));
  const cmp = mount(component, { target, props } as never) as Record<string, unknown>;
  mounted.push({ cmp, target });
  flushSync();
  return target;
}

export function cleanup(): void {
  for (const m of mounted.splice(0)) {
    void unmount(m.cmp);
    m.target.remove();
  }
  document.body.innerHTML = '';
}

/** Waits until the assertion holds (async UI after promises of the mock transport). */
export async function until(check: () => void): Promise<void> {
  await vi.waitFor(check, { timeout: 2000, interval: 5 });
  flushSync();
}

export function button(root: ParentNode, label: string | RegExp): HTMLButtonElement {
  const all = [...root.querySelectorAll('button')];
  const hit = all.find((b) => (typeof label === 'string' ? b.textContent?.trim() === label : label.test(b.textContent?.trim() ?? '')));
  expect(hit, `button "${String(label)}" among: ${all.map((b) => b.textContent?.trim()).join(' | ')}`).toBeTruthy();
  return hit as HTMLButtonElement;
}

export function hasButton(root: ParentNode, label: string | RegExp): boolean {
  return [...root.querySelectorAll('button')].some((b) => (typeof label === 'string' ? b.textContent?.trim() === label : label.test(b.textContent?.trim() ?? '')));
}

export async function click(el: HTMLElement): Promise<void> {
  el.click();
  flushSync();
  await Promise.resolve();
}

export function type(input: HTMLInputElement, value: string): void {
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  flushSync();
}

export function input(root: ParentNode, selector: string): HTMLInputElement {
  const el = root.querySelector<HTMLInputElement>(selector);
  expect(el, `input ${selector}`).toBeTruthy();
  return el as HTMLInputElement;
}

export async function submit(form: HTMLFormElement): Promise<void> {
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  flushSync();
  await Promise.resolve();
}

export const text = (root: ParentNode): string => (root.textContent ?? '').replace(/\s+/g, ' ').trim();
