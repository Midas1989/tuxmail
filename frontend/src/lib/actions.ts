// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Small Svelte actions shared by the messenger UI.

/** Long press (touch / pen) -> callback with the pointer position; also handles the context menu (right click). */
export function longpress(node: HTMLElement, cb: (x: number, y: number) => void) {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let start: { x: number; y: number } | null = null;
  let handler = cb;
  const clear = () => {
    if (timer) clearTimeout(timer);
    timer = null;
  };
  const down = (e: PointerEvent) => {
    if (e.pointerType === 'mouse') return;
    start = { x: e.clientX, y: e.clientY };
    clear();
    timer = setTimeout(() => {
      timer = null;
      if (start) handler(start.x, start.y);
      navigator.vibrate?.(10);
    }, 480);
  };
  const move = (e: PointerEvent) => {
    if (start && Math.hypot(e.clientX - start.x, e.clientY - start.y) > 10) clear();
  };
  const ctx = (e: MouseEvent) => {
    e.preventDefault();
    clear();
    handler(e.clientX, e.clientY);
  };
  node.addEventListener('pointerdown', down);
  node.addEventListener('pointermove', move);
  node.addEventListener('pointerup', clear);
  node.addEventListener('pointercancel', clear);
  node.addEventListener('contextmenu', ctx);
  return {
    update(next: (x: number, y: number) => void) {
      handler = next;
    },
    destroy() {
      clear();
      node.removeEventListener('pointerdown', down);
      node.removeEventListener('pointermove', move);
      node.removeEventListener('pointerup', clear);
      node.removeEventListener('pointercancel', clear);
      node.removeEventListener('contextmenu', ctx);
    },
  };
}

/** Horizontal swipe on touch devices: left / right callbacks (e.g. archive, mark read). */
export function swipe(node: HTMLElement, opts: { left?: () => void; right?: () => void }) {
  let o = opts;
  let x0 = 0;
  let y0 = 0;
  let active = false;
  const down = (e: PointerEvent) => {
    if (e.pointerType === 'mouse') return;
    x0 = e.clientX;
    y0 = e.clientY;
    active = true;
  };
  const move = (e: PointerEvent) => {
    if (!active) return;
    const dx = e.clientX - x0;
    if (Math.abs(e.clientY - y0) > 30) {
      active = false;
      node.style.transform = '';
      return;
    }
    node.style.transform = `translateX(${Math.max(-90, Math.min(90, dx))}px)`;
  };
  const up = (e: PointerEvent) => {
    if (!active) return;
    active = false;
    const dx = e.clientX - x0;
    node.style.transform = '';
    if (dx < -70) o.left?.();
    else if (dx > 70) o.right?.();
  };
  node.addEventListener('pointerdown', down);
  node.addEventListener('pointermove', move);
  node.addEventListener('pointerup', up);
  node.addEventListener('pointercancel', up);
  return {
    update(next: { left?: () => void; right?: () => void }) {
      o = next;
    },
    destroy() {
      node.removeEventListener('pointerdown', down);
      node.removeEventListener('pointermove', move);
      node.removeEventListener('pointerup', up);
      node.removeEventListener('pointercancel', up);
    },
  };
}
