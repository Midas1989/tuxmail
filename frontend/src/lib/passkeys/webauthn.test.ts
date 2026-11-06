// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import {
  assertionToJSON,
  attestationToJSON,
  b64urlDecode,
  b64urlEncode,
  classifyPasskeyError,
  creationOptionsFromJSON,
  passkeyLoginAvailable,
  passkeysSupported,
  requestOptionsFromJSON,
  type CreationOptionsJSON,
  type WebAuthnEnv,
} from './webauthn';
import { B64_VECTORS } from './vectors';

const buf = (...n: number[]) => new Uint8Array(n).buffer;

describe('base64url', () => {
  it.each(B64_VECTORS)('encodes and decodes %j', (bytes, text) => {
    expect(b64urlEncode(new Uint8Array(bytes))).toBe(text);
    expect([...b64urlDecode(text)]).toEqual(bytes);
  });

  it('accepts padding and round-trips every byte value, also from views with an offset', () => {
    expect([...b64urlDecode('AQIDBA==')]).toEqual([1, 2, 3, 4]);
    const all = Uint8Array.from({ length: 256 }, (_, i) => i);
    expect([...b64urlDecode(b64urlEncode(all))]).toEqual([...all]);
    const view = new Uint8Array([9, 9, 1, 2, 3, 9]).subarray(2, 5);
    expect(b64urlEncode(view)).toBe('AQID');
    expect(b64urlEncode(view.buffer)).not.toBe('AQID'); // the whole buffer, not the view
    expect(b64urlEncode(all.buffer)).not.toMatch(/[+/=]/);
  });

  it('rejects characters outside the alphabet and impossible lengths', () => {
    for (const bad of ['a+b', 'a/b', 'a b', 'ab!c', 'A', 'AAAAA', 'a=b']) expect(() => b64urlDecode(bad), bad).toThrow();
  });
});

describe('server options -> browser options', () => {
  const creation: CreationOptionsJSON = {
    rp: { id: 'mail.example.org', name: 'TuxMail' },
    user: { id: 'AQID', name: 'anna', displayName: 'Anna' },
    challenge: '-__-',
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
    timeout: 300000,
    excludeCredentials: [{ type: 'public-key', id: 'BAUG', transports: ['internal'] }, { type: 'public-key', id: 'BwgJ' }],
    authenticatorSelection: { residentKey: 'required', requireResidentKey: true, userVerification: 'required' },
    attestation: 'none',
  };

  it('decodes challenge, user id and credential ids', () => {
    const o = creationOptionsFromJSON(creation);
    expect([...(o.challenge as Uint8Array)]).toEqual([0xfb, 0xff, 0xfe]);
    expect([...(o.user.id as Uint8Array)]).toEqual([1, 2, 3]);
    expect(o.excludeCredentials).toHaveLength(2);
    expect([...(o.excludeCredentials![0].id as Uint8Array)]).toEqual([4, 5, 6]);
    expect(o.excludeCredentials![0].transports).toEqual(['internal']);
    expect(o.excludeCredentials![1]).not.toHaveProperty('transports');
    expect(o.rp.id).toBe('mail.example.org');
    expect(o.authenticatorSelection).toMatchObject({ residentKey: 'required', userVerification: 'required' });
    expect(o.attestation).toBe('none');
    expect(o.timeout).toBe(300000);
  });

  it('request options default to required user verification and an empty allow list', () => {
    const o = requestOptionsFromJSON({ challenge: 'AQID', rpId: 'mail.example.org' });
    expect([...(o.challenge as Uint8Array)]).toEqual([1, 2, 3]);
    expect(o.userVerification).toBe('required');
    expect(o.allowCredentials).toEqual([]);
    expect(o.rpId).toBe('mail.example.org');
    expect(requestOptionsFromJSON({ challenge: 'AQID', userVerification: 'preferred' }).userVerification).toBe('preferred');
  });

  it('a malformed challenge throws instead of sending garbage', () => {
    expect(() => requestOptionsFromJSON({ challenge: '***' })).toThrow();
  });
});

