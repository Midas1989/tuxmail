// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/pdf"
	"github.com/Midas1989/Mailclient/internal/store"
)

func addrString(as []domain.Address) string {
	var out []string
	for _, a := range as {
		if a.Name != "" {
			out = append(out, a.Name+" <"+a.Email+">")
		} else {
			out = append(out, a.Email)
		}
	}
	return strings.Join(out, ", ")
}

func lang2(l string) string {
	if strings.HasPrefix(strings.ToLower(l), "de") {
		return "de"
	}
	return "en"
}

func (a *App) uiLang(ctx context.Context) string {
	if p, err := a.GetPreferences(ctx); err == nil && p.Locale != "" {
		return lang2(p.Locale)
	}
	return "de"
}

// renderArchivePDF is the PDFRenderer of the archive_pdf workflow action:
// Chromium for HTML mails if available, otherwise the built-in PDF/A-3b
// text renderer; the original .eml and the attachments are embedded.
func (a *App) renderArchivePDF(ctx context.Context, raw []byte, req PDFRenderRequest) ([]byte, error) {
	pm, err := parse.ParseContext(ctx, raw, parse.Options{SMIME: smimeReader{a: a}})
	if err != nil {
		return nil, err
	}
	lang := a.uiLang(ctx)
	labels := pdf.DefaultLabels(lang)
	from := addrString(pm.Header.From)
	meta := pdf.Meta{Title: pm.Header.Subject, Author: from, Created: pm.Header.Date, Subject: "E-Mail"}
	if meta.Created.IsZero() {
		meta.Created = a.now()
	}
	var files []pdf.EmbeddedFile
	if req.EmbedOriginal {
		files = append(files, pdf.EmbeddedFile{Name: "original.eml", MIME: "message/rfc822", Data: raw, Description: "Original e-mail", ModTime: meta.Created})
	}
	var attNames []string
	for _, at := range pm.Attachments {
		if at.Inline {
			continue
		}
		attNames = append(attNames, at.Filename)
		if req.IncludeAttachments && len(at.Data) > 0 {
			rel := "Supplement"
			if n := strings.ToLower(at.Filename); n == "factur-x.xml" || n == "zugferd-invoice.xml" || n == "xrechnung.xml" {
				rel = "Data"
			}
			files = append(files, pdf.EmbeddedFile{Name: uniqueName(files, at.Filename), MIME: at.MIMEType, Data: at.Data, Relationship: rel, ModTime: meta.Created})
		}
	}
	s := a.comfortSettings()
	// HTML mails: Gotenberg (TUXMAIL_GOTENBERG_URL, server), else headless
	// Chromium (TUXMAIL_CHROMIUM_PATH / settings / auto-detected), else the
	// built-in text renderer below.
	if strings.TrimSpace(pm.HTML) != "" {
		gotenberg := pdf.GotenbergURL()
		browser, berr := "", error(pdf.ErrNoChromium)
		if gotenberg == "" && s.PDF.UseChromium {
			browser, berr = a.chromium(s)
		}
		if gotenberg != "" || berr == nil {
			if body := printFragment(pm, 0, 0); body != "" {
				html, _ := pdf.PrintHTML(pdf.PrintView{Title: pm.Header.Subject, Lang: lang, Offline: true, Mails: []pdf.PrintMail{{From: from,
					To: addrString(pm.Header.To), Cc: addrString(pm.Header.Cc), Date: pm.Header.Date, Subject: pm.Header.Subject,
					Attachments: attNames, HTML: template.HTML(body)}}})
				var out []byte
				var err error
				if gotenberg != "" {
					out, err = pdf.GotenbergToPDF(ctx, a.opts.HTTPClient, gotenberg, html)
				} else if out, err = pdf.HTMLToPDF(ctx, browser, html); err == nil {
					chromiumBroken.Store(false)
				} else {
					chromiumBroken.Store(true) // do not wait for a broken browser again
				}
				if err == nil {
					if enriched, err := pdf.Enrich(out, meta, files); err == nil {
						return enriched, nil
					}
				} else {
					a.log.Info("comfort: HTML to PDF failed, using the built-in renderer", "err", err)
				}
			}
		}
	}
	sec := pdf.Section{Title: pm.Header.Subject, Body: pm.Text, Attachments: attNames, Fields: []pdf.Field{{Label: labels.From, Value: from}}}
	if to := addrString(pm.Header.To); to != "" {
		sec.Fields = append(sec.Fields, pdf.Field{Label: labels.To, Value: to})
	}
	if cc := addrString(pm.Header.Cc); cc != "" {
		sec.Fields = append(sec.Fields, pdf.Field{Label: labels.Cc, Value: cc})
	}
	sec.Fields = append(sec.Fields, pdf.Field{Label: labels.Date, Value: formatDate(pm.Header.Date, lang)})
	return pdf.RenderText(pdf.Document{Meta: meta, Sections: []pdf.Section{sec}, Files: files, FooterLeft: "TuxMail",
		AttachmentsLabel: labels.Attachments})
}

