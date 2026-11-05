// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest';
import { isPlainHttpUrl } from './insecure';

describe('isPlainHttpUrl', () => {
  it('flags plain http to remote hosts', () => {
    expect(isPlainHttpUrl('http://mail.example.com/Microsoft-Server-ActiveSync')).toBe(true);
    expect(isPlainHttpUrl(' HTTP://mail.example.com ')).toBe(true);
    expect(isPlainHttpUrl('http://127.0.0.1.evil.example/')).toBe(true);
  });
  it('accepts https, loopback and empty values', () => {
    expect(isPlainHttpUrl('https://mail.example.com')).toBe(false);
    expect(isPlainHttpUrl('http://localhost:1080/x')).toBe(false);
    expect(isPlainHttpUrl('http://127.0.0.1:1080/v1.1/')).toBe(false);
    expect(isPlainHttpUrl('http://[::1]:8080/')).toBe(false);
    expect(isPlainHttpUrl('')).toBe(false);
    expect(isPlainHttpUrl(undefined)).toBe(false);
  });
});
