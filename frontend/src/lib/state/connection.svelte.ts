// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The desktop app's connection to a TuxMail server, as the interface sees it
// (internal/app/remote_conn.go). The bindings always reach the local core, also
// while the interface works through the server. The Android app brings its own
// core with the same methods (native/offline/bridge.ts, useBackend()). In the
// browser there is no such connection: `available` is false, nothing is ever offline.

import { ApiError } from '../api/api';
import { WailsTransport, type WailsWindow } from '../api/wails';
import type { DesktopWin } from '../desktop/bridge';
import type { ClientMode, ConnectionStatus, DisconnectInput, DisconnectResult, OutgoingMessage, ProvisioningProfile } from '../types';

export type ConnectionWizard = { purpose: 'connect' } | { purpose: 'reauth'; serverUrl: string } | { purpose: 'switchLocal'; serverUrl: string };

const NONE: ConnectionStatus = { connected: false, state: 'none', cacheBytes: 0, outbox: [], settingsLog: [] };

/** A core with the desktop's methods (ServerConnectionStatus, ServerOutboxDiscard, ...) that is not Wails: the Android app. */
export interface CoreBackend {
  call(method: string, ...args: unknown[]): Promise<unknown>;
  /** Status changes; returns the unsubscribe function. */
  subscribe(onChange: () => void): () => void;
  /** "Neu anmelden" when the device was rejected. */
  reauth(): void;
}

export class ConnectionState {
  status = $state<ConnectionStatus>(NONE);
  wizard = $state<ConnectionWizard | null>(null);
  /** Things the settings synchronisation changed: the app reloads what it shows. */
  onSettingsSynced: (() => void) | null = null;
  /** More things wait for the user than before: the app says so once. */
  onAttention: (() => void) | null = null;
  private off: (() => void) | undefined;
  private backend: CoreBackend | null = null;
  private readonly win: DesktopWin;
  private readonly reload: () => void;

  constructor(win?: DesktopWin, reload?: () => void) {
    this.win = win ?? ((typeof window !== 'undefined' ? window : {}) as unknown as DesktopWin);
    this.reload = reload ?? (() => location.reload());
  }

  /** The Android app's core; call before the app starts (native/capacitor.ts). */
  useBackend(b: CoreBackend | null): void {
    this.backend = b;
  }

  private async call<T>(method: string, ...args: unknown[]): Promise<T> {
    const fn = this.backend ? (...a: unknown[]) => this.backend!.call(method, ...a) : this.win.go?.app?.Api?.[method];
    if (!fn) throw new ApiError(`binding ${method} not available`, 0, 'not_bound');
    try {
      return (await fn(...args)) as T;
    } catch (err) {
      const text = typeof err === 'string' ? err : String(err);
      let parsed: { code?: string; message?: string } | null = null;
      try {
        parsed = JSON.parse(text);
      } catch {
        /* not the core's error format */
      }
      if (parsed?.code) throw new ApiError(parsed.message ?? parsed.code, 0, parsed.code);
      throw new ApiError(text, 0, 'backend');
    }
  }

  /** This app has a local core (desktop). */
  get available(): boolean {
    return !!this.backend || !!this.win.go?.app?.Api?.ServerConnectionStatus;
  }
  get connected(): boolean {
    return this.status.connected;
  }
  /** The interface works through the server. */
  get serverMode(): boolean {
    return this.status.connected && this.status.mode === 'thin';
  }
  get expired(): boolean {
    return this.status.connected && this.status.state === 'expired';
  }
  /** The server cannot be used right now, for any reason. */
  get unreachable(): boolean {
    return this.status.connected && this.status.state !== 'online';
  }
  /** Server mode without the server: what needs it is switched off in the interface. */
  get offline(): boolean {
    return this.serverMode && this.status.state !== 'online';
  }
  /** Offline, and moving/deleting cannot wait for the connection (desktop): those buttons are off. */
  get movesBlocked(): boolean {
    return this.offline && !this.status.queuesMoves;
  }
  /** Mails waiting to be sent (noted marks are not worth a number). */
  get waitingMails(): number {
    return this.status.outbox.filter((o) => o.kind === 'send').length;
  }
  /** Things only the user can decide. */
  get needsAttention(): number {
    const a = this.status.attention;
    return this.status.outbox.filter((o) => o.state !== 'pending').length + (a?.pendingWorkflows.length ?? 0) + (a?.conflicts.length ?? 0) + (a?.deletes.filter((d) => !d.decided).length ?? 0);
  }