func uniqueName(files []pdf.EmbeddedFile, name string) string {
	if name == "" {
		name = "attachment"
	}
	n, i := name, 2
	for {
		dup := false
		for _, f := range files {
			if f.Name == n {
				dup = true
			}
		}
		if !dup {
			return n
		}
		ext := filepath.Ext(name)
		n = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
		i++
	}
}

func formatDate(t time.Time, lang string) string {
	if t.IsZero() {
		return ""
	}
	if lang == "de" {
		return t.Local().Format("02.01.2006 15:04")
	}
	return t.Local().Format("Jan 2, 2006 3:04 PM")
}

// chromiumBroken is set after a failed Chromium run; later renders use the
// built-in renderer right away (until the settings change).
var chromiumBroken atomic.Bool

func (a *App) chromium(s ComfortSettings) (string, error) {
	if chromiumBroken.Load() {
		return "", pdf.ErrNoChromium
	}
	if s.PDF.ChromiumPath != "" {
		if _, err := os.Stat(s.PDF.ChromiumPath); err == nil {
			return s.PDF.ChromiumPath, nil
		}
	}
	return pdf.FindChromium()
}

// PrintInput selects what PrintView renders.
type PrintInput struct {
	MessageID      int64  `json:"messageId,omitempty"`
	ConversationID int64  `json:"conversationId,omitempty"`
	Layout         string `json:"layout,omitempty"` // mail | chat
	Lang           string `json:"lang,omitempty"`
}

// PrintView returns the print view HTML of a mail or a whole conversation
// (classic or as chat transcript). Remote content stays blocked.
func (a *App) PrintView(ctx context.Context, in PrintInput) (string, error) {
	var msgs []store.Message
	title := ""
	switch {
	case in.MessageID != 0:
		m, err := a.st.GetMessage(ctx, in.MessageID)
		if err != nil {
			return "", err
		}
		msgs = []store.Message{m}
		title = m.Subject
	case in.ConversationID != 0:
		ms, err := a.st.ListMessagesByConversation(ctx, in.ConversationID)
		if err != nil {
			return "", err
		}
		msgs = ms
		if c, err := a.GetConversation(ctx, in.ConversationID); err == nil {
			title = c.Title
		}
	default:
		return "", invalid("messageId or conversationId required")
	}
	lang := lang2(in.Lang)
	if in.Lang == "" {
		lang = a.uiLang(ctx)
	}
	own := map[string]bool{}
	if ids, err := a.st.ListIdentities(ctx, 0); err == nil {
		for _, i := range ids {
			own[strings.ToLower(i.Email)] = true
		}
	}
	v := pdf.PrintView{Title: title, Lang: lang, Layout: in.Layout, Footer: "TuxMail"}
	for _, m := range msgs {
		raw, err := a.rawMessage(m)
		if err != nil {
			v.Mails = append(v.Mails, pdf.PrintMail{From: m.From, Subject: m.Subject, Date: msgTime(m), Text: m.Snippet})
			continue
		}
		pm, err := parse.ParseContext(ctx, raw, parse.Options{SkipAttachmentData: true, SMIME: smimeReader{a: a}})
		if err != nil {
			continue
		}
		pmail := pdf.PrintMail{From: addrString(pm.Header.From), To: addrString(pm.Header.To), Cc: addrString(pm.Header.Cc),
			Date: pm.Header.Date, Subject: pm.Header.Subject, Text: pm.Text, Own: own[fromEmail(pm)]}
		for _, at := range pm.Attachments {
			if !at.Inline {
				pmail.Attachments = append(pmail.Attachments, at.Filename)
			}
		}
		if in.Layout != "chat" {
			if body := printFragment(pm, m.ID, len(v.Mails)); body != "" {
				pmail.HTML = template.HTML(body) // sanitized, style sheet scoped to this mail
			}
		}
		v.Mails = append(v.Mails, pmail)
	}
	out, err := pdf.PrintHTML(v)
	return string(out), err
}

