// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import { restoreRemoteImages } from './remote';

const gif = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';

describe('restoreRemoteImages', () => {
  it('restores https images but keeps trackers and other schemes blocked', () => {
    const html =
      `<p>x</p><img src="${gif}" data-remote-src="https://cdn.example.com/a.jpg">` +
      `<img src="${gif}" data-remote-src="https://track.example.com/p.gif" data-tracker="track.example.com">` +
      `<img src="${gif}" data-remote-src="http://insecure.example.com/b.jpg">`;
    const out = restoreRemoteImages(html);
    expect(out).toContain('src="https://cdn.example.com/a.jpg"');
    expect(out).not.toContain(' src="https://track.example.com/p.gif"');
    expect(out).not.toContain(' src="http://insecure.example.com/b.jpg"');
  });
  it('returns HTML without remote images unchanged', () => {
    expect(restoreRemoteImages('<p>hi</p>')).toBe('<p>hi</p>');
  });
});
