// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// draftData converts a MIME message into the ApplicationData of an EAS 16
// draft ([MS-ASEMAIL] 2.2.2.38 IsDraft): recipients, subject, the HTML (or
// plain text) body and attachments.
func draftData(raw []byte, read bool) ([]*wbxml.Node, error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, fmt.Errorf("eas: parse draft: %w", err)
	}
	h := mr.Header
	addrs := func(key string) string {
		list, err := h.AddressList(key)
		if err != nil || len(list) == 0 {
			return ""
		}
		parts := make([]string, len(list))
		for i, a := range list {
			parts[i] = a.String()
		}
		return strings.Join(parts, ", ")
	}
	subject, _ := h.Subject()
	var plain, html string
	type att struct {
		name, ctype, cid string
		inline           bool
		data             []byte
	}
	var atts []att
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			return nil, fmt.Errorf("eas: parse draft: %w", err)
		}
		if part == nil {
			break
		}
		body, err := io.ReadAll(io.LimitReader(part.Body, 64<<20))
		if err != nil {
			return nil, err
		}
		switch ph := part.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := ph.ContentType()
			switch {
			case ct == "text/html" && html == "":
				html = string(body)
			case strings.HasPrefix(ct, "text/") && plain == "" && ct != "text/html":
				plain = string(body)
			default:
				cid := strings.Trim(ph.Get("Content-Id"), "<>")
				atts = append(atts, att{name: cid, ctype: ct, cid: cid, inline: true, data: body})
			}
		case *gomail.AttachmentHeader:
			name, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			if name == "" {
				name = "attachment"
			}
			atts = append(atts, att{name: name, ctype: ct, data: body})
		}
	}
	var data []*wbxml.Node
	if v := addrs("To"); v != "" {
		data = append(data, wbxml.S("Email:To", v))
	}
	if v := addrs("Cc"); v != "" {
		data = append(data, wbxml.S("Email:Cc", v))
	}
	data = append(data, wbxml.S("Email:Subject", subject))
	if read {
		data = append(data, wbxml.S("Email:Read", "1"))
	}
	btype, btext := "1", plain
	if html != "" {
		btype, btext = "2", html
	}
	data = append(data, wbxml.E("AirSyncBase:Body", wbxml.S("AirSyncBase:Type", btype), wbxml.S("AirSyncBase:Data", btext)))
	if v := addrs("Bcc"); v != "" {
		data = append(data, wbxml.S("Email2:Bcc", v))
	}
	if len(atts) > 0 {
		an := wbxml.E("AirSyncBase:Attachments")
		for _, a := range atts {
			add := wbxml.E("AirSyncBase:Add", wbxml.S("AirSyncBase:ClientId", newClientID()),
				wbxml.Op("AirSyncBase:Content", a.data), wbxml.S("AirSyncBase:DisplayName", a.name),
				wbxml.S("AirSyncBase:Method", "1"))
			if a.ctype != "" {
				add.Add(wbxml.S("AirSyncBase:ContentType", a.ctype))
			}
			if a.inline {
				add.Add(wbxml.S("AirSyncBase:IsInline", "1"))
				if a.cid != "" {
					add.Add(wbxml.S("AirSyncBase:ContentId", a.cid))
				}
			}
			an.Add(add)
		}
		data = append(data, an)
	}
	return data, nil
}
