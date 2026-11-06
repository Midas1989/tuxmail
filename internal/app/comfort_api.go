// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"encoding/base64"

	"github.com/Midas1989/Mailclient/internal/meeting"
	"github.com/Midas1989/Mailclient/internal/merge"
)

// Wails bindings of the mail comfort features (frontend/src/lib/api/comfort.ts,
// ComfortWailsTransport). Method names start with "Comfort".

func (x *Api) ComfortGetSettings() (ComfortSettings, error) { return r1(x.app.ComfortSettings(x.ctx)) }
func (x *Api) ComfortSaveSettings(s ComfortSettings) (ComfortSettings, error) {
	return r1(x.app.SaveComfortSettings(x.ctx, s))
}
func (x *Api) ComfortInsights(id int64) (MessageInsights, error) {
	return r1(x.app.MessageInsights(x.ctx, id))
}
func (x *Api) ComfortMarkSpam(in MarkSpamInput) error { return werr(x.app.MarkSpam(x.ctx, in)) }
func (x *Api) ComfortSetCategory(in SetCategoryInput) error {
	return werr(x.app.SetMessageCategory(x.ctx, in))
}
func (x *Api) ComfortListCategories(role string) ([]CategoryInfo, error) {
	return r1(x.app.ListCategories(x.ctx, role))
}
func (x *Api) ComfortCategoryMessages(category, role string, limit int) ([]MessageSummary, error) {
	return r1(x.app.CategoryMessages(x.ctx, category, role, limit))
}
func (x *Api) ComfortMessageCategories(ids []int64) (map[int64]string, error) {
	return r1(x.app.MessageCategories(x.ctx, ids))
}
func (x *Api) ComfortListSubscriptions() ([]Subscription, error) {
	return r1(x.app.ListSubscriptions(x.ctx))
}
func (x *Api) ComfortUnsubscribe(key string) (UnsubscribeResult, error) {
	return r1(x.app.Unsubscribe(x.ctx, key))
}
func (x *Api) ComfortUnsubscribeMessage(id int64) (UnsubscribeResult, error) {
	return r1(x.app.UnsubscribeMessage(x.ctx, id))
}
func (x *Api) ComfortArchiveSubscription(key string) (int, error) {
	return r1(x.app.ArchiveSubscription(x.ctx, key))
}
func (x *Api) ComfortAutoArchiveSubscription(key string) (Workflow, error) {
	return r1(x.app.AutoArchiveSubscription(x.ctx, key))
}
func (x *Api) ComfortInvoiceTask(id int64) (IDResult, error) {
	return r1(x.app.CreateInvoiceTask(x.ctx, id))
}
func (x *Api) ComfortSpellCheck(in SpellCheckInput) (SpellCheckResult, error) {
	return r1(x.app.SpellCheck(x.ctx, in))
}
func (x *Api) ComfortSpellWords() ([]string, error) { return r1(x.app.SpellWords(x.ctx)) }
func (x *Api) ComfortAddSpellWord(w string) error   { return werr(x.app.AddSpellWord(x.ctx, w)) }
func (x *Api) ComfortDeleteSpellWord(w string) error {
	return werr(x.app.DeleteSpellWord(x.ctx, w))
}
func (x *Api) ComfortPrintView(in PrintInput) (string, error) { return r1(x.app.PrintView(x.ctx, in)) }

