// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// collState is the decoded form of a mail folder's domain.SyncState: the
// collection SyncKey plus the flags of every known item (EAS Change
// commands may only carry the modified properties, so the full flag set is
// reconstructed from this map).
type collState struct {
	Key   string
	Items map[string]itemFlags
}

func newCollState() *collState { return &collState{Items: map[string]itemFlags{}} }

func (s *collState) clone() *collState {
	c := &collState{Key: s.Key, Items: make(map[string]itemFlags, len(s.Items))}
	for k, v := range s.Items {
		v.Categories = append([]string(nil), v.Categories...)
		c.Items[k] = v
	}
	return c
}

const mailStatePrefix = "eas1:"

// encode serializes s as "eas1:" + base64url(deflate(lines)). The first line
// is the SyncKey, each further line "serverId \t mask [\t category \x1f ...]".
func (s *collState) encode() domain.SyncState {
	var raw bytes.Buffer
	raw.WriteString(s.Key)
	raw.WriteByte('\n')
	ids := make([]string, 0, len(s.Items))
	for id := range s.Items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		f := s.Items[id]
		raw.WriteString(id)
		raw.WriteByte('\t')
		raw.WriteString(strconv.Itoa(int(f.Mask)))
		if len(f.Categories) > 0 {
			raw.WriteByte('\t')
			raw.WriteString(strings.Join(f.Categories, "\x1f"))
		}
		raw.WriteByte('\n')
	}
	var out bytes.Buffer
	zw, _ := flate.NewWriter(&out, flate.BestCompression)
	zw.Write(raw.Bytes())
	zw.Close()
	return domain.SyncState(mailStatePrefix + base64.RawURLEncoding.EncodeToString(out.Bytes()))
}

var errBadState = errors.New("eas: invalid sync state")

func decodeCollState(st domain.SyncState) (*collState, error) {
	s := newCollState()
	if st == "" {
		return s, nil
	}
	b64, ok := strings.CutPrefix(string(st), mailStatePrefix)
	if !ok {
		return nil, errBadState
	}
	z, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, errBadState
	}
	raw, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(z)), 256<<20))
	if err != nil {
		return nil, errBadState
	}
	lines := strings.Split(string(raw), "\n")
	s.Key = lines[0]
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		parts := strings.SplitN(l, "\t", 3)
		if len(parts) < 2 {
			return nil, errBadState
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil || m < 0 || m > 255 {
			return nil, errBadState
		}
		f := itemFlags{Mask: uint8(m)}
		if len(parts) == 3 && parts[2] != "" {
			f.Categories = strings.Split(parts[2], "\x1f")
		}
		s.Items[parts[0]] = f
	}
	return s, nil
}

// ==== folder remote ids ==========================================================

// Folder remote ids are the escaped ServerIds of the folder and its parents
// joined by "/", e.g. "5/1a": the sync engine derives the hierarchy from
// the delimiter, while the last segment is the collection id.
const delimiter = "/"

var idEscaper = strings.NewReplacer("%", "%25", "/", "%2F")
var idUnescaper = strings.NewReplacer("%2F", "/", "%2f", "/", "%25", "%")

func escapeID(id string) string { return idEscaper.Replace(id) }

// CollectionID returns the EAS ServerId of a folder remote id.
func CollectionID(remoteID string) string {
	if i := strings.LastIndex(remoteID, delimiter); i >= 0 {
		remoteID = remoteID[i+1:]
	}
	return idUnescaper.Replace(remoteID)
}
