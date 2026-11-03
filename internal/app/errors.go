// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"errors"
	"fmt"
	"net/http"

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
	return &Error{Code: CodeInternal, Status: http.StatusInternalServerError, Message: err.Error(), Err: err}
}
