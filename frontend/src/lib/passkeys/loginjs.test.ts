// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The server-rendered login page (internal/server/pages.go) loads
// internal/server/passkey_login.js. Run that file in a sandbox: its base64url
// helpers must agree with lib/passkeys/webauthn.ts, and the button must only be
// offered with browser support AND passkeysEnabled from the server.

import { describe, expect, it, vi } from 'vitest';
import { assertionToJSON, b64urlEncode, passkeyLoginAvailable, requestOptionsFromJSON, type WebAuthnEnv } from './webauthn';
import { B64_VECTORS } from './vectors';

const files = import.meta.glob('../../../../internal/server/passkey_login.js', { query: '?raw', import: 'default', eager: true }) as Record<string, string>;
const src = Object.values(files)[0];

interface LoginJs {
  pkB64urlToBytes(s: string): Uint8Array;
  pkBytesToB64url(b: ArrayBuffer | Uint8Array): string;
  pkRequestOptions(o: unknown): PublicKeyCredentialRequestOptions;
  pkAssertionJSON(c: unknown): unknown;
  pkSupported(env: unknown): boolean;
  pkShowButton(env: unknown, config: unknown): boolean;
  pkErrorMessage(err: unknown): string;
  pkSignIn(post: (path: string, body: unknown) => Promise<{ status: number; body: Record<string, unknown> }>, nav: unknown): Promise<Record<string, unknown>>;
}

function load(): LoginJs {
  const msg = (j: { message?: string; error?: string }) => j.message || j.error || 'Error';
  const names = ['pkB64urlToBytes', 'pkBytesToB64url', 'pkRequestOptions', 'pkAssertionJSON', 'pkSupported', 'pkShowButton', 'pkErrorMessage', 'pkSignIn'];
  return new Function('atob', 'btoa', 'msg', `${src}\nreturn { ${names.join(', ')} };`)(atob, btoa, msg) as LoginJs;
}

const js = load();
const ok: WebAuthnEnv = { PublicKeyCredential: class {}, isSecureContext: true, navigator: { credentials: { create: async () => null, get: async () => null } } };
const buf = (...n: number[]) => new Uint8Array(n).buffer;

describe('login page script: encoding', () => {
  it.each(B64_VECTORS)('agrees with the typed helpers on %j', (bytes, text) => {
    expect(js.pkBytesToB64url(new Uint8Array(bytes))).toBe(text);
    expect([...js.pkB64urlToBytes(text)]).toEqual(bytes);
    expect(b64urlEncode(new Uint8Array(bytes))).toBe(text);
  });

  it('request options and assertion JSON equal the typed versions', () => {
    const server = { challenge: '-__-', rpId: 'mail.example.org', timeout: 300000, userVerification: 'required' as const, allowCredentials: [] };
    const a = js.pkRequestOptions(server);
    const b = requestOptionsFromJSON(server);
    expect([...(a.challenge as Uint8Array)]).toEqual([...(b.challenge as Uint8Array)]);
    expect(a).toMatchObject({ rpId: 'mail.example.org', timeout: 300000, userVerification: 'required', allowCredentials: [] });

    const cred = {
      id: 'AQID',
      rawId: buf(1, 2, 3),
      type: 'public-key',
      authenticatorAttachment: 'platform',
      getClientExtensionResults: () => ({}),
      response: { clientDataJSON: buf(1), authenticatorData: buf(2, 3), signature: buf(0xff, 0xff), userHandle: buf(7, 8) },
    };
    expect(js.pkAssertionJSON(cred)).toEqual(assertionToJSON(cred as unknown as PublicKeyCredential));
    const noHandle = { ...cred, response: { ...cred.response, userHandle: null }, authenticatorAttachment: undefined };
    expect(js.pkAssertionJSON(noHandle)).toEqual(assertionToJSON(noHandle as unknown as PublicKeyCredential));
  });
});

