// SPDX-License-Identifier: AGPL-3.0-or-later
import { afterEach, describe, expect, it, vi } from 'vitest';
import { bridgeFetch, bridgeSocket, desktopHttpOptions, isDesktopServerMode, lastUnclearOutboxId, prepareDesktopBridge, setDesktopServerModeForTest, type DesktopWin } from './bridge';
import { createApi, selectTransportKind } from '../api/select';
import { DesktopServerTransport } from '../api/desktopServer';
import { HttpTransport } from '../api/http';
import { WailsTransport, type WailsWindow } from '../api/wails';

const b64 = (s: string) => btoa(String.fromCharCode(...new TextEncoder().encode(s)));
const unb64 = (s: string) => new TextDecoder().decode(Uint8Array.from(atob(s), (c) => c.charCodeAt(0)));

function fakeCore(answer: (req: { method: string; path: string; headers: Record<string, string>; body?: string }) => unknown, status: unknown = { connected: true, mode: 'thin', state: 'online' }) {
  const handlers: ((d: unknown) => void)[] = [];
  const calls: { method: string; path: string; headers: Record<string, string>; body?: string }[] = [];
  const win: DesktopWin = {
    go: {
      app: {
        Api: {
          ServerConnectionStatus: async () => status,
          ServerRequest: async (r: unknown) => {
            const req = r as (typeof calls)[number];
            calls.push(req);
            return answer(req);
          },
        },
      },
    },
    runtime: {
      EventsOn: (_n, cb) => {
        handlers.push(cb);
        return () => handlers.splice(handlers.indexOf(cb), 1);
      },
    },
  };
  return { win, calls, handlers };
}

afterEach(() => setDesktopServerModeForTest(false, 'none'));

describe('choosing the way to the data', () => {
  it('stays on the local core without a connection', async () => {
    await prepareDesktopBridge(fakeCore(() => ({}), { connected: false, state: 'none' }).win);
    expect(isDesktopServerMode()).toBe(false);
    const api = createApi('wails', { go: {} } as unknown as WailsWindow);
    expect(api).toBeInstanceOf(WailsTransport);
    expect(api.kind).toBe('wails');
  });
  it('stays local in the mode "on this device"', async () => {
    await prepareDesktopBridge(fakeCore(() => ({}), { connected: true, mode: 'local', state: 'online' }).win);
    expect(isDesktopServerMode()).toBe(false);
  });
  it('uses the web client\'s calls through the core in server mode, also offline', async () => {
    await prepareDesktopBridge(fakeCore(() => ({}), { connected: true, mode: 'thin', state: 'offline' }).win);
    expect(isDesktopServerMode()).toBe(true);
    expect(selectTransportKind({ go: {} })).toBe('wails');
    const api = createApi('wails', { go: {} } as unknown as WailsWindow);
    expect(api).toBeInstanceOf(DesktopServerTransport);
    expect(api.kind).toBe('http');
  });
  it('starts locally when the core does not answer: the app always opens', async () => {
    const win: DesktopWin = { go: { app: { Api: { ServerConnectionStatus: () => Promise.reject(new Error('x')) } } } };
    await prepareDesktopBridge(win);
    expect(isDesktopServerMode()).toBe(false);
  });
  it('signing in stays with the local core in server mode', async () => {
    const deviceAuthorize = vi.fn(async () => ({ deviceCode: 'x' }));
    const vaultStatus = vi.fn(async () => ({ locked: true }));
    const win = { go: { app: { Api: { DeviceAuthorize: deviceAuthorize, VaultStatus: vaultStatus, ServerRequest: vi.fn() } } } } as unknown as WailsWindow;
    const api = new DesktopServerTransport(win);
    await api.deviceAuthorize('https://x');
    await api.vaultStatus();
    expect(deviceAuthorize).toHaveBeenCalledWith('https://x');
    expect(vaultStatus).toHaveBeenCalled();
    expect((win.go!.app.Api.ServerRequest as ReturnType<typeof vi.fn>)).not.toHaveBeenCalled();
  });
});

