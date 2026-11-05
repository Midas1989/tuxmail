// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * True for a plain `http://` URL to a host other than the local machine:
 * passwords sent to it travel unencrypted. The server (account create/update
 * API, field `allowInsecure`) refuses such ActiveSync, JMAP and CalDAV/CardDAV
 * endpoints unless the user confirmed them, like the "unencrypted connection"
 * checkbox for IMAP/POP3/SMTP.
 */
export function isPlainHttpUrl(url: string | undefined | null): boolean {
  const u = (url ?? '').trim();
  if (!/^http:\/\//i.test(u)) return false;
  return !/^http:\/\/(localhost|[^/@:]*\.localhost|127\.\d{1,3}\.\d{1,3}\.\d{1,3}|\[::1\])(?=[:/?#]|$)/i.test(u);
}
