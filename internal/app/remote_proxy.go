// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/store"
)

// Forwarding the interface's HTTP calls to the server. The UI in server mode
// is the same as the web client's (HttpTransport); its fetch ends here. The
// core adds the token, keeps responses for reading offline and notes actions
// for later, so that a network failure never turns into a hang or an empty app.

// ServerProxyRequest is a fetch of the interface.
type ServerProxyRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"` // base64
}

// ServerProxyResponse is its answer.
type ServerProxyResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"` // base64
}

// headerOffline marks an answer from this device's memory.
const headerOffline = "X-Tuxmail-Offline"

// Only these headers cross the boundary, in both directions: never
// Authorization, Cookie or Set-Cookie.
var (
	proxyRequestHeaders  = []string{"Accept", "Content-Type", "X-Filename", "If-Match", "If-None-Match", "Range"}
	proxyResponseHeaders = []string{"Content-Type", "Content-Disposition", "Retry-After", "Etag", "Last-Modified", "Content-Range", "Accept-Ranges", "X-Content-Type-Options", "Content-Security-Policy"}
)

// remoteForbidden are routes the interface has no business calling through
// the device token: the sealed secrets and the device flow belong to the core.
var remoteForbidden = regexp.MustCompile(`^/api/(provisioning/|device/)`)

// parseProxyPath validates the path: it stays below /api/ on the server.
func parseProxyPath(p string) (*url.URL, error) {
	u, err := url.ParseRequestURI(p)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/api/") ||
		strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") || strings.Contains(u.Path, "//") {
		return nil, invalid("invalid request path")
	}
	if remoteForbidden.MatchString(u.Path) {
		return nil, Errorf(http.StatusForbidden, "forbidden", "this route is not available here")
	}
	return u, nil
}

// connResult is what ServerRequest and the asset handler write out.
type connResult struct {
	status int
	header http.Header
	body   []byte
}

func jsonResult(status int, v any, offline bool) *connResult {
	b, _ := json.Marshal(v)
	h := http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	if offline {
		h.Set(headerOffline, "1")
	}
	return &connResult{status: status, header: h, body: b}
}

func offlineUnavailable() *connResult {
	return jsonResult(http.StatusServiceUnavailable, map[string]string{"error": "offline", "message": "not available without a connection to the server"}, true)
}

// forward handles one request of the interface in server mode.
func (a *App) forward(ctx context.Context, method, pathQuery string, reqHeader http.Header, body []byte) (*connResult, error) {
	c := a.connCurrent()
	if c == nil || c.Mode != ConnModeThin {
		return nil, Errorf(http.StatusConflict, "not_connected", "not connected to a server")
	}
	u, err := parseProxyPath(pathQuery)
	if err != nil {
		return nil, err
	}
	hdr := http.Header{}
	for _, k := range proxyRequestHeaders {
		if v := reqHeader.Get(k); v != "" {
			hdr.Set(k, v)
		}
	}
	if method == http.MethodGet || method == http.MethodHead {
		return a.forwardRead(ctx, c, method, u, hdr)
	}
	return a.forwardWrite(ctx, c, method, u, hdr, body)
}

func (a *App) forwardRead(ctx context.Context, c *serverConnection, method string, u *url.URL, hdr http.Header) (*connResult, error) {
	cacheable := method == http.MethodGet && remoteCacheable(u.Path) && hdr.Get("Range") == ""
	if a.connState() == ConnStateOnline {
		res, err := a.connDo(ctx, c.URL, method, u.RequestURI(), hdr, nil, a.cr.timing.ReadTimeout)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err() // the caller gave up: says nothing about the server
			}
			a.connMarkOffline(err)
		case connGatewayDown(res.Status):
			a.connMarkOffline(errConnNetwork)
		case connIsTokenRejected(res):
			a.connMarkExpired(ctx)
		default:
			a.connSeen()
			if cacheable && res.Status == http.StatusOK {
				a.cacheResponse(ctx, u, res)
			}
			return resultOf(res), nil
		}
	}
	return a.offlineRead(ctx, u)
}

