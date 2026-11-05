// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it, vi } from 'vitest';
import { MockTransport } from '../api/mock';
import type { AppState } from './app.svelte';
import { TeamState } from './team.svelte';

function makeTeam() {
  const root = new MockTransport({ latencyMs: 0 });
  const team = new TeamState(root, { setTarget: () => {} }, {} as AppState);
  const enter = vi.spyOn(team, 'enter').mockImplementation(async (id: number) => {
    team.activeId = id;
  });
  return { team, enter };
}

describe('enterIfMember (NG-10: deep links go through the membership check)', () => {
  it('enters a mailbox the user is a member of', async () => {
    const { team, enter } = makeTeam();
    expect(await team.enterIfMember(1)).toBe(true);
    expect(enter).toHaveBeenCalledWith(1);
    expect(team.activeId).toBe(1);
  });

  it('refuses ids that are not in the server-provided mailbox list', async () => {
    const { team, enter } = makeTeam();
    await team.loadMailboxes();
    expect(team.mailboxes.length).toBeGreaterThan(0);
    for (const id of [99, 0, -1, 1.5, NaN]) expect(await team.enterIfMember(id)).toBe(false);
    expect(enter).not.toHaveBeenCalled();
    expect(team.activeId).toBeNull();
  });

  it('does not re-enter the already active mailbox', async () => {
    const { team, enter } = makeTeam();
    expect(await team.enterIfMember(2)).toBe(true);
    expect(await team.enterIfMember(2)).toBe(true);
    expect(enter).toHaveBeenCalledTimes(1);
  });
});
