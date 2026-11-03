// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"reflect"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func rel(src, dst string, k RelationKind, o Origin, c float64) Relation {
	return Relation{SourceKey: src, TargetKey: dst, Kind: k, Origin: o, Confidence: c}
}

func TestExtractRelations(t *testing.T) {
	const tidx = "AdpHncrgb5YZ/4uG0BG0LQDAT8lk/wAAEjRW"
	ti, _ := DecodeThreadIndex(tidx)
	tests := []struct {
		name string
		in   MessageInput
		want []Relation
	}{
		{
			name: "no key",
			in:   MessageInput{Header: domain.MessageHeaderInfo{InReplyTo: []string{"a@x"}}},
			want: nil,
		},
		{
			name: "in-reply-to and references",
			in: MessageInput{Header: domain.MessageHeaderInfo{
				MessageID:  "<m@x>",
				InReplyTo:  []string{"<p@X>"},
				References: []string{"<r@x>", "<q@x>", "<p@x>"},
			}},
			want: []Relation{
				rel("m@x", "p@x", KindReply, OriginHeader, 1),
				rel("m@x", "r@x", KindReference, OriginHeader, 1),
				rel("m@x", "q@x", KindReference, OriginHeader, 1),
			},
		},
		{
			name: "references only: last is reply",
			in: MessageInput{Header: domain.MessageHeaderInfo{
				MessageID:  "m@x",
				References: []string{"<r@x> <q@x>"},
			}},
			want: []Relation{
				rel("m@x", "r@x", KindReference, OriginHeader, 1),
				rel("m@x", "q@x", KindReply, OriginHeader, 1),
			},
		},
		{
			name: "in-reply-to differs from last reference",
			in: MessageInput{Header: domain.MessageHeaderInfo{
				MessageID:  "m@x",
				InReplyTo:  []string{"p@x"},
				References: []string{"r@x"},
			}},
			want: []Relation{
				rel("m@x", "p@x", KindReply, OriginHeader, 1),
				rel("m@x", "r@x", KindReply, OriginHeader, 1),
			},
		},
		{
			name: "self reference dropped",
			in: MessageInput{Header: domain.MessageHeaderInfo{
				MessageID:  "m@x",
				References: []string{"m@X"},
			}},
			want: nil,
		},
		{
			name: "server thread and thread-index",
			in: MessageInput{Namespace: "acc1", Header: domain.MessageHeaderInfo{
				MessageID:      "m@x",
				ServerThreadID: "1788",
				ThreadIndex:    tidx,
			}},
			want: []Relation{
				rel("m@x", "~thread:acc1:1788", KindServerThread, OriginServerThread, 0.95),
				rel("m@x", ti.RootKey(), KindServerThread, OriginThreadIndex, 0.95),
			},
		},
		{
			name: "invalid thread-index ignored",
			in:   MessageInput{Header: domain.MessageHeaderInfo{MessageID: "m@x", ThreadIndex: "garbage"}},
			want: nil,
		},
		{
			name: "compose reply with draft",
			in: MessageInput{
				Header:  domain.MessageHeaderInfo{MessageID: "m@x"},
				Compose: &ComposeContext{Action: ComposeReply, OriginalKeys: []string{"<o@X>"}, DraftKey: "~fp:abc"},
			},
			want: []Relation{
				rel("m@x", "o@x", KindReply, OriginCompose, 1),
				rel("m@x", "~fp:abc", KindDraftOf, OriginCompose, 1),
			},
		},
		{
			name: "compose forward of two",
			in: MessageInput{
				Header:  domain.MessageHeaderInfo{MessageID: "m@x"},
				Compose: &ComposeContext{Action: ComposeForward, OriginalKeys: []string{"a@x", "b@x"}},
			},
			want: []Relation{
				rel("m@x", "a@x", KindForward, OriginCompose, 1),
				rel("m@x", "b@x", KindForward, OriginCompose, 1),
			},
		},
		{
			name: "compose new message has no relations",
			in: MessageInput{
				Header:  domain.MessageHeaderInfo{MessageID: "m@x"},
				Compose: &ComposeContext{OriginalKeys: []string{"a@x"}},
			},
			want: nil,
		},
		{
			name: "attachment, inline forward and calendar",
			in: MessageInput{
				Header:             domain.MessageHeaderInfo{MessageID: "m@x"},
				AttachedMessageIDs: []string{"<att@x>"},
				InlineForwardKeys:  []string{"inl@x"},
				CalendarUIDs:       []string{"uid-1", ""},
			},
			want: []Relation{
				rel("m@x", "att@x", KindForward, OriginAttachment, 1),
				rel("m@x", "inl@x", KindForward, OriginInlineForward, 0.8),
				rel("m@x", "~ical:uid-1", KindInviteUpdate, OriginCalendar, 1),
			},
		},
		{
			name: "duplicate forward keeps highest confidence",
			in: MessageInput{
				Header:             domain.MessageHeaderInfo{MessageID: "m@x"},
				InlineForwardKeys:  []string{"f@x"},
				AttachedMessageIDs: []string{"f@x"},
			},
			want: []Relation{
				rel("m@x", "f@x", KindForward, OriginAttachment, 1),
			},
		},
		{
			name: "fingerprint key",
			in: MessageInput{
				Fingerprint: "ff",
				Header:      domain.MessageHeaderInfo{InReplyTo: []string{"p@x"}},
			},
			want: []Relation{rel("~fp:ff", "p@x", KindReply, OriginHeader, 1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractRelations(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractRelations =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestRelationHelpers(t *testing.T) {
	got := ComposeRelations("m@x", ComposeContext{Action: ComposeReply, OriginalKeys: []string{"o@x"}})
	if len(got) != 1 || got[0].Origin != OriginCompose || got[0].Kind != KindReply {
		t.Fatalf("ComposeRelations = %+v", got)
	}
	if r := ManualRelation("a", "b"); r.Kind != KindManual || r.Origin != OriginUser || r.Confidence != 1 {
		t.Fatalf("ManualRelation = %+v", r)
	}
	if r := InlineForwardRelation("a", "b"); r.Confidence != ConfidenceInline || r.Kind != KindForward {
		t.Fatalf("InlineForwardRelation = %+v", r)
	}
	d := DedupeRelations([]Relation{
		rel("b", "c", KindReply, OriginHeader, 0.5),
		rel("a", "c", KindReply, OriginHeader, 1),
		rel("b", "c", KindReply, OriginCompose, 1),
	})
	if len(d) != 2 || d[0].SourceKey != "a" || d[1].Confidence != 1 {
		t.Fatalf("DedupeRelations = %+v", d)
	}
}
