// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Desktop app in server mode ("Über den Server"): the interface is the same
// as the web client's (HttpTransport), but its requests go through the Go core
// (Api.ServerRequest): the device token never reaches JavaScript and no CORS is
// needed. Live events arrive the same way. See internal/app/remote_proxy.go.

import type { HttpTransportOptions } from '../api/http';
import { notifyMfaSetupRequired } from '../mfaGate';
import { t } from '../i18n/index.svelte';
import type { ConnectionStatus } from '../types';

type Bound = (...args: unknown[]) => Promise<unknown>;

export interface DesktopWin {
  go?: { app?: { Api?: Record<string, Bound> } };
  runtime?: { EventsOn?: (name: string, cb: (...data: unknown[]) => void) => () => void };
}

function currentWin(): DesktopWin {
  return (typeof window !== 'undefined' ? window : {}) as unknown as DesktopWin;
}

interface ProxyResponse {
  status: number;
  headers?: Record<string, string>;
  body?: string;
}

let lastUnclearId: string | null = null;

/** The outbox entry of the mail whose sending just ended unclear. */
export function lastUnclearOutboxId(): string | null {
  return lastUnclearId;
}

let serverMode = false;
let bootState = 'none';

/** Reads the mode from the core; call once before the app state is created (main.ts). */
export async function prepareDesktopBridge(win: DesktopWin = currentWin()): Promise<void> {
  const status = win.go?.app?.Api?.ServerConnectionStatus;
  if (!status) return;
  try {
    const s = (await status()) as ConnectionStatus;
    serverMode = s.connected && s.mode === 'thin';
    bootState = s.state;
  } catch {
    serverMode = false; // the local core is always there
  }
}

export function isDesktopServerMode(): boolean {
  return serverMode;
}

/** Test support. */
export function setDesktopServerModeForTest(on: boolean, state = 'online'): void {
  serverMode = on;
  bootState = state;
}

function toBase64(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

function fromBase64(b64: string): Uint8Array<ArrayBuffer> {
  const s = atob(b64);
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

async function bodyToBase64(body: BodyInit | null | undefined): Promise<string | undefined> {
  if (body == null) return undefined;
  if (typeof body === 'string') return toBase64(new TextEncoder().encode(body));
  if (body instanceof Blob) return toBase64(new Uint8Array(await body.arrayBuffer()));
  if (body instanceof ArrayBuffer) return toBase64(new Uint8Array(body));
  if (ArrayBuffer.isView(body)) return toBase64(new Uint8Array(body.buffer, body.byteOffset, body.byteLength));
  throw new TypeError('unsupported request body');
}

function headersToRecord(h: HeadersInit | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  new Headers(h).forEach((v, k) => (out[k] = v));
  return out;
}

/** fetch() through the core. A failure means the core itself refused (no network failure: the core answers those). */
export function bridgeFetch(win: DesktopWin = currentWin()): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const call = win.go?.app?.Api?.ServerRequest;
    if (!call) throw new TypeError('desktop core not available');
    const res = (await call({
      method: init?.method ?? 'GET',
      path,
      headers: headersToRecord(init?.headers),
      body: await bodyToBase64(init?.body),
    })) as ProxyResponse;
    let bytes: Uint8Array<ArrayBuffer> | null = res.body ? fromBase64(res.body) : null;
    if (res.status === 503 && bytes && new TextDecoder().decode(bytes).includes('"offline"')) {
      // Whatever screen ends up showing it: a sentence for people.
      bytes = new TextEncoder().encode(JSON.stringify({ error: 'offline', message: t('connection.notAvailableOffline') }));
    }
    if (res.status === 409 && bytes && new TextDecoder().decode(bytes).includes('"send_unclear"')) {
      const doc = JSON.parse(new TextDecoder().decode(bytes)) as { outboxId?: string };
      lastUnclearId = doc.outboxId ?? null;
      bytes = new TextEncoder().encode(JSON.stringify({ ...doc, message: t('connection.unclear.text') }));
    }
    const empty = res.status === 204 || res.status === 304 || res.status === 205;
    return new Response(empty ? null : bytes, { status: res.status, headers: res.headers });
  }) as typeof fetch;
}

export const REMOTE_EVENT = 'tuxmail:remote';

/** A WebSocket as HttpTransport knows it, fed by the event stream the core holds. It never closes by itself. */
export function bridgeSocket(win: DesktopWin = currentWin()): typeof WebSocket {
  return class BridgeSocket {
    onopen: ((ev: Event) => void) | null = null;
    onmessage: ((ev: MessageEvent) => void) | null = null;
    onclose: ((ev: CloseEvent) => void) | null = null;
    onerror: ((ev: Event) => void) | null = null;
    private off: (() => void) | undefined;
    constructor() {
      this.off = win.runtime?.EventsOn?.(REMOTE_EVENT, (frame) => this.deliver(frame as string));
      // Lists loaded before the core reached the server came from the read
      // cache: telling "offline → online" makes the app load them again.
      void (win.go?.app?.Api?.ServerConnectionStatus?.() as Promise<ConnectionStatus> | undefined)?.then((s) => {
        if (s.state === 'online' && bootState !== 'online') this.deliver(JSON.stringify({ type: 'connection', state: 'offline' }));
        this.deliver(JSON.stringify({ type: 'connection', state: s.state === 'online' ? 'online' : 'offline' }));
      });
    }
    private deliver(data: string) {
      this.onmessage?.({ data } as MessageEvent);
    }
    close() {
      this.off?.();
      this.off = undefined;
    }
  } as unknown as typeof WebSocket;
}

/** HttpTransport options of the desktop app in server mode. A rejected token: the core notices and tells the status. */
export function desktopHttpOptions(win: DesktopWin = currentWin()): HttpTransportOptions {
  return { fetchImpl: bridgeFetch(win), webSocketImpl: bridgeSocket(win), onMfaSetupRequired: notifyMfaSetupRequired };
}
