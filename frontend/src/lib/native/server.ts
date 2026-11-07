// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Talking to a TuxMail server before a session exists (KONZEPT 15.8):
// server validation and the two login variants of internal/server/api_device.go
//   GET  /healthz                -> {status:"ok", product:"tuxmail", version?}
//   GET  /api/auth/config        -> {product:"tuxmail", version?, registration, oidcEnabled, ...}
//   (the version is only sent when the operator set security.expose_version)
//   POST /api/device/authorize   {client_name} -> RFC 8628 grant (snake_case)
//   POST /api/device/token       {device_code} -> token | 400 {error: authorization_pending|slow_down|...}
//   POST /api/device/login       {username, password, totp_code | recovery_code, device_name} -> token | 401 {error: mfa_required}
//   POST /api/device/passkey/begin + /login  passkey sign-in through Credential Manager (passkey.ts)
// Pure functions with an injectable fetch (unit-tested without Capacitor).

import { normalizeServerUrl } from '../platform';
import { PasskeyError, type PasskeyBridge } from './passkey';

export type Fetch = typeof fetch;

export interface ServerInfo {
  serverUrl: string;
  /** Empty when the server does not disclose its version to anonymous clients. */
  version: string;
  oidcEnabled: boolean;
  registration?: string;
  maxUploadMB?: number;
  /** The server lets apps sign in with a passkey directly (assetlinks.json is published). */
  passkeyAppLogin: boolean;
}

export interface DeviceGrant {
  deviceCode: string;
  userCode: string;
  verificationUri: string;
  verificationUriComplete: string;
  /** Polling interval in seconds. */
  interval: number;
  expiresIn: number;
}

export interface TokenResult {
  token: string;
  expiresIn?: number;
  sessionId?: number;
  user?: { id?: number; username?: string; displayName?: string; email?: string };
}

export type ServerErrorCode =
  | 'invalid_url'
  | 'unreachable'
  | 'not_tuxmail'
  | 'unhealthy'
  | 'invalid_credentials'
  | 'mfa_required'
  | 'invalid_code'
  | 'throttled'
  | 'user_disabled'
  | 'access_denied'
  | 'expired_token'
  | 'aborted'
  | 'passkey_cancelled'
  | 'passkey_unavailable'
  | 'passkey_failed'
  | 'server_error';

export class ServerError extends Error {
  constructor(
    readonly code: ServerErrorCode,
    message: string = code,
    readonly status = 0,
  ) {
    super(message);
    this.name = 'ServerError';
  }
}

async function getJson(f: Fetch, url: string, signal?: AbortSignal): Promise<{ status: number; body: Record<string, unknown> | null }> {
  let res: Response;
  try {
    res = await f(url, { headers: { Accept: 'application/json' }, credentials: 'omit', signal });
  } catch (err) {
    if (signal?.aborted) throw new ServerError('aborted');
    throw new ServerError('unreachable', err instanceof Error ? err.message : String(err));
  }
  let body: Record<string, unknown> | null = null;
  try {
    body = (await res.json()) as Record<string, unknown>;
  } catch {
    body = null;
  }
  return { status: res.status, body };
}

async function postJson(f: Fetch, url: string, payload: unknown, signal?: AbortSignal): Promise<{ status: number; body: Record<string, unknown> }> {
  let res: Response;
  try {
    res = await f(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(payload),
      credentials: 'omit',
      signal,
    });
  } catch (err) {
    if (signal?.aborted) throw new ServerError('aborted');
    throw new ServerError('unreachable', err instanceof Error ? err.message : String(err));
  }
  const body = ((await res.json().catch(() => ({}))) ?? {}) as Record<string, unknown>;
  return { status: res.status, body };
}

/**
 * Normalise the URL and check that a TuxMail server answers there:
 * /healthz must report status "ok" and /api/auth/config must be TuxMail's.
 */
