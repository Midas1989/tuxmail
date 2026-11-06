// SPDX-License-Identifier: AGPL-3.0-or-later

export const ADMIN_TABS = ['users', 'invites', 'groups', 'audit'] as const;
export type AdminTab = (typeof ADMIN_TABS)[number];
