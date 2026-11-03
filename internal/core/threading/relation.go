// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"sort"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// RelationKind is the type of a relation between two keys
// (message_relations.kind).
type RelationKind string

const (
	KindReply        RelationKind = "reply"
	KindForward      RelationKind = "forward"
	KindReference    RelationKind = "reference"
	KindDraftOf      RelationKind = "draft_of"
	KindRedirect     RelationKind = "redirect"
	KindInviteUpdate RelationKind = "invite_update"
	KindServerThread RelationKind = "server_thread"
	KindHeuristic    RelationKind = "heuristic"
	KindManual       RelationKind = "manual"
)

// Origin tells where a relation was detected (message_relations.origin).
type Origin string

const (
	OriginHeader        Origin = "header"         // In-Reply-To / References
	OriginThreadIndex   Origin = "thread_index"   // Outlook Thread-Index
	OriginServerThread  Origin = "server_thread"  // X-GM-THRID, Graph conversationId, EAS ConversationId
	OriginCompose       Origin = "compose"        // reply/forward action in this client
	OriginAttachment    Origin = "attachment"     // message/rfc822 attachment
	OriginInlineForward Origin = "inline_forward" // forwarded block in the body
	OriginCalendar      Origin = "calendar"       // iCalendar UID
	OriginHeuristic     Origin = "heuristic"      // subject heuristic
	OriginUser          Origin = "user"           // manual link by the user
)

// Confidence values from KONZEPT 6.3.
const (
	ConfidenceCertain      = 1.0
	ConfidenceServerThread = 0.95
	ConfidenceInline       = 0.8
	ConfidenceHeuristic    = 0.5
)

// Relation links a source message (the newer one) to a target key (the
// message or virtual thread it refers to). Relations are keyed by message
// key, so they survive moves, deletions and re-imports of a message.
type Relation struct {
	SourceKey  string       `json:"sourceKey"`
	TargetKey  string       `json:"targetKey"`
	Kind       RelationKind `json:"kind"`
	Origin     Origin       `json:"origin"`
	Confidence float64      `json:"confidence"`
}

// ID returns the unique identity of a relation (source, target, kind), which
// matches UNIQUE(source_key, target_key, kind) in the schema.
func (r Relation) ID() RelationID { return RelationID{r.SourceKey, r.TargetKey, r.Kind} }

// RelationID is the unique identity of a stored relation.
type RelationID struct {
	SourceKey string
	TargetKey string
	Kind      RelationKind
}

// ComposeAction is an action of the user in this client that created the message.
type ComposeAction string

const (
	ComposeNew     ComposeAction = ""
	ComposeReply   ComposeAction = "reply"
	ComposeForward ComposeAction = "forward"
)

// ComposeContext describes how an outgoing message was created. It is
// recorded when sending (KONZEPT 6.3: "wird beim Senden gespeichert").
type ComposeContext struct {
	Action ComposeAction
	// OriginalKeys are the keys of the messages replied to or forwarded.
	OriginalKeys []string
	// DraftKey is the key of the draft this message was produced from.
	DraftKey string
}

// MessageInput is everything known about a message that is relevant for
// relation extraction.
type MessageInput struct {
	Header domain.MessageHeaderInfo
	// Key is the stable message key. If empty, MessageKey(Header, ...) with
	// Namespace and Fingerprint is used.
	Key string
	// Namespace scopes server IDs (usually the account ID).
	Namespace string
	// Fingerprint is the fallback fingerprint for messages without Message-ID.
	Fingerprint string
	// Compose is set for messages written in this client.
	Compose *ComposeContext
	// AttachedMessageIDs are Message-IDs of message/rfc822 attachments
	// (forward as attachment). Raw header values are accepted.
	AttachedMessageIDs []string
	// CalendarUIDs are iCalendar UIDs of text/calendar parts.
	CalendarUIDs []string
	// InlineForwardKeys are keys of messages matched from inline-forwarded
	// blocks (see DetectForwards and MatchForward).
	InlineForwardKeys []string
}

// ResolveKey returns the message key of in.
func (in MessageInput) ResolveKey() string {
	if in.Key != "" {
		return in.Key
	}
	return MessageKey(in.Header, KeyOptions{Namespace: in.Namespace, Fingerprint: in.Fingerprint})
}

