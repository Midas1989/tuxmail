// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { highlightSieve, lintSieve, tokenizeSieve } from './sieve';
import { remoteImageSources } from './components/mail/MailFrame.svelte';
import { MockMailProto } from './api/mockMailProto';

const src = 'require ["fileinto"];\n# c\nif header :contains "subject" "x" {\n  fileinto "A";\n}\nvacation text:\nHallo\n.\n;\n';

describe('sieve tokenizer', () => {
  it('round-trips and classifies tokens', () => {
    const toks = tokenizeSieve(src);
    expect(toks.map((t) => t.text).join('')).toBe(src);
    expect(toks.find((t) => t.text === ':contains')?.kind).toBe('tag');
    expect(toks.find((t) => t.text === 'fileinto')?.kind).toBe('keyword');
    expect(toks.find((t) => t.text.startsWith('text:'))?.kind).toBe('string');
    expect(highlightSieve('if "<x>"')).toContain('&lt;x&gt;');
  });
  it('lints unbalanced braces and strings', () => {
    expect(lintSieve(src)).toEqual({});
    expect(lintSieve('if true {\n keep;\n').line).toBe(1);
    expect(lintSieve('keep;\nfileinto "x;\n').line).toBe(2);
  });
});

describe('remote image CSP sources', () => {
  it('allows the proxy origin instead of https', () => {
    expect(remoteImageSources('<img src="https://srv.example/api/proxy/image?url=x">')).toBe('https://srv.example');
    expect(remoteImageSources('<img src="https://cdn.example/a.png">')).toBe('https:');
  });
});

describe('mock server rules', () => {
  it('toggles the managed section and keeps user scripts', () => {
    const m = new MockMailProto();
    expect(m.status(1).scripts[0].active).toBe(true);
    m.setWorkflowSync(1, true);
    expect(m.script(1, 'roundcube').script).toContain('# BEGIN TuxMail');
    m.setWorkflowSync(1, false);
    expect(m.script(1, 'roundcube').script).not.toContain('# BEGIN TuxMail');
    expect(() => m.put(1, 'bad', 'if true {', false)).toThrow();
    m.allow('domain', '@Example.org');
    expect(m.allowed('a@mail.example.org')).toBe(true);
  });
});
