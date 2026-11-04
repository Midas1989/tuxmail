// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Target stores archive files (local folder here; Nextcloud, OpenCloud and
// WebDAV targets are provided by the cloud integration).
type Target interface {
	// Exists reports whether the path exists already.
	Exists(ctx context.Context, p string) (bool, error)
	// Put writes data to the path (relative, "/"-separated), creating
	// directories as needed.
	Put(ctx context.Context, p string, data []byte) error
}

// Targets resolves a workflow target name ("local", "nextcloud", an
// account name ...) to a Target.
type Targets interface {
	Target(ctx context.Context, name string) (Target, error)
}

// LocalTarget stores below a directory.
type LocalTarget struct{ Dir string }

// ErrPathEscape is returned for paths leaving the target directory.
var ErrPathEscape = errors.New("pdf: archive path leaves the target directory")

func (t LocalTarget) resolve(p string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(p, `\`, "/"))
	full := filepath.Join(t.Dir, filepath.FromSlash(clean))
	rel, err := filepath.Rel(t.Dir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", ErrPathEscape
	}
	return full, nil
}

// Exists implements Target.
func (t LocalTarget) Exists(_ context.Context, p string) (bool, error) {
	full, err := t.resolve(p)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(full)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Put implements Target.
func (t LocalTarget) Put(_ context.Context, p string, data []byte) error {
	full, err := t.resolve(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, full)
}

// NameVars are the values for file-name templates.
type NameVars struct {
	Date      time.Time
	From      string // sender name or address
	Subject   string
	MessageID string
	Account   string
	Folder    string
	Category  string
	// Invoice data (empty if none).
	InvoiceNumber string
	InvoiceSeller string
	InvoiceAmount string
}

var tplVarRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_.]+)\s*\}\}`)

// RenderPath fills a path template such as
// "Buchhaltung/{{jahr}}/{{monat}}/{{datum}}_{{absender}}_{{betreff}}.pdf".
// Variables (German and English): datum/date (2006-01-02), jahr/year,
// monat/month, tag/day, zeit/time (1504), absender/from, betreff/subject,
// konto/account, ordner/folder, kategorie/category, messageid,
// rechnungsnummer/invoice.number, lieferant/invoice.seller,
// betrag/invoice.amount. Values are sanitized for file names; "/" in the
// template separates directories. ".pdf" is appended if missing.
func RenderPath(tpl string, v NameVars) string {
	if strings.TrimSpace(tpl) == "" {
		tpl = "{{datum}}_{{absender}}_{{betreff}}.pdf"
	}
	d := v.Date
	if d.IsZero() {
		d = time.Now()
	}
	out := tplVarRe.ReplaceAllStringFunc(tpl, func(m string) string {
		k := strings.ToLower(tplVarRe.FindStringSubmatch(m)[1])
		var val string
		switch k {
		case "datum", "date":
			val = d.Format("2006-01-02")
		case "jahr", "year":
			val = d.Format("2006")
		case "monat", "month":
			val = d.Format("01")
		case "tag", "day":
			val = d.Format("02")
		case "zeit", "time":
			val = d.Format("1504")
		case "absender", "from", "sender":
			val = v.From
		case "betreff", "subject":
			val = v.Subject
		case "konto", "account":
			val = v.Account
		case "ordner", "folder":
			val = v.Folder
		case "kategorie", "category":
			val = v.Category
		case "messageid", "message_id":
			val = v.MessageID
		case "rechnungsnummer", "invoice.number":
			val = v.InvoiceNumber
		case "lieferant", "invoice.seller":
			val = v.InvoiceSeller
		case "betrag", "invoice.amount":
			val = v.InvoiceAmount
		}
		return SanitizeName(val)
	})
	parts := strings.Split(strings.ReplaceAll(out, `\`, "/"), "/")
	var clean []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		clean = append(clean, p)
	}
	res := strings.Join(clean, "/")
	if !strings.HasSuffix(strings.ToLower(res), ".pdf") {
		res += ".pdf"
	}
	return res
}

// SanitizeName makes s safe as a file-name component (max. 80 runes).
func SanitizeName(s string) string {
	var b strings.Builder
	n := 0
	lastUnderscore := false
	for _, r := range strings.TrimSpace(s) {
		if n >= 80 {
			break
		}
		switch {
		case strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r):
			r = '_'
		case unicode.IsSpace(r):
			r = ' '
		}
		if r == '_' && lastUnderscore {
			continue
		}
		lastUnderscore = r == '_'
		b.WriteRune(r)
		n++
	}
	return strings.Trim(b.String(), " ._")
}

// Store writes data to the target at p. With skipDuplicates, an existing
// file is left alone (duplicate protection via Message-ID in the name or
// the caller's index) and its path returned; otherwise a counter is added.
func Store(ctx context.Context, t Target, p string, data []byte, skipDuplicates bool) (string, bool, error) {
	ok, err := t.Exists(ctx, p)
	if err != nil {
		return "", false, err
	}
	if ok && skipDuplicates {
		return p, false, nil
	}
	final := p
	for i := 2; ok; i++ {
		ext := path.Ext(p)
		final = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(p, ext), i, ext)
		if ok, err = t.Exists(ctx, final); err != nil {
			return "", false, err
		}
		if i > 999 {
			return "", false, errors.New("pdf: too many files with the same name")
		}
	}
	if err := t.Put(ctx, final, data); err != nil {
		return "", false, err
	}
	return final, true, nil
}