// ComfortMessagePDF returns the PDF as base64 (the desktop saves it).
func (x *Api) ComfortMessagePDF(id int64) (PDFFile, error) {
	c, err := x.app.MessagePDF(x.ctx, id)
	if err != nil {
		return PDFFile{}, werr(err)
	}
	return PDFFile{Filename: c.Filename, Data: base64.StdEncoding.EncodeToString(c.Data)}, nil
}
func (x *Api) ComfortExportPDFs(in ExportPDFInput) (string, error) {
	return r1(x.app.ExportPDFsToFolder(x.ctx, in))
}
func (x *Api) ComfortParseMergeTable(data string) (MergeTable, error) {
	return r1(x.app.ParseMergeTable(x.ctx, data))
}
func (x *Api) ComfortMergeFromGroup(group string) (MergeTable, error) {
	return r1(x.app.MergeRecipientsFromGroup(x.ctx, group))
}
func (x *Api) ComfortPreviewMerge(in MergeJobInput) ([]merge.Rendered, error) {
	return r1(x.app.PreviewMerge(x.ctx, in, 10))
}
func (x *Api) ComfortStartMerge(in MergeJobInput) (MergeJobView, error) {
	return r1(x.app.StartMerge(x.ctx, in))
}
func (x *Api) ComfortListMergeJobs() ([]MergeJobView, error) { return r1(x.app.ListMergeJobs(x.ctx)) }
func (x *Api) ComfortGetMergeJob(id string) (MergeJobView, error) {
	return r1(x.app.GetMergeJob(x.ctx, id))
}
func (x *Api) ComfortRetryMergeJob(id string) (MergeJobView, error) {
	return r1(x.app.RetryMergeJob(x.ctx, id))
}
func (x *Api) ComfortCancelMergeJob(id string) error { return werr(x.app.CancelMergeJob(x.ctx, id)) }
func (x *Api) ComfortCreateMeeting(in MeetingInput) (MeetingLink, error) {
	return r1(x.app.CreateMeeting(x.ctx, in))
}
func (x *Api) ComfortApplyMeetingICS(ics string, l meeting.Link, lang string) (string, error) {
	return r1(x.app.ApplyMeetingToICS(x.ctx, ics, l, lang))
}
func (x *Api) ComfortListCertificates() ([]SMIMECertificate, error) {
	return r1(x.app.ListSMIMECertificates(x.ctx))
}
func (x *Api) ComfortImportCertificate(in SMIMEImportInput) (SMIMECertificate, error) {
	return r1(x.app.ImportSMIME(x.ctx, in))
}
func (x *Api) ComfortTrustCertificate(id int64, trusted bool) error {
	return werr(x.app.SetSMIMETrusted(x.ctx, id, trusted))
}
func (x *Api) ComfortDeleteCertificate(id int64) error {
	return werr(x.app.DeleteSMIMECertificate(x.ctx, id))
}
func (x *Api) ComfortSMIMEStatus(from string, recipients []string) (SMIMEStatus, error) {
	return r1(x.app.SMIMEStatusFor(x.ctx, from, recipients))
}

// PDFFile is a generated PDF (base64) for the desktop binding.
type PDFFile struct {
	Filename string `json:"filename"`
	Data     string `json:"data"`
}

func (x *Api) ComfortListPGPKeys() ([]PGPKeyInfo, error) { return r1(x.app.ListPGPKeys(x.ctx)) }
func (x *Api) ComfortGeneratePGPKey(in PGPGenerateInput) (PGPKeyInfo, error) {
	return r1(x.app.GeneratePGPKey(x.ctx, in))
}
func (x *Api) ComfortImportPGPKeys(in PGPImportInput) ([]PGPKeyInfo, error) {
	return r1(x.app.ImportPGPKeys(x.ctx, in))
}
func (x *Api) ComfortExportPGPPublicKey(id int64) (string, error) {
	return r1(x.app.ExportPGPPublicKey(x.ctx, id))
}
func (x *Api) ComfortExportPGPSecretKey(id int64, in PGPExportInput) (string, error) {
	return r1(x.app.ExportPGPSecretKey(x.ctx, id, in))
}
func (x *Api) ComfortSetPGPKeyVerified(id int64, verified bool) error {
	return werr(x.app.SetPGPKeyVerified(x.ctx, id, verified))
}
func (x *Api) ComfortDeletePGPKey(id int64) error { return werr(x.app.DeletePGPKey(x.ctx, id)) }
func (x *Api) ComfortLookupPGPKey(in PGPLookupInput) (PGPKeyInfo, error) {
	return r1(x.app.LookupPGPKey(x.ctx, in))
}
func (x *Api) ComfortPGPStatus(from string, recipients []string) (PGPStatus, error) {
	return r1(x.app.PGPStatusFor(x.ctx, from, recipients))
}
func (x *Api) ComfortListPGPPeers() ([]PGPPeerInfo, error) { return r1(x.app.ListPGPPeers(x.ctx)) }
func (x *Api) ComfortVerifyPGPPeer(in PGPPeerInput) (PGPKeyInfo, error) {
	return r1(x.app.VerifyPGPPeer(x.ctx, in))
}
func (x *Api) ComfortDeletePGPPeer(address string) error {
	return werr(x.app.DeletePGPPeer(x.ctx, address))
}
func (x *Api) ComfortUnlockPGPKey(id int64, in PGPUnlockInput) (PGPKeyInfo, error) {
	return r1(x.app.UnlockPGPKey(x.ctx, id, in))
}
func (x *Api) ComfortChangePGPPassphrase(id int64, in PGPPassphraseInput) (PGPKeyInfo, error) {
	return r1(x.app.ChangePGPPassphrase(x.ctx, id, in))
}
func (x *Api) ComfortLockPGPKeys() error {
	x.app.LockPGPKeys()
	return nil
}
