// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import "strings"

// LocalFolderPrefix marks folders that exist only in the local store (no
// server counterpart), e.g. the local folders of POP3 accounts
// ("local/Archive"). Providers that support local folders report them in
// ListFolders so that the sync engine keeps them.
const LocalFolderPrefix = "local/"

// RoleLocal marks a local-only folder without another special-use role.
const RoleLocal FolderRole = "local"

// IsLocalFolder reports whether a folder remote id denotes a local-only folder.
func IsLocalFolder(remoteID string) bool { return strings.HasPrefix(remoteID, LocalFolderPrefix) }
