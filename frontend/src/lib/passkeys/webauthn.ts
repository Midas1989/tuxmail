// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WebAuthn helpers for passkeys (server mode, internal/server/api_passkeys.go):
// base64url handling, conversion between the server's JSON options and the
// binary options of navigator.credentials, conversion of the browser's
// credential back to JSON, feature detection and error classification. No DOM
// access besides the objects handed in, so it is unit-testable in node.
//
// The server-rendered login page (internal/server/passkey_login.js) has its own
// plain-JS copy of the encoding helpers; both are tested with the same vectors.

// ---------------------------------------------------------------- base64url

/** Unpadded base64url (RFC 4648 section 5). */
export function b64urlEncode(data: ArrayBuffer | ArrayBufferView): string {
  const bytes = ArrayBuffer.isView(data) ? new Uint8Array(data.buffer, data.byteOffset, data.byteLength) : new Uint8Array(data);
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Decodes base64url with or without padding; throws on characters outside the alphabet. */
export function b64urlDecode(s: string): Uint8Array<ArrayBuffer> {
  if (!/^[A-Za-z0-9_-]*={0,2}$/.test(s)) throw new Error('invalid base64url');
  let b64 = s.replace(/=+$/, '').replace(/-/g, '+').replace(/_/g, '/');
  if (b64.length % 4 === 1) throw new Error('invalid base64url length');
  while (b64.length % 4) b64 += '=';
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// ------------------------------------------------------------ server options

interface DescriptorJSON {
  type: PublicKeyCredentialType;
  id: string;
  transports?: AuthenticatorTransport[];
}

/** PublicKeyCredentialCreationOptions as sent by the server (binary fields are base64url). */
export interface CreationOptionsJSON {
  rp: PublicKeyCredentialRpEntity;
  user: { id: string; name: string; displayName: string };
  challenge: string;
  pubKeyCredParams: PublicKeyCredentialParameters[];
  timeout?: number;
  excludeCredentials?: DescriptorJSON[];
  authenticatorSelection?: AuthenticatorSelectionCriteria;
  attestation?: AttestationConveyancePreference;
  hints?: string[];
}

/** PublicKeyCredentialRequestOptions as sent by the server. */
export interface RequestOptionsJSON {
  challenge: string;
  timeout?: number;
  rpId?: string;
  allowCredentials?: DescriptorJSON[];
  userVerification?: UserVerificationRequirement;
}

function descriptor(d: DescriptorJSON): PublicKeyCredentialDescriptor {
  return { type: d.type, id: b64urlDecode(d.id), ...(d.transports?.length ? { transports: d.transports } : {}) };
}

export function creationOptionsFromJSON(o: CreationOptionsJSON): PublicKeyCredentialCreationOptions {
  return {
    rp: o.rp,
    user: { id: b64urlDecode(o.user.id), name: o.user.name, displayName: o.user.displayName },
    challenge: b64urlDecode(o.challenge),
    pubKeyCredParams: o.pubKeyCredParams,
    ...(o.timeout ? { timeout: o.timeout } : {}),
    excludeCredentials: (o.excludeCredentials ?? []).map(descriptor),
    ...(o.authenticatorSelection ? { authenticatorSelection: o.authenticatorSelection } : {}),
    ...(o.attestation ? { attestation: o.attestation } : {}),
  };
}

export function requestOptionsFromJSON(o: RequestOptionsJSON): PublicKeyCredentialRequestOptions {
  return {
    challenge: b64urlDecode(o.challenge),
    ...(o.timeout ? { timeout: o.timeout } : {}),
    ...(o.rpId ? { rpId: o.rpId } : {}),
    allowCredentials: (o.allowCredentials ?? []).map(descriptor),
    userVerification: o.userVerification ?? 'required',
  };
}

// ---------------------------------------------------------- browser answers

export interface AttestationJSON {
  id: string;
  rawId: string;
  type: string;
  authenticatorAttachment?: string;
  clientExtensionResults: AuthenticationExtensionsClientOutputs;
  response: { clientDataJSON: string; attestationObject: string; transports?: string[] };
}

export interface AssertionJSON {
  id: string;
  rawId: string;
  type: string;
  authenticatorAttachment?: string;
  clientExtensionResults: AuthenticationExtensionsClientOutputs;
  response: { clientDataJSON: string; authenticatorData: string; signature: string; userHandle?: string };
}

/** The credential from navigator.credentials.create() in the server's JSON form. */
export function attestationToJSON(cred: PublicKeyCredential): AttestationJSON {
  const r = cred.response as AuthenticatorAttestationResponse;
  const transports = typeof r.getTransports === 'function' ? r.getTransports() : [];
  return {
    id: cred.id,
    rawId: b64urlEncode(cred.rawId),
    type: cred.type,
    ...(cred.authenticatorAttachment ? { authenticatorAttachment: cred.authenticatorAttachment } : {}),
    clientExtensionResults: typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
    response: {
      clientDataJSON: b64urlEncode(r.clientDataJSON),
      attestationObject: b64urlEncode(r.attestationObject),
      ...(transports.length ? { transports } : {}),
    },
  };
}

/** The credential from navigator.credentials.get() in the server's JSON form. */
export function assertionToJSON(cred: PublicKeyCredential): AssertionJSON {
  const r = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: b64urlEncode(cred.rawId),
    type: cred.type,
    ...(cred.authenticatorAttachment ? { authenticatorAttachment: cred.authenticatorAttachment } : {}),
    clientExtensionResults: typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
    response: {
      clientDataJSON: b64urlEncode(r.clientDataJSON),
      authenticatorData: b64urlEncode(r.authenticatorData),
      signature: b64urlEncode(r.signature),
      ...(r.userHandle && r.userHandle.byteLength ? { userHandle: b64urlEncode(r.userHandle) } : {}),
    },
  };
}

// -------------------------------------------------------- feature detection

/** The pieces of the browser environment the helpers look at (injectable for tests). */
export interface WebAuthnEnv {
  PublicKeyCredential?: unknown;
  isSecureContext?: boolean;
  navigator?: { credentials?: Partial<Pick<CredentialsContainer, 'create' | 'get'>> };
}

/** WebAuthn is usable: the API exists and the page is a secure context. */
export function passkeysSupported(env: WebAuthnEnv | undefined = typeof window === 'undefined' ? undefined : (window as unknown as WebAuthnEnv)): boolean {
  if (!env?.PublicKeyCredential || env.isSecureContext === false) return false;
  const c = env.navigator?.credentials;
  return typeof c?.create === 'function' && typeof c?.get === 'function';
}

/** Whether the "Sign in with a passkey" button is offered: browser support AND the server announces passkeys. */
export function passkeyLoginAvailable(env: WebAuthnEnv | undefined, config: { passkeysEnabled?: unknown } | null | undefined): boolean {
  return passkeysSupported(env) && config?.passkeysEnabled === true;
}

// --------------------------------------------------------- error handling

export type PasskeyErrorKind =
  | 'cancelled' // the user dismissed the browser dialog, or it timed out
  | 'unsupported' // no WebAuthn / no suitable authenticator
  | 'insecure' // not a secure context or wrong domain (SecurityError)
  | 'duplicate' // this authenticator already holds a passkey for the account
  | 'reauth' // password / code wrong or missing
  | 'badCode' // the two-factor code was wrong
  | 'reauthRequired' // sign in again first (accounts without a password)
  | 'rateLimited' // too many attempts / step-up locked
  | 'limit' // maximum number of passkeys reached
  | 'mfaSetup' // the admin must set up two-factor authentication first
  | 'disabled' // passkeys are switched off on the server
  | 'expired' // the ceremony timed out or was used already
  | 'invalid' // the server rejected the answer
  | 'forbidden' // not allowed from this session (device token)
  | 'network'
  | 'failed';

interface ErrorLike {
  name?: unknown;
  code?: unknown;
  status?: unknown;
}

/** Maps a DOMException from navigator.credentials or an ApiError of the server to a user-facing kind. */
export function classifyPasskeyError(err: unknown): PasskeyErrorKind {
  const e = (typeof err === 'object' && err !== null ? err : {}) as ErrorLike;
  switch (e.name) {
    case 'NotAllowedError':
    case 'AbortError':
      return 'cancelled';
    case 'InvalidStateError':
      return 'duplicate';
    case 'NotSupportedError':
      return 'unsupported';
    case 'SecurityError':
      return 'insecure';
  }
  const status = typeof e.status === 'number' ? e.status : 0;
  switch (e.code) {
    case 'invalid_credentials':
      return 'reauth';
    case 'invalid_code':
      return 'badCode';
    case 'reauth_required':
      return 'reauthRequired';
    case 'throttled':
    case 'rate_limited':
      return 'rateLimited';
    case 'passkey_limit':
      return 'limit';
    case 'mfa_setup_required':
      return 'mfaSetup';
    case 'passkeys_disabled':
      return 'disabled';
    case 'passkey_challenge':
      return 'expired';
    case 'passkey_invalid':
      return 'invalid';
    case 'exists':
      return 'duplicate';
    case 'forbidden':
      return 'forbidden';
    case 'network':
      return 'network';
  }
  if (status === 429) return 'rateLimited';
  if (status === 0 && e.code === undefined && err instanceof TypeError) return 'network';
  return 'failed';
}
