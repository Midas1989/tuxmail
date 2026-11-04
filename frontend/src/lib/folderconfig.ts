// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Folder configuration logic shared by the settings card, the folder tree
// context menu and the mock transport (pure functions, unit tested). The Go
// side is internal/app/folderconfig.go + internal/store/folderconfig.go.

import type { AssignableRole, Folder, FolderConfig, FolderRole, FolderRoleEntry, RoleSource } from './types';

/** Roles shown in Settings → account → folders, in display order. */
export const ASSIGNABLE_ROLES: AssignableRole[] = ['sent', 'drafts', 'trash', 'junk', 'archive', 'templates'];

/** Roles offered in the folder tree's "Use as …" menu. */
export const MENU_ROLES: AssignableRole[] = ['sent', 'drafts', 'trash', 'junk', 'archive', 'templates'];

/** Account to preselect when the folder settings open (tree context menu). */
export const folderSettingsTarget = { accountId: 0 };

export type BadgeKind = RoleSource | 'none' | 'unresolved';

/** Badge of a role row: where the folder came from. */
export function badgeOf(e: FolderRoleEntry): BadgeKind {
  if (e.overridden && e.unresolved) return 'unresolved';
  if (!e.folderId) return 'none';
  return e.source ?? 'server';
}

/** Roles without a folder (offer "create folder"). */
export function missingRoles(cfg: FolderConfig): AssignableRole[] {
  return cfg.roles.filter((r) => !r.folderId).map((r) => r.role);
}

export interface FolderOption {
  id: number;
  label: string;
  depth: number;
}

/** Folders of an account in tree order, for dropdowns ("Parent/Child"). */
export function folderOptions(folders: Folder[]): FolderOption[] {
  const out: FolderOption[] = [];
  const ids = new Set(folders.map((f) => f.id));
  const walk = (parentId: number | undefined, depth: number, prefix: string) => {
    const children = folders
      .filter((f) => (f.parentId && ids.has(f.parentId) ? f.parentId : undefined) === parentId)
      .sort((a, b) => (a.role === 'inbox' ? -1 : b.role === 'inbox' ? 1 : a.name.localeCompare(b.name)));
    for (const f of children) {
      const label = prefix ? `${prefix}/${f.name}` : f.name;
      out.push({ id: f.id, label, depth });
      walk(f.id, depth + 1, label);
    }
  };
  walk(undefined, 0, '');
  return out;
}

/** Whether the tree context menu may assign roles to this folder. */
export function canAssignRole(f: Folder): boolean {
  return f.role !== 'inbox';
}

/** Roles to offer for a folder in the context menu (not its current one). */
export function menuRolesFor(f: Folder): AssignableRole[] {
  if (!canAssignRole(f)) return [];
  return MENU_ROLES.filter((r) => r !== f.role);
}

export interface ServerRole {
  folderId: number;
  source: RoleSource;
}

/**
 * Effective roles (mirrors applyRolesSQL in internal/store/folderconfig.go):
 * the target of an override gets the role ("user"); a folder whose server
 * role is overridden by another existing folder loses it; an override whose
 * folder is missing is ignored (unresolved).
 */
export function applyOverrides(
  folders: Folder[],
  server: Partial<Record<FolderRole, ServerRole>>,
  overrides: Partial<Record<AssignableRole, number>>,
): Folder[] {
  const exists = new Set(folders.map((f) => f.id));
  const byFolder = new Map<number, AssignableRole>();
  for (const [role, id] of Object.entries(overrides) as [AssignableRole, number][]) {
    if (id && exists.has(id)) byFolder.set(id, role);
  }
  const overridden = new Set(byFolder.values());
  const serverOf = new Map<number, { role: FolderRole; source: RoleSource }>();
  for (const [role, s] of Object.entries(server) as [FolderRole, ServerRole][]) {
    if (s) serverOf.set(s.folderId, { role, source: s.source });
  }
  return folders.map((f) => {
    const o = byFolder.get(f.id);
    if (o) return { ...f, role: o, roleSource: 'user' as RoleSource };
    const s = serverOf.get(f.id);
    if (s && !overridden.has(s.role as AssignableRole)) return { ...f, role: s.role, roleSource: s.source };
    return { ...f, role: undefined, roleSource: undefined };
  });
}

/** Builds the role table from effective folders (mock transport). */
export function roleEntries(
  folders: Folder[],
  server: Partial<Record<FolderRole, ServerRole>>,
  overrides: Partial<Record<AssignableRole, number>>,
): FolderRoleEntry[] {
  const exists = new Set(folders.map((f) => f.id));
  return ASSIGNABLE_ROLES.map((role) => {
    const f = folders.find((x) => x.role === role);
    const s = server[role];
    const o = overrides[role];
    return {
      role,
      folderId: f?.id,
      source: f?.roleSource,
      serverFolderId: s?.folderId,
      serverSource: s?.source,
      overridden: o !== undefined,
      unresolved: o !== undefined && !exists.has(o) ? true : undefined,
    };
  });
}
