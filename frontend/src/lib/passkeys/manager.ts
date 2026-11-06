// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Logic of the passkey card (Settings -> Security): load, register, rename and
// remove passkeys. Framework free so it can be tested without a browser; the
// Svelte component only holds the reactive state and calls these methods.

import type { PasskeyClient, PasskeyEntry, PasskeyListing, Reauth } from './client';
import {
  attestationToJSON,
  classifyPasskeyError,
  creationOptionsFromJSON,
  passkeysSupported,
  type PasskeyErrorKind,
  type WebAuthnEnv,
} from './webauthn';

export const MAX_PASSKEY_NAME = 64;

export type Outcome<T = void> = { ok: true; value: T } | { ok: false; kind: PasskeyErrorKind };

/** What the user typed to prove it is them. */
export interface ReauthInput {
  password: string;
  code: string;
}

/** Trims and checks a passkey name; an empty name becomes the fallback. */
export function normalizeName(name: string, fallback: string): { ok: true; name: string } | { ok: false } {
  const n = name.trim() || fallback;
  // eslint-disable-next-line no-control-regex
  if ([...n].length > MAX_PASSKEY_NAME || /[\u0000-\u001f\u007f-\u009f]/.test(n)) return { ok: false };
  return { ok: true, name: n };
}

/**
 * The re-authentication body for the request: only the proofs the server asked
 * for (listing.reauth); a missing one is reported without a request, so no
 * attempt is counted against the server's step-up lockout.
 */
export function buildReauth(listing: Pick<PasskeyListing, 'reauth'>, input: ReauthInput): { ok: true; reauth: Reauth } | { ok: false; kind: PasskeyErrorKind } {
  const reauth: Reauth = {};
  if (listing.reauth.password) {
    if (!input.password) return { ok: false, kind: 'reauth' };
    reauth.password = input.password;
  }
  if (listing.reauth.code) {
    const code = input.code.replace(/\s+/g, '');
    if (!code) return { ok: false, kind: 'badCode' };
    // A recovery code has letters and a dash (xxxxx-xxxxx), a TOTP code is six digits.
    if (/^\d{6}$/.test(code)) reauth.code = code;
    else reauth.recoveryCode = code;
  }
  return { ok: true, reauth };
}

export interface PasskeyManagerDeps {
  client: PasskeyClient;
  /** navigator.credentials */
  credentials?: Pick<CredentialsContainer, 'create'>;
  env?: WebAuthnEnv;
}

export class PasskeyManager {
  constructor(private readonly deps: PasskeyManagerDeps) {}

  /** Whether this browser can create passkeys. */
  get supported(): boolean {
    return passkeysSupported(this.deps.env);
  }

  async load(): Promise<Outcome<PasskeyListing>> {
    try {
      return { ok: true, value: await this.deps.client.list() };
    } catch (err) {
      return { ok: false, kind: classifyPasskeyError(err) };
    }
  }

  /**
   * Adds a passkey: re-authenticate and get the options from the server,
   * let the browser create the credential, hand it back to the server.
   */
  async register(listing: PasskeyListing, name: string, input: ReauthInput, fallbackName: string): Promise<Outcome<PasskeyEntry>> {
    const n = normalizeName(name, fallbackName);
    if (!n.ok) return { ok: false, kind: 'invalid' };
    if (!this.supported || !this.deps.credentials) return { ok: false, kind: 'unsupported' };
    if (listing.passkeys.length >= listing.max) return { ok: false, kind: 'limit' };
    const r = buildReauth(listing, input);
    if (!r.ok) return r;
    try {
      const begin = await this.deps.client.beginRegistration(r.reauth);
      const cred = await this.deps.credentials.create({ publicKey: creationOptionsFromJSON(begin.publicKey) });
      if (!cred) return { ok: false, kind: 'cancelled' };
      const passkey = await this.deps.client.finishRegistration({
        challengeId: begin.challengeId,
        name: n.name,
        credential: attestationToJSON(cred as PublicKeyCredential),
      });
      return { ok: true, value: passkey };
    } catch (err) {
      return { ok: false, kind: classifyPasskeyError(err) };
    }
  }

  async rename(id: number, name: string): Promise<Outcome> {
    const n = normalizeName(name, '');
    if (!n.ok || !n.name) return { ok: false, kind: 'invalid' };
    try {
      await this.deps.client.rename(id, n.name);
      return { ok: true, value: undefined };
    } catch (err) {
      return { ok: false, kind: classifyPasskeyError(err) };
    }
  }

  async remove(listing: PasskeyListing, id: number, input: ReauthInput): Promise<Outcome> {
    const r = buildReauth(listing, input);
    if (!r.ok) return r;
    try {
      await this.deps.client.remove(id, r.reauth);
      return { ok: true, value: undefined };
    } catch (err) {
      return { ok: false, kind: classifyPasskeyError(err) };
    }
  }
}

