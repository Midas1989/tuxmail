// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
// @vitest-environment jsdom
//
// Boot sequence of the native bridge with Capacitor fully mocked.
import { beforeEach, describe, expect, it, vi } from 'vitest';

const prefs = new Map<string, string>();
const secure = new Map<string, string>();
const listeners: Record<string, ((e: unknown) => void)[]> = {};
const on = (name: string) => (event: string, fn: (e: unknown) => void) => {
  (listeners[`${name}.${event}`] ??= []).push(fn);
  return Promise.resolve({ remove: async () => {} });
};

const TuxMail = {
  secureGet: vi.fn(async ({ key }: { key: string }) => ({ value: secure.get(key) ?? null })),
  secureSet: vi.fn(async ({ key, value }: { key: string; value: string }) => void secure.set(key, value)),
  secureRemove: vi.fn(async ({ key }: { key: string }) => void secure.delete(key)),
  setActiveSession: vi.fn(async () => {}),
  clearActiveSession: vi.fn(async () => {}),
  authenticate: vi.fn(async () => {}),
  biometricStatus: vi.fn(async () => ({ available: true })),
  getPendingShare: vi.fn(async () => ({})),
  pushInfo: vi.fn(async () => ({ provider: 'unifiedpush', distributors: ['io.heckel.ntfy'], configured: true })),
  pushRegister: vi.fn(async () => {}),
  pushUnregister: vi.fn(async () => {}),
  checkPermissions: vi.fn(async () => ({ notifications: 'prompt' })),
  requestPermissions: vi.fn(async () => ({ notifications: 'granted' })),
  cancelConversationNotifications: vi.fn(async () => {}),
  addListener: on('TuxMail'),
};

vi.mock('./plugins', () => ({
  App: { addListener: on('App'), getLaunchUrl: vi.fn(async () => undefined), minimizeApp: vi.fn(async () => {}), getInfo: vi.fn(async () => ({ id: 'org.tuxmail.app', version: '0.1.0', build: '100', name: 'TuxMail' })) },
  Browser: { open: vi.fn(async () => {}), close: vi.fn(async () => {}) },
  Preferences: {
    get: vi.fn(async ({ key }: { key: string }) => ({ value: prefs.get(key) ?? null })),
    set: vi.fn(async ({ key, value }: { key: string; value: string }) => void prefs.set(key, value)),
    remove: vi.fn(async ({ key }: { key: string }) => void prefs.delete(key)),
  },
  SplashScreen: { hide: vi.fn(async () => {}) },
  SystemBars: { setStyle: vi.fn(async () => {}) },
  SystemBarsStyle: { Dark: 'DARK', Light: 'LIGHT', Default: 'DEFAULT' },
  TuxMail,
}));

vi.mock('@capacitor/core', () => ({ Capacitor: { convertFileSrc: (p: string) => p, isNativePlatform: () => true } }));

const mounted: string[] = [];
vi.mock('svelte', async (orig) => ({
  ...(await orig<typeof import('svelte')>()),
  mount: vi.fn((cmp: { name?: string }) => {
    mounted.push(cmp.name ?? 'component');
    return {};
  }),
  unmount: vi.fn(),
}));

describe('bootNative', () => {
  beforeEach(() => {
    prefs.clear();
    secure.clear();
    mounted.length = 0;
    localStorage.clear();
    vi.clearAllMocks();
    for (const k of Object.keys(listeners)) delete listeners[k];
  });

  it('shows the connect screen when no profile exists', async () => {
    const { bootNative } = await import('./capacitor');
    const mountApp = vi.fn();
    await bootNative(document.createElement('div'), mountApp);
    expect(mountApp).not.toHaveBeenCalled();
    expect(mounted.some((n) => /ServerConnect/.test(n))).toBe(true);
    expect(document.documentElement.classList.contains('tm-native')).toBe(true);
    expect(TuxMail.authenticate).not.toHaveBeenCalled();
  }, 20_000); // first import compiles the Svelte connect screen (slow on busy CI runners)

  it('mounts the app with the token from secure storage', async () => {
    const { ProfileStore } = await import('./profiles');
    const kv = (m: Map<string, string>) => ({ get: async (k: string) => m.get(k) ?? null, set: async (k: string, v: string) => void m.set(k, v), remove: async (k: string) => void m.delete(k) });
    const p = await new ProfileStore(kv(prefs), kv(secure)).upsert({ serverUrl: 'https://mail.example.org', username: 'anna' }, 'device-token');
    const { bootNative } = await import('./capacitor');
    const { sessionToken, readActiveMirror } = await import('./session');
    const mountApp = vi.fn();
    await bootNative(document.createElement('div'), mountApp);
    expect(mountApp).toHaveBeenCalledOnce();
    expect(sessionToken()).toBe('device-token');
    expect(readActiveMirror()).toEqual({ profileId: p.id, serverUrl: 'https://mail.example.org' });
    expect(TuxMail.setActiveSession).toHaveBeenCalledWith({ serverUrl: 'https://mail.example.org', token: 'device-token', profileId: p.id });
  });

  it('asks for biometrics first when the app lock is enabled', async () => {
    prefs.set('tuxmail.appLock', JSON.stringify({ enabled: true, graceSeconds: 0 }));
    const { bootNative } = await import('./capacitor');
    const done = bootNative(document.createElement('div'), vi.fn());
    await vi.waitFor(() => expect(mounted.some((n) => /AppLock/.test(n))).toBe(true));
    // Lock screen blocks until unlocked: the connect screen is not shown yet.
    expect(mounted.some((n) => /ServerConnect/.test(n))).toBe(false);
    const { mount } = await import('svelte');
    const props = (mount as unknown as ReturnType<typeof vi.fn>).mock.calls[0][1].props as { onunlocked: () => void };
    props.onunlocked();
    await done;
    expect(mounted.some((n) => /ServerConnect/.test(n))).toBe(true);
  });
});

