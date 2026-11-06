// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/api';
import type { PasskeyClient, PasskeyEntry, PasskeyListing } from './client';
import { buildReauth, normalizeName, PasskeyManager, MAX_PASSKEY_NAME } from './manager';
import type { CreationOptionsJSON, WebAuthnEnv } from './webauthn';

const env: WebAuthnEnv = { PublicKeyCredential: class {}, isSecureContext: true, navigator: { credentials: { create: async () => null, get: async () => null } } };

const entry = (id: number, name = 'k'): PasskeyEntry => ({ id, name, createdAt: '2026-01-01T00:00:00Z', transports: [], backupEligible: true, backupState: false, usable: true });
const listing = (n = 0, reauth = { password: true, code: false }): PasskeyListing => ({ passkeys: Array.from({ length: n }, (_, i) => entry(i + 1)), enabled: true, max: 10, reauth });

const options: CreationOptionsJSON = {
  rp: { id: 'mail.example.org', name: 'TuxMail' },
  user: { id: 'AQID', name: 'anna', displayName: 'anna' },
  challenge: 'AQID',
  pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
};

const fakeCredential = { id: 'AQID', rawId: new Uint8Array([1, 2, 3]).buffer, type: 'public-key', response: { clientDataJSON: new Uint8Array([1]).buffer, attestationObject: new Uint8Array([2]).buffer, getTransports: () => ['internal'] }, getClientExtensionResults: () => ({}) };

function setup(over: Partial<PasskeyClient> = {}, create: (o: CredentialCreationOptions) => Promise<unknown> = async () => fakeCredential) {
  const client: PasskeyClient = {
    list: vi.fn(async () => listing()),
    beginRegistration: vi.fn(async () => ({ challengeId: 'cid', publicKey: options })),
    finishRegistration: vi.fn(async (i) => entry(5, i.name)),
    rename: vi.fn(async () => undefined),
    remove: vi.fn(async () => undefined),
    ...over,
  };
  const createFn = vi.fn(create);
  const manager = new PasskeyManager({ client, credentials: { create: createFn as unknown as CredentialsContainer['create'] }, env });
  return { client, createFn, manager };
}

describe('names', () => {
  it('trims, defaults and limits', () => {
    expect(normalizeName('  Work  ', 'Passkey')).toEqual({ ok: true, name: 'Work' });
    expect(normalizeName('   ', 'Passkey')).toEqual({ ok: true, name: 'Passkey' });
    expect(normalizeName('ä'.repeat(MAX_PASSKEY_NAME), 'x')).toMatchObject({ ok: true });
    expect(normalizeName('ä'.repeat(MAX_PASSKEY_NAME + 1), 'x')).toEqual({ ok: false });
    expect(normalizeName('a\nb', 'x')).toEqual({ ok: false });
    expect(normalizeName('tab\there', 'x')).toEqual({ ok: false });
  });
});

describe('re-authentication body', () => {
  it('sends only what the server asks for and does not hit the server with missing proofs', () => {
    expect(buildReauth(listing(0, { password: true, code: false }), { password: 'pw', code: '123456' })).toEqual({ ok: true, reauth: { password: 'pw' } });
    expect(buildReauth(listing(0, { password: true, code: false }), { password: '', code: '' })).toEqual({ ok: false, kind: 'reauth' });
    expect(buildReauth(listing(0, { password: true, code: true }), { password: 'pw', code: '' })).toEqual({ ok: false, kind: 'badCode' });
    expect(buildReauth(listing(0, { password: true, code: true }), { password: 'pw', code: '123 456' })).toEqual({ ok: true, reauth: { password: 'pw', code: '123456' } });
    expect(buildReauth(listing(0, { password: true, code: true }), { password: 'pw', code: 'abcde-23456' })).toEqual({ ok: true, reauth: { password: 'pw', recoveryCode: 'abcde-23456' } });
    // accounts without a local password and without TOTP: nothing to type
    expect(buildReauth(listing(0, { password: false, code: false }), { password: '', code: '' })).toEqual({ ok: true, reauth: {} });
  });
});

