// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest';
import { styleNonce, withStyleNonce } from './stylenonce';

describe('withStyleNonce', () => {
  afterEach(() => document.head.replaceChildren());

  it('leaves documents alone without a server nonce', () => {
    expect(styleNonce()).toBeUndefined();
    expect(withStyleNonce('<style>a{}</style>')).toBe('<style>a{}</style>');
  });

  it('marks every style element with the nonce of the page', () => {
    const m = document.createElement('meta');
    m.name = 'tuxmail-style-nonce';
    m.content = 'n0nce';
    document.head.append(m);
    expect(withStyleNonce('<style>a{}</style><p style="x"><STYLE media="print">b{}</STYLE>')).toBe('<style nonce="n0nce">a{}</style><p style="x"><style nonce="n0nce" media="print">b{}</STYLE>');
  });
});
