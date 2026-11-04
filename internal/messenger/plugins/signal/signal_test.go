// SPDX-License-Identifier: AGPL-3.0-or-later

package signal

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// fakeCLI is an in-process signal-cli speaking JSON-RPC over pipes.
type fakeCLI struct {
	io.Reader
	io.WriteCloser
	conn *wire.Conn
}

func (f *fakeCLI) Kill() error { f.WriteCloser.Close(); return nil }

type recorder struct {
	mu    sync.Mutex
	calls []string
	param []map[string]any
	clis  []*fakeCLI
}

func (r *recorder) spawn(_ context.Context, _ string) (CLI, error) {
	pr, hw := io.Pipe()
	hr, pw := io.Pipe()
	f := &fakeCLI{Reader: hr, WriteCloser: hw}
	f.conn = wire.NewConn(pr, pw, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(params, &m)
		r.mu.Lock()
		r.calls, r.param = append(r.calls, method), append(r.param, m)
		r.mu.Unlock()
		switch method {
		case "startLink":
			return map[string]string{"deviceLinkUri": "sgnl://linkdevice?uuid=x&pub_key=y"}, nil
		case "finishLink":
			return map[string]string{"number": "+4917011111111"}, nil
		case "listGroups":
			return []map[string]any{{"id": "R1JPVVA=", "name": "Familie"}}, nil
		case "listContacts":
			return []map[string]any{{"number": "+4915112345678", "uuid": "u-maria", "name": "Maria"}}, nil
		case "send":
			return map[string]any{"timestamp": 1700000000123}, nil
		}
		return map[string]any{}, nil
	})
	r.mu.Lock()
	r.clis = append(r.clis, f)
	r.mu.Unlock()
	return f, nil
}

func (r *recorder) last(method string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.calls) - 1; i >= 0; i-- {
		if r.calls[i] == method {
			return r.param[i]
		}
	}
	return nil
}

