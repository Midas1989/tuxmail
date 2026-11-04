// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Messenger plugins of the MockTransport (KONZEPT 11): plugin catalog with
// opt-in and risk acknowledgment, simulated pairing (QR, pairing code,
// Telegram login code, Matrix password, bridge) and plugin status.

import type {
  Account,
  ApiEvent,
  MessengerAccountState,
  MessengerPairing,
  MessengerPairingInput,
  MessengerPlugin,
  MessengerPluginPatch,
} from '../types';
import { ApiError } from './api';

export interface MockMessengerHost {
  accounts(): Account[];
  addAccount(a: Omit<Account, 'id' | 'sortOrder'>): Account;
  removeAccount(id: number): void;
  emit(e: ApiEvent): void;
  /** Delay before a scanned QR code counts as accepted (ms, 0 = never). */
  autoAcceptMs: number;
}

function plugin(p: Partial<MessengerPlugin> & Pick<MessengerPlugin, 'name' | 'displayName' | 'channel'>): MessengerPlugin {
  return {
    risk: 'low',
    official: false,
    experimental: false,
    needsRiskAck: false,
    pairingMethods: ['qr'],
    settingKeys: [],
    installed: true,
    enabled: false,
    settings: {},
    accounts: 0,
    status: { state: 'stopped', restarts: 0, capabilities: [] },
    ...p,
  };
}

const caps = ['media', 'reactions', 'replies', 'edit', 'delete', 'read_receipts', 'typing'];

/** Kind of the account a plugin creates. */
const kindOf: Record<string, Account['kind']> = { whatsapp: 'whatsapp', signal: 'signal', telegram: 'telegram', matrix: 'matrix' };
const colorOf: Record<string, string> = { whatsapp: '#25d366', signal: '#3a76f0', telegram: '#229ed9', matrix: '#0dbd8b' };

export class MockMessenger {
  private plugins: MessengerPlugin[];
  private pairings = new Map<string, MessengerPairing & { input: MessengerPairingInput }>();
  private timers: ReturnType<typeof setTimeout>[] = [];
  private seq = 0;

  constructor(private readonly host: MockMessengerHost) {
    const has = (kind: string) => host.accounts().filter((a) => a.kind === kind).length;
    this.plugins = [
      plugin({ name: 'whatsapp', displayName: 'WhatsApp', channel: 'whatsapp', risk: 'medium', needsRiskAck: true, pairingMethods: ['qr', 'phone'],
        enabled: has('whatsapp') > 0, riskAcceptedAt: has('whatsapp') ? '2026-10-01T09:00:00Z' : undefined, accounts: has('whatsapp'),
        status: { state: has('whatsapp') ? 'running' : 'stopped', version: '0.1.0', pid: has('whatsapp') ? 4711 : undefined, restarts: 0,
          capabilities: [...caps, 'e2ee'], startedAt: '2026-11-03T07:58:00Z' } }),
      plugin({ name: 'signal', displayName: 'Signal (signal-cli)', channel: 'signal', pairingMethods: ['qr'], settingKeys: ['signal_cli_path'],
        enabled: has('signal') > 0, accounts: has('signal'),
        status: { state: 'crashed', version: '0.1.0', restarts: 2, lastError: 'signal-cli exited: exit status 1', lastCrash: '2026-11-03T08:12:00Z',
          nextRetry: '2026-11-03T08:12:04Z', capabilities: [...caps, 'e2ee'] } }),
      plugin({ name: 'telegram', displayName: 'Telegram', channel: 'telegram', official: true, pairingMethods: ['qr', 'phone'],
        settingKeys: ['api_id', 'api_hash'], enabled: has('telegram') > 0, accounts: has('telegram'),
        status: { state: has('telegram') ? 'running' : 'stopped', version: '0.1.0', pid: 4712, restarts: 0, capabilities: [...caps, 'history', 'threads'] } }),
      plugin({ name: 'matrix', displayName: 'Matrix', channel: 'matrix', official: true, pairingMethods: ['password', 'token', 'bridge'],
        settingKeys: ['default_homeserver'], status: { state: 'stopped', version: '0.1.0', restarts: 0, capabilities: [...caps, 'bridge', 'e2ee'],
          updateHint: undefined } }),
    ];
  }

  private find(name: string): MessengerPlugin {
    const p = this.plugins.find((x) => x.name === name);
    if (!p) throw new ApiError('unknown plugin', 404, 'not_found');
    return p;
  }

  list(): MessengerPlugin[] {
    for (const p of this.plugins) p.accounts = this.host.accounts().filter((a) => a.extra?.plugin === p.name || a.kind === kindOf[p.name]).length;
    return this.plugins;
  }

  update(name: string, patch: MessengerPluginPatch): MessengerPlugin {
    const p = this.find(name);
    if (patch.acceptRisk && !p.riskAcceptedAt) p.riskAcceptedAt = new Date().toISOString();
    if (patch.enabled !== undefined) {
      if (patch.enabled && p.needsRiskAck && !p.riskAcceptedAt) {
        throw new ApiError('the risk notice of this plugin must be acknowledged', 409, 'risk_not_accepted');
      }
      p.enabled = patch.enabled;
      p.status = { ...p.status, state: patch.enabled ? 'running' : 'stopped', pid: patch.enabled ? 4800 + this.plugins.indexOf(p) : undefined };
    }
    for (const [k, v] of Object.entries(patch.settings ?? {})) {
      if (v) p.settings[k] = v;
      else delete p.settings[k];
    }
    this.host.emit({ type: 'messenger.plugin', name });
    return p;
  }

