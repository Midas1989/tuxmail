// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// folderState is the decoded form of the opaque domain.SyncState.
//
// Without CONDSTORE (ModSeq == 0) a flag fingerprint is kept per UID so that
// flag changes can be detected by diffing; with CONDSTORE only UIDs are kept
// (needed to detect expunges without QRESYNC).
type folderState struct {
	UIDValidity uint32
	UIDNext     uint32
	ModSeq      uint64
	UIDs        []uint32    // ascending
	Flags       []flagPrint // parallel to UIDs; nil with CONDSTORE
}

// flagPrint is a compact fingerprint of a message's flags: a bit mask of the
// well-known flags plus an FNV hash of all other keywords.
type flagPrint struct {
	Mask    uint8
	KwsHash uint32
}

const (
	maskSeen uint8 = 1 << iota
	maskAnswered
	maskFlagged
	maskDeleted
	maskDraft
	maskForwarded
	maskKeywords // KwsHash follows
)

func fingerprint(flags []imap.Flag) flagPrint {
	var fp flagPrint
	var kws []string
	for _, f := range flags {
		switch strings.ToLower(string(f)) {
		case `\seen`:
			fp.Mask |= maskSeen
		case `\answered`:
			fp.Mask |= maskAnswered
		case `\flagged`:
			fp.Mask |= maskFlagged
		case `\deleted`:
			fp.Mask |= maskDeleted
		case `\draft`:
			fp.Mask |= maskDraft
		case strings.ToLower(string(domain.FlagForwarded)):
			fp.Mask |= maskForwarded
		case `\recent`:
		default:
			kws = append(kws, strings.ToLower(string(f)))
		}
	}
	if len(kws) > 0 {
		sort.Strings(kws)
		h := fnv.New32a()
		for _, k := range kws {
			h.Write([]byte(k))
			h.Write([]byte{0})
		}
		fp.Mask |= maskKeywords
		fp.KwsHash = h.Sum32()
	}
	return fp
}

const statePrefix = "imap1:"

func (s *folderState) encode() domain.SyncState {
	var raw bytes.Buffer
	var tmp [binary.MaxVarintLen64]byte
	put := func(v uint64) { raw.Write(tmp[:binary.PutUvarint(tmp[:], v)]) }
	put(uint64(s.UIDValidity))
	put(uint64(s.UIDNext))
	put(s.ModSeq)
	put(uint64(len(s.UIDs)))
	hasFlags := s.Flags != nil && len(s.Flags) == len(s.UIDs)
	if hasFlags {
		raw.WriteByte(1)
	} else {
		raw.WriteByte(0)
	}
	var prev uint32
	for _, u := range s.UIDs {
		put(uint64(u - prev))
		prev = u
	}
	if hasFlags {
		for _, f := range s.Flags {
			raw.WriteByte(f.Mask)
			if f.Mask&maskKeywords != 0 {
				put(uint64(f.KwsHash))
			}
		}
	}
	var out bytes.Buffer
	zw, _ := flate.NewWriter(&out, flate.BestCompression)
	zw.Write(raw.Bytes())
	zw.Close()
	return domain.SyncState(statePrefix + base64.RawURLEncoding.EncodeToString(out.Bytes()))
}

var errBadState = errors.New("imap: invalid sync state")

func decodeState(st domain.SyncState) (*folderState, error) {
	if st == "" {
		return &folderState{}, nil
	}
	b64, ok := strings.CutPrefix(string(st), statePrefix)
	if !ok {
		return nil, errBadState
	}
	z, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, errBadState
	}
	raw, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(z)), 64<<20))
	if err != nil {
		return nil, errBadState
	}
	r := bytes.NewReader(raw)
	get := func() uint64 {
		v, e := binary.ReadUvarint(r)
		if e != nil && err == nil {
			err = e
		}
		return v
	}
	s := &folderState{}
	s.UIDValidity = uint32(get())
	s.UIDNext = uint32(get())
	s.ModSeq = get()
	n := get()
	if err != nil || n > uint64(len(raw)) {
		return nil, errBadState
	}
	hasFlags, e := r.ReadByte()
	if e != nil {
		return nil, errBadState
	}
	s.UIDs = make([]uint32, n)
	var prev uint32
	for i := range s.UIDs {
		prev += uint32(get())
		s.UIDs[i] = prev
	}
	if hasFlags == 1 {
		s.Flags = make([]flagPrint, n)
		for i := range s.Flags {
			m, e := r.ReadByte()
			if e != nil {
				return nil, errBadState
			}
			s.Flags[i].Mask = m
			if m&maskKeywords != 0 {
				s.Flags[i].KwsHash = uint32(get())
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadState, err)
	}
	return s, nil
}

// index returns the position of uid in s.UIDs or -1.
func (s *folderState) index(uid uint32) int {
	i := sort.Search(len(s.UIDs), func(i int) bool { return s.UIDs[i] >= uid })
	if i < len(s.UIDs) && s.UIDs[i] == uid {
		return i
	}
	return -1
}