export async function validateServer(input: string, f: Fetch = fetch, signal?: AbortSignal): Promise<ServerInfo> {
  const serverUrl = normalizeServerUrl(input);
  if (!serverUrl) throw new ServerError('invalid_url');
  const health = await getJson(f, serverUrl + '/healthz', signal);
  if (!health.body || typeof health.body.status !== 'string') throw new ServerError('not_tuxmail', 'no TuxMail health endpoint', health.status);
  if (health.body.status !== 'ok') throw new ServerError('unhealthy', String(health.body.error ?? 'unhealthy'), health.status);
  const cfg = await getJson(f, serverUrl + '/api/auth/config', signal);
  // Servers recognise themselves by "product" (older ones by a version string).
  if (cfg.status !== 200 || !cfg.body || !(cfg.body.product === 'tuxmail' || typeof cfg.body.version === 'string')) {
    throw new ServerError('not_tuxmail', 'no TuxMail auth config', cfg.status);
  }
  return {
    serverUrl,
    version: typeof cfg.body.version === 'string' ? cfg.body.version : '',
    oidcEnabled: cfg.body.oidcEnabled === true,
    registration: typeof cfg.body.registration === 'string' ? cfg.body.registration : undefined,
    maxUploadMB: typeof cfg.body.maxUploadMB === 'number' ? cfg.body.maxUploadMB : undefined,
    passkeyAppLogin: cfg.body.passkeyAppLogin === true,
  };
}

function toToken(body: Record<string, unknown>): TokenResult {
  const token = body.access_token;
  if (typeof token !== 'string' || !token) throw new ServerError('server_error', 'no access_token in response');
  return {
    token,
    expiresIn: typeof body.expires_in === 'number' ? body.expires_in : undefined,
    sessionId: typeof body.session_id === 'number' ? body.session_id : undefined,
    user: (body.user as TokenResult['user']) ?? undefined,
  };
}

function errorFor(status: number, body: Record<string, unknown>): ServerError {
  const code = String(body.error ?? '');
  const msg = String(body.error_description || body.message || code || 'HTTP ' + status);
  const known: ServerErrorCode[] = ['invalid_credentials', 'mfa_required', 'invalid_code', 'throttled', 'user_disabled', 'access_denied', 'expired_token'];
  if ((known as string[]).includes(code)) return new ServerError(code as ServerErrorCode, msg, status);
  if (code === 'invalid_grant') return new ServerError('expired_token', msg, status);
  return new ServerError('server_error', msg, status);
}

/** The app works on the user's own data; it never provisions other clients (server: internal/auth/devicetokens.go). */
const DEVICE_SCOPE = 'data';

export async function startDeviceAuthorization(serverUrl: string, clientName: string, f: Fetch = fetch, signal?: AbortSignal): Promise<DeviceGrant> {
  const { status, body } = await postJson(f, serverUrl + '/api/device/authorize', { client_name: clientName, scope: DEVICE_SCOPE }, signal);
  if (status !== 200) throw errorFor(status, body);
  return {
    deviceCode: String(body.device_code),
    userCode: String(body.user_code),
    verificationUri: String(body.verification_uri),
    verificationUriComplete: String(body.verification_uri_complete || body.verification_uri),
    interval: typeof body.interval === 'number' && body.interval > 0 ? body.interval : 5,
    expiresIn: typeof body.expires_in === 'number' ? body.expires_in : 600,
  };
}

export interface PollOptions {
  fetch?: Fetch;
  signal?: AbortSignal;
  /** Injected for tests. */
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
}

const defaultSleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener(
      'abort',
      () => {
        clearTimeout(t);
        reject(new ServerError('aborted'));
      },
      { once: true },
    );
  });

/** Poll /api/device/token until approved, denied or expired (RFC 8628 §3.5). */
export async function pollDeviceToken(serverUrl: string, grant: DeviceGrant, opts: PollOptions = {}): Promise<TokenResult> {
  const f = opts.fetch ?? fetch;
  const now = opts.now ?? Date.now;
  const sleep = opts.sleep ?? ((ms: number) => defaultSleep(ms, opts.signal));
  const deadline = now() + grant.expiresIn * 1000;
  let interval = grant.interval;
  for (;;) {
    if (opts.signal?.aborted) throw new ServerError('aborted');
    await sleep(interval * 1000);
    if (now() > deadline) throw new ServerError('expired_token');
    const { status, body } = await postJson(f, serverUrl + '/api/device/token', { device_code: grant.deviceCode }, opts.signal);
    if (status === 200) return toToken(body);
    const code = String(body.error ?? '');
    if (code === 'authorization_pending') continue;
    if (code === 'slow_down') {
      interval += 5;
      continue;
    }
    if (status === 429) {
      interval = Math.max(interval * 2, 10);
      continue;
    }
    throw errorFor(status, body);
  }
}

