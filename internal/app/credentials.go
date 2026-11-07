// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/sync"
)

// The rule for stored credentials, for every client and every entry point:
//
//	A request that changes the server, port or user of a stored access does not
//	get to use the stored password. It must bring a new one.
//
// Otherwise whoever holds a session or a device token could point an account at
// his own server, test it, and read the password that arrives there. It covers
// account settings, the connection test of unsaved settings, ManageSieve, the
// WebDAV target of backup schedules and the AI service. OAuth tokens cannot be
// entered, so such an account must sign in again instead.
//
// Restoring an archive in replace mode brings account settings next to the stored
// secrets as well: internal/server/api refuses that to device tokens and asks a
// browser session for a re-authentication.

// CodePasswordRequired is the error code of requests under this rule.
const CodePasswordRequired = "password_required"

func errPasswordRequired(msg string) error {
	return &Error{Code: CodePasswordRequired, Status: http.StatusBadRequest, Message: msg}
}

// extraSieveSecretRef holds the password of a ManageSieve server of its own.
const extraSieveSecretRef = "sieve_secret_ref"

func serverTarget(c *ServerConfig, fallbackUser string) string {
	if c == nil {
		return ""
	}
	user := c.Username
	if user == "" {
		user = fallbackUser
	}
	plain := ""
	if c.TLS == domain.TLSNone {
		plain = "plain"
	}
	return strings.ToLower(c.Host) + "|" + strconv.Itoa(c.Port) + "|" + user + "|" + plain
}

func incomingTarget(acc domain.Account) string {
	x := func(k string) string { return acc.Extra[k] }
	return strings.Join([]string{serverTarget(acc.Incoming, x("address")), x(eas.ExtraURL), x("jmapUrl"),
		x(extraCalDAV), x(extraCardDAV), x(extraDavUsername)}, "\x00")
}

func outgoingTarget(acc domain.Account) string { return serverTarget(acc.Outgoing, "") }

// sieveTarget is an explicitly configured ManageSieve server; "" is the default,
// which follows the incoming server.
func sieveTarget(acc domain.Account) string {
	host, port := acc.Extra["sieveHost"], acc.Extra["sievePort"]
	if host == "" && port == "" {
		return ""
	}
	return strings.ToLower(host) + "|" + port + "|" + map[bool]string{true: "plain"}[acc.Extra["sieveTLS"] == string(domain.TLSNone)]
}

func hasStoredCredential(acc domain.Account) bool {
	return acc.SecretRef != "" || isOAuthAccount(acc) || acc.Extra[send.ExtraSMTPSecretRef] != "" || acc.Extra[extraSieveSecretRef] != ""
}

// requireNewCredential is the rule above: next is acc with the requested changes.
func requireNewCredential(acc, next domain.Account, password string) error {
	moved := incomingTarget(acc) != incomingTarget(next) || outgoingTarget(acc) != outgoingTarget(next)
	if s := sieveTarget(next); s != "" && s != sieveTarget(acc) {
		moved = true
	}
	if !moved || !hasStoredCredential(acc) {
		return nil
	}
	if isOAuthAccount(acc) {
		return errPasswordRequired("another server: this account signs in with OAuth, remove it and add it again; its token is not sent there")
	}
	if password == "" {
		return errPasswordRequired("another server, port or user: enter the password for it, the stored one is not sent there")
	}
	return nil
}

// withSieveUpdate returns acc with the ManageSieve settings of the update applied.
func withSieveUpdate(acc domain.Account, c *SieveConfig) domain.Account {
	if c == nil || acc.Kind != domain.KindIMAP {
		return acc
	}
	out := acc
	out.Extra = make(map[string]string, len(acc.Extra))
	for k, v := range acc.Extra {
		out.Extra[k] = v
	}
	applySieveExtra(&out, *c)
	return out
}

// storeMovedCredential keeps the new password of an update that moved the account.
// A new SMTP server alone gets a secret of its own: the password of the unchanged
// incoming server stays.
func (a *App) storeMovedCredential(acc domain.Account, next *domain.Account, password string) error {
	if password == "" {
		return nil
	}
	in := incomingTarget(acc) != incomingTarget(*next)
	out := outgoingTarget(acc) != outgoingTarget(*next)
	if in {
		ref := next.SecretRef
		if ref == "" {
			ref = accountSecretRef(acc.ID)
		}
		if err := a.opts.Secrets.Set(ref, []byte(password)); err != nil {
			return err
		}
		next.SecretRef = ref
	}
	if out {
		ref := next.Extra[send.ExtraSMTPSecretRef]
		if ref == "" && !in {
			ref = accountSecretRef(acc.ID) + ":smtp"
			next.Extra[send.ExtraSMTPSecretRef] = ref
		}
		if ref != "" {
			if err := a.opts.Secrets.Set(ref, []byte(password)); err != nil {
				return err
			}
		}
	}
	return nil
}

// storeSievePassword keeps the password of a ManageSieve server of its own, and
// forgets it when the account goes back to the default server.
func (a *App) storeSievePassword(acc *domain.Account, stored domain.Account, c SieveConfig) error {
	target := sieveTarget(withSieveUpdate(stored, &c))
	if target == "" {
		if ref := acc.Extra[extraSieveSecretRef]; ref != "" {
			delete(acc.Extra, extraSieveSecretRef)
			return a.opts.Secrets.Delete(ref)
		}
		return nil
	}
	if target == sieveTarget(stored) || c.Password == "" {
		return nil
	}
	ref := accountSecretRef(acc.ID) + ":sieve"
	if err := a.opts.Secrets.Set(ref, []byte(c.Password)); err != nil {
		return err
	}
	acc.Extra[extraSieveSecretRef] = ref
	return nil
}

// sievePassword is the password for ManageSieve: its own, or the account's.
func (a *App) sievePassword(ctx context.Context, acc domain.Account, creds sync.Credentials) (string, error) {
	if ref := acc.Extra[extraSieveSecretRef]; ref != "" {
		b, err := a.opts.Secrets.Get(ref)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return creds.Password(ctx, acc)
}

// PGPKeyIsPrivate reports whether a key has its private part here.
func (a *App) PGPKeyIsPrivate(ctx context.Context, id int64) (bool, error) {
	row, err := a.st.GetPGPKey(ctx, id)
	if err != nil {
		return false, err
	}
	return row.SecretRef != "", nil
}

// SMIMECertificateIsPrivate reports whether a certificate has its private key here.
func (a *App) SMIMECertificateIsPrivate(ctx context.Context, id int64) (bool, error) {
	c, err := a.st.GetSMIMECert(ctx, id)
	if err != nil {
		return false, err
	}
	return c.SecretRef != "", nil
}
