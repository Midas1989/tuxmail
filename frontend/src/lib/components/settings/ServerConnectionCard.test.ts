// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { flushSync } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ConnectionState, connection as globalConnection } from '../../state/connection.svelte';
import type { DesktopWin } from '../../desktop/bridge';
import type { ConnectionStatus } from '../../types';
import ServerConnectionCard from './ServerConnectionCard.svelte';
import ServerModeChoice from './ServerModeChoice.svelte';
import ConnectionBar from '../common/ConnectionBar.svelte';
import { button, cleanup, click, render, until } from '../security/mountUtil';
import en from '$locales/en.json';
import de from '$locales/de.json';

afterEach(() => {
  cleanup();
  globalConnection.status = { connected: false, state: 'none', cacheBytes: 0, outbox: [], settingsLog: [] };
});

const base: ConnectionStatus = {
  connected: true,
  serverUrl: 'https://mail.example.de',
  serverName: 'mail.example.de',
  mode: 'thin',
  username: 'anna',
  displayName: 'Anna',
  deviceName: 'TuxMail Desktop (pc)',
  state: 'online',
  connectedAt: '2026-01-01T10:00:00Z',
  lastConnectedAt: '2026-01-02T10:00:00Z',
  lastSettingsSyncAt: '2026-01-02T11:00:00Z',
  cacheBytes: 2048,
  outbox: [],
  settingsLog: [],
};

function conn(status: ConnectionStatus, api: Record<string, (...a: never[]) => Promise<unknown>> = {}) {
  const reload = vi.fn();
  const win = { go: { app: { Api: { ServerConnectionStatus: async () => status, ...api } } } } as unknown as DesktopWin;
  const c = new ConnectionState(win, reload);
  c.status = status;
  return { c, reload };
}

describe('status', () => {
  it('not connected: offers to connect', () => {
    const { c } = conn({ ...base, connected: false, state: 'none' });
    const root = render(ServerConnectionCard, { conn: c });
    click(button(root, en.onboarding.connect.title));
    flushSync();
    expect(c.wizard).toEqual({ purpose: 'connect' });
  });
  it('shows server, user, mode, state and times', () => {
    const { c } = conn(base);
    const text = render(ServerConnectionCard, { conn: c }).textContent!;
    expect(text).toContain('mail.example.de');
    expect(text).toContain('Anna');
    expect(text).toContain(en.onboarding.connect.modeThin);
    expect(text).toContain(en.connection.state.online);
    expect(text).toContain(en.connection.lastConnection);
    expect(text).toContain('TuxMail Desktop (pc)');
  });
  it('says administration is only available in the browser', () => {
    const root = render(ServerConnectionCard, { conn: conn(base).c });
    expect(root.querySelector('[data-testid="admin-note"]')?.textContent).toContain(en.connection.adminBrowser);
  });
  it('mode on this device: shows the settings sync and its log', () => {
    const { c } = conn({ ...base, mode: 'local', settingsLog: [{ at: '2026-01-02T11:00:00Z', kind: 'signature', name: 'Firma', action: 'fromServer', conflict: true }] });
    const root = render(ServerConnectionCard, { conn: c });
    expect(root.querySelector('[data-testid="last-settings-sync"]')).not.toBeNull();
    expect(root.textContent).toContain('Firma');
    expect(root.textContent).toContain(en.connection.log.conflict);
  });
  it('offline: the state is text too, changing the mode is switched off', () => {
    const root = render(ServerConnectionCard, { conn: conn({ ...base, state: 'offline' }).c });
    expect(root.querySelector('[data-testid="server-state"]')?.textContent).toBe(en.connection.state.offline);
    expect(button(root, en.connection.changeMode).disabled).toBe(true);
    expect(button(root, en.connection.syncNow).disabled).toBe(false);
  });
  it('expired: clear message and sign in again, disconnecting stays possible', () => {
    const { c } = conn({ ...base, state: 'expired' });
    const root = render(ServerConnectionCard, { conn: c });
    expect(root.querySelector('[role="alert"]')?.textContent).toContain(en.connection.expiredTitle);
    expect(root.textContent).toContain(en.connection.expiredText);
    click(button(root, en.connection.reauth));
    expect(c.wizard).toEqual({ purpose: 'reauth', serverUrl: base.serverUrl });
    expect(button(root, en.connection.disconnect).disabled).toBe(false);
  });
  it('shows the outbox with what to do', async () => {
    const discard = vi.fn(async () => {});
    const { c } = conn({ ...base, outbox: [{ id: 'a', kind: 'send', title: 'Hallo', recipients: 'b@x', state: 'pending', createdAt: '2026-01-01T00:00:00Z' }, { id: 'b', kind: 'send', title: 'Zweifel', state: 'in_doubt', createdAt: '2026-01-01T00:00:00Z' }] }, { ServerOutboxDiscard: discard });
    const root = render(ServerConnectionCard, { conn: c });
    const box = root.querySelector('[data-testid="outbox"]')!;
    expect(box.textContent).toContain('Hallo');
    expect(box.textContent).toContain(en.connection.outbox.inDoubt);
    expect(box.textContent).toContain(en.connection.outbox.sendAgain);
    click(button(box as HTMLElement, en.connection.outbox.discard));
    await until(() => expect(discard).toHaveBeenCalledWith('a'));
  });
});

