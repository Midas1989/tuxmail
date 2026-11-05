// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it } from 'vitest';
import { safeAvatarUrl } from './avatar';

const png = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';

describe('safeAvatarUrl (NG-09)', () => {
  it('accepts raster data: images, blob: and same-origin /api/ paths', () => {
    expect(safeAvatarUrl(png)).toBe(png);
    expect(safeAvatarUrl('data:image/jpeg;base64,/9j/4AAQ')).toBe('data:image/jpeg;base64,/9j/4AAQ');
    expect(safeAvatarUrl('blob:https://mail.example.org/1234-abcd')).toBe('blob:https://mail.example.org/1234-abcd');
    expect(safeAvatarUrl('/api/contacts/7/photo')).toBe('/api/contacts/7/photo');
  });

  it('rejects remote URLs, svg, scripts and protocol-relative tricks', () => {
    for (const bad of [
      'https://tracker.example/a.png',
      'http://tracker.example/a.png',
      '//tracker.example/api/x.png',
      '/\\tracker.example/api/x',
      'data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+',
      'data:image/svg+xml,<svg onload=alert(1)>',
      'data:text/html;base64,PHNjcmlwdD4=',
      'javascript:alert(1)',
      'file:///etc/passwd',
      '/other/path.png',
      '/api/../etc/passwd',
      'ftp://x/a.png',
      '',
      '   ',
    ]) {
      expect(safeAvatarUrl(bad), bad).toBeUndefined();
    }
    expect(safeAvatarUrl(undefined)).toBeUndefined();
    expect(safeAvatarUrl(null)).toBeUndefined();
  });

  it('native app: only /api/ URLs of the configured server (transport makes paths absolute)', () => {
    const base = 'https://mail.example.org';
    expect(safeAvatarUrl('https://mail.example.org/api/contacts/7/photo', base)).toBe('https://mail.example.org/api/contacts/7/photo');
    expect(safeAvatarUrl('https://mail.example.org/tuxmail/api/x', base + '/tuxmail')).toBe('https://mail.example.org/tuxmail/api/x');
    expect(safeAvatarUrl('https://mail.example.org.evil.tld/api/x', base)).toBeUndefined();
    expect(safeAvatarUrl('https://mail.example.org@evil.tld/api/x', base)).toBeUndefined();
    expect(safeAvatarUrl('https://mail.example.org/files/x.png', base)).toBeUndefined();
    expect(safeAvatarUrl('https://tracker.example/a.png', base)).toBeUndefined();
    expect(safeAvatarUrl('http://mail.example.org/api/x', 'http://mail.example.org')).toBeUndefined();
  });
});
