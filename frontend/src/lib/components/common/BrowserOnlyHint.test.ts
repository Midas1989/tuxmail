// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ACTIVE_MIRROR_KEY } from '../../native/session';
import { ApiError } from '../../api/api';
import { errorWording } from '../../errorWording';
import { cleanup, click, render } from '../security/mountUtil';
import BrowserOnlyHint from './BrowserOnlyHint.svelte';

afterEach(() => {
  cleanup();
  localStorage.clear();
  ApiError.translate = undefined;
});

describe('BrowserOnlyHint', () => {
  it('says why and links to the active server', async () => {
    localStorage.setItem(ACTIVE_MIRROR_KEY, JSON.stringify({ profileId: 'p', serverUrl: 'https://mail.example.org' }));
    const open = vi.spyOn(window, 'open').mockImplementation(() => null);
    const root = render(BrowserOnlyHint, { what: 'Backup' });
    expect(root.textContent).toContain('Backup');
    expect(root.textContent).toContain('only in the browser');
    await click(root.querySelector('button') as HTMLButtonElement);
    expect(open).toHaveBeenCalledWith('https://mail.example.org', '_blank', expect.anything());
    open.mockRestore();
  });
});

describe('error wording', () => {
  it('device_token_forbidden and password_required are translated in every transport error', () => {
    ApiError.translate = errorWording;
    expect(new ApiError('this needs a browser session', 403, 'device_token_forbidden').message).toContain('only in the browser');
    expect(new ApiError('x', 400, 'password_required').message).toContain('password');
    expect(new ApiError('kept', 500, 'internal').message).toBe('kept');
  });
});