describe('register', () => {
  it('re-authenticates, lets the browser create the credential and finishes with the server', async () => {
    const { manager, client, createFn } = setup();
    const r = await manager.register(listing(), '  Laptop ', { password: 'pw', code: '' }, 'Passkey');
    expect(r).toMatchObject({ ok: true, value: { name: 'Laptop' } });
    expect(client.beginRegistration).toHaveBeenCalledWith({ password: 'pw' });
    const arg = createFn.mock.calls[0][0] as CredentialCreationOptions;
    expect([...(arg.publicKey!.challenge as Uint8Array)]).toEqual([1, 2, 3]); // decoded
    expect(client.finishRegistration).toHaveBeenCalledWith({
      challengeId: 'cid',
      name: 'Laptop',
      credential: expect.objectContaining({ id: 'AQID', rawId: 'AQID', response: expect.objectContaining({ clientDataJSON: 'AQ', attestationObject: 'Ag', transports: ['internal'] }) }),
    });
  });

  it('uses the fallback name for an empty one', async () => {
    const { manager, client } = setup();
    await manager.register(listing(), '', { password: 'pw', code: '' }, 'Passkey');
    expect(client.finishRegistration).toHaveBeenCalledWith(expect.objectContaining({ name: 'Passkey' }));
  });

  it('a dismissed browser dialog is "cancelled" and nothing is stored', async () => {
    for (const create of [async () => null, async () => Promise.reject(Object.assign(new Error('x'), { name: 'NotAllowedError' }))]) {
      const { manager, client } = setup({}, create);
      expect(await manager.register(listing(), 'a', { password: 'pw', code: '' }, 'P')).toEqual({ ok: false, kind: 'cancelled' });
      expect(client.finishRegistration).not.toHaveBeenCalled();
    }
  });

  it('an authenticator that already has a passkey is "duplicate"', async () => {
    const { manager } = setup({}, async () => Promise.reject(Object.assign(new Error('x'), { name: 'InvalidStateError' })));
    expect(await manager.register(listing(), 'a', { password: 'pw', code: '' }, 'P')).toEqual({ ok: false, kind: 'duplicate' });
  });

  it('missing password or code never reaches the server or the browser', async () => {
    const { manager, client, createFn } = setup();
    expect(await manager.register(listing(), 'a', { password: '', code: '' }, 'P')).toEqual({ ok: false, kind: 'reauth' });
    expect(await manager.register(listing(0, { password: true, code: true }), 'a', { password: 'pw', code: '' }, 'P')).toEqual({ ok: false, kind: 'badCode' });
    expect(client.beginRegistration).not.toHaveBeenCalled();
    expect(createFn).not.toHaveBeenCalled();
  });

  it('maps server errors: wrong password, throttling, limit, expired ceremony', async () => {
    for (const [code, status, kind] of [
      ['invalid_credentials', 401, 'reauth'],
      ['throttled', 429, 'rateLimited'],
      ['passkey_limit', 409, 'limit'],
      ['mfa_setup_required', 403, 'mfaSetup'],
      ['passkeys_disabled', 404, 'disabled'],
    ] as const) {
      const { manager, createFn } = setup({ beginRegistration: async () => Promise.reject(new ApiError('x', status, code)) });
      expect(await manager.register(listing(), 'a', { password: 'pw', code: '' }, 'P'), code).toEqual({ ok: false, kind });
      expect(createFn).not.toHaveBeenCalled();
    }
    const { manager } = setup({ finishRegistration: async () => Promise.reject(new ApiError('x', 400, 'passkey_challenge')) });
    expect(await manager.register(listing(), 'a', { password: 'pw', code: '' }, 'P')).toEqual({ ok: false, kind: 'expired' });
  });

  it('respects the limit, an unsupported browser and bad names without a request', async () => {
    const { manager, client } = setup();
    expect(await manager.register(listing(10), 'a', { password: 'pw', code: '' }, 'P')).toEqual({ ok: false, kind: 'limit' });
    expect(await manager.register(listing(), 'x'.repeat(65), { password: 'pw', code: '' }, 'P')).toEqual({ ok: false, kind: 'invalid' });
    const noBrowser = new PasskeyManager({ client, credentials: undefined, env: {} });
    expect(noBrowser.supported).toBe(false);
    expect(await noBrowser.register(listing(), 'a', { password: 'pw', code: '' }, 'P')).toEqual({ ok: false, kind: 'unsupported' });
    expect(client.beginRegistration).not.toHaveBeenCalled();
  });
});

describe('load, rename, remove', () => {
  it('load returns the listing or a classified error', async () => {
    expect(await setup().manager.load()).toMatchObject({ ok: true, value: { enabled: true, max: 10 } });
    const down = setup({ list: async () => Promise.reject(new ApiError('x', 0, 'network')) });
    expect(await down.manager.load()).toEqual({ ok: false, kind: 'network' });
  });

  it('rename validates and reports', async () => {
    const { manager, client } = setup();
    expect(await manager.rename(3, '  New  ')).toEqual({ ok: true, value: undefined });
    expect(client.rename).toHaveBeenCalledWith(3, 'New');
    expect(await manager.rename(3, '   ')).toEqual({ ok: false, kind: 'invalid' });
    expect(await manager.rename(3, 'x'.repeat(65))).toEqual({ ok: false, kind: 'invalid' });
    expect(client.rename).toHaveBeenCalledTimes(1);
    const missing = setup({ rename: async () => Promise.reject(new ApiError('x', 404, 'not_found')) });
    expect(await missing.manager.rename(9, 'a')).toEqual({ ok: false, kind: 'failed' });
  });

  it('remove needs the proofs the server asks for', async () => {
    const { manager, client } = setup();
    expect(await manager.remove(listing(1), 1, { password: '', code: '' })).toEqual({ ok: false, kind: 'reauth' });
    expect(client.remove).not.toHaveBeenCalled();
    expect(await manager.remove(listing(1, { password: true, code: true }), 1, { password: 'pw', code: '123456' })).toEqual({ ok: true, value: undefined });
    expect(client.remove).toHaveBeenCalledWith(1, { password: 'pw', code: '123456' });
    const wrong = setup({ remove: async () => Promise.reject(new ApiError('x', 401, 'invalid_credentials')) });
    expect(await wrong.manager.remove(listing(1), 1, { password: 'nope', code: '' })).toEqual({ ok: false, kind: 'reauth' });
    const locked = setup({ remove: async () => Promise.reject(new ApiError('x', 429, 'throttled')) });
    expect(await locked.manager.remove(listing(1), 1, { password: 'pw', code: '' })).toEqual({ ok: false, kind: 'rateLimited' });
  });
});
