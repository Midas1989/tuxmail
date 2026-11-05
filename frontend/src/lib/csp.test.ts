// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import indexHtml from '../../index.html?raw';
import { applyCsp, applyNativeServerCsp, cspFor, nativeServerCsp, targetForMode } from './csp';

const directive = (policy: string, name: string): string[] => {
  const d = policy.split(';').map((s) => s.trim().split(/\s+/)).find((p) => p[0] === name);
  return d ? d.slice(1) : [];
};
const metaContent = (html: string) => /<meta\s+http-equiv="Content-Security-Policy"\s+content="([^"]*)"/.exec(html)?.[1];

describe('CSP per shell (NG-08)', () => {
  it('index.html ships the strict web/Wails policy', () => {
    expect(metaContent(indexHtml)).toBe(cspFor('web'));
    expect(indexHtml).toContain('<meta name="tuxmail-csp-target" content="web" />');
  });

  it('web/Wails: connect and img only to the own origin - no https:, no cleartext ws:', () => {
    const p = cspFor('web');
    expect(directive(p, 'connect-src')).toEqual(["'self'"]);
    expect(directive(p, 'img-src')).toEqual(["'self'", 'data:', 'blob:']);
    for (const bad of ['ws:', 'wss:', 'https:', 'http:', '*']) expect(p.split(/[\s;]+/)).not.toContain(bad);
    expect(directive(p, 'script-src')).toEqual(["'self'"]);
    expect(directive(p, 'object-src')).toEqual(["'none'"]);
  });

  it('web meta is never looser than the server header', () => {
    // Directives of internal/server/middleware.go (contentSecurityPolicy); the meta may only narrow them.
    const header = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src 'self' blob:; worker-src 'self'; manifest-src 'self'; object-src 'none'";
    const meta = cspFor('web');
    for (const name of ['default-src', 'script-src', 'img-src', 'connect-src', 'worker-src', 'manifest-src', 'object-src', 'style-src']) {
      const allowed = directive(header, name);
      for (const src of directive(meta, name)) expect(allowed, `${name} ${src}`).toContain(src);
    }
  });

  it('Capacitor: https: and wss: for the user-chosen server, never ws:', () => {
    const p = cspFor('capacitor');
    expect(directive(p, 'connect-src')).toEqual(["'self'", 'https:', 'wss:']);
    expect(directive(p, 'img-src')).toEqual(["'self'", 'data:', 'blob:', 'https:']);
    expect(p.split(/[\s;]+/)).not.toContain('ws:');
  });

  it('targets differ only in connect-src / img-src', () => {
    const other = (p: string) => p.split(';').map((s) => s.trim()).filter((s) => !/^(connect|img)-src /.test(s));
    expect(other(cspFor('capacitor'))).toEqual(other(cspFor('web')));
  });

  it('the dev server additionally allows its hot-reload websocket', () => {
    expect(directive(cspFor('web', { dev: true }), 'connect-src')).toContain('ws:');
    expect(directive(cspFor('web'), 'connect-src')).not.toContain('ws:');
  });

  it('only the capacitor mode selects the Android policy', () => {
    expect(targetForMode('capacitor')).toBe('capacitor');
    for (const m of ['production', 'development', 'test', undefined]) expect(targetForMode(m)).toBe('web');
  });

  it('applyCsp rewrites meta and marker for the target and refuses a page without them', () => {
    const cap = applyCsp(indexHtml, 'capacitor');
    expect(metaContent(cap)).toBe(cspFor('capacitor'));
    expect(cap).toContain('name="tuxmail-csp-target" content="capacitor"');
    expect(applyCsp(indexHtml, 'web')).toBe(indexHtml);
    expect(() => applyCsp('<html></html>', 'web')).toThrow(/missing/);
  });
});

describe('Capacitor runtime narrowing to the active server', () => {
  it('limits connect/img to the origin of the server (https/wss only)', () => {
    const p = nativeServerCsp('https://mail.example.org:8443/tuxmail')!;
    expect(directive(p, 'connect-src')).toEqual(["'self'", 'https://mail.example.org:8443', 'wss://mail.example.org:8443']);
    expect(directive(p, 'img-src')).toEqual(["'self'", 'data:', 'blob:', 'https://mail.example.org:8443']);
  });

  it('adds nothing for cleartext or malformed servers', () => {
    expect(nativeServerCsp('http://localhost:8080')).toBeNull();
    expect(nativeServerCsp('not a url')).toBeNull();
    expect(applyNativeServerCsp(document, 'http://localhost:8080')).toBe(false);
  });

  it('appends a second CSP meta (which can only narrow the first)', () => {
    expect(applyNativeServerCsp(document, 'https://mail.example.org')).toBe(true);
    const metas = [...document.head.querySelectorAll('meta[http-equiv="Content-Security-Policy"]')];
    expect(metas).toHaveLength(1);
    expect(metas[0].getAttribute('content')).toContain('https://mail.example.org');
  });
});
