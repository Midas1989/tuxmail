// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
)

// ThreadIndex is a decoded Outlook Thread-Index header
// ([MS-OXOMSG] 2.2.1.3 PidTagConversationIndex).
//
// The header is base64. The first 22 bytes identify the thread root: one
// reserved byte, five bytes of a FILETIME and a 16-byte GUID. Every reply
// appends a 5-byte child block (time delta and random bits).
type ThreadIndex struct {
	Root     []byte    // the 22-byte root block
	Children int       // number of complete 5-byte child blocks
	RootTime time.Time // creation time encoded in the root block (UTC)
}

// ErrInvalidThreadIndex is returned for values that are not a Thread-Index.
var ErrInvalidThreadIndex = errors.New("threading: invalid Thread-Index")

// threadIndexRootLen is the length of the root block.
const threadIndexRootLen = 22

// filetimeUnixOffset is the number of 100ns intervals between 1601-01-01 and 1970-01-01.
const filetimeUnixOffset = 116444736000000000

// DecodeThreadIndex decodes an Outlook Thread-Index header value. It
// tolerates whitespace, missing padding and URL-safe base64. Trailing bytes
// that do not form a complete child block are ignored.
func DecodeThreadIndex(s string) (ThreadIndex, error) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, "=")
	if s == "" {
		return ThreadIndex{}, ErrInvalidThreadIndex
	}
	var raw []byte
	var err error
	if strings.ContainsAny(s, "-_") {
		raw, err = base64.RawURLEncoding.DecodeString(s)
	} else {
		raw, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil || len(raw) < threadIndexRootLen {
		return ThreadIndex{}, ErrInvalidThreadIndex
	}
	ti := ThreadIndex{
		Root:     append([]byte(nil), raw[:threadIndexRootLen]...),
		Children: (len(raw) - threadIndexRootLen) / 5,
	}
	// Bytes 0..5 are the high-order 48 bits of a 64-bit FILETIME.
	var ft uint64
	for _, b := range raw[:6] {
		ft = ft<<8 | uint64(b)
	}
	ft <<= 16
	if ft > filetimeUnixOffset {
		d := ft - filetimeUnixOffset
		ti.RootTime = time.Unix(int64(d/10_000_000), int64(d%10_000_000)*100).UTC()
	}
	return ti, nil
}

// RootKey returns the virtual key of the thread root ("~tidx:<hex>").
func (t ThreadIndex) RootKey() string {
	return PrefixThreadIndex + hex.EncodeToString(t.Root)
}
