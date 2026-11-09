// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { MockTransport } from './api/mock';
import { accountSourceKind, conversationSources, folderExcluded, minLevel, sourceLevel } from './aiaccess';
import type { AiSettings } from './types';

const base: AiSettings = { provider: 'ollama', baseUrl: '', model: '', apiKeySet: false, accountPermissions: {}, maskPersonalData: true, accessVersion: 2 };

describe('AI access', () => {
  it('levels: absent is off, the admin cap limits', () => {
    expect(sourceLevel({ ...base, sources: {} }, 'account:1')).toBe('off');
    expect(sourceLevel({ ...base, sources: { 'account:1': { level: 'content' } } }, 'account:1')).toBe('content');
    expect(sourceLevel({ ...base, maxLevel: 'metadata', sources: { 'account:1': { level: 'content' } } }, 'account:1')).toBe('metadata');
    expect(minLevel('content', 'off')).toBe('off');
    expect(minLevel(undefined)).toBe('off');
    expect(sourceLevel(null, 'account:1')).toBe('off');
  });

  it('junk and trash are excluded unless allowed; chosen folders always', () => {
    expect(folderExcluded({ level: 'content' }, 5, 'junk')).toBe(true);
    expect(folderExcluded({ level: 'content' }, 5, 'trash')).toBe(true);
    expect(folderExcluded({ level: 'content', allowJunkTrash: true }, 5, 'junk')).toBe(false);
    expect(folderExcluded({ level: 'content', excludeFolders: [7] }, 7, 'inbox')).toBe(true);
    expect(folderExcluded({ level: 'content' }, 7, 'inbox')).toBe(false);
  });

  it('kinds of accounts', () => {
    expect(accountSourceKind('imap')).toBe('mail');
    expect(accountSourceKind('signal')).toBe('messenger');
    expect(accountSourceKind('caldav')).toBeNull();
  });

  it('mock: every account and area is listed, new ones off, per-source enforcement', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    let s = await api.getAiSettings();
    const kinds = new Set(s.catalog!.map((c) => c.kind));
    for (const k of ['mail', 'messenger', 'calendar', 'addressbook', 'projects']) expect(kinds).toContain(k);
    expect(s.catalog!.every((c) => c.level === 'off')).toBe(true);
    s = await api.saveAiSettings({ ...s, provider: 'ollama', sources: { 'account:4': { level: 'metadata' } } });
    expect(s.catalog!.find((c) => c.key === 'account:4')?.level).toBe('metadata');
    expect(conversationSources(s, [4, 3])).toEqual([
      { key: 'account:4', name: 'Signal', level: 'metadata' },
      { key: 'account:3', name: 'WhatsApp', level: 'off' },
    ]);
    // a client without sources (version 1) can only switch off
    s = await api.saveAiSettings({ ...s, sources: undefined, accessVersion: undefined, accountPermissions: { '4': false, '3': true } });
    expect(s.sources!['account:4'].level).toBe('off');
    expect(s.sources!['account:3']).toBeUndefined();
  });
});
