// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Error is an application error with a stable code the UI can act on
// (ApiError.code in api.ts) and an HTTP status for the server mode.
type Error struct {
	Code    string
	Status  int
	Message string
	Err     error
	// RetryAfter (429 only) tells the client how long to wait.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf returns an *Error with code and HTTP status.
func Errorf(status int, code, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Message: fmt.Sprintf(format, args...)}
}

// Well-known error codes.
const (
	CodeNotFound         = "not_found"
	CodeInvalidInput     = "invalid_input"
	CodeNotConfigured    = "not_configured"
	CodeAINotConfigured  = "ai_not_configured"
	CodePinLimit         = "pin_limit"
	CodeNotSupported     = "not_supported"
	CodeVaultLocked      = "vault_locked"
	CodeInvalidPassword  = "invalid_password"
	CodeReadOnly         = "read_only"
	CodeConflict         = "conflict"
	CodeSendNotAvailable = "send_not_configured"
	CodeInternal         = "internal"
	// CodeInsecureEndpoint: an unencrypted http:// endpoint (ActiveSync, JMAP,
	// CalDAV/CardDAV) needs the explicit allowInsecure confirmation.
	CodeInsecureEndpoint = "insecure_endpoint"
)

var (
	errNotFound = &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: "not found"}
	errAI       = &Error{Code: CodeAINotConfigured, Status: http.StatusConflict, Message: "no AI provider is configured"}
)

func invalid(format string, args ...any) *Error {
	return Errorf(http.StatusBadRequest, CodeInvalidInput, format, args...)
}

func notSupported(what string) *Error {
	return Errorf(http.StatusNotImplemented, CodeNotSupported, "%s is not supported in this build", what)
}

// AsError maps any error to an *Error (store.ErrNotFound -> not_found ...).
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, secrets.ErrNotFound):
		return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: "not found", Err: err}
	case errors.Is(err, store.ErrPinLimit):
		return &Error{Code: CodePinLimit, Status: http.StatusConflict, Message: "at most three messages can be pinned", Err: err}
	case errors.Is(err, secrets.ErrWrongPassword):
		return &Error{Code: CodeInvalidPassword, Status: http.StatusUnauthorized, Message: "wrong master password", Err: err}
	case errors.Is(err, secrets.ErrLocked):
		return &Error{Code: CodeVaultLocked, Status: http.StatusLocked, Message: "the secret vault is locked", Err: err}
	}
	return &Error{Code: CodeInternal, Status: http.StatusInternalServerError, Message: PublicMessage(err), Err: err}
}

// ==== untrusted error text ======================================================

// Errors of providers embed text the remote server controls (IMAP "NO [ALERT]
// ...", HTTP bodies) and local details (dial addresses). In server mode they are
// replaced by a generic message with a correlation id, the details stay in the
// log; on the desktop the details are kept but stripped of control characters
// and bounded in length (audit NF-12).

// maxErrorMessage bounds foreign error text shown to the user (runes).
const maxErrorMessage = 300

var serverErrorMode atomic.Bool

// setErrorMode selects the error policy of the process (App.New).
func setErrorMode(mode string) { serverErrorMode.Store(mode == ModeServer) }

// SanitizeErrorText removes control characters (a server cannot forge extra
// lines or terminal sequences), collapses whitespace, replaces invalid UTF-8
// and bounds the length.
func SanitizeErrorText(s string) string {
	var b strings.Builder
	lastSpace := true
	n := 0
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		space := unicode.IsSpace(r) || unicode.IsControl(r)
		switch {
		case space && lastSpace:
			continue
		case !space && !unicode.IsPrint(r):
			continue // format characters (bidi overrides, zero-width), private use
		}
		if n >= maxErrorMessage {
			b.WriteString("...")
			break
		}
		if space {
			r = ' '
		}
		b.WriteRune(r)
		lastSpace = space
		n++
	}
	return strings.TrimSpace(b.String())
}

func newErrorRef() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// PublicMessage returns the text to show a user for an error that is not an
// application error: sanitized details on the desktop, a generic message with
// a correlation id in server mode (the details are logged with the id).
func PublicMessage(err error) string {
	if err == nil {
		return ""
	}
	if !serverErrorMode.Load() {
		return SanitizeErrorText(err.Error())
	}
	ref := newErrorRef()
	slog.Error("app: unmapped error", "ref", ref, "err", SanitizeErrorText(err.Error()))
	return "internal error (reference " + ref + ")"
}

// connectionMessage is PublicMessage for connection and login failures: in
// server mode the generic text names the class (code from classifyConnErr).
func connectionMessage(err error) string {
	if err == nil {
		return ""
	}
	if !serverErrorMode.Load() {
		return SanitizeErrorText(err.Error())
	}
	ref := newErrorRef()
	slog.Warn("app: connection error", "ref", ref, "err", SanitizeErrorText(err.Error()))
	text := "the connection failed"
	switch classifyConnErr(err) {
	case "auth_failed":
		text = "login failed"
	case "unreachable":
		text = "the server is not reachable"
	case "timeout":
		text = "the server did not answer in time"
	case "tls":
		text = "the TLS connection could not be verified"
	}
	return text + " (reference " + ref + ")"
}