  restart(name: string): void {
    const p = this.find(name);
    if (!p.enabled) throw new ApiError('messenger plugin is not enabled', 409, 'messenger_disabled');
    p.status = { ...p.status, state: 'starting', lastError: undefined, nextRetry: undefined };
    this.later(300, () => {
      p.status = { ...p.status, state: 'running', pid: 4900 + this.seq++, startedAt: new Date().toISOString() };
      this.host.emit({ type: 'messenger.plugin', name });
    });
    this.host.emit({ type: 'messenger.plugin', name });
  }

  private later(ms: number, f: () => void): void {
    this.timers.push(setTimeout(f, ms));
  }

  private set(id: string, patch: Partial<MessengerPairing>): void {
    const cur = this.pairings.get(id);
    if (!cur || cur.state === 'cancelled' || cur.state === 'success' || cur.state === 'error') return;
    Object.assign(cur, patch);
    const { input: _input, ...pairing } = cur;
    this.host.emit({ type: 'messenger.pairing', pairing: { ...pairing } });
  }

  start(input: MessengerPairingInput): MessengerPairing {
    const p = this.find(input.plugin);
    if (!p.enabled) throw new ApiError('messenger plugin is not enabled', 409, 'messenger_disabled');
    if (!p.pairingMethods.includes(input.method)) throw new ApiError(`${p.name} does not support ${input.method}`, 400, 'invalid_input');
    const id = `pair_mock${++this.seq}`;
    const pr = { id, plugin: p.name, method: input.method, network: input.network, state: 'waiting' as const, input };
    this.pairings.set(id, pr);
    const step = 250;
    switch (input.method) {
      case 'qr':
      case 'bridge':
        this.later(step, () => {
          this.set(id, {
            state: 'qr',
            qr: p.name === 'signal' ? 'sgnl://linkdevice?uuid=mock-uuid&pub_key=BQ7mock' : `2@TuxMailMockPairing${id},Kx9q2,AbCdEf==,mock`,
            expiresAt: new Date(Date.now() + 60_000).toISOString(),
          });
          if (this.host.autoAcceptMs > 0) this.later(this.host.autoAcceptMs, () => this.succeed(id));
        });
        break;
      case 'phone':
        if (p.name === 'telegram') this.later(step, () => this.set(id, { state: 'input', field: 'code', message: 'Login code sent by Telegram' }));
        else
          this.later(step, () => {
            this.set(id, { state: 'code', code: 'TUX4-M41L', expiresAt: new Date(Date.now() + 180_000).toISOString() });
            if (this.host.autoAcceptMs > 0) this.later(this.host.autoAcceptMs, () => this.succeed(id));
          });
        break;
      default:
        this.later(step * 2, () => (input.password === 'wrong' ? this.set(id, { state: 'error', message: 'login failed: M_FORBIDDEN' }) : this.succeed(id)));
    }
    const { input: _i, ...out } = pr;
    return out;
  }

  get(id: string): MessengerPairing {
    const p = this.pairings.get(id);
    if (!p) throw new ApiError('unknown pairing', 404, 'not_found');
    const { input: _i, ...out } = p;
    return out;
  }

  submit(id: string, field: string, value: string): void {
    const p = this.pairings.get(id);
    if (!p) throw new ApiError('unknown pairing', 404, 'not_found');
    if (field === 'code' && p.plugin === 'telegram') {
      if (value === '00000') this.later(150, () => this.set(id, { state: 'input', field: 'password', message: 'Two-step verification password' }));
      else this.later(150, () => this.succeed(id));
    } else {
      this.later(150, () => this.succeed(id));
    }
  }

  cancel(id: string): void {
    this.set(id, { state: 'cancelled' });
  }

  private succeed(id: string): void {
    const p = this.pairings.get(id);
    if (!p || p.state === 'success' || p.state === 'cancelled' || p.state === 'error') return;
    if (p.method === 'bridge') {
      this.set(id, { state: 'success', qr: undefined, accountId: p.input.bridgeAccountId });
      return;
    }
    const address =
      p.input.phone ?? (p.plugin === 'matrix' ? `@${p.input.username ?? 'anna'}:${(p.input.homeserver ?? 'matrix.org').replace(/^https?:\/\//, '')}` : '+49 170 9988776');
    const acc = this.host.addAccount({
      kind: kindOf[p.plugin] ?? 'whatsapp',
      displayName: p.input.displayName || `${this.find(p.plugin).displayName.replace(/ \(.*\)$/, '')} ${address}`,
      color: colorOf[p.plugin],
      enabled: true,
      extra: { address, plugin: p.plugin, mode: 'direct' },
    });
    this.set(id, { state: 'success', qr: undefined, code: undefined, field: undefined, accountId: acc.id });
  }

  accounts(): MessengerAccountState[] {
    return this.host
      .accounts()
      .filter((a) => kindOf[a.kind] || a.kind === 'matrix_bridge')
      .map((a) => ({
        accountId: a.id,
        plugin: a.extra?.plugin ?? (a.kind === 'matrix_bridge' ? 'matrix' : a.kind),
        mode: a.kind === 'matrix_bridge' ? ('bridge' as const) : ('direct' as const),
        state: a.kind === 'signal' ? 'error' : 'connected',
        lastError: a.kind === 'signal' ? 'signal-cli exited' : undefined,
        syncedAt: '2026-11-03T08:00:00Z',
      }));
  }

  remove(accountId: number): void {
    this.host.removeAccount(accountId);
  }

  dispose(): void {
    for (const t of this.timers) clearTimeout(t);
    this.timers = [];
  }
}
