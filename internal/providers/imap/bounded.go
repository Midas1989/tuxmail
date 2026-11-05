// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"errors"
	"fmt"
	"io"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Limits for data a server can make us buffer. They protect the process (a
// shared one in server mode) from memory exhaustion by a hostile or broken
// IMAP server: go-imap's Collect/ReadAll buffer every literal completely
// before the caller sees a single byte.
const (
	// DefaultMaxMessageSize bounds one FETCH BODY[] literal (FetchRaw).
	DefaultMaxMessageSize = 64 << 20
	// DefaultMaxFolderMessages bounds the message count of a folder that is
	// synchronised; larger folders (or servers announcing more) are refused.
	DefaultMaxFolderMessages = 2_000_000

	// maxFetchSection bounds a single header section literal.
	maxFetchSection = 1 << 20
	// maxFetchTotal bounds all buffered literals of one FETCH command.
	maxFetchTotal = 64 << 20
	// maxListEntries bounds the folders returned by one LIST command.
	maxListEntries = 100_000
)

// ErrMessageTooLarge is returned when a message exceeds the size limit.
var ErrMessageTooLarge = errors.New("imap: message exceeds the size limit")

// ErrResponseTooLarge is returned when a server response exceeds the limits
// the client is willing to buffer.
var ErrResponseTooLarge = errors.New("imap: server response exceeds the size limit")

// ErrFolderTooLarge is returned for folders with more messages than the
// configured limit.
var ErrFolderTooLarge = errors.New("imap: folder has too many messages")

// hangUp closes the connection of a client whose reader goroutine is blocked
// handing data to the caller: imapclient.Client.Close waits for that goroutine,
// which only continues once the pending command is released by drain (cheap,
// because the connection is already gone).
func hangUp(c *imapclient.Client, drain func()) {
	done := make(chan struct{})
	go func() {
		_ = c.Close()
		close(done)
	}()
	drain()
	<-done
}

// readLimited reads at most max bytes from r. It reports ok=false when r holds
// more.
func readLimited(r io.Reader, max int64) (b []byte, ok bool, err error) {
	b, err = io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > max {
		return nil, false, nil
	}
	return b, true, nil
}

// collectFetch is imapclient.FetchCommand.Collect with bounds: no body
// section literal may exceed maxSection, all literals together maxTotal and
// at most maxMsgs messages are accepted (0 = unlimited). When a bound is hit
// the connection is closed (draining the rest of an oversized literal would
// cost as much bandwidth as the attacker wants to spend) and
// ErrResponseTooLarge is returned.
func collectFetch(c *imapclient.Client, cmd *imapclient.FetchCommand, maxSection, maxTotal int64, maxMsgs int) ([]*imapclient.FetchMessageBuffer, error) {
	var (
		out   []*imapclient.FetchMessageBuffer
		total int64
	)
	abort := func(why string) ([]*imapclient.FetchMessageBuffer, error) {
		hangUp(c, func() { _ = cmd.Close() })
		return nil, fmt.Errorf("%w (%s)", ErrResponseTooLarge, why)
	}
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		if maxMsgs > 0 && len(out) >= maxMsgs {
			return abort("too many messages")
		}
		buf := &imapclient.FetchMessageBuffer{SeqNum: msg.SeqNum}
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch item := item.(type) {
			case imapclient.FetchItemDataBodySection:
				var b []byte
				if item.Literal != nil {
					limit := min(maxSection, maxTotal-total)
					var ok bool
					var err error
					b, ok, err = readLimited(item.Literal, limit)
					if err != nil {
						_ = cmd.Close()
						return nil, err
					}
					if !ok {
						return abort("literal too large")
					}
					total += int64(len(b))
				}
				buf.BodySection = append(buf.BodySection, imapclient.FetchBodySectionBuffer{Section: item.Section, Bytes: b})
			case imapclient.FetchItemDataBinarySection:
				// Never requested; refuse rather than buffer.
				return abort("unexpected binary section")
			case imapclient.FetchItemDataFlags:
				buf.Flags = item.Flags
			case imapclient.FetchItemDataEnvelope:
				buf.Envelope = item.Envelope
			case imapclient.FetchItemDataInternalDate:
				buf.InternalDate = item.Time
			case imapclient.FetchItemDataRFC822Size:
				buf.RFC822Size = item.Size
			case imapclient.FetchItemDataUID:
				buf.UID = item.UID
			case imapclient.FetchItemDataBodyStructure:
				buf.BodyStructure = item.BodyStructure
			case imapclient.FetchItemDataBinarySectionSize:
				buf.BinarySectionSize = append(buf.BinarySectionSize, item)
			case imapclient.FetchItemDataModSeq:
				buf.ModSeq = item.ModSeq
			}
		}
		out = append(out, buf)
	}
	if err := cmd.Close(); err != nil {
		return out, err
	}
	return out, nil
}

// collectList is imapclient.ListCommand.Collect with a bound on the number of
// mailboxes.
func collectList(c *imapclient.Client, cmd *imapclient.ListCommand) ([]*imap.ListData, error) {
	var out []*imap.ListData
	for {
		d := cmd.Next()
		if d == nil {
			break
		}
		if len(out) >= maxListEntries {
			hangUp(c, func() { _ = cmd.Close() })
			return nil, fmt.Errorf("%w (too many mailboxes)", ErrResponseTooLarge)
		}
		out = append(out, d)
	}
	if err := cmd.Close(); err != nil {
		return out, err
	}
	return out, nil
}
