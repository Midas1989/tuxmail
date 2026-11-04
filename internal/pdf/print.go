// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"html/template"
	"time"
)

// PrintMail is one mail (or chat message) of a print view.
type PrintMail struct {
	From        string
	To          string
	Cc          string
	Date        time.Time
	Subject     string
	Attachments []string
	// HTML is the sanitized HTML body (remote content already removed or
	// allowed by the caller); Text is used when HTML is empty.
	HTML template.HTML
	Text string
	// Own marks messages of the user (chat layout: right-aligned bubbles).
	Own bool
}

// PrintView is the input of PrintHTML.
type PrintView struct {
	Title  string
	Lang   string // "de" or "en"
	Layout string // "mail" (classic, one block per mail) or "chat" (transcript)
	Mails  []PrintMail
	// Labels for the header block.
	Labels PrintLabels
	// Footer is printed at the end (e.g. "Exported with TuxMail").
	Footer string
}

// PrintLabels are the localized header labels.
type PrintLabels struct {
	From, To, Cc, Date, Subject, Attachments string
}

// DefaultLabels returns labels for "de" or "en".
func DefaultLabels(lang string) PrintLabels {
	if lang == "de" {
		return PrintLabels{From: "Von", To: "An", Cc: "Cc", Date: "Datum", Subject: "Betreff", Attachments: "Anhänge"}
	}
	return PrintLabels{From: "From", To: "To", Cc: "Cc", Date: "Date", Subject: "Subject", Attachments: "Attachments"}
}

var printTpl = template.Must(template.New("print").Funcs(template.FuncMap{
	"date": func(t time.Time, lang string) string {
		if t.IsZero() {
			return ""
		}
		if lang == "de" {
			return t.Format("02.01.2006 15:04")
		}
		return t.Format("Jan 2, 2006 3:04 PM")
	},
	"join": func(s []string) string {
		var b bytes.Buffer
		for i, v := range s {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(v)
		}
		return b.String()
	},
}).Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>
@page { size: A4; margin: 18mm 16mm; }
body { font: 10.5pt/1.45 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; color: #111; margin: 0; }
h1 { font-size: 15pt; margin: 0 0 10pt; }
.mail { break-inside: auto; margin-bottom: 18pt; }
.mail + .mail { border-top: 1px solid #ccc; padding-top: 12pt; }
table.hdr { border-collapse: collapse; margin-bottom: 8pt; font-size: 9.5pt; }
table.hdr th { text-align: left; padding: 1pt 10pt 1pt 0; color: #555; font-weight: 600; vertical-align: top; white-space: nowrap; }
table.hdr td { padding: 1pt 0; }
.body { border-top: 1px solid #ddd; padding-top: 8pt; overflow-wrap: anywhere; }
.body pre { white-space: pre-wrap; font: inherit; margin: 0; }
.chat .msg { max-width: 75%; margin: 4pt 0; padding: 5pt 8pt; border-radius: 8pt; background: #f1f3f5; break-inside: avoid; }
.chat .msg.own { margin-left: auto; background: #dbeafe; }
.chat .meta { font-size: 8pt; color: #555; margin-bottom: 2pt; }
footer { margin-top: 20pt; font-size: 8pt; color: #777; }
img { max-width: 100%; }
</style></head><body>
<h1>{{.Title}}</h1>
{{if eq .Layout "chat"}}<div class="chat">{{range .Mails}}
<div class="msg{{if .Own}} own{{end}}"><div class="meta">{{.From}} · {{date .Date $.Lang}}</div>
{{if .HTML}}{{.HTML}}{{else}}<pre>{{.Text}}</pre>{{end}}
{{if .Attachments}}<div class="meta">{{$.Labels.Attachments}}: {{join .Attachments}}</div>{{end}}</div>
{{end}}</div>{{else}}{{range .Mails}}
<section class="mail"><table class="hdr">
<tr><th>{{$.Labels.From}}</th><td>{{.From}}</td></tr>
{{if .To}}<tr><th>{{$.Labels.To}}</th><td>{{.To}}</td></tr>{{end}}
{{if .Cc}}<tr><th>{{$.Labels.Cc}}</th><td>{{.Cc}}</td></tr>{{end}}
<tr><th>{{$.Labels.Date}}</th><td>{{date .Date $.Lang}}</td></tr>
<tr><th>{{$.Labels.Subject}}</th><td>{{.Subject}}</td></tr>
{{if .Attachments}}<tr><th>{{$.Labels.Attachments}}</th><td>{{join .Attachments}}</td></tr>{{end}}
</table>
<div class="body">{{if .HTML}}{{.HTML}}{{else}}<pre>{{.Text}}</pre>{{end}}</div></section>
{{end}}{{end}}
{{if .Footer}}<footer>{{.Footer}}</footer>{{end}}
</body></html>`))

// PrintHTML renders the print view.
func PrintHTML(v PrintView) ([]byte, error) {
	if v.Lang == "" {
		v.Lang = "en"
	}
	if v.Labels == (PrintLabels{}) {
		v.Labels = DefaultLabels(v.Lang)
	}
	if v.Layout == "" {
		v.Layout = "mail"
	}
	var b bytes.Buffer
	if err := printTpl.Execute(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
