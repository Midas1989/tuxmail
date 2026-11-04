// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { ComfortHttp, ComfortWails, comfortFor, fileToBase64 } from './comfort';
import { comfortMock, mockCategory } from './comfortMock';
import { mails } from './mockData';
import type { Api } from './api';

describe('comfort API', () => {
  it('selects the transport from the main API', () => {
    expect(comfortFor({ kind: 'http' } as Api)).toBeInstanceOf(ComfortHttp);
    expect(comfortFor({ kind: 'wails' } as Api)).toBeInstanceOf(ComfortWails);
    const mock = { kind: 'mock' } as Api;
    expect(comfortFor(mock)).toBe(comfortFor(mock));
  });

  it('HTTP transport calls the documented routes', async () => {
    const calls: [string, string, string | undefined][] = [];
    const f = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push([init?.method ?? 'GET', url, init?.body as string | undefined]);
      return new Response(JSON.stringify({ language: 'de', detected: true, misspellings: [] }), { status: 200 });
    }) as unknown as typeof fetch;
    const c = new ComfortHttp('https://mail.example', f, () => 'tok');
    await c.spellCheck('Hallo', 'auto');
    await c.markSpam([1, 2], true, true);
    expect(calls[0]).toEqual(['POST', 'https://mail.example/api/comfort/spell/check', JSON.stringify({ text: 'Hallo', language: 'auto' })]);
    expect(calls[1][1]).toBe('https://mail.example/api/comfort/spam');
    expect(c.printUrl({ conversationId: 7, layout: 'chat' })).toBe('https://mail.example/api/comfort/print?conversationId=7&layout=chat');
    expect(c.pdfUrl(3, true)).toBe('https://mail.example/api/comfort/messages/3/pdf?download=1');
  });

  it('Wails transport maps errors', async () => {
    const w = new ComfortWails({ go: { app: { Api: { ComfortInsights: async () => Promise.reject('{"code":"not_found","message":"not found"}') } } } });
    await expect(w.insights(1)).rejects.toMatchObject({ code: 'not_found' });
    await expect(w.listCategories()).rejects.toMatchObject({ code: 'not_bound' });
  });

  it('mock derives categories, invoices and newsletters', async () => {
    const m = comfortMock();
    const inv = await m.insights(29);
    expect(inv.category).toBe('invoice');
    expect(inv.invoice?.number).toBeTruthy();
    const news = await m.insights(31);
    expect(news.category).toBe('newsletter');
    expect(news.unsubscribe?.oneClick).toBe(true);
    expect((await m.insights(41)).spam.level).toBe('spam');
    await m.setCategory([31], 'personal');
    expect((await m.insights(31)).category).toBe('personal');
    const counts = await m.listCategories('inbox');
    expect(counts.map((c) => c.id)).toEqual(['personal', 'newsletter', 'invoice', 'notification', 'social', 'appointment']);
    expect(mockCategory(mails.find((x) => x.id === 9)!)).toBe('appointment');
    const prev = await m.previewMerge({ identityId: 1, template: { subject: 'Hallo {{vorname}}', text: '{{firma|Kunde}}' }, recipients: [{ email: 'a@b', fields: { vorname: 'Anna' } }] });
    expect(prev[0].subject).toBe('Hallo Anna');
    expect(prev[0].text).toBe('Kunde');
    const sp = await m.spellCheck('Anbei die Rechnug.', 'de');
    expect(sp.misspellings[0]).toMatchObject({ word: 'Rechnug', start: 10, suggestions: ['Rechnung'] });
  });

  it('encodes files as base64', async () => {
    expect(await fileToBase64(new Blob(['hallo']))).toBe(btoa('hallo'));
  });
});
