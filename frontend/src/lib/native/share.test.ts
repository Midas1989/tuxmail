// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it, vi } from 'vitest';
import { MAX_SHARE_BYTES, SHARE_EVENT, dispatchShare, shareRecipients, shareSubject, shareTextToHtml, sharedFilesToFiles } from './share';
import { DEFAULT_LOCK, parseLockSettings, shouldLock } from './applock';
import { deviceName } from './device';
import { cacheFileName } from './files';

describe('share target', () => {
  it('turns text into escaped paragraphs', () => {
    expect(shareTextToHtml('Hallo <b>Welt</b>\nZeile 2\n\nAbsatz')).toBe('<p>Hallo &lt;b&gt;Welt&lt;/b&gt;<br>Zeile 2</p><p>Absatz</p>');
    expect(shareTextToHtml('   ')).toBe('');
  });

  it('accepts only plausible mailto recipients', () => {
    expect(shareRecipients([' anna@example.org ', 'nope', 'a@b', 'x@y.z<script>'])).toEqual(['anna@example.org']);
    expect(shareRecipients(undefined)).toEqual([]);
  });

  it('suggests a subject', () => {
    expect(shareSubject({ subject: ' Betreff ', text: 'x', files: [] })).toBe('Betreff');
    expect(shareSubject({ files: [{ name: 'rechnung.pdf', mimeType: 'application/pdf', size: 1, path: '/c/r.pdf' }] })).toBe('rechnung.pdf');
    expect(shareSubject({ text: '\nErste Zeile\nzweite', files: [] })).toBe('Erste Zeile');
    expect(shareSubject({ text: 'https://example.org/artikel', files: [] })).toBe('');
  });

  it('loads shared files through the WebView file URL', async () => {
    const f = vi.fn(async (url: string) => (url.includes('missing') ? new Response('', { status: 404 }) : new Response(new Blob(['abc'], { type: 'image/png' })))) as unknown as typeof fetch;
    const files = await sharedFilesToFiles(
      {
        files: [
          { name: 'a.png', mimeType: 'image/png', size: 3, path: '/data/cache/shared/a.png' },
          { name: 'missing.txt', mimeType: 'text/plain', size: 1, path: '/data/cache/shared/missing.txt' },
          { name: 'huge.bin', mimeType: 'application/octet-stream', size: MAX_SHARE_BYTES + 1, path: '/data/cache/shared/huge.bin' },
        ],
      },
      (p) => 'https://localhost/_capacitor_file_' + p,
      f,
    );
    expect(files.map((x) => [x.name, x.type, x.size])).toEqual([['a.png', 'image/png', 3]]);
    expect(f).toHaveBeenCalledTimes(2);
  });

  it('lets the UI take over via a cancelable event', () => {
    const t = new EventTarget();
    const detail = { text: 'hi', subject: '', to: [], files: [] };
    expect(dispatchShare(detail, t)).toBe(false);
    t.addEventListener(SHARE_EVENT, (e) => {
      expect((e as CustomEvent).detail).toBe(detail);
      e.preventDefault();
    });
    expect(dispatchShare(detail, t)).toBe(true);
  });
});

describe('app lock', () => {
  it('parses settings defensively', () => {
    expect(parseLockSettings(null)).toEqual(DEFAULT_LOCK);
    expect(parseLockSettings('{"enabled":true,"graceSeconds":300}')).toEqual({ enabled: true, graceSeconds: 300 });
    expect(parseLockSettings('{"enabled":"yes","graceSeconds":-5}')).toEqual({ enabled: false, graceSeconds: 0 });
    expect(parseLockSettings('nope')).toEqual(DEFAULT_LOCK);
  });
  it('locks on cold start and after the grace period', () => {
    const on = { enabled: true, graceSeconds: 60 };
    expect(shouldLock(DEFAULT_LOCK, null, 0)).toBe(false);
    expect(shouldLock(on, null, 0)).toBe(true);
    expect(shouldLock(on, 1000, 30_000)).toBe(false);
    expect(shouldLock(on, 1000, 61_000)).toBe(true);
    expect(shouldLock({ enabled: true, graceSeconds: 0 }, 1000, 1000)).toBe(true);
  });
});

describe('deviceName', () => {
  it('uses the model from the user agent', () => {
    expect(deviceName('Mozilla/5.0 (Linux; Android 15; Pixel 8 Build/AP3A.241005.015; wv) AppleWebKit/537.36')).toBe('TuxMail Android (Pixel 8)');
    expect(deviceName('Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36')).toBe('TuxMail Android');
    expect(deviceName('Mozilla/5.0 (X11; Linux x86_64)')).toBe('TuxMail Android');
  });
});

describe('cacheFileName', () => {
  it('strips directories and control characters', () => {
    expect(cacheFileName('../../etc/passwd')).toBe('passwd');
    expect(cacheFileName('C:\\x\\Rechnung?.pdf')).toBe('Rechnung_.pdf');
    expect(cacheFileName('.hidden')).toBe('hidden');
    expect(cacheFileName('')).toBe('file');
  });
});
