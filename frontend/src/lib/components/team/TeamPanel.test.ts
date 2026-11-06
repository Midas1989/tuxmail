// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, text, until } from '../security/mountUtil';
import { memberHandle, nameWithHandle } from './mentions';

const members = vi.hoisted(() => [
  { userId: 2, username: 'erik', displayName: 'Admin', role: 'reply' },
  { userId: 3, username: 'lena', displayName: 'lena', role: 'read' },
]);

vi.mock('../../state', () => ({
  app: { api: {}, toast: () => {} },
  team: {
    active: { id: 1, name: 'Vertrieb', address: 'v@example.org' },
    activeId: 1,
    myId: 1,
    members,
    others: [{ userId: 2, userName: 'Admin', state: 'viewing' }],
    comments: [
      { id: 1, authorId: 2, authorName: 'Admin', body: 'Bitte zahlen', mentions: [], createdAt: '2026-11-07T10:00:00Z' },
      { id: 2, authorId: 9, authorName: 'Ehemalig', body: 'alt', mentions: [], createdAt: '2026-11-07T10:00:00Z' },
    ],
    detail: { team: { status: 'open', priority: 'normal', assigneeId: null, tags: [] }, drafts: [] },
    can: () => false,
    open: async () => {},
    noteTyping: () => {},
    deleteComment: () => {},
  },
}));

import TeamPanel from './TeamPanel.svelte';

afterEach(cleanup);

describe('team names', () => {
  it('memberHandle: the username of a member, unless it repeats the name or is unknown', () => {
    expect(memberHandle(members, 2, 'Admin')).toBe('erik');
    expect(memberHandle(members, 3, 'lena')).toBeNull();
    expect(memberHandle(members, 9, 'Ehemalig')).toBeNull();
    expect(memberHandle(members, null, 'System')).toBeNull();
    expect(nameWithHandle(members, 2, 'Admin')).toBe('Admin (@erik)');
    expect(nameWithHandle(members, 9, 'Ehemalig')).toBe('Ehemalig');
  });

  it('a comment and a presence chip show who is behind a self-chosen name', async () => {
    const root = render(TeamPanel, { conversationId: 5 });
    await until(() => expect(root.querySelector('.note')).toBeTruthy());
    const notes = [...root.querySelectorAll('.note')];
    expect(notes[0].querySelector('.handle')?.textContent).toBe('@erik');
    expect(notes[1].querySelector('.handle')).toBeNull();
    expect(text(root.querySelector('.presence')!)).toContain('Admin (@erik)');
  });
});
