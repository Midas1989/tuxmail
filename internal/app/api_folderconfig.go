// SPDX-License-Identifier: AGPL-3.0-or-later

package app

// Folder config (Wails bindings, see wails.ts "// --- Folder config ---").

func (x *Api) GetFolderConfig(accountID int64) (FolderConfig, error) {
	return r1(x.app.GetFolderConfig(x.ctx, accountID))
}
func (x *Api) SetFolderRole(accountID int64, role string, folderID int64) (FolderConfig, error) {
	return r1(x.app.SetFolderRole(x.ctx, accountID, role, folderID))
}
func (x *Api) ResetFolderRoles(accountID int64) (FolderConfig, error) {
	return r1(x.app.ResetFolderRoles(x.ctx, accountID))
}
func (x *Api) CreateRoleFolder(accountID int64, role, name string) (FolderConfig, error) {
	return r1(x.app.CreateRoleFolder(x.ctx, accountID, role, name))
}
func (x *Api) SaveFolderBehaviour(accountID int64, b FolderBehaviour) (FolderConfig, error) {
	return r1(x.app.SaveFolderBehaviour(x.ctx, accountID, b))
}
func (x *Api) SetIdentityFolders(identityID, sentFolderID, draftsFolderID int64) (FolderConfig, error) {
	return r1(x.app.SetIdentityFolders(x.ctx, identityID, sentFolderID, draftsFolderID))
}
func (x *Api) ExpungeFolder(folderID int64) (int, error) {
	return r1(x.app.ExpungeFolder(x.ctx, folderID))
}
