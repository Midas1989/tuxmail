// SPDX-License-Identifier: AGPL-3.0-or-later

// Package api serves the frontend API (frontend/src/lib/api/http.ts) over
// HTTPS/JSON and a WebSocket for live events (KONZEPT 4.5). Every request
// runs against the application layer (internal/app) of the authenticated
// user; authentication, CSRF protection and the per-user App lookup are
// provided by the embedding server through the wrap function of Mount.
//
// Error responses follow the server convention {"error": code, "message": text}.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/app"
)

// AppHandler handles a request for the App of the authenticated user.
type AppHandler func(w http.ResponseWriter, r *http.Request, a *app.App)

// Route is one mounted endpoint.
type Route struct {
	Pattern string // "METHOD /path/{param}"
	Handler AppHandler
}

// Options configure Mount.
type Options struct {
	// Skip lists patterns the embedding server serves itself.
	Skip map[string]bool
	// AllowOrigin checks the Origin of WebSocket handshakes (nil: same host only).
	AllowOrigin func(r *http.Request, origin string) bool
	Logger      *slog.Logger
}

// Mount registers all routes on mux; wrap authenticates the request and
// resolves the user's App.
func Mount(mux *http.ServeMux, wrap func(AppHandler) http.HandlerFunc, opts Options) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	for _, rt := range append(Routes(opts), MessengerRoutes()...) {
		if opts.Skip[rt.Pattern] {
			continue
		}
		mux.HandleFunc(rt.Pattern, wrap(rt.Handler))
	}
}

// ==== helpers ==================================================================

const maxBody = 4 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// writeErr writes an application error.
func writeErr(w http.ResponseWriter, err error) {
	e := app.AsError(err)
	status := e.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	msg := e.Error()
	if status >= 500 && e.Code == app.CodeInternal {
		slog.Error("api: request failed", "err", err)
		msg = "internal server error"
	}
	writeJSON(w, status, errorBody{Error: e.Code, Message: msg})
}

func badRequest(w http.ResponseWriter, format string, args ...any) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad_request", Message: fmt.Sprintf(format, args...)})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, errorBody{Error: "unsupported_media_type", Message: "expected application/json"})
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		badRequest(w, "invalid JSON body: %v", err)
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		badRequest(w, "invalid %s", name)
		return 0, false
	}
	return id, true
}

func qInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func qInt64(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

func qBool(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "1" || v == "true"
}

// reply writes v (200) or the error.
func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// done writes 204 or the error.
func done(w http.ResponseWriter, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// jsonHandler decodes the body into a new T and calls fn.
func jsonHandler[T any](fn func(w http.ResponseWriter, r *http.Request, a *app.App, in T)) AppHandler {
	return func(w http.ResponseWriter, r *http.Request, a *app.App) {
		var in T
		if !readJSON(w, r, &in) {
			return
		}
		fn(w, r, a, in)
	}
}

// withID parses the {id} path parameter.
func withID(fn func(w http.ResponseWriter, r *http.Request, a *app.App, id int64)) AppHandler {
	return func(w http.ResponseWriter, r *http.Request, a *app.App) {
		id, ok := pathID(w, r, "id")
		if !ok {
			return
		}
		fn(w, r, a, id)
	}
}

func convScope(r *http.Request) app.ConversationScope {
	return app.ConversationScope{Kind: r.URL.Query().Get("scope"), AccountID: qInt64(r, "accountId"), FolderID: qInt64(r, "folderId"),
		Role: r.URL.Query().Get("role"), TagID: qInt64(r, "tagId"), SearchFolderID: qInt64(r, "searchFolderId")}
}
