// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Transport of the desktop app in server mode: same calls as the web client
// (lib/desktop/bridge.ts). What belongs to this device stays with the local
// core: signing in at a server, and the secret store's password.

import { HttpTransport } from './http';
import { WailsTransport, type WailsWindow } from './wails';
import { desktopHttpOptions, type DesktopWin } from '../desktop/bridge';
import type { DeviceAuthorization, DeviceTokenResult, MailStorageEstimate, MailStorageSettings, ProvisioningImport, ServerLoginInput, VaultStatus } from '../types';

export class DesktopServerTransport extends HttpTransport {
  private readonly local: WailsTransport;

  constructor(win: WailsWindow) {
    super(desktopHttpOptions(win as unknown as DesktopWin));
    this.local = new WailsTransport(win);
  }

  override deviceAuthorize(serverUrl: string): Promise<DeviceAuthorization> {
    return this.local.deviceAuthorize(serverUrl);
  }
  override deviceToken(serverUrl: string, deviceCode: string): Promise<DeviceTokenResult> {
    return this.local.deviceToken(serverUrl, deviceCode);
  }
  override serverLogin(serverUrl: string, credentials: ServerLoginInput): Promise<DeviceTokenResult> {
    return this.local.serverLogin(serverUrl, credentials);
  }
  override importProvisioning(input: ProvisioningImport): Promise<void> {
    return this.local.importProvisioning(input);
  }
  override vaultStatus(): Promise<VaultStatus> {
    return this.local.vaultStatus();
  }
  override unlockVault(password: string): Promise<void> {
    return this.local.unlockVault(password);
  }
  /** The offline read cache of this device keeps older mails with header only. */
  override getMailStorageSettings(): Promise<MailStorageSettings> {
    return this.local.getMailStorageSettings();
  }
  override setMailStorageSettings(settings: Pick<MailStorageSettings, 'headersOnlyOlderThanDays'> & { downloadPaused?: boolean }): Promise<MailStorageSettings> {
    return this.local.setMailStorageSettings(settings);
  }
  /** The read cache downloads nothing ahead: nothing to estimate. */
  override estimateMailStorage(_days: number): Promise<MailStorageEstimate> {
    return Promise.resolve({ messages: 0, bytes: 0 });
  }
}