describe('login page script: button visibility', () => {
  it('is shown only with browser support and passkeysEnabled', () => {
    for (const [env, config] of [
      [ok, { passkeysEnabled: true }],
      [ok, { passkeysEnabled: false }],
      [ok, {}],
      [ok, null],
      [ok, { passkeysEnabled: 'yes' }],
      [{ ...ok, PublicKeyCredential: undefined }, { passkeysEnabled: true }],
      [{ ...ok, isSecureContext: false }, { passkeysEnabled: true }],
      [{ ...ok, navigator: {} }, { passkeysEnabled: true }],
      [{}, { passkeysEnabled: true }],
    ] as [WebAuthnEnv, unknown][]) {
      expect(js.pkShowButton(env, config)).toBe(passkeyLoginAvailable(env, config as never));
    }
    expect(js.pkShowButton(ok, { passkeysEnabled: true })).toBe(true);
    expect(js.pkShowButton({ ...ok, PublicKeyCredential: undefined }, { passkeysEnabled: true })).toBe(false);
    expect(js.pkShowButton(ok, { passkeysEnabled: false })).toBe(false);
  });
});

describe('login page script: sign-in flow', () => {
  const assertion = { id: 'AQID', rawId: buf(1, 2, 3), type: 'public-key', response: { clientDataJSON: buf(1), authenticatorData: buf(2), signature: buf(3), userHandle: buf(4) }, getClientExtensionResults: () => ({}) };
  const begin = { status: 200, body: { challengeId: 'cid', publicKey: { challenge: 'AQID', rpId: 'mail.example.org', userVerification: 'required' } } };

  it('begin -> browser dialog -> finish', async () => {
    const post = vi.fn(async (path: string, _body?: unknown) => (path.endsWith('/begin') ? begin : { status: 200, body: { status: 'ok' } }));
    const get = vi.fn(async () => assertion);
    const r = await js.pkSignIn(post, { credentials: { get } });
    expect(r).toEqual({ status: 'ok' });
    expect(post.mock.calls.map((c) => c[0])).toEqual(['/api/auth/passkeys/login/begin', '/api/auth/passkeys/login/finish']);
    const finish = post.mock.calls[1][1] as { challengeId: string; credential: { rawId: string; response: { userHandle: string } } };
    expect(finish.challengeId).toBe('cid');
    expect(finish.credential.rawId).toBe('AQID');
    expect(finish.credential.response.userHandle).toBe('BA');
    const arg = (get.mock.calls[0] as unknown[])[0] as { publicKey: PublicKeyCredentialRequestOptions };
    expect([...(arg.publicKey.challenge as Uint8Array)]).toEqual([1, 2, 3]);
  });

  it('server and browser failures end in readable messages', async () => {
    const failBegin = vi.fn(async () => ({ status: 429, body: { error: 'rate_limited', message: 'too many login attempts' } }));
    const e1 = await js.pkSignIn(failBegin, {}).catch((e: unknown) => e);
    expect(js.pkErrorMessage(e1)).toMatch(/Too many attempts/);
    const e2 = await js.pkSignIn(async () => begin, { credentials: { get: async () => Promise.reject(Object.assign(new Error('x'), { name: 'NotAllowedError' })) } }).catch((e: unknown) => e);
    expect(js.pkErrorMessage(e2)).toMatch(/cancelled/);
    const e3 = await js.pkSignIn(async () => begin, { credentials: { get: async () => null } }).catch((e: unknown) => e);
    expect(js.pkErrorMessage(e3)).toMatch(/cancelled/);
    const post = vi.fn(async (p: string) => (p.endsWith('/begin') ? begin : { status: 401, body: { error: 'invalid_credentials', message: 'passkey sign-in failed' } }));
    const e4 = await js.pkSignIn(post, { credentials: { get: async () => assertion } }).catch((e: unknown) => e);
    expect(js.pkErrorMessage(e4)).toBe('passkey sign-in failed');
    expect(js.pkErrorMessage(Object.assign(new Error('x'), { name: 'SecurityError' }))).toMatch(/do not work on this address/);
    expect(js.pkErrorMessage({})).toMatch(/failed/);
  });
});