func TestSignalPairSendReceive(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	p := New()
	p.Spawn = rec.spawn
	evs := make(chan wire.Event, 64)
	env := sdk.NewTestEnv(t.TempDir(), nil, func(ev wire.Event) { evs <- ev })
	if err := p.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	var qr string
	acc, err := p.Pair(ctx, wire.PairRequest{Account: "a1", Method: wire.PairQR}, sdk.NewTestPairing("a1", func(pe wire.PairEvent) { qr = pe.QR }))
	if err != nil || acc.Self.Phone != "+4917011111111" || qr == "" {
		t.Fatal(acc, err, qr)
	}
	if fin := rec.last("finishLink"); fin["deviceName"] != "TuxMail" {
		t.Fatal(fin)
	}
	// The session survives a plugin restart.
	p2 := New()
	p2.Spawn = rec.spawn
	if err := p2.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	if accs, _ := p2.Accounts(ctx); len(accs) != 1 || accs[0].Self.Phone != "+4917011111111" {
		t.Fatal(accs)
	}
	if err := p.Connect(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	chats, err := p.ListChats(ctx, "a1")
	if err != nil || len(chats) != 2 || chats[0].ID != "g:R1JPVVA=" || chats[1].Peer.Phone != "+4915112345678" {
		t.Fatal(chats, err)
	}
	res, err := p.Send(ctx, wire.SendRequest{Account: "a1", ChatID: "+4915112345678", Text: "Hi", ReplyTo: "1699999999000@+4915112345678"})
	if err != nil || res.ID != "1700000000123@+4917011111111" {
		t.Fatal(res, err)
	}
	sp := rec.last("send")
	if sp["account"] != "+4917011111111" || sp["quoteAuthor"] != "+4915112345678" || sp["message"] != "Hi" {
		t.Fatal(sp)
	}
	if err := p.React(ctx, wire.ReactRequest{Account: "a1", ChatID: "g:R1JPVVA=", MessageID: "1699999999000@+4915112345678", Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	if rp := rec.last("sendReaction"); rp["groupId"] != "R1JPVVA=" || rp["targetAuthor"] != "+4915112345678" {
		t.Fatal(rp)
	}
	if err := p.MarkRead(ctx, wire.MarkReadRequest{Account: "a1", ChatID: "+4915112345678", MessageIDs: []string{"1@+4915112345678", "2@+4915112345678"}}); err != nil {
		t.Fatal(err)
	}
	if rp := rec.last("sendReceipt"); rp["recipient"] != "+4915112345678" || len(rp["targetTimestamp"].([]any)) != 2 {
		t.Fatal(rp)
	}
	// Incoming message pushed by signal-cli.
	cli := rec.clis[len(rec.clis)-1]
	drain := func() {
		for {
			select {
			case <-evs:
			default:
				return
			}
		}
	}
	drain()
	err = cli.conn.Notify("receive", map[string]any{"account": "+4917011111111", "envelope": map[string]any{
		"sourceNumber": "+4915112345678", "sourceUuid": "u-maria", "sourceName": "Maria", "timestamp": 1700000001000,
		"dataMessage": map[string]any{"timestamp": 1700000001000, "message": "Hallo", "groupInfo": map[string]any{"groupId": "R1JPVVA="},
			"attachments": []map[string]any{{"contentType": "image/jpeg", "filename": "a.jpg", "id": "att1.jpg", "size": 3}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var msg *wire.Message
	var chat *wire.Chat
	deadline := time.After(3 * time.Second)
	for msg == nil {
		select {
		case ev := <-evs:
			if ev.Chat != nil {
				chat = ev.Chat
			}
			if ev.Message != nil {
				msg = ev.Message
			}
		case <-deadline:
			t.Fatal("no message")
		}
	}
	if chat.Title != "Familie" || msg.ID != "1700000001000@+4915112345678" || msg.Sender.Phone != "+4915112345678" || msg.Media.Ref != "att1.jpg" {
		t.Fatalf("chat %+v msg %+v", chat, msg)
	}
	// Media is copied from signal-cli's attachment store.
	attDir := filepath.Join(env.DataDir, "a1", "signal-cli", "attachments")
	if err := os.MkdirAll(attDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attDir, "att1.jpg"), []byte("JPG"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(env.DataDir, "a1", "dl")
	if r, err := p.DownloadMedia(ctx, wire.MediaRequest{Account: "a1", Ref: "att1.jpg", Path: dst}); err != nil || r.Size != 3 {
		t.Fatal(r, err)
	}
	if _, err := p.DownloadMedia(ctx, wire.MediaRequest{Account: "a1", Ref: "../account.json", Path: dst}); wire.ErrorCode(err) != wire.CodeInvalid {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
}

func TestConvertEnvelopes(t *testing.T) {
	none := func(string) string { return "" }
	var e envelope
	_ = json.Unmarshal([]byte(`{"sourceNumber":"+49151","sourceUuid":"u1","receiptMessage":{"when":1700000000000,"isRead":true,"timestamps":[5,6]}}`), &e)
	evs := Convert("a", "+49170", e, none)
	if len(evs) != 1 || evs[0].Receipt.Kind != wire.ReceiptRead || evs[0].Receipt.MessageIDs[1] != "6@+49170" || evs[0].Receipt.ChatID != "+49151" {
		t.Fatalf("%+v", evs)
	}
	e = envelope{}
	_ = json.Unmarshal([]byte(`{"sourceNumber":"+49151","dataMessage":{"timestamp":9,"reaction":{"emoji":"❤","targetAuthorNumber":"+49170","targetSentTimestamp":5,"isRemove":true}}}`), &e)
	evs = Convert("a", "+49170", e, none)
	if len(evs) != 1 || evs[0].Reaction.MessageID != "5@+49170" || evs[0].Reaction.Emoji != "" {
		t.Fatalf("%+v", evs[0].Reaction)
	}
	e = envelope{}
	_ = json.Unmarshal([]byte(`{"sourceNumber":"+49170","syncMessage":{"sentMessage":{"destinationNumber":"+49151","timestamp":7,"message":"von Handy"}}}`), &e)
	evs = Convert("a", "+49170", e, none)
	if len(evs) != 2 || !evs[1].Message.FromMe || evs[1].Message.ChatID != "+49151" || evs[1].Message.ID != "7@+49170" {
		t.Fatalf("%+v", evs)
	}
	e = envelope{}
	_ = json.Unmarshal([]byte(`{"sourceNumber":"+49151","typingMessage":{"action":"STARTED"}}`), &e)
	if evs = Convert("a", "+49170", e, none); len(evs) != 1 || !evs[0].Typing.Active {
		t.Fatalf("%+v", evs)
	}
	e = envelope{}
	_ = json.Unmarshal([]byte(`{"sourceNumber":"+49151","dataMessage":{"timestamp":9,"remoteDelete":{"timestamp":4}}}`), &e)
	if evs = Convert("a", "+49170", e, none); len(evs) != 1 || evs[0].Kind != wire.EventDelete || evs[0].MessageID != "4@+49151" {
		t.Fatalf("%+v", evs)
	}
}