export interface DirectLogin {
  username: string;
  password: string;
  totpCode?: string;
  recoveryCode?: string;
  deviceName: string;
}

export async function directLogin(serverUrl: string, input: DirectLogin, f: Fetch = fetch, signal?: AbortSignal): Promise<TokenResult> {
  const { status, body } = await postJson(
    f,
    serverUrl + '/api/device/login',
    {
      username: input.username,
      password: input.password,
      totp_code: input.totpCode || undefined,
      recovery_code: input.recoveryCode || undefined,
      device_name: input.deviceName,
      scope: DEVICE_SCOPE,
    },
    signal,
  );
  if (status === 200) return toToken(body);
  throw errorFor(status, body);
}

/**
 * The verification page is opened in a Custom Tab. Only https is accepted
 * (plain http only when the server itself was entered as http://localhost
 * for development); relative URIs are resolved against the server.
 */
export function safeVerificationUrl(serverUrl: string, url: string): string | null {
  try {
    const s = new URL(serverUrl);
    const u = new URL(url, s.href.endsWith('/') ? s.href : s.href + '/');
    if (u.protocol === 'https:') return u.href;
    if (u.protocol === 'http:' && s.protocol === 'http:' && u.hostname === s.hostname) return u.href;
    return null;
  } catch {
    return null;
  }
}

/**
 * One field for the second factor: six digits are a TOTP code, anything else
 * is a recovery code (the server checks both the same way as in the web login).
 */
export function secondFactor(code: string | undefined): { totpCode?: string; recoveryCode?: string } {
  const c = (code ?? '').trim();
  if (!c) return {};
  const digits = c.replace(/\s+/g, '');
  return /^\d{6}$/.test(digits) ? { totpCode: digits } : { recoveryCode: c };
}

/**
 * Direct passkey sign-in of the app: begin -> Credential Manager -> login.
 * Every server refusal of the assertion is the same 401 (it never says why);
 * it becomes passkey_failed, cancelling in the system dialog passkey_cancelled.
 */
export async function passkeyLogin(
  serverUrl: string,
  bridge: PasskeyBridge,
  deviceName: string,
  f: Fetch = fetch,
  signal?: AbortSignal,
): Promise<TokenResult> {
  const begin = await postJson(f, serverUrl + '/api/device/passkey/begin', {}, signal);
  if (begin.status !== 200) throw errorFor(begin.status, begin.body);
  const challengeId = begin.body.challengeId;
  if (typeof challengeId !== 'string' || !begin.body.publicKey) throw new ServerError('server_error', 'no passkey options');
  let credential: unknown;
  try {
    credential = JSON.parse(await bridge.get(JSON.stringify(begin.body.publicKey)));
  } catch (err) {
    if (err instanceof PasskeyError) {
      if (err.code === 'cancelled') throw new ServerError('passkey_cancelled', err.message);
      if (err.code === 'no_credential' || err.code === 'unsupported') throw new ServerError('passkey_unavailable', err.message);
    }
    throw new ServerError('passkey_failed', err instanceof Error ? err.message : String(err));
  }
  if (signal?.aborted) throw new ServerError('aborted');
  const { status, body } = await postJson(
    f,
    serverUrl + '/api/device/passkey/login',
    { challengeId, credential, device_name: deviceName, scope: DEVICE_SCOPE },
    signal,
  );
  if (status === 200) return toToken(body);
  if (status === 401) throw new ServerError('passkey_failed', String(body.message ?? 'passkey sign-in failed'), status);
  throw errorFor(status, body);
}
