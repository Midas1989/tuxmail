// SPDX-License-Identifier: AGPL-3.0-or-later

package wire

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Error codes. Negative codes are JSON-RPC 2.0 codes, positive codes are
// protocol errors a host can act upon.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603

	CodeNotSupported = 1 // the plugin/network does not support the call
	CodeNotLinked    = 2 // the account is not paired or was logged out
	CodeInvalid      = 3 // bad input (unknown chat, empty message ...)
	CodeRateLimited  = 4
	CodeNotFound     = 5
	CodeUnavailable  = 6 // network offline, try again
	CodeOutdated     = 7 // the network rejected the client version: update the plugin
	// CodeComponentMissing: an external program the plugin needs is missing
	// (e.g. signal-cli); the host can offer to install it.
	CodeComponentMissing = 8
)

// Error is an error returned by the remote side.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// Errorf creates a protocol error.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorCode returns the protocol error code of err (0 if it is none).
func ErrorCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// ErrClosed is returned by calls on a closed connection (e.g. the plugin
// process exited).
var ErrClosed = errors.New("wire: connection closed")

// MaxFrame bounds the size of one JSON frame.
const MaxFrame = 32 << 20

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Handler answers requests and notifications from the other side. For
// notifications the result is ignored; they are handled one after the
// other in the read loop, requests concurrently.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Conn is a bidirectional JSON-RPC 2.0 connection with newline-delimited
// frames. Both sides may call and notify. Incoming requests are handled
// concurrently.
type Conn struct {
	r       *bufio.Reader
	w       io.Writer
	wmu     sync.Mutex
	handler Handler

	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan frame
	closed  bool
	err     error
	done    chan struct{}

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startOnce sync.Once
}

// NewConn creates a connection and starts reading from r. h may be nil.
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	c := NewIdleConn(r, w, h)
	c.Start()
	return c
}

// NewIdleConn creates a connection that does not read before Start.
func NewIdleConn(r io.Reader, w io.Writer, h Handler) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &Conn{r: bufio.NewReaderSize(r, 64<<10), w: w, handler: h, pending: map[int64]chan frame{}, done: make(chan struct{}),
		ctx: ctx, cancel: cancel}
}

// Start starts reading (once).
func (c *Conn) Start() { c.startOnce.Do(func() { go c.readLoop() }) }

// Done is closed when the connection ended.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended (nil while it is open; io.EOF for a
// clean end of the stream).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection; pending calls fail with ErrClosed.
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

func (c *Conn) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.err = true, err
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	c.cancel()
	for _, ch := range pending {
		close(ch)
	}
	close(c.done)
}

func (c *Conn) readLoop() {
	for {
		line, err := readLine(c.r)
		if err != nil {
			c.shutdown(err)
			return
		}
		if len(line) == 0 {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			// A plugin printing garbage to stdout is a protocol violation.
			c.shutdown(fmt.Errorf("wire: bad frame: %w", err))
			return
		}
		switch {
		case f.Method != "" && f.ID == nil:
			// Notifications are delivered in order; handlers must not block long.
			c.serve(f)
		case f.Method != "":
			c.wg.Add(1)
			go func() { defer c.wg.Done(); c.serve(f) }()
		case f.ID != nil:
			c.mu.Lock()
			ch := c.pending[*f.ID]
			delete(c.pending, *f.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- f
			}
		}
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > MaxFrame {
			return nil, errors.New("wire: frame too large")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

func (c *Conn) serve(f frame) {
	var (
		res any
		err error
	)
	if c.handler == nil {
		err = Errorf(CodeMethodNotFound, "method %q not found", f.Method)
	} else {
		res, err = c.callHandler(f)
	}
	if f.ID == nil {
		return // notification
	}
	out := frame{JSONRPC: "2.0", ID: f.ID}
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Code: CodeInternal, Message: err.Error()}
		}
		out.Error = e
	} else {
		if res == nil {
			res = struct{}{}
		}
		b, merr := json.Marshal(res)
		if merr != nil {
			out.Error = &Error{Code: CodeInternal, Message: merr.Error()}
		} else {
			out.Result = b
		}
	}
	_ = c.write(out)
}

func (c *Conn) callHandler(f frame) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Errorf(CodeInternal, "panic in %s: %v", f.Method, r)
		}
	}()
	return c.handler(c.ctx, f.Method, f.Params)
}

func (c *Conn) write(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return errors.New("wire: frame too large")
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// Call sends a request and decodes the result into out (may be nil).
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	id := c.nextID.Add(1)
	ch := make(chan frame, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(frame{JSONRPC: "2.0", ID: &id, Method: method, Params: raw}); err != nil {
		c.forget(id)
		return fmt.Errorf("wire: write %s: %w", method, err)
	}
	select {
	case f, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if f.Error != nil {
			return f.Error
		}
		if out != nil && len(f.Result) > 0 {
			if err := json.Unmarshal(f.Result, out); err != nil {
				return fmt.Errorf("wire: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	if c.pending != nil {
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// Notify sends a notification (no answer).
func (c *Conn) Notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return c.write(frame{JSONRPC: "2.0", Method: method, Params: raw})
}

// Wait blocks until all running handlers returned (after the connection ended).
func (c *Conn) Wait() { c.wg.Wait() }

func marshalParams(p any) (json.RawMessage, error) {
	if p == nil {
		return nil, nil
	}
	b, err := json.Marshal(p)
	return b, err
}

// Decode unmarshals request params (an empty body leaves v unchanged).
func Decode(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return Errorf(CodeInvalidParams, "invalid params: %v", err)
	}
	return nil
}