// ExtractRelations derives all relations of a message per KONZEPT 6.3:
//
//	In-Reply-To                       -> reply            1.0
//	References (last entry = reply)   -> reference/reply  1.0
//	compose reply/forward             -> reply/forward    1.0
//	compose draft                     -> draft_of         1.0
//	server thread ID                  -> server_thread    0.95
//	Outlook Thread-Index root         -> server_thread    0.95
//	message/rfc822 attachment         -> forward          1.0
//	inline forward match              -> forward          0.8
//	iCalendar UID                     -> invite_update    1.0
//
// Self references are dropped and the result is deduplicated by
// (source, target, kind), keeping the highest confidence. The order is
// deterministic.
func ExtractRelations(in MessageInput) []Relation {
	src := in.ResolveKey()
	if src == "" {
		return nil
	}
	var b relBuilder
	b.src = src
	h := in.Header

	inReplyTo := normalizeIDs(h.InReplyTo)
	for _, id := range inReplyTo {
		b.add(id, KindReply, OriginHeader, ConfidenceCertain)
	}
	refs := normalizeIDs(h.References)
	for i, id := range refs {
		kind := KindReference
		if i == len(refs)-1 {
			// The last References entry is the direct parent; it collapses
			// with the In-Reply-To relation when both name the same ID.
			kind = KindReply
		}
		b.add(id, kind, OriginHeader, ConfidenceCertain)
	}

	if c := in.Compose; c != nil {
		kind := KindReply
		if c.Action == ComposeForward {
			kind = KindForward
		}
		if c.Action != ComposeNew {
			for _, k := range c.OriginalKeys {
				b.add(normalizeKey(k), kind, OriginCompose, ConfidenceCertain)
			}
		}
		if c.DraftKey != "" {
			b.add(normalizeKey(c.DraftKey), KindDraftOf, OriginCompose, ConfidenceCertain)
		}
	}

	if k := ServerThreadKey(in.Namespace, h.ServerThreadID); k != "" {
		b.add(k, KindServerThread, OriginServerThread, ConfidenceServerThread)
	}
	if h.ThreadIndex != "" {
		if ti, err := DecodeThreadIndex(h.ThreadIndex); err == nil {
			b.add(ti.RootKey(), KindServerThread, OriginThreadIndex, ConfidenceServerThread)
		}
	}
	for _, id := range normalizeIDs(in.AttachedMessageIDs) {
		b.add(id, KindForward, OriginAttachment, ConfidenceCertain)
	}
	for _, k := range in.InlineForwardKeys {
		b.add(normalizeKey(k), KindForward, OriginInlineForward, ConfidenceInline)
	}
	for _, uid := range in.CalendarUIDs {
		b.add(ICalKey(uid), KindInviteUpdate, OriginCalendar, ConfidenceCertain)
	}
	return b.list()
}

// normalizeKey normalizes a key that may be a raw Message-ID; prefixed keys
// are returned unchanged.
func normalizeKey(k string) string {
	k = strings.TrimSpace(k)
	if strings.HasPrefix(k, "~") {
		return k
	}
	return normalizeOne(k)
}

// ComposeRelations returns the relations recorded when the user sends a
// reply or forward of the given originals in this client.
func ComposeRelations(sourceKey string, c ComposeContext) []Relation {
	return ExtractRelations(MessageInput{Key: sourceKey, Compose: &c})
}

// ManualRelation returns a user-created link between two messages.
func ManualRelation(sourceKey, targetKey string) Relation {
	return Relation{SourceKey: sourceKey, TargetKey: targetKey, Kind: KindManual, Origin: OriginUser, Confidence: ConfidenceCertain}
}

// InlineForwardRelation returns the relation for a matched inline forward.
func InlineForwardRelation(sourceKey, targetKey string) Relation {
	return Relation{SourceKey: sourceKey, TargetKey: targetKey, Kind: KindForward, Origin: OriginInlineForward, Confidence: ConfidenceInline}
}

type relBuilder struct {
	src  string
	rels []Relation
	idx  map[RelationID]int
}

func (b *relBuilder) add(target string, kind RelationKind, origin Origin, conf float64) {
	if target == "" || target == b.src {
		return
	}
	if b.idx == nil {
		b.idx = map[RelationID]int{}
	}
	r := Relation{SourceKey: b.src, TargetKey: target, Kind: kind, Origin: origin, Confidence: conf}
	if i, ok := b.idx[r.ID()]; ok {
		if conf > b.rels[i].Confidence {
			b.rels[i] = r
		}
		return
	}
	b.idx[r.ID()] = len(b.rels)
	b.rels = append(b.rels, r)
}

func (b *relBuilder) list() []Relation { return b.rels }

// DedupeRelations removes duplicates by (source, target, kind), keeping the
// highest confidence, and sorts the result.
func DedupeRelations(rels []Relation) []Relation {
	best := map[RelationID]Relation{}
	for _, r := range rels {
		if old, ok := best[r.ID()]; !ok || r.Confidence > old.Confidence {
			best[r.ID()] = r
		}
	}
	out := make([]Relation, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.SourceKey != b.SourceKey {
			return a.SourceKey < b.SourceKey
		}
		if a.TargetKey != b.TargetKey {
			return a.TargetKey < b.TargetKey
		}
		return a.Kind < b.Kind
	})
	return out
}
