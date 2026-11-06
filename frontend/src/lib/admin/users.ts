// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Logic of the user table in Settings -> Administration: status, filter and
// which actions an administrator may take on which account. Pure module so
// the rules (no self-lockout, last administrator) are unit-testable; the
// server enforces the last-administrator rule as well (409 last_admin).

import type { AdminRole, AdminUser } from '../api/admin';

export type UserStatus = 'active' | 'disabled' | 'locked';

/** Disabled wins over locked; "locked" also covers an account under distributed attack. */
export function userStatus(u: Pick<AdminUser, 'disabled' | 'locked' | 'underPressure'>): UserStatus {
  if (u.disabled) return 'disabled';
  if (u.locked || u.underPressure) return 'locked';
  return 'active';
}

/** The unlock action only exists while something is throttled for the account (also for a disabled one). */
export function needsUnlock(u: Pick<AdminUser, 'locked' | 'underPressure'>): boolean {
  return u.locked || u.underPressure;
}

export interface UserFilter {
  /** Matches user name, display name and e-mail address (case-insensitive, all words). */
  query: string;
  role: AdminRole | '';
  status: UserStatus | '';
  totp: '' | 'on' | 'off';
}

export const EMPTY_FILTER: UserFilter = { query: '', role: '', status: '', totp: '' };

export function isFiltered(f: UserFilter): boolean {
  return !!(f.query.trim() || f.role || f.status || f.totp);
}

function norm(s: string): string {
  return s.toLowerCase().normalize('NFD').replace(/\p{M}/gu, '');
}

export function filterUsers(users: readonly AdminUser[], f: UserFilter): AdminUser[] {
  const words = norm(f.query).split(/\s+/).filter(Boolean);
  return users.filter((u) => {
    if (f.role && u.role !== f.role) return false;
    // "locked" lists everything the unlock action applies to, also a disabled account.
    if (f.status === 'locked' && !needsUnlock(u)) return false;
    if (f.status === 'disabled' && !u.disabled) return false;
    if (f.status === 'active' && userStatus(u) !== 'active') return false;
    if (f.totp === 'on' && !u.totpEnabled) return false;
    if (f.totp === 'off' && u.totpEnabled) return false;
    if (!words.length) return true;
    const hay = norm(`${u.username} ${u.displayName} ${u.email}`);
    return words.every((w) => hay.includes(w));
  });
}

/** What the actions dialog of an account offers. */
export type UserAction = 'edit' | 'toggleDisabled' | 'resetPassword' | 'reset2fa' | 'unlock' | 'sessions' | 'delete';

export type GuardReason = 'self' | 'lastAdmin';

export interface UserGuards {
  /** Change the role (demote or promote). */
  role: GuardReason | null;
  /** Disable the account; enabling is always allowed. */
  disable: GuardReason | null;
  delete: GuardReason | null;
  /** Reset the password / revoke all sessions: both end the sessions of the account, an administrator must not log themself out. */
  resetPassword: GuardReason | null;
  revokeAll: GuardReason | null;
  /** Remove the own second factor in Security settings instead. */
  reset2FA: GuardReason | null;
}

/** Number of enabled administrators. */
export function activeAdmins(users: readonly AdminUser[]): number {
  return users.filter((u) => u.role === 'admin' && !u.disabled).length;
}

/**
 * Which actions are blocked for `u`, and why (null = allowed). Mirrors the
 * server: ensureNotLastAdmin blocks demoting, disabling and deleting the only
 * enabled administrator. Additionally the UI never lets administrators lock
 * themself out (role, disable, delete, password reset, session revoke, 2FA reset).
 */
export function userGuards(u: AdminUser, all: readonly AdminUser[], meId: number | null): UserGuards {
  const self = meId !== null && u.id === meId;
  const onlyAdmin = u.role === 'admin' && !u.disabled && activeAdmins(all) <= 1;
  const s: GuardReason | null = self ? 'self' : null;
  const last: GuardReason | null = onlyAdmin ? 'lastAdmin' : null;
  return {
    role: s ?? last,
    disable: u.disabled ? null : (s ?? last),
    delete: s ?? last,
    resetPassword: s,
    revokeAll: s,
    reset2FA: s,
  };
}