describe('what waits for the user', () => {
  const rule = { id: 'r1', name: 'Sortieren', isNew: false, wasEnabled: true, before: ['archive'], after: ['delete', 'run_workflow'], external: true };
  it('shows rules waiting for approval, with the change, and releases them only on request', async () => {
    const approve = vi.fn(async () => {});
    const { c } = conn({ ...base, mode: 'local', attention: { pendingWorkflows: [rule], conflicts: [], deletes: [] } }, { ServerWorkflowApprove: approve });
    const root = render(ServerConnectionCard, { conn: c });
    expect(root.querySelector('[data-testid="pending-title"]')?.textContent).toContain('1 rule waiting');
    const box = root.querySelector('[data-testid="pending-rules"]') as HTMLElement;
    expect(box.textContent).toContain('delete, run_workflow');
    expect(box.textContent).toContain('archive');
    expect(box.textContent).toContain(en.connection.rules.wasOn);
    expect(approve).not.toHaveBeenCalled();
    click(button(box, en.connection.rules.approve));
    await until(() => expect(approve).toHaveBeenCalledWith('r1'));
  });
  it('asks on a conflict of the first sync', async () => {
    const resolve = vi.fn(async () => {});
    const { c } = conn({ ...base, mode: 'local', attention: { pendingWorkflows: [], conflicts: [{ id: 'r1', name: 'Sortieren', local: ['archive'], server: ['delete'] }], deletes: [] } }, { ServerWorkflowResolve: resolve });
    const box = render(ServerConnectionCard, { conn: c }).querySelector('[data-testid="rule-conflicts"]') as HTMLElement;
    click(button(box, en.connection.rules.keepMine));
    await until(() => expect(resolve).toHaveBeenCalledWith('r1', true));
  });
  it('asks before deleting a lot at once', async () => {
    const decide = vi.fn(async () => {});
    const { c } = conn({ ...base, mode: 'local', attention: { pendingWorkflows: [], conflicts: [], deletes: [{ kind: 'signature', names: ['A', 'B', 'C'] }] } }, { ServerSettingsDeletes: decide });
    const box = render(ServerConnectionCard, { conn: c }).querySelector('[data-testid="mass-deletes"]') as HTMLElement;
    expect(box.textContent).toContain('A, B, C');
    click(button(box, en.connection.deletes.keep));
    await until(() => expect(decide).toHaveBeenCalledWith('signature', false));
  });
  it('a mail stopped by the send checks shows why and can go back as a draft', () => {
    const f = { check: 'forgotten_attachment', severity: 'warn' as const, key: 'k', message: 'Attachment forgotten?' };
    const { c } = conn({ ...base, outbox: [{ id: 'a', kind: 'send', title: 'Hallo', state: 'blocked', findings: [f], createdAt: '2026-01-01T00:00:00Z' }] });
    const box = render(ServerConnectionCard, { conn: c }).querySelector('[data-testid="outbox"]') as HTMLElement;
    expect(box.textContent).toContain('Attachment forgotten?');
    expect(box.textContent).toContain(en.connection.outbox.asDraft);
    expect(box.textContent).toContain(en.connection.outbox.sendAgain);
  });
});

