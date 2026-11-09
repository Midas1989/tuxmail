// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
// Einstellungen › Mail › Speicher & offline (mail.headersOnlyOlderThanDays) and
// the "nur Kopf" behaviour of the demo transport.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { button, cleanup, render, until } from '../security/mountUtil';
import { MockTransport } from '../../api/mock';
import type { MailStorageSettings as Settings } from '../../types';
import { visibleCategories, ALL_ENV } from '../../settings/catalog';
import en from '$locales/en.json';
import de from '$locales/de.json';

const m = vi.hoisted(() => ({
  state: { headersOnlyOlderThanDays: -1, available: true, headersOnly: 3, complete: 40 } as Settings,
  set: vi.fn(),
  estimate: { messages: 0, bytes: 0 },
  serverMode: false,
}));

vi.mock('../../state', () => ({
  app: {
    api: {
      kind: 'wails',
      getMailStorageSettings: async () => ({ ...m.state }),
      setMailStorageSettings: async (s: { headersOnlyOlderThanDays: number; downloadPaused?: boolean }) => {
        m.set(s);
        m.state = { ...m.state, headersOnlyOlderThanDays: s.headersOnlyOlderThanDays, downloadPaused: !!s.downloadPaused };
        return { ...m.state };
      },
      estimateMailStorage: async () => ({ ...m.estimate }),
    },
  },
  connection: {
    get serverMode() {
      return m.serverMode;
    },
  },
}));

import MailStorageSettings from './MailStorageSettings.svelte';

beforeEach(() => {
  vi.clearAllMocks();
  m.state = { headersOnlyOlderThanDays: -1, available: true, headersOnly: 3, complete: 40 };
  m.estimate = { messages: 0, bytes: 0 };
  m.serverMode = false;
});
afterEach(cleanup);

const q = <T extends Element>(root: ParentNode, sel: string) => root.querySelector(sel) as T;

describe('MailStorageSettings', () => {
  const radio = (root: ParentNode, m: string) => q<HTMLInputElement>(root, `[data-testid="storage-mode-${m}"]`);

  it('explains the setting and keeps the previous behaviour by default', async () => {
    const root = render(MailStorageSettings, {});
    await until(() => expect(radio(root, 'default')).toBeTruthy());
    const text = root.textContent!;
    expect(text).toContain(en.settings.storage.intro);
    expect(text).toContain(en.settings.storage.off);
    expect(text).toContain(en.settings.storage.exceptions);
    expect(radio(root, 'default').checked).toBe(true);
    expect(q<HTMLInputElement>(root, '[data-testid="storage-days"]').disabled).toBe(true);
    expect(button(root, en.common.action.save).disabled).toBe(true);
    expect(text).not.toContain(en.settings.storage.thin);
  });

  it('saves a free day count, "all completely" and back to the default', async () => {
    const root = render(MailStorageSettings, {});
    await until(() => expect(radio(root, 'days')).toBeTruthy());
    radio(root, 'days').click();
    flushSync();
    const days = q<HTMLInputElement>(root, '[data-testid="storage-days"]');
    days.value = '45';
    days.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    expect(root.textContent).toContain(en.settings.storage.on.replace('{days}', '45'));
    button(root, en.common.action.save).click();
    await until(() => expect(m.set).toHaveBeenCalledWith({ headersOnlyOlderThanDays: 45, downloadPaused: false }));
    await until(() => expect(root.textContent).toContain(en.settings.storage.saved));
    radio(root, 'all').click();
    flushSync();
    expect(root.textContent).toContain(en.settings.storage.all);
    button(root, en.common.action.save).click();
    await until(() => expect(m.set).toHaveBeenLastCalledWith({ headersOnlyOlderThanDays: 0, downloadPaused: false }));
    radio(root, 'default').click();
    flushSync();
    button(root, en.common.action.save).click();
    await until(() => expect(m.set).toHaveBeenLastCalledWith({ headersOnlyOlderThanDays: -1, downloadPaused: false }));
  });

  it('refuses an invalid day count', async () => {
    const root = render(MailStorageSettings, {});
    await until(() => expect(radio(root, 'days')).toBeTruthy());
    radio(root, 'days').click();
    flushSync();
    const days = q<HTMLInputElement>(root, '[data-testid="storage-days"]');
    days.value = '0';
    days.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    expect(button(root, en.common.action.save).disabled).toBe(true);
    expect(days.getAttribute('aria-invalid')).toBe('true');
  });

  it('asks with an estimate before downloading more, and shows a pausable progress', async () => {
    m.estimate = { messages: 1234, bytes: 500 * 1024 * 1024 };
    const root = render(MailStorageSettings, {});
    await until(() => expect(radio(root, 'all')).toBeTruthy());
    radio(root, 'all').click();
    flushSync();
    button(root, en.common.action.save).click();
    await until(() => expect(q(root, '[data-testid="storage-confirm"]')).toBeTruthy());
    expect(m.set).not.toHaveBeenCalled();
    expect(root.textContent).toMatch(/1,234|1.234/);
    button(root, en.settings.storage.confirmNo).click();
    flushSync();
    expect(q(root, '[data-testid="storage-confirm"]')).toBeNull();
    expect(m.set).not.toHaveBeenCalled();
    button(root, en.common.action.save).click();
    await until(() => expect(q(root, '[data-testid="storage-confirm"]')).toBeTruthy());
    m.state = { ...m.state, pending: 1234, pendingBytes: 500 * 1024 * 1024 };
    button(root, en.settings.storage.confirmYes).click();
    await until(() => expect(m.set).toHaveBeenCalledWith({ headersOnlyOlderThanDays: 0, downloadPaused: false }));
    await until(() => expect(q(root, '[data-testid="storage-progress"]')).toBeTruthy());
    button(root, en.settings.storage.pause).click();
    await until(() => expect(m.set).toHaveBeenLastCalledWith({ headersOnlyOlderThanDays: 0, downloadPaused: true }));
    await until(() => expect(button(root, en.settings.storage.resume)).toBeTruthy());
  });

  it('mentions the read cache in the mode "Über den Server"', async () => {
    m.serverMode = true;
    const root = render(MailStorageSettings, {});
    await until(() => expect(root.textContent).toContain(en.settings.storage.thin));
  });

  it('is listed under Mail only where this app keeps mail itself', () => {
    const mail = (env: typeof ALL_ENV) => visibleCategories(env).find((c) => c.id === 'mail')!.sections.map((s) => s.id);
    expect(mail(ALL_ENV)).toContain('storage');
    expect(mail({ ...ALL_ENV, mailStore: false })).not.toContain('storage');
  });

  it('has the same texts in German', () => {
    expect(de.settings.storage.intro).toBe(
      'Mails, die älter als X Tage sind, lädt TuxMail nur mit Absender, Betreff, Datum und Vorschau; den Inhalt erst beim Öffnen. Spart Speicher; ohne Netz sind diese Mails nur mit Kopf lesbar.',
    );
    expect(Object.keys(de.settings.storage).sort()).toEqual(Object.keys(en.settings.storage).sort());
    expect(de.mail.reader.contentOffline).toBe('Inhalt nur online verfügbar');
  });
});