func (a *App) forwardWrite(ctx context.Context, c *serverConnection, method string, u *url.URL, hdr http.Header, body []byte) (*connResult, error) {
	q := queueable(method, u.Path)
	if u.Path == "/api/search" && method == http.MethodPost && a.connState() != ConnStateOnline {
		return a.offlineSearch(ctx, body)
	}
	if a.connState() != ConnStateOnline {
		if u.Path == "/api/uploads" && method == http.MethodPost {
			return a.offlineUpload(ctx, hdr, body)
		}
		if q == queueNo {
			return offlineUnavailable(), nil
		}
		return a.enqueue(ctx, method, u, body, q, "")
	}
	// Attachments kept while offline first have to reach the server: the outbox knows how.
	if q == queueSend && len(outboxUploads(body)) > 0 {
		res, err := a.enqueue(ctx, method, u, body, q, "")
		if err == nil {
			a.connKick()
		}
		return res, err
	}
	// A mail in doubt: the same mail again goes nowhere until the user has decided.
	if q == queueSend {
		if it, err := a.unclearMail(ctx, u, body); err != nil {
			return nil, err
		} else if it != nil {
			return unclearResult(it.ID), nil
		}
	}
	// Sending carries a key from the first try on: if the answer gets lost,
	// the replay of the same key cannot send twice.
	id := ""
	if q == queueSend {
		id = newID("out_")
		hdr.Set("Idempotency-Key", id)
	}
	wait := a.cr.timing.WriteTimeout
	if len(body) > 8<<20 {
		wait *= 5
	}
	res, err := a.connDo(ctx, c.URL, method, u.RequestURI(), hdr, body, wait)
	switch {
	case err != nil:
		if errors.Is(err, errConnLocked) || !errors.Is(err, errConnNetwork) {
			a.connMarkOffline(err)
			return offlineUnavailable(), nil
		}
		if u.Path == "/api/search" {
			a.connMarkOffline(err)
			return a.offlineSearch(ctx, body)
		}
		a.connMarkOffline(err)
	case connGatewayDown(res.Status):
		if u.Path == "/api/search" {
			a.connMarkOffline(errConnNetwork)
			return a.offlineSearch(ctx, body)
		}
		a.connMarkOffline(errConnNetwork)
	case connIsTokenRejected(res):
		a.connMarkExpired(ctx)
	case q == queueSend && (res.Status >= 500 || isIdemConflict(res.Body)):
		// The server failed after taking the mail, perhaps after the relay took
		// it: "try again" could deliver it twice. It stays, with its key, as
		// unclear; the user looks into "Sent" and decides.
		a.connSeen()
		a.log.Warn("app: sending ended unclear", "status", res.Status)
		it := store.RemoteOutboxItem{ID: id, Kind: "send", Method: method, Path: u.RequestURI(), Body: body, CreatedAt: a.now()}
		describeMail(&it, body)
		if err := a.st.AddRemoteOutbox(ctx, it); err != nil {
			return nil, err
		}
		if err := a.st.SetRemoteOutboxState(ctx, id, store.OutboxInDoubt, fmt.Sprintf("server error (%d)", res.Status)); err != nil {
			return nil, err
		}
		a.connNotify()
		return unclearResult(id), nil
	default:
		a.connSeen()
		return resultOf(res), nil
	}
	if q == queueNo {
		return offlineUnavailable(), nil
	}
	return a.enqueue(ctx, method, u, body, q, id)
}

// unclearResult tells the composer that it cannot know whether the mail left.
func unclearResult(id string) *connResult {
	return jsonResult(http.StatusConflict, map[string]string{"error": "send_unclear", "message": "the mail may have been sent already", "outboxId": id}, false)
}

func resultOf(res *connResponse) *connResult {
	h := http.Header{}
	for _, k := range proxyResponseHeaders {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	return &connResult{status: res.Status, header: h, body: res.Body}
}

func isIdemConflict(body []byte) bool {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return strings.HasPrefix(e.Error, "idempotency_")
}

// ServerRequest runs one fetch of the interface (Wails binding).
func (a *App) ServerRequest(ctx context.Context, in ServerProxyRequest) (ServerProxyResponse, error) {
	method := strings.ToUpper(in.Method)
	if method == "" {
		method = http.MethodGet
	}
	var body []byte
	if in.Body != "" {
		var err error
		if body, err = base64.StdEncoding.DecodeString(in.Body); err != nil {
			return ServerProxyResponse{}, invalid("invalid request body")
		}
	}
	hdr := http.Header{}
	for k, v := range in.Headers {
		hdr.Set(k, v)
	}
	res, err := a.forward(ctx, method, in.Path, hdr, body)
	if err != nil {
		return ServerProxyResponse{}, err
	}
	out := ServerProxyResponse{Status: res.status, Headers: map[string]string{}}
	for k := range res.header {
		out.Headers[k] = res.header.Get(k)
	}
	if len(res.body) > 0 {
		out.Body = base64.StdEncoding.EncodeToString(res.body)
	}
	return out, nil
}

// RemoteAssetHandler serves server-relative URLs of the window in server mode
// (attachments, inline images, signed image links) from the server, otherwise
// next (the local API handlers). The browser engine never sees the token.
func (a *App) RemoteAssetHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.ThinClientActive() || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		var body []byte
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			var err error
			if body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxConnBody)); err != nil {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
		}
		res, err := a.forward(r.Context(), r.Method, r.URL.RequestURI(), r.Header, body)
		if err != nil {
			e := AsError(err)
			status := e.Status
			if status == 0 {
				status = http.StatusInternalServerError
			}
			res = jsonResult(status, map[string]string{"error": e.Code, "message": e.Error()}, false)
		}
		for k, v := range res.header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(res.body)))
		w.WriteHeader(res.status)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, bytes.NewReader(res.body))
		}
	})
}
