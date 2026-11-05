// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { createApi, selectTransportKind } from './select';
import { HttpTransport, buildQuery } from './http';
import { WailsTransport } from './wails';
import { MockTransport } from './mock';

describe('selectTransportKind', () => {
  it('uses Wails when window.go exists', () => {
    expect(selectTransportKind({ go: { app: {} }, search: '' })).toBe('wails');
    expect(selectTransportKind({ go: { app: {} }, search: '?api=http' })).toBe('wails');
  });

  it('uses HTTP with ?api=http outside Wails', () => {
    expect(selectTransportKind({ search: '?api=http' })).toBe('http');
    expect(selectTransportKind({ search: '?foo=1&api=http' })).toBe('http');
  });

  it('falls back to the mock', () => {
    expect(selectTransportKind({})).toBe('mock');
    expect(selectTransportKind({ search: '?api=other' })).toBe('mock');
    expect(selectTransportKind({ go: undefined, search: '' })).toBe('mock');
  });

  it('?api=mock forces the mock in development builds only', () => {
    expect(selectTransportKind({ search: '?api=mock', dev: true })).toBe('mock');
  });

  it('?api=mock is ignored in production builds (NG-02)', () => {
    expect(selectTransportKind({ search: '?api=mock', dev: false, meta: 'http' })).toBe('http');
    expect(selectTransportKind({ search: '?api=mock', dev: false, native: true })).toBe('http');
    expect(selectTransportKind({ search: '?api=mock', dev: false, go: {} })).toBe('wails');
  });

  it('?api=mock is ignored when the server meta tag, Wails or a native shell is present, even in dev (NG-02)', () => {
    expect(selectTransportKind({ search: '?api=mock', dev: true, meta: 'http' })).toBe('http');
    expect(selectTransportKind({ search: '?api=mock', dev: true, go: {} })).toBe('wails');
    expect(selectTransportKind({ search: '?api=mock', dev: true, native: true })).toBe('http');
  });

  it('defaults to import.meta.env.DEV', () => {
    expect(selectTransportKind({ search: '?api=mock', meta: 'http' })).toBe('http');
    expect(import.meta.env.DEV).toBe(true); // vitest runs with the dev flag
  });

  it('createApi returns the matching implementation', () => {
    expect(createApi('http')).toBeInstanceOf(HttpTransport);
    expect(createApi('wails', {})).toBeInstanceOf(WailsTransport);
    expect(createApi('mock')).toBeInstanceOf(MockTransport);
  });
});

describe('HttpTransport', () => {
  it('builds query strings without empty values', () => {
    expect(buildQuery({ a: 1, b: undefined, c: '', d: 'x y' })).toBe('?a=1&d=x+y');
    expect(buildQuery({})).toBe('');
  });

  it('maps listMessages to the REST route', async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ items: [], total: 0, offset: 0 }), { status: 200 }));
    const api = new HttpTransport({ fetchImpl: fetchImpl as unknown as typeof fetch, webSocketImpl: undefined });
    await api.listMessages({ scope: { kind: 'folder', folderId: 7 }, offset: 0, limit: 50, search: 'Angebot' });
    expect(fetchImpl).toHaveBeenCalledOnce();
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/messages?scope=folder&folderId=7&offset=0&limit=50&q=Angebot');
    expect(init.method).toBe('GET');
  });

  it('turns HTTP errors into ApiError', async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ error: 'nope', code: 'forbidden' }), { status: 403 }));
    const api = new HttpTransport({ fetchImpl: fetchImpl as unknown as typeof fetch });
    await expect(api.listAccounts()).rejects.toMatchObject({ name: 'ApiError', status: 403, code: 'forbidden', message: 'nope' });
  });
});

describe('WailsTransport', () => {
  it('calls the bound Go method with the same arguments', async () => {
    const GetMessage = vi.fn(async (id: unknown) => ({ id }));
    const api = new WailsTransport({ go: { app: { Api: { GetMessage } } } });
    await expect(api.getMessage(42)).resolves.toEqual({ id: 42 });
    expect(GetMessage).toHaveBeenCalledWith(42);
  });

  it('rejects clearly when a binding is missing', async () => {
    const api = new WailsTransport({ go: {} });
    await expect(api.listAccounts()).rejects.toMatchObject({ code: 'not_bound' });
  });

  it('forwards runtime events to subscribers', () => {
    let cb: ((d: unknown) => void) | undefined;
    const off = vi.fn();
    const api = new WailsTransport({
      go: {},
      runtime: {
        EventsOn: (_n, c) => {
          cb = c;
          return off;
        },
      },
    });
    const seen: unknown[] = [];
    const unsub = api.subscribe((e) => seen.push(e));
    cb?.({ type: 'message.updated', messageId: 1 });
    expect(seen).toEqual([{ type: 'message.updated', messageId: 1 }]);
    unsub();
    expect(off).toHaveBeenCalled();
  });
});
