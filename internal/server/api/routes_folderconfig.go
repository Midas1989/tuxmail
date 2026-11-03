// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import "github.com/Midas1989/Mailclient/internal/app"

// Folder config handlers (http.ts "// --- Folder config ---").

var (
	getFolderConfig = withID(func(w w, r r, a A, id int64) { send(w)(a.GetFolderConfig(r.Context(), id)) })

	putFolderRole = withID(func(w w, r r, a A, id int64) {
		var in struct {
			FolderID int64 `json:"folderId"`
		}
		if readJSON(w, r, &in) {
			send(w)(a.SetFolderRole(r.Context(), id, r.PathValue("role"), in.FolderID))
		}
	})

	resetFolderRoles = withID(func(w w, r r, a A, id int64) { send(w)(a.ResetFolderRoles(r.Context(), id)) })

	createRoleFolder = withID(func(w w, r r, a A, id int64) {
		var in struct {
			Role string `json:"role"`
			Name string `json:"name"`
		}
		if readJSON(w, r, &in) {
			send(w)(a.CreateRoleFolder(r.Context(), id, in.Role, in.Name))
		}
	})

	putFolderBehaviour = withID(func(w w, r r, a A, id int64) {
		var in app.FolderBehaviour
		if readJSON(w, r, &in) {
			send(w)(a.SaveFolderBehaviour(r.Context(), id, in))
		}
	})

	putIdentityFolders = withID(func(w w, r r, a A, id int64) {
		var in struct {
			SentFolderID   int64 `json:"sentFolderId"`
			DraftsFolderID int64 `json:"draftsFolderId"`
		}
		if readJSON(w, r, &in) {
			send(w)(a.SetIdentityFolders(r.Context(), id, in.SentFolderID, in.DraftsFolderID))
		}
	})

	expungeFolder = withID(func(w w, r r, a A, id int64) {
		n, err := a.ExpungeFolder(r.Context(), id)
		send(w)(map[string]int{"expunged": n}, err)
	})
)
