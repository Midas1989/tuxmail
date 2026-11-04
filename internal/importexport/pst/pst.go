// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pst reads Outlook PST/OST files with github.com/mooijtech/go-pst
// (Apache-2.0) and turns every mail into an RFC 5322 message. Appointments,
// contacts and tasks are not converted (they are counted as skipped).
//
// Outlook keeps the original internet headers of received mail
// (PR_TRANSPORT_MESSAGE_HEADERS); they are reused, and the body and the
// attachments are re-encoded as MIME. Mails without them (sent items,
// drafts) get headers built from the MAPI properties.
package pst

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"os"
	"strings"
	"sync"
	"time"

	charsets "github.com/emersion/go-message/charset"
	gopst "github.com/mooijtech/go-pst/v6/pkg"
	"github.com/mooijtech/go-pst/v6/pkg/properties"
	"golang.org/x/text/encoding"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// Available reports whether PST import is compiled in.
const Available = true

var charsetOnce sync.Once

// Source reads one PST/OST file.
type Source struct {
	ie.ProblemList
	path    string
	skipped int
}

// Open returns a source for a PST or OST file.
func Open(path string) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || string(head) != "!BDN" {
		return nil, ie.Corrupt("%s is not a PST/OST file", path)
	}
	return &Source{path: path}, nil
}

// Skipped returns the number of non-mail items (appointments, contacts ...).
func (s *Source) Skipped() int { return s.skipped }

// errStop carries a callback error through go-pst.
type errStop struct{ err error }

func (e errStop) Error() string { return e.err.Error() }

