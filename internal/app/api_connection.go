// SPDX-License-Identifier: AGPL-3.0-or-later

package app

// Wails bindings of the server connection (remote_conn.go). They always talk
// to the local core, also while the interface works through a server.

func (x *Api) ServerConnectionStatus() (ConnectionStatus, error) {
	return r1(x.app.ServerConnectionStatus(x.ctx))
}
func (x *Api) ServerRequest(in ServerProxyRequest) (ServerProxyResponse, error) {
	return r1(x.app.ServerRequest(x.ctx, in))
}
func (x *Api) ServerReauthComplete(serverURL string) error {
	return werr(x.app.ServerReauthComplete(x.ctx, serverURL))
}
func (x *Api) ServerAbortConnect(serverURL string) error {
	return werr(x.app.ServerAbortConnect(x.ctx, serverURL))
}
func (x *Api) ServerDisconnect(in DisconnectInput) (DisconnectResult, error) {
	return r1(x.app.ServerDisconnect(x.ctx, in))
}
func (x *Api) ServerSetMode(mode string) error { return werr(x.app.ServerSetMode(x.ctx, mode)) }
func (x *Api) ServerSyncNow() (ConnectionStatus, error) {
	return r1(x.app.ServerSyncNow(x.ctx))
}
func (x *Api) ServerProfile() (ProvisioningProfile, error) {
	return r1(x.app.ServerProfile(x.ctx))
}
func (x *Api) ServerOutboxDiscard(id string) error {
	return werr(x.app.ServerOutboxDiscard(x.ctx, id))
}
func (x *Api) ServerOutboxRetry(id string) error {
	return werr(x.app.ServerOutboxRetry(x.ctx, id))
}
func (x *Api) ServerOutboxMessage(id string) (OutgoingMessage, error) {
	return r1(x.app.ServerOutboxMessage(x.ctx, id))
}
func (x *Api) ServerWorkflowApprove(id, hash string) error {
	return werr(x.app.ServerWorkflowApproveVersion(x.ctx, id, hash))
}
func (x *Api) ServerWorkflowDismiss(id string) error {
	return werr(x.app.ServerWorkflowDismiss(x.ctx, id))
}
func (x *Api) ServerWorkflowResolve(id string, keepLocal bool) error {
	return werr(x.app.ServerWorkflowResolve(x.ctx, id, keepLocal))
}
func (x *Api) ServerSettingsDeletes(kind string, approve bool) error {
	return werr(x.app.ServerSettingsDeletes(x.ctx, kind, approve))
}
func (x *Api) ServerClearCache() error { return werr(x.app.st.ClearRemoteCache(x.ctx)) }