describe('bridgeFetch', () => {
  it('passes method, path, headers and body to the core and returns its answer', async () => {
    const { win, calls } = fakeCore(() => ({ status: 200, headers: { 'Content-Type': 'application/json' }, body: b64('{"a":"ä"}') }));
    const res = await bridgeFetch(win)('/api/x?y=1', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: '{"b":"ü"}' });
    expect(await res.json()).toEqual({ a: 'ä' });
    expect(calls[0].method).toBe('PUT');
    expect(calls[0].path).toBe('/api/x?y=1');
    expect(calls[0].headers['content-type']).toBe('application/json');
    expect(unb64(calls[0].body!)).toBe('{"b":"ü"}');
  });
  it('sends blobs as they are', async () => {
    const { win, calls } = fakeCore(() => ({ status: 201, body: b64('{}') }));
    await bridgeFetch(win)('/api/uploads', { method: 'POST', body: new Blob([new Uint8Array([0, 255, 7])]) });
    expect(Array.from(atob(calls[0].body!), (c) => c.charCodeAt(0))).toEqual([0, 255, 7]);
  });
  it('204 has no body', async () => {
    const { win } = fakeCore(() => ({ status: 204 }));
    expect((await bridgeFetch(win)('/api/x', { method: 'POST' })).status).toBe(204);
  });
  it('gives the offline answer a sentence for people', async () => {
    const { win } = fakeCore(() => ({ status: 503, body: b64('{"error":"offline","message":"not available"}') }));
    const api = new HttpTransport({ fetchImpl: bridgeFetch(win) });
    await expect(api.listTags()).rejects.toMatchObject({ code: 'offline', status: 503 });
  });
  it('the options of the transport never contain a token', () => {
    const o = desktopHttpOptions(fakeCore(() => ({})).win);
    expect(o.authToken).toBeUndefined();
    expect(o.onUnauthorized).toBeUndefined();
  });
  it('a rejected device does not clear the app or reload it (no web session logic)', async () => {
    const { win } = fakeCore(() => ({ status: 401, body: b64('{"error":"unauthenticated"}') }));
    const api = new HttpTransport(desktopHttpOptions(win));
    await expect(api.listTags()).rejects.toMatchObject({ status: 401 });
  });
});

describe('bridgeSocket', () => {
  it('delivers the frames the core receives; closing stops them', async () => {
    const { win, handlers } = fakeCore(() => ({}));
    const Sock = bridgeSocket(win);
    const ws = new Sock('ignored');
    const got: string[] = [];
    ws.onmessage = (e) => got.push(e.data as string);
    handlers[0]('{"type":"mail.new"}');
    expect(got).toEqual(['{"type":"mail.new"}']);
    ws.close();
    expect(handlers.length).toBe(0);
  });
  it('says offline then online when the core connected after the app started', async () => {
    setDesktopServerModeForTest(true, 'offline');
    const { win } = fakeCore(() => ({}));
    const ws = new (bridgeSocket(win))('ignored');
    const got: string[] = [];
    ws.onmessage = (e) => got.push(JSON.parse(e.data as string).state);
    await vi.waitFor(() => expect(got).toEqual(['offline', 'online']));
  });
  it('tells the app when it starts offline', async () => {
    setDesktopServerModeForTest(true, 'offline');
    const { win } = fakeCore(() => ({}), { connected: true, mode: 'thin', state: 'offline' });
    const ws = new (bridgeSocket(win))('ignored');
    const got: string[] = [];
    ws.onmessage = (e) => got.push(JSON.parse(e.data as string).state);
    await vi.waitFor(() => expect(got).toEqual(['offline']));
  });
});

describe('a mail ending unclear', () => {
  it('reaches the composer as send_unclear with the plain sentence, and the outbox entry is known', async () => {
    const { win } = fakeCore(() => ({ status: 409, headers: { 'Content-Type': 'application/json' }, body: b64('{"error":"send_unclear","message":"x","outboxId":"out_1"}') }));
    const api = new HttpTransport({ fetchImpl: bridgeFetch(win) });
    await expect(api.send({} as never)).rejects.toMatchObject({ code: 'send_unclear', status: 409 });
    expect(lastUnclearOutboxId()).toBe('out_1');
  });
});
