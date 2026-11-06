// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AdminInvite } from '../api/admin';

export type InviteStatus = 'open' | 'used' | 'expired';

/** Lifetimes offered when creating an invite (the server default is 7 days). */
export const INVITE_LIFETIMES_HOURS = [24, 72, 24 * 7, 24 * 14, 24 * 30] as const;

export function inviteStatus(inv: Pick<AdminInvite, 'usedAt' | 'expiresAt'>, now: Date = new Date()): InviteStatus {
  if (inv.usedAt) return 'used';
  return new Date(inv.expiresAt).getTime() <= now.getTime() ? 'expired' : 'open';
}
