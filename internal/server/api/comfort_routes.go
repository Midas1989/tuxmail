// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/base64"
	"io"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/meeting"
)

// comfortRoutes serve the mail comfort features (spam, categories,
// subscriptions, e-invoices, print/PDF, spell checking, mail merge, video
// links, S/MIME). The route list is documented at the top of
// frontend/src/lib/api/comfort.ts.
func comfortRoutes() []Route {
	return []Route{
		{"GET /api/comfort/settings", func(w w, r r, a A) { send(w)(a.ComfortSettings(r.Context())) }},
		{"PUT /api/comfort/settings", jsonHandler(func(w w, r r, a A, in app.ComfortSettings) {
			send(w)(a.SaveComfortSettings(r.Context(), in))
		})},
		{"GET /api/comfort/messages/{id}/insights", withID(func(w w, r r, a A, id int64) { send(w)(a.MessageInsights(r.Context(), id)) })},
		{"POST /api/comfort/messages/{id}/unsubscribe", withID(func(w w, r r, a A, id int64) {
			send(w)(a.UnsubscribeMessage(r.Context(), id))
		})},
		{"POST /api/comfort/messages/{id}/invoice-task", withID(func(w w, r r, a A, id int64) {
			send(w)(a.CreateInvoiceTask(r.Context(), id))
		})},
		{"GET /api/comfort/messages/{id}/pdf", withID(func(w w, r r, a A, id int64) {
			c, err := a.MessagePDF(r.Context(), id)
			if err != nil {
				writeErr(w, err)
				return
			}
			serveBlob(w, c, r.URL.Query().Get("download") == "")
		})},
		{"GET /api/comfort/print", func(w w, r r, a A) {
			q := r.URL.Query()
			mid, _ := strconv.ParseInt(q.Get("messageId"), 10, 64)
			cid, _ := strconv.ParseInt(q.Get("conversationId"), 10, 64)
			html, err := a.PrintView(r.Context(), app.PrintInput{MessageID: mid, ConversationID: cid, Layout: q.Get("layout"), Lang: q.Get("lang")})
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data: 'self'; frame-ancestors 'self'")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = io.WriteString(w, html)
		}},
		{"POST /api/comfort/pdf/export", jsonHandler(func(w w, r r, a A, in app.ExportPDFInput) {
			data, err := a.ExportPDFZip(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="tuxmail-pdf-export.zip"`)
			_, _ = w.Write(data)
		})},
		{"POST /api/comfort/spam", jsonHandler(func(w w, r r, a A, in app.MarkSpamInput) { done(w, a.MarkSpam(r.Context(), in)) })},
		{"POST /api/comfort/category", jsonHandler(func(w w, r r, a A, in app.SetCategoryInput) {
			done(w, a.SetMessageCategory(r.Context(), in))
		})},
		{"GET /api/comfort/categories", func(w w, r r, a A) { send(w)(a.ListCategories(r.Context(), r.URL.Query().Get("role"))) }},
		{"GET /api/comfort/categories/{category}/messages", func(w w, r r, a A) {
			send(w)(a.CategoryMessages(r.Context(), r.PathValue("category"), r.URL.Query().Get("role"), qInt(r, "limit")))
		}},
		{"POST /api/comfort/message-categories", jsonHandler(func(w w, r r, a A, in struct {
			IDs []int64 `json:"ids"`
		}) {
			send(w)(a.MessageCategories(r.Context(), in.IDs))
		})},
		{"GET /api/comfort/subscriptions", func(w w, r r, a A) { send(w)(a.ListSubscriptions(r.Context())) }},
		{"POST /api/comfort/subscriptions/unsubscribe", jsonHandler(func(w w, r r, a A, in struct{ Key string }) {
			send(w)(a.Unsubscribe(r.Context(), in.Key))
		})},
		{"POST /api/comfort/subscriptions/archive", jsonHandler(func(w w, r r, a A, in struct{ Key string }) {
			n, err := a.ArchiveSubscription(r.Context(), in.Key)
			send(w)(map[string]int{"moved": n}, err)
		})},
		{"POST /api/comfort/subscriptions/auto-archive", jsonHandler(func(w w, r r, a A, in struct{ Key string }) {
			send(w)(a.AutoArchiveSubscription(r.Context(), in.Key))
		})},
		{"POST /api/comfort/spell/check", jsonHandler(func(w w, r r, a A, in app.SpellCheckInput) { send(w)(a.SpellCheck(r.Context(), in)) })},
		{"GET /api/comfort/spell/words", func(w w, r r, a A) { send(w)(a.SpellWords(r.Context())) }},
		{"POST /api/comfort/spell/words", jsonHandler(func(w w, r r, a A, in struct{ Word string }) {
			done(w, a.AddSpellWord(r.Context(), in.Word))
		})},
		{"DELETE /api/comfort/spell/words/{word}", func(w w, r r, a A) { done(w, a.DeleteSpellWord(r.Context(), r.PathValue("word"))) }},
		{"POST /api/comfort/merge/table", func(w w, r r, a A) {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
			if err != nil {
				badRequest(w, "read body: %v", err)
				return
			}
			send(w)(a.ParseMergeTable(r.Context(), base64.StdEncoding.EncodeToString(raw)))
		}},
		{"GET /api/comfort/merge/group", func(w w, r r, a A) { send(w)(a.MergeRecipientsFromGroup(r.Context(), r.URL.Query().Get("name"))) }},
		{"POST /api/comfort/merge/preview", jsonHandler(func(w w, r r, a A, in app.MergeJobInput) { send(w)(a.PreviewMerge(r.Context(), in, 10)) })},
		{"GET /api/comfort/merge/jobs", func(w w, r r, a A) { send(w)(a.ListMergeJobs(r.Context())) }},
		{"POST /api/comfort/merge/jobs", jsonHandler(func(w w, r r, a A, in app.MergeJobInput) { send(w)(a.StartMerge(r.Context(), in)) })},
		{"GET /api/comfort/merge/jobs/{jobId}", func(w w, r r, a A) { send(w)(a.GetMergeJob(r.Context(), r.PathValue("jobId"))) }},
		{"POST /api/comfort/merge/jobs/{jobId}/retry", func(w w, r r, a A) { send(w)(a.RetryMergeJob(r.Context(), r.PathValue("jobId"))) }},
		{"POST /api/comfort/merge/jobs/{jobId}/cancel", func(w w, r r, a A) { done(w, a.CancelMergeJob(r.Context(), r.PathValue("jobId"))) }},
		{"POST /api/comfort/meeting", jsonHandler(func(w w, r r, a A, in app.MeetingInput) { send(w)(a.CreateMeeting(r.Context(), in)) })},
		{"POST /api/comfort/meeting/ics", jsonHandler(func(w w, r r, a A, in struct {
			ICS  string       `json:"ics"`
			Link meeting.Link `json:"link"`
			Lang string       `json:"lang"`
		}) {
			out, err := a.ApplyMeetingToICS(r.Context(), in.ICS, in.Link, in.Lang)
			send(w)(map[string]string{"ics": out}, err)
		})},
		{"GET /api/comfort/smime/certificates", func(w w, r r, a A) { send(w)(a.ListSMIMECertificates(r.Context())) }},
		{"POST /api/comfort/smime/certificates", jsonHandler(func(w w, r r, a A, in app.SMIMEImportInput) { send(w)(a.ImportSMIME(r.Context(), in)) })},
		{"PUT /api/comfort/smime/certificates/{id}/trust", withID(func(w w, r r, a A, id int64) {
			var in struct{ Trusted bool }
			if readJSON(w, r, &in) {
				done(w, a.SetSMIMETrusted(r.Context(), id, in.Trusted))
			}
		})},
		{"DELETE /api/comfort/smime/certificates/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteSMIMECertificate(r.Context(), id)) })},
		{"GET /api/comfort/smime/status", func(w w, r r, a A) {
			q := r.URL.Query()
			var rcpts []string
			for _, s := range strings.Split(q.Get("recipients"), ",") {
				if s = strings.TrimSpace(s); s != "" {
					rcpts = append(rcpts, s)
				}
			}
			send(w)(a.SMIMEStatusFor(r.Context(), q.Get("from"), rcpts))
		}},
		{"GET /api/comfort/pgp/keys", func(w w, r r, a A) { send(w)(a.ListPGPKeys(r.Context())) }},
		{"POST /api/comfort/pgp/keys", jsonHandler(func(w w, r r, a A, in app.PGPImportInput) { send(w)(a.ImportPGPKeys(r.Context(), in)) })},
		{"POST /api/comfort/pgp/keys/generate", jsonHandler(func(w w, r r, a A, in app.PGPGenerateInput) { send(w)(a.GeneratePGPKey(r.Context(), in)) })},
		{"POST /api/comfort/pgp/keys/lookup", jsonHandler(func(w w, r r, a A, in app.PGPLookupInput) { send(w)(a.LookupPGPKey(r.Context(), in)) })},
		{"GET /api/comfort/pgp/keys/{id}/public", withID(func(w w, r r, a A, id int64) {
			armored, err := a.ExportPGPPublicKey(r.Context(), id)
			send(w)(map[string]string{"armored": armored}, err)
		})},
		{"POST /api/comfort/pgp/keys/{id}/secret", withID(func(w w, r r, a A, id int64) {
			var in app.PGPExportInput
			if readJSON(w, r, &in) {
				armored, err := a.ExportPGPSecretKey(r.Context(), id, in)
				send(w)(map[string]string{"armored": armored}, err)
			}
		})},
		{"PUT /api/comfort/pgp/keys/{id}/trust", withID(func(w w, r r, a A, id int64) {
			var in struct{ Verified bool }
			if readJSON(w, r, &in) {
				done(w, a.SetPGPKeyVerified(r.Context(), id, in.Verified))
			}
		})},
		{"DELETE /api/comfort/pgp/keys/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeletePGPKey(r.Context(), id)) })},
		{"GET /api/comfort/pgp/peers", func(w w, r r, a A) { send(w)(a.ListPGPPeers(r.Context())) }},
		{"POST /api/comfort/pgp/peers/verify", jsonHandler(func(w w, r r, a A, in app.PGPPeerInput) { send(w)(a.VerifyPGPPeer(r.Context(), in)) })},
		{"POST /api/comfort/pgp/peers/delete", jsonHandler(func(w w, r r, a A, in app.PGPPeerInput) { done(w, a.DeletePGPPeer(r.Context(), in.Address)) })},
		{"GET /api/comfort/pgp/status", func(w w, r r, a A) {
			q := r.URL.Query()
			var rcpts []string
			for _, s := range strings.Split(q.Get("recipients"), ",") {
				if s = strings.TrimSpace(s); s != "" {
					rcpts = append(rcpts, s)
				}
			}
			send(w)(a.PGPStatusFor(r.Context(), q.Get("from"), rcpts))
		}},
	}
}
