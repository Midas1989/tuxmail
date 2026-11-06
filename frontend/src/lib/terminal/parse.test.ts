// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import vectors from '$mailcmd/testdata/vectors.json';
import { ParseError, bodyText, cleanLine, cleanText, parseLine } from './parse';

// The same file runs against the Go parser (internal/mailcmd/parse_test.go).
describe('command syntax vectors', () => {
  for (const v of vectors.parse) {
    it(JSON.stringify(v.line), () => {
      if ('error' in v && v.error) {
        let code = '';
        try {
          parseLine(v.line);
        } catch (e) {
          code = (e as ParseError).code;
        }
        expect(code).toBe(v.error);
      } else {
        expect(parseLine(v.line)).toEqual((v as { want: unknown }).want);
      }
    });
  }
});

describe('control character removal', () => {
  for (const c of vectors.cleanLine) it(`line ${JSON.stringify(c.in)}`, () => expect(cleanLine(c.in)).toBe(c.want));
  for (const c of vectors.cleanText) it(`text ${JSON.stringify(c.in)}`, () => expect(cleanText(c.in)).toBe(c.want));
});

describe('bodyText', () => {
  it('prefers the text part', () => expect(bodyText('Hallo\x1b[31m', '<p>x</p>')).toBe('Hallo'));
  it('turns HTML into text without scripts and images', () => {
    const t = bodyText('', '<p>Hallo <b>Welt</b><img src="https://track.example/p.gif"></p><script>alert(1)</script><div>zwei</div>');
    expect(t).toBe('Hallo Welt\nzwei');
  });
});
