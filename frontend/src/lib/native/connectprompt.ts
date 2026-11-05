// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// State of the "tuxmail://connect?server=..." confirmation (NG-01).
//
// The link comes from outside the app (exported intent filter, QR code, chat),
// so it never changes the running session by itself:
//   idle --request(valid link)--> confirm --confirm--> connect (login screen)
//                                    \--cancel--> idle
// The active profile is only replaced by ProfileStore.upsert() after the new
// login has succeeded. Pure functions, unit-tested without Capacitor.

import { parseConnectLink, serverDisplay } from './navigation';

export type ConnectPromptState =
  | { phase: 'idle' }
  | {
      phase: 'confirm';
      /** Normalised server URL (https, or loopback http). */
      server: string;
      /** Full host the user is asked about, e.g. "mail.example.org:8443". */
      host: string;
      /** Host of the currently active session, if any. */
      current: string | null;
    }
  | { phase: 'connect'; server: string; host: string };

export const IDLE: ConnectPromptState = { phase: 'idle' };

/** A connect link arrived. Invalid / non-https links are refused (state stays idle). */
export function requestConnect(url: string, currentServerUrl: string | null): ConnectPromptState {
  const link = parseConnectLink(url);
  if (!link) return IDLE;
  return { phase: 'confirm', server: link.server, host: serverDisplay(link.server), current: currentServerUrl ? serverDisplay(currentServerUrl) : null };
}

/** Only an explicit confirmation lets the login screen appear. */
export function confirmConnect(s: ConnectPromptState): ConnectPromptState {
  return s.phase === 'confirm' ? { phase: 'connect', server: s.server, host: s.host } : s;
}

export function cancelConnect(): ConnectPromptState {
  return IDLE;
}
