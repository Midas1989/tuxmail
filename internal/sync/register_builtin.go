// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// registerBuiltin adds a provider to DefaultRegistry (builtinFactories is
// declared in cloud_register.go); call it from an init function.
func registerBuiltin(kind domain.AccountKind, f ProviderFactory) { builtinFactories[kind] = f }

// ProviderEnv is the local context of the account a provider is built for.
// Factories that need local state (POP3 local folders) read it with
// ProviderEnvFrom; it is set by the sync worker.
type ProviderEnv struct {
	Store     *store.Store
	AccountID int64
}

type providerEnvKey struct{}

// WithProviderEnv attaches env to ctx (used by the worker before Build).
func WithProviderEnv(ctx context.Context, env ProviderEnv) context.Context {
	return context.WithValue(ctx, providerEnvKey{}, env)
}

// ProviderEnvFrom returns the env attached by WithProviderEnv.
func ProviderEnvFrom(ctx context.Context) (ProviderEnv, bool) {
	env, ok := ctx.Value(providerEnvKey{}).(ProviderEnv)
	return env, ok && env.Store != nil
}
