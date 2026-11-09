// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Pull to refresh in the mail list (MessageList.svelte, touch only).
//
// The rows have their own horizontal swipe (delete / read). Both must not fire
// together: the pull only starts when the finger moved mostly downwards
// (dy > 2·|dx|) by more than a few pixels, a mostly sideways move ends it for this
// touch, and the list hands it over to the swipe the moment a row swipe starts.

export const PULL_TRIGGER = 64;
const SLOP = 10;

export class PullGesture {
  private start: { x: number; y: number } | null = null;
  private active = false;
  /** Shown height of the pull indicator in px. */
  distance = 0;

  /** Finger down. `atTop`: the list is scrolled to the top; `enabled`: no refresh running. */
  down(x: number, y: number, atTop: boolean, enabled: boolean): void {
    this.start = atTop && enabled ? { x, y } : null;
    this.active = false;
    this.distance = 0;
  }

  /** Finger moved; returns the indicator height. */
  move(x: number, y: number, atTop: boolean): number {
    const s = this.start;
    if (!s) return (this.distance = 0);
    const dx = x - s.x;
    const dy = y - s.y;
    if (!this.active) {
      if (Math.abs(dx) > SLOP && Math.abs(dx) * 2 >= Math.abs(dy)) {
        this.cancel(); // sideways: a row swipe, not a pull
        return 0;
      }
      if (dy > SLOP && dy > 2 * Math.abs(dx) && atTop) this.active = true;
      else if (dy < -SLOP) {
        this.cancel(); // scrolling up the list
        return 0;
      }
    }
    if (!this.active) return (this.distance = 0);
    if (dy <= 0 || !atTop) return (this.distance = 0);
    return (this.distance = Math.min(PULL_TRIGGER * 1.5, (dy - SLOP) * 0.6));
  }

  /** Finger up; true = refresh now. */
  up(): boolean {
    const go = this.active && this.distance >= PULL_TRIGGER;
    this.cancel();
    return go;
  }

  cancel(): void {
    this.start = null;
    this.active = false;
    this.distance = 0;
  }
}
