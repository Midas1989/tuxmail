// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render } from '../security/mountUtil';
import ComfortSettingsCard from './ComfortSettingsCard.svelte';
import { comfort } from '../../state';
import { t } from '../../i18n/index.svelte';

afterEach(cleanup);

describe('meeting settings label', () => {
  it('shows the Jitsi server field only for Jitsi, a matching hint otherwise', async () => {
    const root = render(ComfortSettingsCard, {});
    await new Promise((r) => setTimeout(r, 50));
    const sel = Array.from(root.querySelectorAll('select')).find((s) => s.querySelector('option[value="teams"]')) as HTMLSelectElement | undefined;
    if (!sel) throw new Error('provider select missing (settings not loaded: ' + (comfort ? 'comfort ok' : 'no comfort') + ')');
    expect(root.textContent).toContain(t('comfort.settings.jitsiUrl'));
    sel.value = 'teams';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    await new Promise((r) => setTimeout(r, 20));
    expect(root.textContent).not.toContain(t('comfort.settings.jitsiUrl'));
    expect(root.querySelector('[data-testid="meeting-hint"]')?.textContent).toBe(t('comfort.meeting.hint.teams'));
    sel.value = 'nextcloud_talk';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    await new Promise((r) => setTimeout(r, 20));
    expect(root.querySelector('[data-testid="meeting-hint"]')?.textContent).toBe(t('comfort.meeting.hint.nextcloud_talk'));
  });
});
