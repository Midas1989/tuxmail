// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { MockTransport } from './api/mock';
import { MockMessenger } from './api/mockMessenger';
import { qrMatrix } from './qr';
import type { Account, ApiEvent, MessengerPairing } from './types';

function nextPairing(api: MockTransport, id: string, state: MessengerPairing['state']): Promise<MessengerPairing> {
  return new Promise((resolve) => {
    const off = api.subscribe((e: ApiEvent) => {
      if (e.type === 'messenger.pairing' && e.pairing.id === id && e.pairing.state === state) {
        off();
        resolve(e.pairing);
      }
    });
  });
}

describe('messenger plugins (mock transport)', () => {
  it('requires the risk acknowledgment for WhatsApp and pairs by QR code', async () => {
    const api = new MockTransport({ latencyMs: 0, messengerAutoAcceptMs: 30 });
    const plugins = await api.listMessengerPlugins();
    const wa = plugins.find((p) => p.name === 'whatsapp')!;
    expect(wa.needsRiskAck).toBe(true);
    expect(plugins.find((p) => p.name === 'telegram')!.official).toBe(true);
    await api.updateMessengerPlugin('whatsapp', { enabled: true, acceptRisk: true });
    await expect(api.updateMessengerPlugin('matrix', { enabled: true })).resolves.toMatchObject({ enabled: true });

    const before = (await api.listAccounts()).length;
    const p = await api.startMessengerPairing({ plugin: 'whatsapp', method: 'qr' });
    expect(p.state).toBe('waiting');
    const qr = await nextPairing(api, p.id, 'qr');
    expect(qr.qr).toMatch(/^2@/);
    const ok = await nextPairing(api, p.id, 'success');
    expect(ok.accountId).toBeGreaterThan(0);
    expect((await api.listAccounts()).length).toBe(before + 1);
    expect((await api.listMessengerAccounts()).some((a) => a.accountId === ok.accountId)).toBe(true);
  });

  it('asks for the Telegram login code', async () => {
    const api = new MockTransport({ latencyMs: 0, messengerAutoAcceptMs: 0 });
    await api.updateMessengerPlugin('telegram', { enabled: true });
    const p = await api.startMessengerPairing({ plugin: 'telegram', method: 'phone', phone: '+491701234567' });
    const ask = await nextPairing(api, p.id, 'input');
    expect(ask.field).toBe('code');
    const done = nextPairing(api, p.id, 'success');
    await api.submitMessengerPairingInput(p.id, 'code', '12345');
    expect((await done).accountId).toBeGreaterThan(0);
  });

  it('refuses pairing with a disabled plugin and enabling without risk acceptance', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    await expect(api.startMessengerPairing({ plugin: 'matrix', method: 'password' })).rejects.toMatchObject({ code: 'messenger_disabled' });
    // A fresh catalog without WhatsApp accounts: risk not yet accepted.
    const wa = (await api.listMessengerPlugins()).find((p) => p.name === 'whatsapp')!;
    if (!wa.riskAcceptedAt) {
      await expect(api.updateMessengerPlugin('whatsapp', { enabled: true })).rejects.toMatchObject({ code: 'risk_not_accepted' });
    }
  });
});

describe('qrMatrix', () => {
  it('encodes data with a quiet zone', () => {
    const m = qrMatrix('sgnl://linkdevice?uuid=abc&pub_key=def');
    expect(m.size).toBeGreaterThanOrEqual(21);
    expect(m.viewBox).toBe(m.size + 8);
    expect(m.path).toMatch(/^M\d+ \d+h1v1h-1z/);
    // Finder pattern: the top-left module (after the quiet zone) is dark.
    expect(m.path.startsWith('M4 4h1v1h-1z')).toBe(true);
  });

  it('reports a missing signal-cli as component_missing and installs it on demand', async () => {
    const listeners: ((e: ApiEvent) => void)[] = [];
    const accounts: Account[] = [];
    const m = new MockMessenger({
      accounts: () => accounts,
      addAccount: (a) => {
        const acc = { ...a, id: accounts.length + 1, sortOrder: accounts.length } as Account;
        accounts.push(acc);
        return acc;
      },
      removeAccount: () => {},
      emit: (e) => listeners.forEach((l) => l(e)),
      autoAcceptMs: 0,
    });
    const until = (pred: (e: ApiEvent) => boolean) =>
      new Promise<ApiEvent>((resolve) => {
        const l = (e: ApiEvent) => {
          if (pred(e)) {
            listeners.splice(listeners.indexOf(l), 1);
            resolve(e);
          }
        };
        listeners.push(l);
      });
    const signal = () => m.list().find((p) => p.name === 'signal')!;
    m.update('signal', { enabled: true });
    expect(signal().component).toMatchObject({ state: 'missing', installable: true });

    const p = m.start({ plugin: 'signal', method: 'qr' });
    const failed = await until((e) => e.type === 'messenger.pairing' && e.pairing.id === p.id && e.pairing.state === 'error');
    expect(failed.type === 'messenger.pairing' && failed.pairing.reason).toBe('component_missing');

    const ready = until((e) => e.type === 'messenger.plugin' && signal().component?.state === 'ready');
    m.installComponent('signal');
    expect(signal().component?.state).toBe('installing');
    expect(() => m.installComponent('signal')).toThrow(/already running/);
    await ready;
    expect(signal().component).toMatchObject({ state: 'ready', version: '0.13.18' });
    const again = m.start({ plugin: 'signal', method: 'qr' });
    const qr = await until((e) => e.type === 'messenger.pairing' && e.pairing.id === again.id && e.pairing.state === 'qr');
    expect(qr.type === 'messenger.pairing' && qr.pairing.qr).toMatch(/^sgnl:\/\/linkdevice/);
  });
});
