// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Signal "this administrator must set up two-factor authentication first".
// The HTTP transport raises it on any 403 `mfa_setup_required` (platformHttpOptions
// in session.ts); the security state (state/security.svelte.ts) listens and shows
// the blocking set-up dialog. Kept free of framework imports so the transport
// layer can use it.

type Listener = () => void;
const listeners = new Set<Listener>();

/** Called by the transport when the server answered 403 mfa_setup_required. */
export function notifyMfaSetupRequired(): void {
  for (const l of [...listeners]) {
    try {
      l();
    } catch (err) {
      console.error('mfa gate listener failed', err);
    }
  }
}

/** Subscribe; returns the unsubscribe function. */
export function onMfaSetupRequired(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