describe('demo transport: older mails with header only', () => {
  const api = () => new MockTransport({ latencyMs: 0 });

  it('marks older mails in lists and search, never drafts', async () => {
    const t = api();
    expect((await t.getMailStorageSettings()).headersOnlyOlderThanDays).toBe(-1);
    let page = await t.listMessages({ scope: { kind: 'unified', role: 'inbox' }, offset: 0, limit: 200 });
    expect(page.items.some((x) => x.headersOnly)).toBe(false);
    const s = await t.setMailStorageSettings({ headersOnlyOlderThanDays: 7 });
    expect(s.headersOnly).toBeGreaterThan(0);
    page = await t.listMessages({ scope: { kind: 'unified', role: 'inbox' }, offset: 0, limit: 200 });
    const old = page.items.filter((x) => x.headersOnly);
    expect(old.length).toBeGreaterThan(0);
    for (const x of old) expect(Date.parse(x.date)).toBeLessThan(Date.now() - 7 * 86_400_000);
    const word = old[0].subject.split(/\s+/)[0];
    const res = await t.search({ text: word, mode: 'simple', scopes: ['mail'], accountIds: [], folderIds: [], includeSubfolders: true, excludeTrashSpam: false, location: 'auto', sort: 'relevance' });
    expect(res.hits.find((h) => h.messageId === old[0].id)?.headersOnly).toBe(true);
    await expect(t.setMailStorageSettings({ headersOnlyOlderThanDays: -3 })).rejects.toMatchObject({ code: 'invalid_input' });
  });

  it('opens an older mail offline with header and preview only', async () => {
    const t = api();
    await t.setMailStorageSettings({ headersOnlyOlderThanDays: 7 });
    const page = await t.listMessages({ scope: { kind: 'unified', role: 'inbox' }, offset: 0, limit: 200 });
    const old = page.items.find((x) => x.headersOnly)!;
    const online = await t.getMessage(old.id);
    expect(online.contentUnavailable).toBeFalsy();
    const spy = vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);
    try {
      const d = await t.getMessage(old.id);
      expect(d.contentUnavailable).toBe(true);
      expect(d.text).toBe(old.preview);
      expect(d.html).toBeUndefined();
    } finally {
      spy.mockRestore();
    }
  });

  it('honours the own day count of an account', async () => {
    const t = api();
    await t.setMailStorageSettings({ headersOnlyOlderThanDays: 7 });
    await t.updateAccount(1, { headersOnlyOlderThanDays: 0 });
    const page = await t.listMessages({ scope: { kind: 'unified', role: 'inbox' }, offset: 0, limit: 200 });
    expect(page.items.filter((x) => x.accountId === 1).some((x) => x.headersOnly)).toBe(false);
    await expect(t.updateAccount(1, { headersOnlyOlderThanDays: -2 })).rejects.toMatchObject({ code: 'invalid_input' });
  });
});
