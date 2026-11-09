// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
//
// The page that opens a secret link (internal/server/secretlinks.go, secretJS) is plain JavaScript
// inside a Go string. Run it against what lib/secretlink.ts produces: the two must agree on the
// format, the key must stay in the fragment, and an incomplete link must not use up a view.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { encryptSecret } from './secretlink';

const files = import.meta.glob('../../../internal/server/secretlinks.go', { query: '?raw', import: 'default', eager: true }) as Record<string, string>;
const go = Object.values(files)[0];
const script = /const secretJS = `([\s\S]*?)`\n/.exec(go)![1];
const body = /const secretBody = `([\s\S]*?)`\n/.exec(go)![1];

type Post = (path: string, body: unknown) => Promise<{ status: number; body: Record<string, unknown> }>;

function mount(path: string, hash: string, post: Post) {
  document.body.innerHTML = `<main>${body}</main>`;
  history.replaceState(null, '', path + hash);
  new Function('post', script)(post);
}
const $ = (id: string) => document.getElementById(id)!;

beforeEach(() => {
  document.body.innerHTML = '';
});

describe('secret page', () => {
  it('opens with an explicit click only, decrypts and clears the key from the address bar', async () => {
    const e = await encryptSecret('Zugang: 4711 ä€');
    const post = vi.fn<Post>(async () => ({ status: 200, body: { ciphertext: e.ciphertext } }));
    mount('/secret/AAAAAAAAAAAAAAAAAAAAAA', '#' + e.key, post);
    expect(post).not.toHaveBeenCalled(); // loading the page (link previews) uses nothing up
    ($('show') as HTMLButtonElement).click();
    await vi.waitFor(() => expect($('out').textContent).toBe('Zugang: 4711 ä€'));
    expect(post).toHaveBeenCalledTimes(1);
    expect(post.mock.calls[0][0]).toBe('/api/secrets/AAAAAAAAAAAAAAAAAAAAAA/open');
    expect(JSON.stringify(post.mock.calls)).not.toContain(e.key); // the key never goes to the server
    expect(location.hash).toBe('');
    expect($('result').classList.contains('hidden')).toBe(false);
  });

  it('an incomplete link says so and does not call the server', () => {
    const post = vi.fn<Post>();
    mount('/secret/AAAAAAAAAAAAAAAAAAAAAA', '', post);
    ($('show') as HTMLButtonElement).click();
    expect($('err').textContent).not.toBe('');
    expect(($('show') as HTMLButtonElement).disabled).toBe(true);
    expect(post).not.toHaveBeenCalled();
  });

  it('a used-up link shows the one message for gone links', async () => {
    const e = await encryptSecret('x');
    const post = vi.fn<Post>(async () => ({ status: 404, body: { error: 'gone' } }));
    mount('/secret/AAAAAAAAAAAAAAAAAAAAAA', '#' + e.key, post);
    ($('show') as HTMLButtonElement).click();
    await vi.waitFor(() => expect($('err').textContent).not.toBe(''));
    expect($('out').textContent).toBe('');
  });

  it('a wrong key shows an error instead of text', async () => {
    const e = await encryptSecret('geheim');
    const other = await encryptSecret('anderes');
    const post = vi.fn<Post>(async () => ({ status: 200, body: { ciphertext: e.ciphertext } }));
    mount('/secret/AAAAAAAAAAAAAAAAAAAAAA', '#' + other.key, post);
    ($('show') as HTMLButtonElement).click();
    await vi.waitFor(() => expect($('err').textContent).not.toBe(''));
    expect($('out').textContent).toBe('');
  });
});
