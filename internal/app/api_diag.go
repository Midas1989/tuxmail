// SPDX-License-Identifier: AGPL-3.0-or-later

package app

// Wails bindings of the sync status and Settings → Info & Diagnose
// (diagnostics.go).

func (x *Api) SyncStatus() ([]AccountStatus, error)  { return r1(x.app.SyncStatus(x.ctx)) }
func (x *Api) Diagnostics() (DiagnosticsInfo, error) { return r1(x.app.Diagnostics(x.ctx)) }
func (x *Api) ExportDiagnostics() (DiagnosticsArchive, error) {
	return r1(x.app.ExportDiagnostics(x.ctx))
}
func (x *Api) SyncNow(accountID int64) error { return werr(x.app.SyncNow(x.ctx, accountID)) }
