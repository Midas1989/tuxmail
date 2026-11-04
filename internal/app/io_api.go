// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"encoding/base64"

	"github.com/Midas1989/Mailclient/internal/backup"
)

// Wails bindings of import/export and backup/restore (wails.ts section
// "Import/Export/Backup"). Large uploads and downloads go through the
// desktop asset handler (/api/io/uploads, /api/io/downloads/{token}).

func (x *Api) ListIOFormats() (IOFormats, error) { return r1(x.app.ListIOFormats(x.ctx)) }
func (x *Api) SaveIOUpload(name, data string) (IOUpload, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return IOUpload{}, werr(invalid("invalid upload data"))
	}
	return r1(x.app.SaveIOUpload(x.ctx, name, bytes.NewReader(raw)))
}
func (x *Api) InspectImport(src IOSource) (ImportPreview, error) {
	return r1(x.app.InspectImport(x.ctx, src))
}
func (x *Api) StartImport(req ImportRequest) (IOJob, error) { return r1(x.app.StartImport(x.ctx, req)) }
func (x *Api) StartExport(req ExportRequest) (IOJob, error) { return r1(x.app.StartExport(x.ctx, req)) }
func (x *Api) ListIOJobs() ([]IOJob, error)                 { return r1(x.app.ListIOJobs(x.ctx)) }
func (x *Api) GetIOJob(id string) (IOJob, error)            { return r1(x.app.GetIOJob(x.ctx, id)) }
func (x *Api) CancelIOJob(id string) error                  { return werr(x.app.CancelIOJob(x.ctx, id)) }
func (x *Api) CreateBackup(req BackupRequest) (IOJob, error) {
	return r1(x.app.CreateBackup(x.ctx, req))
}
func (x *Api) InspectBackup(src BackupSource, passphrase string) (BackupInspection, error) {
	return r1(x.app.InspectBackup(x.ctx, src, passphrase))
}
func (x *Api) StartRestore(req RestoreRequest) (IOJob, error) {
	return r1(x.app.StartRestore(x.ctx, req))
}
func (x *Api) ListBackupSchedules() ([]backup.Schedule, error) {
	return r1(x.app.ListBackupSchedules(x.ctx))
}
func (x *Api) SaveBackupSchedule(in BackupScheduleInput) (backup.Schedule, error) {
	return r1(x.app.SaveBackupSchedule(x.ctx, in))
}
func (x *Api) DeleteBackupSchedule(id string) error {
	return werr(x.app.DeleteBackupSchedule(x.ctx, id))
}
func (x *Api) RunBackupSchedule(id string) (IOJob, error) {
	return r1(x.app.RunBackupSchedule(x.ctx, id))
}
func (x *Api) ListBackupArchives(scheduleID string) ([]backup.Entry, error) {
	return r1(x.app.ListBackupArchives(x.ctx, scheduleID))
}
