// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Client of the passkey endpoints of `tuxmail serve` (internal/server/api_passkeys.go):
//
//   GET    /api/auth/config                          -> {passkeysEnabled, ...}
//   GET    /api/auth/passkeys                        -> PasskeyListing
//   POST   /api/auth/passkeys/register/begin         Reauth -> {challengeId, publicKey}
//   POST   /api/auth/passkeys/register/finish        {challengeId, name, credential} -> {passkey}
//   PATCH  /api/auth/passkeys/{id}                   {name}
//   DELETE /api/auth/passkeys/{id}                   Reauth
//
// Only the web client (cookie session) has them; desktop shells and the demo
// transport have no server session, createPasskeyClient returns null there.

import type { Api } from '../api/api';
import { ApiError } from '../api/api';
import { readCsrfToken } from '../api/http';
import type { AttestationJSON, CreationOptionsJSON } from './webauthn';

export interface PasskeyEntry {
  id: number;
  name: string;
  createdAt: string;
  lastUsedAt?: string;
  transports: string[];
  /** A synced passkey (can be backed up to the platform account). */
  backupEligible: boolean;
  backupState: boolean;
  /** False when the passkey belongs to another domain (public_url changed); it can only be removed. */
  usable: boolean;
}

export interface PasskeyListing {
  passkeys: PasskeyEntry[];
  enabled: boolean;
  max: number;
  /** Which proofs adding or removing a passkey asks for. */
  reauth: { password: boolean; code: boolean };
}

/** Re-authentication sent with add / remove. */
export interface Reauth {
  password?: string;
  /** TOTP code */
  code?: string;
  recoveryCode?: string;
}

export interface PasskeyClient {
  list(): Promise<PasskeyListing>;
  beginRegistration(reauth: Reauth): Promise<{ challengeId: string; publicKey: CreationOptionsJSON }>;
  finishRegistration(input: { challengeId: string; name: string; credential: AttestationJSON }): Promise<PasskeyEntry>;
  rename(id: number, name: string): Promise<void>;
  remove(id: number, reauth: Reauth): Promise<void>;
}

export interface PasskeyClientOptions {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
}

export class HttpPasskeyClient implements PasskeyClient {
  constructor(private readonly opts: PasskeyClientOptions = {}) {}

  private async req<T>(method: string, path: string, body?: unknown): Promise<T> {
    const h: Record<string, string> = { Accept: 'application/json' };
    const csrf = readCsrfToken();
    if (csrf) h['X-CSRF-Token'] = csrf;
    if (body !== undefined) h['Content-Type'] = 'application/json';
    const f = this.opts.fetchImpl ?? ((...a: Parameters<typeof fetch>) => fetch(...a));
    let res: Response;
    try {
      res = await f((this.opts.baseUrl ?? '').replace(/\/$/, '') + path, {
        method,
        headers: h,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        credentials: 'same-origin',
      });
    } catch (err) {
      throw new ApiError(err instanceof Error ? err.message : 'network error', 0, 'network');
    }
    if (!res.ok) {
      let code = 'http_' + res.status;
      let msg = res.statusText;
      try {
        const j = (await res.json()) as { error?: string; message?: string };
        if (j.error) code = j.error;
        if (j.message) msg = j.message;
      } catch {
        /* not JSON */
      }
      throw new ApiError(msg, res.status, code);
    }
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  list() {
    return this.req<PasskeyListing>('GET', '/api/auth/passkeys');
  }
  beginRegistration(reauth: Reauth) {
    return this.req<{ challengeId: string; publicKey: CreationOptionsJSON }>('POST', '/api/auth/passkeys/register/begin', reauth);
  }
  async finishRegistration(input: { challengeId: string; name: string; credential: AttestationJSON }) {
    return (await this.req<{ passkey: PasskeyEntry }>('POST', '/api/auth/passkeys/register/finish', input)).passkey;
  }
  rename(id: number, name: string) {
    return this.req<void>('PATCH', `/api/auth/passkeys/${id}`, { name });
  }
  remove(id: number, reauth: Reauth) {
    return this.req<void>('DELETE', `/api/auth/passkeys/${id}`, reauth);
  }
}

/** The passkey client for a transport: only the HTTP transport (web client) has server accounts. */
export function createPasskeyClient(root: Api, opts: PasskeyClientOptions = {}): PasskeyClient | null {
  return root.kind === 'http' ? new HttpPasskeyClient(opts) : null;
}