// Walk implements importexport.Source.
func (s *Source) Walk(ctx context.Context, fn func(ie.Message) error) (err error) {
	charsetOnce.Do(func() {
		gopst.ExtendCharsets(func(name string, enc encoding.Encoding) { charsets.RegisterEncoding(name, enc) })
	})
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	defer func() {
		// go-pst panics on some malformed files.
		if r := recover(); r != nil {
			err = ie.Corrupt("pst: %v", r)
		}
	}()
	file, err := gopst.New(f)
	if err != nil {
		return ie.Corrupt("pst: %v", err)
	}
	defer file.Cleanup()
	root, err := file.GetRootFolder()
	if err != nil {
		return ie.Corrupt("pst: %v", err)
	}
	var stop error
	var walk func(folder *gopst.Folder, path []string, depth int) error
	walk = func(folder *gopst.Folder, path []string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if folder.MessageCount > 0 {
			if err := s.folderMessages(ctx, folder, path, fn); err != nil {
				return err
			}
		}
		if !folder.HasSubFolders || depth > 32 {
			return nil
		}
		subs, err := folder.GetSubFolders()
		if err != nil {
			s.Add(strings.Join(path, "/"), err)
			return nil
		}
		for i := range subs {
			sub := &subs[i]
			p := path
			// "Top of Personal Folders" / "Top of Outlook data file" is the
			// invisible root of the tree.
			if depth > 0 || !strings.HasPrefix(strings.ToLower(sub.Name), "top of") {
				p = append(append([]string{}, path...), sub.Name)
			}
			if err := walk(sub, p, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&root, nil, 0); err != nil {
		var es errStop
		if errors.As(err, &es) {
			return es.err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return stop
}

func (s *Source) folderMessages(ctx context.Context, folder *gopst.Folder, path []string, fn func(ie.Message) error) error {
	it, err := folder.GetMessageIterator()
	if errors.Is(err, gopst.ErrMessagesNotFound) {
		return nil
	} else if err != nil {
		s.Add(strings.Join(path, "/"), err)
		return nil
	}
	for it.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg := it.Value()
		props, ok := msg.Properties.(*properties.Message)
		if !ok {
			s.skipped++
			continue
		}
		raw, recv, flags, err := buildMessage(msg, props)
		if err != nil {
			s.Add(strings.Join(path, "/")+": "+props.GetSubject(), err)
			continue
		}
		if err := fn(ie.Message{Raw: raw, Folder: path, Flags: flags, Received: recv}); err != nil {
			return errStop{err}
		}
	}
	if err := it.Err(); err != nil {
		s.Add(strings.Join(path, "/"), err)
	}
	return nil
}

// skipHeaders are replaced by the regenerated MIME structure.
var skipHeaders = map[string]bool{"content-type": true, "content-transfer-encoding": true, "mime-version": true,
	"content-disposition": true}

func unixNano(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func buildMessage(msg *gopst.Message, p *properties.Message) ([]byte, time.Time, ie.Flags, error) {
	var b bytes.Buffer
	recv := unixNano(p.GetMessageDeliveryTime())
	sent := unixNano(p.GetClientSubmitTime())
	if sent.IsZero() {
		sent = recv
	}
	// MSGFLAG_READ 0x1, MSGFLAG_UNSENT 0x8.
	mf := p.GetMessageFlags()
	flags := ie.Flags{Seen: mf&0x1 != 0, Draft: mf&0x8 != 0}

	if th := strings.TrimSpace(p.GetTransportMessageHeaders()); th != "" {
		writeHeaders(&b, th)
	} else {
		from := p.GetSenderEmailAddress()
		if name := p.GetSenderName(); name != "" && from != "" {
			from = mime.QEncoding.Encode("utf-8", name) + " <" + from + ">"
		}
		if from != "" {
			fmt.Fprintf(&b, "From: %s\r\n", from)
		}
		if to := p.GetDisplayTo(); to != "" {
			fmt.Fprintf(&b, "To: %s\r\n", mime.QEncoding.Encode("utf-8", to))
		}
		if cc := p.GetDisplayCc(); cc != "" {
			fmt.Fprintf(&b, "Cc: %s\r\n", mime.QEncoding.Encode("utf-8", cc))
		}
		fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", p.GetSubject()))
		if !sent.IsZero() {
			fmt.Fprintf(&b, "Date: %s\r\n", sent.Format(time.RFC1123Z))
		}
		if id := p.GetInternetMessageId(); id != "" {
			fmt.Fprintf(&b, "Message-ID: %s\r\n", id)
		}
		if irt := p.GetInReplyToId(); irt != "" {
			fmt.Fprintf(&b, "In-Reply-To: %s\r\n", irt)
		}
		if refs := p.GetInternetReferences(); refs != "" {
			fmt.Fprintf(&b, "References: %s\r\n", refs)
		}
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	type att struct {
		name, mime, cid string
		data            []byte
	}
	var atts []att
	if n, err := msg.GetAttachmentCount(); err == nil && n > 0 {
		it, err := msg.GetAttachmentIterator()
		if err == nil {
			for it.Next() {
				a := it.Value()
				var buf bytes.Buffer
				if _, err := a.WriteTo(&buf); err != nil {
					continue
				}
				name := a.GetAttachLongFilename()
				if name == "" {
					name = a.GetAttachFilename()
				}
				mt := a.GetAttachMimeTag()
				if mt == "" {
					mt = "application/octet-stream"
				}
				atts = append(atts, att{name: name, mime: mt, cid: a.GetAttachContentId(), data: buf.Bytes()})
			}
		}
	}

	text, html := p.GetBody(), p.GetBodyHtml()
	boundary := func(tag string) string { return "=_tuxmail_pst_" + tag + fmt.Sprint(time.Now().UnixNano()) }
	writeBody := func(w *bytes.Buffer) {
		switch {
		case html != "" && text != "":
			bd := boundary("alt")
			fmt.Fprintf(w, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", bd)
			fmt.Fprintf(w, "--%s\r\n", bd)
			writeText(w, "text/plain", text)
			fmt.Fprintf(w, "--%s\r\n", bd)
			writeText(w, "text/html", html)
			fmt.Fprintf(w, "--%s--\r\n", bd)
		case html != "":
			writeText(w, "text/html", html)
		default:
			writeText(w, "text/plain", text)
		}
	}
	if len(atts) == 0 {
		writeBody(&b)
		return b.Bytes(), recv, flags, nil
	}
	bd := boundary("mix")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", bd)
	fmt.Fprintf(&b, "--%s\r\n", bd)
	writeBody(&b)
	for _, a := range atts {
		fmt.Fprintf(&b, "--%s\r\n", bd)
		params := map[string]string{}
		if a.name != "" {
			params["name"] = a.name
		}
		fmt.Fprintf(&b, "Content-Type: %s\r\n", mime.FormatMediaType(a.mime, params))
		disp := "attachment"
		if a.cid != "" {
			fmt.Fprintf(&b, "Content-ID: <%s>\r\n", strings.Trim(a.cid, "<>"))
			disp = "inline"
		}
		dp := map[string]string{}
		if a.name != "" {
			dp["filename"] = a.name
		}
		fmt.Fprintf(&b, "Content-Disposition: %s\r\n", mime.FormatMediaType(disp, dp))
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(a.data)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", bd)
	return b.Bytes(), recv, flags, nil
}

func writeHeaders(b *bytes.Buffer, th string) {
	lines := strings.Split(strings.ReplaceAll(th, "\r\n", "\n"), "\n")
	skip := false
	for _, l := range lines {
		if l == "" {
			break
		}
		if l[0] == ' ' || l[0] == '\t' {
			if !skip {
				b.WriteString(l + "\r\n")
			}
			continue
		}
		k, _, _ := strings.Cut(l, ":")
		skip = skipHeaders[strings.ToLower(strings.TrimSpace(k))]
		if !skip {
			b.WriteString(l + "\r\n")
		}
	}
}

func writeText(w *bytes.Buffer, ct, body string) {
	fmt.Fprintf(w, "Content-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", ct)
	qp := quotedprintable.NewWriter(w)
	_, _ = qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")))
	_ = qp.Close()
	w.WriteString("\r\n")
}