describe('disconnect', () => {
  it('asks first and says what stays; then removes the connection and reloads', async () => {
    const disconnect = vi.fn(async () => ({ revoked: true }));
    const { c, reload } = conn(base, { ServerDisconnect: disconnect });
    const root = render(ServerConnectionCard, { conn: c });
    click(button(root, en.connection.disconnect));
    flushSync();
    const dlg = document.querySelector('[data-testid="leave-dialog"]')!;
    expect(dlg.textContent).toContain(en.connection.leave.server);
    expect(dlg.textContent).toContain(en.connection.leave.local);
    expect(disconnect).not.toHaveBeenCalled();
    const buttons = [...document.querySelectorAll<HTMLButtonElement>('.btn-danger')];
    click(buttons[buttons.length - 1]);
    await until(() => expect(disconnect).toHaveBeenCalledWith({ deleteCache: true, discardOutbox: false }));
    await until(() => expect(reload).toHaveBeenCalled());
  });
  it('waiting mails need a decision', () => {
    const { c } = conn({ ...base, outbox: [{ id: 'a', kind: 'send', state: 'pending', createdAt: '2026-01-01T00:00:00Z' }] });
    const root = render(ServerConnectionCard, { conn: c });
    click(button(root, en.connection.disconnect));
    flushSync();
    const danger = [...document.querySelectorAll<HTMLButtonElement>('.btn-danger')].pop()!;
    expect(danger.disabled).toBe(true);
  });
  it('offline: says the device is still listed at the server', async () => {
    const { c, reload } = conn(base, { ServerDisconnect: async () => ({ revoked: false }) });
    const root = render(ServerConnectionCard, { conn: c });
    click(button(root, en.connection.disconnect));
    flushSync();
    click([...document.querySelectorAll<HTMLButtonElement>('.btn-danger')].pop()!);
    await until(() => expect(document.body.textContent).toContain(en.connection.leave.notRevoked));
    expect(reload).not.toHaveBeenCalled();
  });
});

describe('the choice of the mode', () => {
  it('uses plain words, recommends the server, says web and Android keep working', () => {
    const root = render(ServerModeChoice, { mode: 'thin' });
    const text = root.textContent!;
    expect(text).toContain(en.onboarding.connect.modeThin);
    expect(text).toContain(en.onboarding.connect.modeLocal);
    expect(text).toContain(en.connection.mode.recommended);
    expect(text).toContain(en.connection.mode.both);
    expect(text).toContain(en.connection.mode.thin3);
    expect(text).toContain(en.connection.mode.local4);
  });
  it('says in one sentence that the offline content is unencrypted', () => {
    expect(render(ServerModeChoice, { mode: 'thin' }).textContent).toContain(en.connection.mode.thinCache);
  });
  it('warns about POP3 only where it matters', () => {
    expect(render(ServerModeChoice, { mode: 'thin', pop3: true }).querySelector('[data-testid="pop3-warning"]')).toBeNull();
    expect(render(ServerModeChoice, { mode: 'local', pop3: false }).querySelector('[data-testid="pop3-warning"]')).toBeNull();
    expect(render(ServerModeChoice, { mode: 'local', pop3: true }).querySelector('[data-testid="pop3-warning"]')).not.toBeNull();
  });
  it('the texts are as agreed, in both languages, and never say "Thin Client"', () => {
    expect(de.onboarding.connect.modeThin).toBe('Über den Server');
    expect(de.onboarding.connect.modeLocal).toBe('Auf diesem Gerät');
    expect(en.onboarding.connect.modeThin).toBe('Through the server');
    for (const l of [de, en]) {
      expect(JSON.stringify(l.onboarding.connect).toLowerCase()).not.toContain('thin client');
      expect(JSON.stringify(l.connection).toLowerCase()).not.toContain('thin client');
    }
  });
});

describe('offline in the app', () => {
  it('derives offline only from the server mode', () => {
    expect(conn({ ...base, state: 'offline' }).c.offline).toBe(true);
    expect(conn({ ...base, state: 'expired' }).c.offline).toBe(true);
    expect(conn(base).c.offline).toBe(false);
    const local = conn({ ...base, mode: 'local', state: 'offline' }).c;
    expect(local.offline).toBe(false);
    expect(local.unreachable).toBe(true);
  });
  it('no core (browser, Android): never offline, nothing to show', () => {
    const c = new ConnectionState({}, () => {});
    expect(c.available).toBe(false);
    expect(c.offline).toBe(false);
    expect(c.start()).toBeTypeOf('function');
  });
  it('the bar counts waiting mails', () => {
    globalConnection.status = { ...base, state: 'offline', outbox: [{ id: 'a', kind: 'send', state: 'pending', createdAt: '' }, { id: 'b', kind: 'action', state: 'pending', createdAt: '' }] };
    const bar = render(ConnectionBar, {} as never).querySelector('[data-testid="connection-bar"]')!;
    expect(bar.textContent).toContain(en.connection.bar.offline);
    expect(bar.textContent).toContain('1 mail waiting');
  });
  it('the bar stays away while online', () => {
    globalConnection.status = base;
    expect(render(ConnectionBar, {} as never).querySelector('[data-testid="connection-bar"]')).toBeNull();
  });
  it('the bar offers signing in again', () => {
    globalConnection.status = { ...base, state: 'expired' };
    const root = render(ConnectionBar, {} as never);
    const b = button(root, en.connection.reauth);
    click(b);
    expect(globalConnection.wizard).toEqual({ purpose: 'reauth', serverUrl: base.serverUrl });
    globalConnection.wizard = null;
  });
});