// MessagePDF renders one mail as archive PDF (PDF/A-3 with the .eml and
// the attachments embedded) and returns it with a file name.
func (a *App) MessagePDF(ctx context.Context, id int64) (AttachmentContent, error) {
	m, err := a.st.GetMessage(ctx, id)
	if err != nil {
		return AttachmentContent{}, err
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return AttachmentContent{}, err
	}
	data, err := a.renderArchivePDF(ctx, raw, PDFRenderRequest{EmbedOriginal: true, IncludeAttachments: true})
	if err != nil {
		return AttachmentContent{}, err
	}
	return AttachmentContent{Filename: a.pdfName(ctx, m), MIMEType: "application/pdf", Data: data}, nil
}

func (a *App) pdfName(ctx context.Context, m store.Message) string {
	s := a.comfortSettings()
	v := pdf.NameVars{Date: msgTime(m), From: m.From, Subject: m.Subject, MessageID: m.MessageIDHdr}
	if in, err := a.st.GetInsight(ctx, m.ID); err == nil {
		v.Category = in.Category
	}
	p := pdf.RenderPath(s.PDF.NameTemplate, v)
	return filepath.Base(p)
}

// ExportPDFInput selects mails for a batch PDF export.
type ExportPDFInput struct {
	IDs      []int64 `json:"ids,omitempty"`
	FolderID int64   `json:"folderId,omitempty"`
	Query    string  `json:"query,omitempty"` // search query (mail scope)
	Limit    int     `json:"limit,omitempty"` // default 500
}

func (a *App) exportIDs(ctx context.Context, in ExportPDFInput) ([]int64, error) {
	limit := in.Limit
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	ids := append([]int64(nil), in.IDs...)
	if in.FolderID != 0 {
		ms, err := a.st.ListMessagesInFolder(ctx, in.FolderID, limit, 0)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
	}
	if q := strings.TrimSpace(in.Query); q != "" {
		found, err := a.searchIDs(ctx, q, store.ScopeMail)
		if err != nil {
			return nil, err
		}
		ids = append(ids, found...)
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// ExportPDFZip renders the selected mails into a ZIP of PDFs.
func (a *App) ExportPDFZip(ctx context.Context, in ExportPDFInput) ([]byte, error) {
	ids, err := a.exportIDs(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, invalid("no mails selected")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := map[string]int{}
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c, err := a.MessagePDF(ctx, id)
		if err != nil {
			a.log.Info("comfort: PDF export skipped a mail", "message", id, "err", err)
			continue
		}
		name := c.Filename
		if n := used[name]; n > 0 {
			name = fmt.Sprintf("%s (%d).pdf", strings.TrimSuffix(name, ".pdf"), n+1)
		}
		used[c.Filename]++
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(c.Data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExportPDFsToFolder writes the PDFs into <data dir>/exports/<timestamp>
// (desktop) and returns the folder.
func (a *App) ExportPDFsToFolder(ctx context.Context, in ExportPDFInput) (string, error) {
	if a.opts.DataDir == "" || a.opts.Mode == ModeServer {
		return "", Errorf(501, CodeNotSupported, "export to a folder is only available on the desktop")
	}
	ids, err := a.exportIDs(ctx, in)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(a.opts.DataDir, "exports", a.now().Format("2006-01-02_150405"))
	t := pdf.LocalTarget{Dir: dir}
	for _, id := range ids {
		c, err := a.MessagePDF(ctx, id)
		if err != nil {
			continue
		}
		if _, _, err := pdf.Store(ctx, t, c.Filename, c.Data, false); err != nil {
			return dir, err
		}
	}
	return dir, nil
}

// printFragment returns the sanitized HTML body of a parsed mail for the
// print view / archive PDF, wrapped in its own scope: the mail's style
// sheet only applies inside the wrapper and the wrapper contains (clips)
// positioned content, so a mail cannot hide or overlay the header block
// printed above it (audit B-12). "" for mails without HTML.
func printFragment(pm *parse.Message, messageID int64, n int) string {
	if strings.TrimSpace(pm.HTML) == "" {
		return ""
	}
	scope := "tm-mail-" + strconv.Itoa(n)
	san := sanitize.New(sanitize.Config{CIDURLTemplate: CIDURLTemplate})
	res := san.Sanitize(pm.HTML, sanitize.Options{MessageID: strconv.FormatInt(messageID, 10), Fragment: true, Scope: scope})
	return `<div class="tm-mail ` + scope + `">` + res.HTML + `</div>`
}