describe('browser credentials -> server JSON', () => {
  const extras = { id: 'AQID', rawId: buf(1, 2, 3), type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}) };

  it('attestation', () => {
    const cred = {
      ...extras,
      response: { clientDataJSON: buf(1), attestationObject: buf(0xfb, 0xff, 0xfe), getTransports: () => ['internal', 'hybrid'] },
    } as unknown as PublicKeyCredential;
    expect(attestationToJSON(cred)).toEqual({
      id: 'AQID',
      rawId: 'AQID',
      type: 'public-key',
      authenticatorAttachment: 'platform',
      clientExtensionResults: {},
      response: { clientDataJSON: 'AQ', attestationObject: '-__-', transports: ['internal', 'hybrid'] },
    });
    const bare = { ...extras, authenticatorAttachment: null, getClientExtensionResults: undefined, response: { clientDataJSON: buf(1), attestationObject: buf(2) } } as unknown as PublicKeyCredential;
    const j = attestationToJSON(bare);
    expect(j).not.toHaveProperty('authenticatorAttachment');
    expect(j.response).not.toHaveProperty('transports');
    expect(j.clientExtensionResults).toEqual({});
  });

  it('assertion, with and without a user handle', () => {
    const response = { clientDataJSON: buf(1), authenticatorData: buf(2, 3), signature: buf(0xff, 0xff), userHandle: buf(7, 8) };
    const j = assertionToJSON({ ...extras, response } as unknown as PublicKeyCredential);
    expect(j.response).toEqual({ clientDataJSON: 'AQ', authenticatorData: 'AgM', signature: '__8', userHandle: 'Bwg' });
    expect(j.rawId).toBe('AQID');
    const none = assertionToJSON({ ...extras, response: { ...response, userHandle: null } } as unknown as PublicKeyCredential);
    expect(none.response).not.toHaveProperty('userHandle');
    const empty = assertionToJSON({ ...extras, response: { ...response, userHandle: new ArrayBuffer(0) } } as unknown as PublicKeyCredential);
    expect(empty.response).not.toHaveProperty('userHandle');
  });
});

describe('feature detection', () => {
  const ok: WebAuthnEnv = { PublicKeyCredential: class {}, isSecureContext: true, navigator: { credentials: { create: async () => null, get: async () => null } } };

  it('needs the API, a secure context and create/get', () => {
    expect(passkeysSupported(ok)).toBe(true);
    expect(passkeysSupported({ ...ok, PublicKeyCredential: undefined })).toBe(false);
    expect(passkeysSupported({ ...ok, isSecureContext: false })).toBe(false);
    expect(passkeysSupported({ ...ok, navigator: {} })).toBe(false);
    expect(passkeysSupported({ ...ok, navigator: { credentials: { get: async () => null } } })).toBe(false);
    expect(passkeysSupported({})).toBe(false);
    expect(passkeysSupported(undefined)).toBe(false); // no window (node)
  });

  it('the login button needs browser support AND passkeysEnabled from the server', () => {
    expect(passkeyLoginAvailable(ok, { passkeysEnabled: true })).toBe(true);
    expect(passkeyLoginAvailable(ok, { passkeysEnabled: false })).toBe(false);
    expect(passkeyLoginAvailable(ok, {})).toBe(false);
    expect(passkeyLoginAvailable(ok, null)).toBe(false);
    expect(passkeyLoginAvailable(ok, { passkeysEnabled: 'true' })).toBe(false); // exactly true
    expect(passkeyLoginAvailable({}, { passkeysEnabled: true })).toBe(false);
  });
});

describe('error classification', () => {
  const dom = (name: string) => Object.assign(new Error(name), { name });
  const api = (code: string, status = 400) => Object.assign(new Error(code), { code, status });

  it.each([
    [dom('NotAllowedError'), 'cancelled'],
    [dom('AbortError'), 'cancelled'],
    [dom('InvalidStateError'), 'duplicate'],
    [dom('NotSupportedError'), 'unsupported'],
    [dom('SecurityError'), 'insecure'],
    [api('invalid_credentials', 401), 'reauth'],
    [api('invalid_code'), 'badCode'],
    [api('reauth_required', 403), 'reauthRequired'],
    [api('throttled', 429), 'rateLimited'],
    [api('rate_limited', 429), 'rateLimited'],
    [api('http_429', 429), 'rateLimited'],
    [api('passkey_limit', 409), 'limit'],
    [api('mfa_setup_required', 403), 'mfaSetup'],
    [api('passkeys_disabled', 404), 'disabled'],
    [api('passkey_challenge'), 'expired'],
    [api('passkey_invalid'), 'invalid'],
    [api('exists', 409), 'duplicate'],
    [api('forbidden', 403), 'forbidden'],
    [api('network', 0), 'network'],
    [new TypeError('Failed to fetch'), 'network'],
    [api('internal', 500), 'failed'],
    ['text', 'failed'],
    [null, 'failed'],
    [undefined, 'failed'],
  ])('%s -> %s', (err, kind) => {
    expect(classifyPasskeyError(err)).toBe(kind);
  });
});
