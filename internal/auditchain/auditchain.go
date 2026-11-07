// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auditchain is the HMAC chain of the audit log in the Basis format
// (Basis spec/BETRIEB.md, ADR 0003):
//
//	hash = HMAC-SHA256(key, prev_hash || JSON{actor,action,target,old,new,ip,ts})
//
// with sorted keys, separators without spaces and unescaped UTF-8. Byte for byte
// the Basis Go module (go/audit), which is not imported because its repository
// is private and tuxmail is built on public CI; the Basis test vector proves it.
package auditchain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Genesis is prev_hash of the first entry.
const Genesis = "0000000000000000000000000000000000000000000000000000000000000000"

var sensitive = map[string]bool{"password": true, "passwort": true, "secret": true, "token": true,
	"totp_secret": true, "api_key": true, "private_key": true}

// Entry is one link of the chain.
type Entry struct {
	Actor, Action, Target string
	Old, New              map[string]any
	IP                    *string
	TS                    string
}

func redact(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sensitive[strings.ToLower(k)] && v != nil && v != "" {
			out[k] = "********"
		} else {
			out[k] = v
		}
	}
	return out
}

func writeCanon(buf *bytes.Buffer, v any) {
	switch t := v.(type) {
	case map[string]any:
		if t == nil {
			buf.WriteString("null")
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanon(buf, k)
			buf.WriteByte(':')
			writeCanon(buf, t[k])
		}
		buf.WriteByte('}')
	default:
		enc := json.NewEncoder(buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(t)
		buf.Truncate(buf.Len() - 1) // the newline of Encode
	}
}

// Payload is the canonical JSON of the entry, secrets redacted.
func (e Entry) Payload() []byte {
	var ip, oldV, newV any
	if e.IP != nil {
		ip = *e.IP
	}
	if e.Old != nil {
		oldV = redact(e.Old)
	}
	if e.New != nil {
		newV = redact(e.New)
	}
	var buf bytes.Buffer
	writeCanon(&buf, map[string]any{"actor": e.Actor, "action": e.Action, "target": e.Target,
		"old": oldV, "new": newV, "ip": ip, "ts": e.TS})
	return buf.Bytes()
}

// Hash computes the hash of the entry after prev.
func (e Entry) Hash(prev string, key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(prev))
	m.Write(e.Payload())
	return hex.EncodeToString(m.Sum(nil))
}

// Valid reports in constant time whether hash is the right one.
func (e Entry) Valid(prev, hash string, key []byte) bool {
	return hmac.Equal([]byte(e.Hash(prev, key)), []byte(hash))
}
