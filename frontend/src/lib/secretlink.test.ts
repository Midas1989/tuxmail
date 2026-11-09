// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { b64urlDecode, b64urlEncode, decryptSecret, encryptSecret, MAX_SECRET_BYTES, secretBytes, secretLinkUrl } from './secretlink';

describe('secret links', () => {
  it('encrypts so that only the key opens it', async () => {
    const e = await encryptSecret('Pässwört 🔑 123');
    expect(e.key).toHaveLength(43); // 32 bytes
    const raw = new TextDecoder('latin1').decode(b64urlDecode(e.ciphertext));
    expect(raw).not.toContain('ssw'); // no plaintext fragments in the ciphertext
    expect(await decryptSecret(e.ciphertext, e.key)).toBe('Pässwört 🔑 123');
    const other = await encryptSecret('x');
    await expect(decryptSecret(e.ciphertext, other.key)).rejects.toThrow();
  });

  it('uses a new key and nonce every time', async () => {
    const a = await encryptSecret('same');
    const b = await encryptSecret('same');
    expect(a.key).not.toBe(b.key);
    expect(a.ciphertext).not.toBe(b.ciphertext);
  });

  it('detects a tampered ciphertext', async () => {
    const e = await encryptSecret('geheim');
    const bytes = b64urlDecode(e.ciphertext);
    bytes[bytes.length - 1] ^= 1;
    await expect(decryptSecret(b64urlEncode(bytes), e.key)).rejects.toThrow();
  });

  it('keeps the key out of everything but the fragment', async () => {
    const e = await encryptSecret('x');
    const url = secretLinkUrl('https://mail.example.org/secret/AAAAAAAAAAAAAAAAAAAAAA', e.key);
    expect(url).toBe(`https://mail.example.org/secret/AAAAAAAAAAAAAAAAAAAAAA#${e.key}`);
    expect(new URL(url).pathname).not.toContain(e.key);
    expect(new URL(url).search).toBe('');
  });

  it('refuses text that would not fit', async () => {
    expect(secretBytes('a')).toBeLessThan(MAX_SECRET_BYTES);
    await expect(encryptSecret('a'.repeat(MAX_SECRET_BYTES))).rejects.toThrow(RangeError);
  });
});
