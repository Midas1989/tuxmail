// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import "encoding/base64"

// Cloud files: Wails bindings (frontend/src/lib/api/wails.ts, section Cloud).

// CloudDownload is a downloaded file over the IPC bridge (data base64).
type CloudDownload struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

func (x *Api) ListCloudAccounts() ([]CloudAccount, error) { return r1(x.app.ListCloudAccounts(x.ctx)) }
func (x *Api) StartCloudLogin(in CloudLoginInput) (CloudLoginFlow, error) {
	return r1(x.app.StartCloudLogin(x.ctx, in))
}
func (x *Api) PollCloudLogin(id string) (CloudLoginFlow, error) {
	return r1(x.app.PollCloudLogin(x.ctx, id))
}
func (x *Api) CancelCloudLogin(id string) error  { return werr(x.app.CancelCloudLogin(x.ctx, id)) }
func (x *Api) DeleteCloudAccount(id int64) error { return werr(x.app.DeleteCloudAccount(x.ctx, id)) }
func (x *Api) ListCloudFiles(accountID int64, path string) (CloudListing, error) {
	return r1(x.app.ListCloudFiles(x.ctx, accountID, path))
}
func (x *Api) CloudQuota(accountID int64) (CloudQuota, error) {
	return r1(x.app.CloudQuota(x.ctx, accountID))
}
func (x *Api) CreateCloudFolder(accountID int64, path string) (CloudEntry, error) {
	return r1(x.app.CreateCloudFolder(x.ctx, accountID, path))
}
func (x *Api) MoveCloudFile(accountID int64, from, to string) error {
	return werr(x.app.MoveCloudFile(x.ctx, accountID, from, to))
}
func (x *Api) DeleteCloudFile(accountID int64, path string) error {
	return werr(x.app.DeleteCloudFile(x.ctx, accountID, path))
}
func (x *Api) UploadCloudFile(accountID int64, dir, uploadToken string) (CloudEntry, error) {
	return r1(x.app.UploadCloudFile(x.ctx, accountID, dir, uploadToken))
}
func (x *Api) DownloadCloudFile(accountID int64, path string) (CloudDownload, error) {
	c, err := x.app.DownloadCloudFile(x.ctx, accountID, path)
	if err != nil {
		return CloudDownload{}, werr(err)
	}
	return CloudDownload{Filename: c.Filename, MIMEType: c.MIMEType, Data: base64.StdEncoding.EncodeToString(c.Data)}, nil
}
func (x *Api) ImportCloudFile(accountID int64, path string) (CloudImportResult, error) {
	return r1(x.app.ImportCloudFile(x.ctx, accountID, path))
}
func (x *Api) SaveAttachmentToCloud(in CloudSaveInput) (CloudEntry, error) {
	return r1(x.app.SaveAttachmentToCloud(x.ctx, in))
}
func (x *Api) CreateCloudShare(in CloudShareInput) (CloudShare, error) {
	return r1(x.app.CreateCloudShare(x.ctx, in))
}
func (x *Api) ListCloudShares() ([]CloudShare, error) { return r1(x.app.ListCloudShares(x.ctx)) }
func (x *Api) RevokeCloudShare(id int64) (CloudShare, error) {
	return r1(x.app.RevokeCloudShare(x.ctx, id))
}
func (x *Api) CloudSharePassword(id int64) (CloudPassword, error) {
	return r1(x.app.CloudSharePassword(x.ctx, id))
}
func (x *Api) GenerateCloudPassword(accountID int64) (CloudPassword, error) {
	return r1(x.app.GenerateCloudPassword(x.ctx, accountID))
}