describe('tuxmail://connect link while signed in (NG-01)', () => {
  const kv = (m: Map<string, string>) => ({ get: async (k: string) => m.get(k) ?? null, set: async (k: string, v: string) => void m.set(k, v), remove: async (k: string) => void m.delete(k) });

  beforeEach(() => {
    prefs.clear();
    secure.clear();
    mounted.length = 0;
    localStorage.clear();
    vi.clearAllMocks();
    for (const k of Object.keys(listeners)) delete listeners[k];
  });

  async function signedIn() {
    const { ProfileStore } = await import('./profiles');
    const p = await new ProfileStore(kv(prefs), kv(secure)).upsert({ serverUrl: 'https://mail.example.org', username: 'anna' }, 'device-token');
    const { bootNative } = await import('./capacitor');
    await bootNative(document.createElement('div'), vi.fn());
    return p;
  }
  const lastDialogProps = async () => {
    const { mount } = await import('svelte');
    const calls = (mount as unknown as ReturnType<typeof vi.fn>).mock.calls;
    return calls[calls.length - 1][1].props as { host: string; current: string | null; onconfirm: () => void; oncancel: () => void };
  };

  it('keeps the session, asks with the full host and proceeds only after confirmation', async () => {
    const p = await signedIn();
    const { promptConnect } = await import('./capacitor');
    const proceed = vi.fn();
    promptConnect('tuxmail://connect?server=' + encodeURIComponent('https://mail.example.org.evil.tld'), proceed);
    expect(mounted.some((n) => /ConnectLinkDialog/.test(n))).toBe(true);
    const props = await lastDialogProps();
    expect(props.host).toBe('mail.example.org.evil.tld');
    expect(props.current).toBe('mail.example.org');
    // Nothing happened to the running session yet.
    expect(proceed).not.toHaveBeenCalled();
    expect(prefs.get('tuxmail.activeProfile')).toBe(p.id);
    expect(TuxMail.clearActiveSession).not.toHaveBeenCalled();
    props.onconfirm();
    expect(proceed).toHaveBeenCalledWith('https://mail.example.org.evil.tld');
    expect(prefs.get('tuxmail.activeProfile')).toBe(p.id); // still signed in until the new login succeeded
  });

  it('cancel does nothing and cleartext links never prompt', async () => {
    await signedIn();
    const { promptConnect } = await import('./capacitor');
    const proceed = vi.fn();
    const before = mounted.length;
    promptConnect('tuxmail://connect?server=' + encodeURIComponent('http://evil.example'), proceed);
    expect(mounted.length).toBe(before);
    promptConnect('tuxmail://connect?server=' + encodeURIComponent('https://other.example.org'), proceed);
    (await lastDialogProps()).oncancel();
    expect(proceed).not.toHaveBeenCalled();
  });
});

describe('native transport selection', () => {
  it('uses HTTP with base URL + Bearer token inside Capacitor', async () => {
    (window as unknown as { Capacitor: unknown }).Capacitor = { isNativePlatform: () => true };
    const { writeActiveMirror, setSessionToken } = await import('./session');
    writeActiveMirror({ profileId: 'p', serverUrl: 'https://mail.example.org' });
    setSessionToken('tok');
    const { createApiFromWindow } = await import('../api/select');
    const api = createApiFromWindow();
    expect(api.kind).toBe('http');
    const fetchSpy = vi.fn(async () => new Response('[]', { status: 200 }));
    vi.stubGlobal('fetch', fetchSpy);
    await api.listAccounts();
    const [url, init] = fetchSpy.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('https://mail.example.org/api/accounts');
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer tok');
    expect(init.credentials).toBe('omit');
    vi.unstubAllGlobals();
    delete (window as unknown as { Capacitor?: unknown }).Capacitor;
  });
});
