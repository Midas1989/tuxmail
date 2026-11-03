// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Server profiles of the native app. One profile = one TuxMail server +
// account; several profiles can be stored (switching is a reload). Profile
// metadata lives in Capacitor Preferences, the device token only in secure
// storage (Android Keystore, TuxMail.secure*).

import { writeActiveMirror } from './session';

export interface ServerProfile {
  id: string;
  /** Normalised https origin + path, without trailing slash. */
  serverUrl: string;
  /** Display label (defaults to the host name). */
  label: string;
  username?: string;
  displayName?: string;
  /** Server-side session id of the device token (for revocation lists). */
  sessionId?: number;
  serverVersion?: string;
  createdAt: string;
  /** Set when the server rejected the token (401): show the login again. */
  needsLogin?: boolean;
}

export interface KeyValueStore {
  get(key: string): Promise<string | null>;
  set(key: string, value: string): Promise<void>;
  remove(key: string): Promise<void>;
}

export const PROFILES_KEY = 'tuxmail.profiles';
export const ACTIVE_KEY = 'tuxmail.activeProfile';
export const tokenKey = (profileId: string) => `token:${profileId}`;

export function newProfileId(): string {
  const c = globalThis.crypto;
  if (c?.randomUUID) return c.randomUUID();
  return 'p-' + Date.now().toString(36) + Math.random().toString(36).slice(2, 10);
}

export function profileLabel(serverUrl: string): string {
  try {
    return new URL(serverUrl).host;
  } catch {
    return serverUrl;
  }
}

export class ProfileStore {
  constructor(
    private readonly prefs: KeyValueStore,
    private readonly secure: KeyValueStore,
    private readonly mirror: (p: ServerProfile | null) => void = (p) => writeActiveMirror(p ? { profileId: p.id, serverUrl: p.serverUrl } : null),
  ) {}

  async list(): Promise<ServerProfile[]> {
    const raw = await this.prefs.get(PROFILES_KEY);
    if (!raw) return [];
    try {
      const v = JSON.parse(raw) as unknown;
      return Array.isArray(v) ? (v.filter((p) => p && typeof p.id === 'string' && typeof p.serverUrl === 'string') as ServerProfile[]) : [];
    } catch {
      return [];
    }
  }

  private async saveList(list: ServerProfile[]): Promise<void> {
    await this.prefs.set(PROFILES_KEY, JSON.stringify(list));
  }

  async active(): Promise<ServerProfile | null> {
    const id = await this.prefs.get(ACTIVE_KEY);
    const list = await this.list();
    return list.find((p) => p.id === id) ?? null;
  }

  async token(profileId: string): Promise<string | null> {
    return this.secure.get(tokenKey(profileId));
  }

  /**
   * Store (or update, matched by server URL + username) a profile with its
   * token and make it the active one.
   */
  async upsert(input: Omit<ServerProfile, 'id' | 'createdAt' | 'label'> & { label?: string }, token: string): Promise<ServerProfile> {
    const list = await this.list();
    const existing = list.find((p) => p.serverUrl === input.serverUrl && (p.username ?? '') === (input.username ?? ''));
    const profile: ServerProfile = {
      ...existing,
      ...input,
      id: existing?.id ?? newProfileId(),
      label: input.label ?? existing?.label ?? profileLabel(input.serverUrl),
      createdAt: existing?.createdAt ?? new Date().toISOString(),
      needsLogin: false,
    };
    await this.secure.set(tokenKey(profile.id), token);
    await this.saveList([...list.filter((p) => p.id !== profile.id), profile]);
    await this.activate(profile);
    return profile;
  }

  async activate(profile: ServerProfile): Promise<void> {
    await this.prefs.set(ACTIVE_KEY, profile.id);
    this.mirror(profile);
  }

  /** Token rejected by the server: forget it, keep the profile for re-login. */
  async markNeedsLogin(profileId: string): Promise<void> {
    const list = await this.list();
    await this.secure.remove(tokenKey(profileId));
    await this.saveList(list.map((p) => (p.id === profileId ? { ...p, needsLogin: true } : p)));
  }

  async remove(profileId: string): Promise<void> {
    const list = await this.list();
    await this.secure.remove(tokenKey(profileId));
    await this.saveList(list.filter((p) => p.id !== profileId));
    if ((await this.prefs.get(ACTIVE_KEY)) === profileId) {
      await this.prefs.remove(ACTIVE_KEY);
      this.mirror(null);
    }
  }

  /** Leave the active profile without deleting it (back to the connect screen). */
  async deactivate(): Promise<void> {
    await this.prefs.remove(ACTIVE_KEY);
    this.mirror(null);
  }
}
