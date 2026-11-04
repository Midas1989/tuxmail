// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Wails bindings of the OAuth / cloud account API (wails.ts, section
// "OAuth/Cloud accounts").

func (x *Api) ListOAuthProviders() ([]OAuthProviderInfo, error) {
	return r1(x.app.OAuthProviders(x.ctx))
}
func (x *Api) SaveOAuthClient(in OAuthClientInput) error {
	return werr(x.app.SaveOAuthClient(x.ctx, in))
}
func (x *Api) DeleteOAuthClient(provider string) error {
	return werr(x.app.DeleteOAuthClient(x.ctx, provider))
}
func (x *Api) StartOAuth(in OAuthStartInput) (OAuthFlowStatus, error) {
	return r1(x.app.StartOAuth(x.ctx, in))
}
func (x *Api) GetOAuthFlow(id string) (OAuthFlowStatus, error) { return r1(x.app.OAuthFlow(x.ctx, id)) }
func (x *Api) CancelOAuth(id string) error                     { return werr(x.app.CancelOAuth(x.ctx, id)) }
func (x *Api) AddSharedMailbox(in SharedMailboxInput) (Account, error) {
	return r1(x.app.AddSharedMailbox(x.ctx, in))
}
func (x *Api) ListIdentityDelegations() ([]store.IdentityDelegation, error) {
	return r1(x.app.ListIdentityDelegations(x.ctx))
}
func (x *Api) SetIdentityDelegation(d store.IdentityDelegation) error {
	return werr(x.app.SetIdentityDelegation(x.ctx, d))
}
func (x *Api) SyncCloudPim(accountID int64) (pimapi.ImportResult, error) {
	return r1(x.app.SyncCloudPIM(x.ctx, accountID))
}
