// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
)

// Reauth carries the proofs of a re-authentication in a request body.
type Reauth struct {
	Password     string `json:"password"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recoveryCode"`
}

// Guards are the decisions only the embedding server can take: it knows the kind of
// credential. They travel with the request; without them everything is allowed.
type Guards struct {
	// Reauth lets a sensitive operation through after a re-authentication. On
	// refusal it has answered the request. Device tokens are always refused.
	Reauth func(w http.ResponseWriter, r *http.Request, re Reauth) bool
	// NoDevice refuses device tokens, answering the request.
	NoDevice func(w http.ResponseWriter, r *http.Request) bool
}

type guardsKey struct{}

// WithGuards attaches the guards of the embedding server to a request context.
func WithGuards(ctx context.Context, g Guards) context.Context {
	return context.WithValue(ctx, guardsKey{}, g)
}

func needReauth(w http.ResponseWriter, r *http.Request, re Reauth) bool {
	if g, ok := r.Context().Value(guardsKey{}).(Guards); ok && g.Reauth != nil {
		return g.Reauth(w, r, re)
	}
	return true
}

func noDevice(w http.ResponseWriter, r *http.Request) bool {
	if g, ok := r.Context().Value(guardsKey{}).(Guards); ok && g.NoDevice != nil {
		return g.NoDevice(w, r)
	}
	return true
}
