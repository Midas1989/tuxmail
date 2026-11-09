// SPDX-License-Identifier: AGPL-3.0-or-later
//
// One-time secret links (server: internal/server/secretlink). The text is encrypted here, in the
// browser, with AES-256-GCM under a random key. Only the ciphertext goes to the server; the key
// is part of the link fragment (https://host/secret/<id>#<key>) and browsers never send a
// fragment. The page that opens the link (internal/server/secretlinks.go) uses the same format:
// ciphertext = 12 byte nonce + AES-GCM output of the JSON {"v":1,"t":text}, both base64url.

/** The server accepts 64 KiB of ciphertext; keep clear of it (JSON overhead, nonce, tag). */
export const MAX_SECRET_BYTES = 60_000;

export const SECRET_TTL_SECONDS = { hour: 3600, day: 86400, week: 7 * 86400 } as const;
export const SECRET_VIEW_CHOICES = [1, 3, 10] as const;

export interface EncryptedSecret {
  /** base64url, goes to the server. */
  ciphertext: string;
  /** base64url (32 bytes), goes into the link fragment only. */
  key: string;
}

export function b64urlEncode(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function b64urlDecode(s: string): Uint8Array<ArrayBuffer> {
  const std = s.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(std + '='.repeat((4 - (std.length % 4)) % 4));
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** Size of the text as it will be encrypted (UTF-8 plus JSON framing). */
export function secretBytes(text: string): number {
  return new TextEncoder().encode(JSON.stringify({ v: 1, t: text })).length;
}

export async function encryptSecret(text: string): Promise<EncryptedSecret> {
  if (secretBytes(text) > MAX_SECRET_BYTES) throw new RangeError('secret too long');
  const raw = crypto.getRandomValues(new Uint8Array(32));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const key = await crypto.subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt']);
  const body = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, key, new TextEncoder().encode(JSON.stringify({ v: 1, t: text }))));
  const out = new Uint8Array(iv.length + body.length);
  out.set(iv);
  out.set(body, iv.length);
  return { ciphertext: b64urlEncode(out), key: b64urlEncode(raw) };
}

/** Counterpart of the page script; used by tests and to check what was built. */
export async function decryptSecret(ciphertext: string, keyB64: string): Promise<string> {
  const data = b64urlDecode(ciphertext);
  const raw = b64urlDecode(keyB64);
  if (raw.length !== 32 || data.length < 29) throw new Error('invalid secret');
  const key = await crypto.subtle.importKey('raw', raw, 'AES-GCM', false, ['decrypt']);
  const plain = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: data.slice(0, 12) }, key, data.slice(12));
  const j = JSON.parse(new TextDecoder().decode(plain)) as { v?: number; t?: unknown };
  if (j.v !== 1 || typeof j.t !== 'string') throw new Error('invalid secret');
  return j.t;
}

/** The link the recipient gets: the address the server returned (public_url + /secret/<id>) plus the key as fragment. */
export function secretLinkUrl(serverUrl: string, key: string): string {
  return `${serverUrl}#${key}`;
}
