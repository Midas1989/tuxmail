// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// The idle limit of a web session counts what the person does, not what an open tab
// does by itself. Requests within a moment of the user's input say so with a header
// (server: internal/server/middleware.go); polling, refetches after a server event
// and the event stream go without it. Native clients have device tokens without an
// idle limit and never send it: a custom header would also force a CORS preflight.

export const ACTIVITY_HEADER = 'X-TuxMail-Activity';
/** A request this soon after the last input counts as caused by it. */
export const ACTIVITY_WINDOW_MS = 60_000;

const INPUT_EVENTS = ['pointerdown', 'keydown', 'wheel', 'touchstart'] as const;

let lastInput = Date.now(); // opening the page is an action of the user
let installed = false;

function install(): void {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  const note = () => {
    lastInput = Date.now();
  };
  for (const e of INPUT_EVENTS) window.addEventListener(e, note, { capture: true, passive: true });
}

install();

/** Headers of a cookie-authenticated request. */
export function activityHeaders(now: number = Date.now()): Record<string, string> {
  return now - lastInput < ACTIVITY_WINDOW_MS ? { [ACTIVITY_HEADER]: '1' } : {};
}