  /** The local core for signing in, also while the interface uses the server. */
  localCore(): WailsTransport {
    return new WailsTransport(this.win as unknown as WailsWindow);
  }

  start(): () => void {
    if (!this.available) return () => {};
    void this.refresh();
    if (this.backend) {
      this.off = this.backend.subscribe(() => void this.refresh());
      return () => this.off?.();
    }
    this.off = this.win.runtime?.EventsOn?.('tuxmail:event', (ev) => {
      const type = (ev as { type?: string } | undefined)?.type;
      if (type === 'server.connection') void this.refresh();
      else if (type === 'settings.synced') {
        void this.refresh();
        this.onSettingsSynced?.();
      }
    });
    return () => this.off?.();
  }

  async refresh(): Promise<void> {
    try {
      const before = this.needsAttention;
      this.status = await this.call<ConnectionStatus>('ServerConnectionStatus');
      if (this.needsAttention > before) this.onAttention?.();
    } catch {
      /* keep the last status */
    }
  }

  async syncNow(): Promise<void> {
    this.status = await this.call<ConnectionStatus>('ServerSyncNow');
  }

  async disconnect(input: DisconnectInput): Promise<DisconnectResult> {
    const r = await this.call<DisconnectResult>('ServerDisconnect', input);
    await this.refresh();
    return r;
  }

  /** To "Über den Server". The other way goes through the wizard: it asks what to set up here. */
  async switchToServer(): Promise<void> {
    await this.call<void>('ServerSetMode', 'thin' satisfies ClientMode);
    this.reloadApp();
  }

  serverProfile(): Promise<ProvisioningProfile> {
    return this.call<ProvisioningProfile>('ServerProfile');
  }
  reauthComplete(serverUrl: string): Promise<void> {
    return this.call<void>('ServerReauthComplete', serverUrl);
  }
  abortConnect(serverUrl: string): Promise<void> {
    return this.call<void>('ServerAbortConnect', serverUrl);
  }
  async outboxDiscard(id: string): Promise<void> {
    await this.call<void>('ServerOutboxDiscard', id);
    await this.refresh();
  }
  outboxMessage(id: string): Promise<OutgoingMessage> {
    return this.call<OutgoingMessage>('ServerOutboxMessage', id);
  }
  async workflowApprove(id: string, hash: string): Promise<void> {
    await this.call<void>('ServerWorkflowApprove', id, hash);
    await this.refresh();
  }
  async workflowDismiss(id: string): Promise<void> {
    await this.call<void>('ServerWorkflowDismiss', id);
    await this.refresh();
  }
  async workflowResolve(id: string, keepLocal: boolean): Promise<void> {
    await this.call<void>('ServerWorkflowResolve', id, keepLocal);
    await this.refresh();
  }
  async settingsDeletes(kind: string, approve: boolean): Promise<void> {
    await this.call<void>('ServerSettingsDeletes', kind, approve);
    await this.refresh();
  }
  async outboxRetry(id: string): Promise<void> {
    await this.call<void>('ServerOutboxRetry', id);
    await this.refresh();
  }

  /** "Neu anmelden": the Android app has its own sign-in, the desktop the wizard. */
  reauth(): void {
    if (this.backend) this.backend.reauth();
    else if (this.status.serverUrl) this.wizard = { purpose: 'reauth', serverUrl: this.status.serverUrl };
  }

  /** The transport is chosen when the app starts. */
  reloadApp(): void {
    this.reload();
  }
}

export const connection = new ConnectionState();

/**
 * Svelte action for actions that need the server: disabled with the reason as
 * tooltip while the app is offline (spec guidelines/DESIGN.md, "ausgegraut mit Grund").
 */
export function needsOnline(el: HTMLButtonElement, reason: () => string): void {
  const title = el.title;
  $effect(() => {
    const off = connection.offline;
    el.disabled = off;
    el.title = off ? reason() : title;
  });
}

/** Like needsOnline for archiving and deleting: the Android app notes them for later, the desktop switches them off. */
export function needsOnlineToMove(el: HTMLButtonElement, reason: () => string): void {
  const title = el.title;
  $effect(() => {
    const off = connection.movesBlocked;
    el.disabled = off;
    el.title = off ? reason() : title;
  });
}
