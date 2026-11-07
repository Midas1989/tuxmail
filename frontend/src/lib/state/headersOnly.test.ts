// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
// Opening a mail kept with header only ("nur Kopf"): header and preview at
// once, a loading state while the content arrives, no prefetch of neighbours,
// "Erneut laden" after "Inhalt nur online verfügbar".
import { describe, expect, it, vi } from 'vitest';
import { AppState } from './app.svelte';
import { MockTransport } from '../api/mock';
import type { MessageDetail, MessageSummary } from '../types';

function summary(id: number, headersOnly: boolean): MessageSummary {
  return { id, accountId: 1, folderIds: [1], conversationId: id, messageId: `m${id}@x`, subject: `Betreff ${id}`, from: [{ name: 'Ben', email: 'ben@example.org' }], to: [], cc: [], date: '2025-01-02T10:00:00Z', preview: `Vorschau ${id}`, flags: ['\\Seen'], hasAttachments: false, size: 10, headersOnly };
}

function setup() {
  const api = new MockTransport({ latencyMs: 0 });
  let release: (d: MessageDetail) => void = () => {};
  const get = vi.spyOn(api, 'getMessage').mockImplementation(
    (id: number) =>
      new Promise<MessageDetail>((res) => {
        release = (d) => res(d);
        void id;
      }),
  );
  const app = new AppState(api);
  app.messages = [summary(1, false), summary(2, true), summary(3, true)];
  return { app, get, release: (d: MessageDetail) => release(d) };
}

describe('opening a mail kept with header only', () => {
  it('shows header and preview while the content loads', async () => {
    const { app, get, release } = setup();
    const p = app.selectMessage(2);
    expect(app.contentLoadingId).toBe(2);
    expect(app.message?.subject).toBe('Betreff 2');
    expect(app.message?.text).toBe('Vorschau 2');
    release({ ...summary(2, false), inReplyTo: [], references: [], attachments: [], text: 'Der ganze Inhalt' });
    await p;
    expect(app.contentLoadingId).toBeNull();
    expect(app.message?.text).toBe('Der ganze Inhalt');
    // Neighbours: only the complete mail 1 is warmed, not the header-only mail 3.
    const prefetched = get.mock.calls.map((c) => c[0]);
    expect(prefetched).toContain(1);
    expect(prefetched).not.toContain(3);
  });

  it('offline: "content only online", then loads again', async () => {
    const { app, release } = setup();
    const p = app.selectMessage(3);
    release({ ...summary(3, true), inReplyTo: [], references: [], attachments: [], text: 'Vorschau 3', contentUnavailable: true });
    await p;
    expect(app.message?.contentUnavailable).toBe(true);
    const again = app.reloadMessage();
    expect(app.contentLoadingId).toBe(3);
    release({ ...summary(3, false), inReplyTo: [], references: [], attachments: [], text: 'Jetzt da' });
    await again;
    expect(app.message?.contentUnavailable).toBeFalsy();
    expect(app.message?.text).toBe('Jetzt da');
  });

  it('a complete mail opens as before, without a loading state', async () => {
    const { app, release } = setup();
    const p = app.selectMessage(1);
    expect(app.contentLoadingId).toBeNull();
    release({ ...summary(1, false), inReplyTo: [], references: [], attachments: [], text: 'x' });
    await p;
    expect(app.message?.id).toBe(1);
  });
});
