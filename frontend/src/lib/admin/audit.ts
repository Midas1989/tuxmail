// SPDX-License-Identifier: AGPL-3.0-or-later

/** Entries per page of the audit log view. */
export const AUDIT_PAGE_SIZE = 50;

/**
 * Suggestions for the action filter: the server matches by prefix, so "user."
 * lists every user action and "login" the sign-ins (also "login.oidc").
 */
export const AUDIT_ACTION_PREFIXES: readonly string[] = ['login', 'user.', 'invite.', 'group.', 'template.', 'provisioning.', 'stepup.'];
