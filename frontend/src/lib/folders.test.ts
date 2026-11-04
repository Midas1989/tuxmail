// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { folderTree, folderViews } from './folders';
import type { Folder, FolderRole } from './types';

const de: Record<string, string> = { inbox: 'Posteingang', drafts: 'Entwürfe', sent: 'Gesendet', archive: 'Archiv', junk: 'Spam', trash: 'Papierkorb' };
const roleName = (r: FolderRole) => de[r] ?? r;

let nextId = 1;
function f(name: string, role?: FolderRole, parentId?: number): Folder {
  return { id: nextId++, accountId: 1, name, role, parentId, unread: 0, total: 0 } as Folder;
}

describe('folder tree', () => {
  // The owner's Dovecot mailbox: "Archive" (\Archive) and a user folder "Archiv" with years.
  const archive = f('Archive', 'archive');
  const archiv = f('Archiv');
  const y24 = f('2024', undefined, archiv.id);
  const y25 = f('2025', undefined, archiv.id);
  const folders = [archiv, y24, y25, archive, f('Entwürfe', 'drafts'), f('Gesendet', 'sent'), f('INBOX', 'inbox'), f('Papierkorb', 'trash'), f('Spam', 'junk'), f('Templates')];

  it('labels a role folder whose localized name collides with a real folder', () => {
    const v = folderViews(folders, roleName);
    expect(v.get(archive.id)?.label).toBe('Archiv (Archive)');
    expect(v.get(archive.id)?.role).toBe('archive');
    expect(v.get(archiv.id)?.label).toBe('Archiv');
    expect(v.get(archiv.id)?.role).toBeUndefined();
  });

  it('sorts role folders first, then the rest alphabetically', () => {
    const order = folderTree(folders, roleName).map((n) => n.label);
    expect(order).toEqual(['Posteingang', 'Entwürfe', 'Gesendet', 'Archiv (Archive)', 'Spam', 'Papierkorb', 'Archiv', '2024', '2025', 'Templates']);
  });

  it('shows only one folder per role with the role', () => {
    const a = f('Archive', 'archive');
    const b = f('Archives', 'archive');
    const v = folderViews([a, b], roleName);
    expect(v.get(a.id)?.role).toBe('archive');
    expect(v.get(b.id)?.role).toBeUndefined();
    expect(v.get(b.id)?.label).toBe('Archives');
  });

  it('hides children of collapsed folders', () => {
    const order = folderTree(folders, roleName, (id) => id === archiv.id).map((n) => n.label);
    expect(order).not.toContain('2024');
  });
});
