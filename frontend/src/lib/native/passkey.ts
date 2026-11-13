// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Direct passkey sign-in of the Android app (no browser): Android's Credential
// Manager through the app's own small plugin "TuxMailPasskey"
// (mobile/android/.../PasskeyPlugin.kt, androidx.credentials). The plugin only
// hands JSON through: the options from POST /api/device/passkey/begin go to
// CredentialManager.getCredential, the assertion (PublicKeyCredential JSON)
// comes back and is verified by the server. No cryptography here.
//
// Android shows a server's passkeys to the app only if the server lists the
// app in /.well-known/assetlinks.json (auth.passkeys.android_apps); the server
// announces that with passkeyAppLogin in /api/auth/config.

import { registerPlugin } from '@capacitor/core';

export interface PasskeyBridge {
  /** Credential Manager is usable on this device (Android 9+, Play services or Android 14+ provider). */
  available(): Promise<boolean>;
  /** requestJson: PublicKeyCredentialRequestOptions JSON; resolves with the PublicKeyCredential JSON. */
  get(requestJson: string, serverUrl: string): Promise<string>;
}

/**
 * The origin of every assertion is the same apk-key-hash for all servers, so the
 * relying party must be pinned to the server the user connected to: rpId is that
 * server's host or a parent domain of it (never a bare public suffix: needs a dot).
 */
export function rpIdMatchesServer(serverUrl: string, rpId: unknown): boolean {
  if (typeof rpId !== 'string') return false;
  const id = rpId.toLowerCase();
  let host: string;
  try {
    host = new URL(serverUrl).hostname.toLowerCase();
  } catch {
    return false;
  }
  if (!id || !host) return false;
  if (host === id) return true;
  return id.includes('.') && !id.startsWith('.') && !id.endsWith('.') && host.endsWith('.' + id);
}

/** Error codes of the plugin (call.reject(message, code)). */
export type PasskeyErrorCode = 'cancelled' | 'no_credential' | 'unsupported' | 'failed';

export class PasskeyError extends Error {
  constructor(
    readonly code: PasskeyErrorCode,
    message: string = code,
  ) {
    super(message);
    this.name = 'PasskeyError';
  }
}

interface TuxMailPasskeyPlugin {
  isAvailable(): Promise<{ available: boolean }>;
  getCredential(options: { requestJson: string; serverUrl: string }): Promise<{ responseJson: string }>;
}

function codeOf(err: unknown): PasskeyErrorCode {
  const c = (err as { code?: unknown })?.code;
  return c === 'cancelled' || c === 'no_credential' || c === 'unsupported' ? c : 'failed';
}

/** The bridge of the Capacitor app; elsewhere (web, desktop) passkeys are not offered in the app. */
export function nativePasskeys(): PasskeyBridge {
  const plugin = registerPlugin<TuxMailPasskeyPlugin>('TuxMailPasskey');
  return {
    async available() {
      try {
        return (await plugin.isAvailable()).available === true;
      } catch {
        return false; // older app build without the plugin
      }
    },
    async get(requestJson, serverUrl) {
      try {
        const r = await plugin.getCredential({ requestJson, serverUrl });
        if (typeof r?.responseJson !== 'string' || !r.responseJson) throw new PasskeyError('failed', 'empty answer');
        return r.responseJson;
      } catch (err) {
        if (err instanceof PasskeyError) throw err;
        throw new PasskeyError(codeOf(err), err instanceof Error ? err.message : String(err));
      }
    },
  };
}
