// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Key prefixes for keys that are not a normalized Message-ID.
const (
	PrefixObjectID     = "~obj:"    // server object ID (EMAILID, X-GM-MSGID, immutableId)
	PrefixFingerprint  = "~fp:"     // content fingerprint
	PrefixServerThread = "~thread:" // server-side thread/conversation ID
	PrefixThreadIndex  = "~tidx:"   // Outlook Thread-Index root
	PrefixICal         = "~ical:"   // iCalendar UID
)

// FingerprintBodyPrefix is the number of body bytes that enter the fingerprint.
const FingerprintBodyPrefix = 4096

// FingerprintInput holds the values the fallback fingerprint is computed
// from (KONZEPT 6.2): Date, From, To, Subject, the size of the text part and
// a hash of the first 4 KiB of the body.
type FingerprintInput struct {
	Date     time.Time
	From     []domain.Address
	To       []domain.Address
	Subject  string
	TextSize int64
	// Body is the decoded text body; only the first FingerprintBodyPrefix
	// bytes are used, so callers may pass a truncated body.
	Body []byte
}

// Fingerprint returns the hex-encoded SHA-256 fingerprint of a message.
// Addresses are compared case-insensitively and order-independently, the
// date at second precision in UTC and the subject with collapsed whitespace,
// so the value is stable across servers and re-downloads.
func Fingerprint(in FingerprintInput) string {
	h := sha256.New()
	writeField := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	writeField("tuxmail-fp-v1")
	if in.Date.IsZero() {
		writeField("")
	} else {
		writeField(in.Date.UTC().Format(time.RFC3339))
	}
	writeField(addrList(in.From))
	writeField(addrList(in.To))
	writeField(collapseSpace(in.Subject))
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(in.TextSize))
	h.Write(n[:])
	body := in.Body
	if len(body) > FingerprintBodyPrefix {
		body = body[:FingerprintBodyPrefix]
	}
	bh := sha256.Sum256(body)
	h.Write(bh[:])
	return hex.EncodeToString(h.Sum(nil))
}

func addrList(as []domain.Address) string {
	list := make([]string, 0, len(as))
	for _, a := range as {
		if e := normalizeEmail(a.Email); e != "" {
			list = append(list, e)
		}
	}
	sort.Strings(list)
	return strings.Join(list, ",")
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// KeyOptions controls MessageKey.
type KeyOptions struct {
	// Namespace scopes server-side IDs, typically the account ID. Server
	// object and thread IDs are only unique within an account.
	Namespace string
	// Fingerprint is the precomputed fallback fingerprint (see Fingerprint).
	Fingerprint string
}

// MessageKey returns the stable key of a message following the priority of
// KONZEPT 6.2: normalized Message-ID, then server object ID, then
// fingerprint. It returns "" if none is available.
func MessageKey(h domain.MessageHeaderInfo, opt KeyOptions) string {
	if id := normalizeOne(h.MessageID); id != "" {
		return id
	}
	if obj := strings.TrimSpace(h.ServerObjectID); obj != "" {
		return PrefixObjectID + opt.Namespace + ":" + obj
	}
	if fp := strings.TrimSpace(opt.Fingerprint); fp != "" {
		return PrefixFingerprint + strings.ToLower(fp)
	}
	return ""
}

// IsVirtualKey reports whether key denotes a thread (server thread,
// Thread-Index root, iCalendar UID) instead of a message.
func IsVirtualKey(key string) bool {
	return strings.HasPrefix(key, PrefixServerThread) ||
		strings.HasPrefix(key, PrefixThreadIndex) ||
		strings.HasPrefix(key, PrefixICal)
}

// ServerThreadKey returns the virtual key of a server-side thread ID.
func ServerThreadKey(namespace, threadID string) string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ""
	}
	return PrefixServerThread + namespace + ":" + threadID
}

// ICalKey returns the virtual key of an iCalendar UID. UIDs are globally
// unique by definition (RFC 5545) and therefore not namespaced.
func ICalKey(uid string) string {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	return PrefixICal + uid
}
