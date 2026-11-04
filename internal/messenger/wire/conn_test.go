// SPDX-License-Identifier: AGPL-3.0-or-later

package wire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func pipePair(hA, hB Handler) (*Conn, *Conn) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return NewConn(ar, aw, hA), NewConn(br, bw, hB)
}

func TestRoundTrip(t *testing.T) {
	got := make(chan string, 10)
	a, b := pipePair(nil, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case MethodListChats:
			var ref AccountRef
			if err := Decode(params, &ref); err != nil {
				return nil, err
			}
			return ChatList{Chats: []Chat{{ID: "c1", Kind: ChatGroup, Title: ref.Account}}}, nil
		case MethodSend:
			return nil, Errorf(CodeNotLinked, "not linked")
		case NotifyEvent:
			var ev Event
			_ = json.Unmarshal(params, &ev)
			got <- ev.Kind
			return nil, nil
		case "panic":
			panic("boom")
		}
		return nil, Errorf(CodeMethodNotFound, "nope")
	})
	defer a.Close()
	defer b.Close()
	ctx := context.Background()
	var cl ChatList
	if err := a.Call(ctx, MethodListChats, AccountRef{Account: "x"}, &cl); err != nil || len(cl.Chats) != 1 || cl.Chats[0].Title != "x" {
		t.Fatal(cl, err)
	}
	if err := a.Call(ctx, MethodSend, SendRequest{}, nil); ErrorCode(err) != CodeNotLinked {
		t.Fatal(err)
	}
	if err := a.Call(ctx, "panic", nil, nil); ErrorCode(err) != CodeInternal || !strings.Contains(err.Error(), "boom") {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if err := a.Notify(NotifyEvent, Event{Kind: k}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		select {
		case k := <-got:
			if k != want {
				t.Fatalf("order: got %s want %s", k, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("notification lost")
		}
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	// No handler on side a: b's call fails with method not found.
	if err := b.Call(tctx, MethodPing, nil, nil); ErrorCode(err) != CodeMethodNotFound {
		t.Fatal(err)
	}
	b.Close()
	if err := b.Call(ctx, MethodPing, nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestPendingFailsOnClose(t *testing.T) {
	block := make(chan struct{})
	a, b := pipePair(nil, func(context.Context, string, json.RawMessage) (any, error) { <-block; return nil, nil })
	defer close(block)
	errc := make(chan error, 1)
	go func() { errc <- a.Call(context.Background(), MethodPing, nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	b.Close()
	a.Close()
	if err := <-errc; !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestBadFrameEndsConnection(t *testing.T) {
	r, w := io.Pipe()
	c := NewConn(r, io.Discard, nil)
	go func() { _, _ = w.Write([]byte("not json\n")) }()
	select {
	case <-c.Done():
		if c.Err() == nil {
			t.Fatal("no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connection not closed")
	}
}
