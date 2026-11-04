// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mimehdr has small helpers to adjust the header of a rendered
// message before submission through an API (Graph, Gmail, EWS) that takes
// the recipients from the MIME headers.
package mimehdr

import (
	"bytes"
	"errors"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

var wordDecoder = mime.WordDecoder{}

// DecodeWords decodes RFC 2047 encoded words (undecodable input is returned as is).
func DecodeWords(s string) string {
	if d, err := wordDecoder.DecodeHeader(s); err == nil {
		return d
	}
	return s
}

// ParseAddressList parses an address header value.
func ParseAddressList(v string) ([]domain.Address, error) {
	p := mail.AddressParser{WordDecoder: &wordDecoder}
	list, err := p.ParseList(v)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Address, 0, len(list))
	for _, a := range list {
		out = append(out, domain.Address{Name: a.Name, Email: strings.ToLower(a.Address)})
	}
	return out, nil
}

// ParseDate parses a Date header.
func ParseDate(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, errors.New("mimehdr: empty date")
	}
	return mail.ParseDate(v)
}

// HeaderEnd returns the offset of the empty line ending the header.
func HeaderEnd(raw []byte) int {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i + 2
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return i + 1
	}
	return len(raw)
}

// MissingRecipients returns the envelope recipients not named in
// To/Cc/Bcc.
func MissingRecipients(raw []byte, rcpts []string) []string {
	m, err := mail.ReadMessage(bytes.NewReader(raw[:HeaderEnd(raw)]))
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, h := range []string{"To", "Cc", "Bcc"} {
		if list, err := m.Header.AddressList(h); err == nil {
			for _, a := range list {
				have[strings.ToLower(a.Address)] = true
			}
		}
	}
	var out []string
	for _, r := range rcpts {
		if r != "" && !have[strings.ToLower(r)] {
			out = append(out, r)
			have[strings.ToLower(r)] = true
		}
	}
	return out
}

// Ensure prepends "name: value" unless the header exists.
func Ensure(raw []byte, name, value string) []byte {
	hdr := raw[:HeaderEnd(raw)]
	m, err := mail.ReadMessage(bytes.NewReader(append(append([]byte{}, hdr...), '\n')))
	if err == nil && m.Header.Get(name) != "" {
		return raw
	}
	eol := "\r\n"
	if !bytes.Contains(hdr, []byte("\r\n")) && bytes.Contains(hdr, []byte("\n")) {
		eol = "\n"
	}
	return append([]byte(name+": "+value+eol), raw...)
}
