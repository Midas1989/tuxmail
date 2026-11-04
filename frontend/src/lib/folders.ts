// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Display rules of the folder tree (pure, testable):
//   - order: Inbox, Drafts, Sent, Archive, Junk, Trash, then the other
//     folders alphabetically (locale aware); subfolders alphabetically;
//   - one folder per role and account shows the role (localized name, role
//     icon); further folders with the same role keep their real name and a
//     generic icon;
//   - when the localized role name collides with the real name of another
//     folder of the account ("Archive" with \Archive shown as "Archiv" next
//     to a user folder "Archiv/2024"), the role folder is labelled
//     "Archiv (Archive)".

import type { Folder, FolderRole } from './types';

export const ROLE_ORDER: FolderRole[] = ['inbox', 'drafts', 'sent', 'archive', 'junk', 'trash', 'templates'];

export interface FolderView {
  folder: Folder;
  /** Role shown (icon, localized name); undefined for ordinary folders. */
  role?: FolderRole;
  label: string;
}

function rank(v: FolderView): number {
  if (!v.role) return ROLE_ORDER.length;
  const i = ROLE_ORDER.indexOf(v.role);
  return i < 0 ? ROLE_ORDER.length : i;
}

/** Labels and effective roles of the folders of one account. */
export function folderViews(folders: Folder[], roleName: (r: FolderRole) => string): Map<number, FolderView> {
  const out = new Map<number, FolderView>();
  const claimed = new Set<string>();
  // The folder that claims a role: the first top-level one (stable order by id).
  const byRoleCandidates = [...folders].filter((f) => f.role).sort((a, b) => (a.parentId ? 1 : 0) - (b.parentId ? 1 : 0) || a.id - b.id);
  const roleOf = new Map<number, FolderRole>();
  for (const f of byRoleCandidates) {
    const r = f.role as FolderRole;
    if (claimed.has(r)) continue;
    claimed.add(r);
    roleOf.set(f.id, r);
  }
  const lower = (s: string) => s.toLocaleLowerCase();
  for (const f of folders) {
    const role = roleOf.get(f.id);
    let label = f.name;
    if (role) {
      const loc = roleName(role);
      const clash = folders.some((o) => o.id !== f.id && lower(o.name) === lower(loc) && lower(o.name) !== lower(f.name));
      label = clash ? `${loc} (${f.name})` : loc;
    }
    out.set(f.id, { folder: f, role, label });
  }
  return out;
}

/** Sorts siblings: role folders in ROLE_ORDER, then the rest alphabetically. */
export function compareFolderViews(a: FolderView, b: FolderView): number {
  const ra = rank(a);
  const rb = rank(b);
  if (ra !== rb) return ra - rb;
  return a.label.localeCompare(b.label, undefined, { sensitivity: 'base', numeric: true });
}

export interface FolderNode extends FolderView {
  depth: number;
  hasChildren: boolean;
}

/** Flattens the tree in display order; collapsed(id) hides the children of a folder. */
export function folderTree(folders: Folder[], roleName: (r: FolderRole) => string, collapsed: (id: number) => boolean = () => false): FolderNode[] {
  const views = folderViews(folders, roleName);
  const ids = new Set(folders.map((f) => f.id));
  const children = new Map<number, FolderView[]>();
  for (const v of views.values()) {
    // Orphans (parent not listed) are shown at the top level.
    const p = v.folder.parentId && ids.has(v.folder.parentId) ? v.folder.parentId : 0;
    const list = children.get(p) ?? [];
    list.push(v);
    children.set(p, list);
  }
  const out: FolderNode[] = [];
  const walk = (parent: number, depth: number) => {
    const list = (children.get(parent) ?? []).sort(compareFolderViews);
    for (const v of list) {
      const hasChildren = (children.get(v.folder.id)?.length ?? 0) > 0;
      out.push({ ...v, depth, hasChildren });
      if (hasChildren && !collapsed(v.folder.id)) walk(v.folder.id, depth + 1);
    }
  };
  walk(0, 0);
  return out;
}
