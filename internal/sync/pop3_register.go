// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Midas1989/Mailclient/internal/domain"
	pop3prov "github.com/Midas1989/Mailclient/internal/providers/pop3"
	"github.com/Midas1989/Mailclient/internal/store"
)

func init() { registerBuiltin(domain.KindPOP3, POP3Factory) }

// POP3Factory builds a POP3 provider. The deletion policy comes from the
// account's Extra (pop3.PolicyFromExtra); local folders and the local-copy
// check use the store of the ProviderEnv in ctx.
func POP3Factory(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	if acc.Incoming == nil {
		return nil, Permanent(fmt.Errorf("sync: account %d has no incoming server", acc.ID))
	}
	cfg := pop3prov.Config{
		Server: *acc.Incoming, TLSConfig: opts.TLSConfig, DialContext: opts.DialContext, Logger: opts.Logger,
		PollInterval: opts.PollInterval, Policy: pop3prov.PolicyFromExtra(acc.Extra),
	}
	if env, ok := ProviderEnvFrom(ctx); ok {
		cfg.Local = pop3Local{st: env.Store, accountID: env.AccountID}
	}
	switch acc.Incoming.Auth {
	case domain.AuthOAuth2:
		ts, err := creds.OAuthToken(ctx, acc)
		if err != nil {
			return nil, err
		}
		cfg.TokenSource = pop3prov.TokenSourceFunc(ts.Token)
	default:
		pw, err := creds.Password(ctx, acc)
		if err != nil {
			return nil, err
		}
		cfg.Password = pw
	}
	p, err := pop3prov.New(cfg)
	if err != nil {
		return nil, Permanent(err)
	}
	return permanentErrors{MailProvider: p, permanent: func(err error) bool {
		return errors.Is(err, pop3prov.ErrMessageNotFound) || errors.Is(err, pop3prov.ErrNotSupported)
	}}, nil
}

// pop3Local implements pop3.LocalStore on the store.
type pop3Local struct {
	st        *store.Store
	accountID int64
}

func (l pop3Local) LocalFolders(ctx context.Context) ([]domain.Folder, error) {
	all, err := l.st.ListFolders(ctx, l.accountID)
	if err != nil {
		return nil, err
	}
	var out []domain.Folder
	for _, f := range all {
		if domain.IsLocalFolder(f.RemoteID) {
			out = append(out, f)
		}
	}
	return out, nil
}

func (l pop3Local) HasLocalCopy(ctx context.Context, uidl string) (bool, error) {
	folders, err := l.st.ListFolders(ctx, l.accountID)
	if err != nil {
		return false, err
	}
	for _, f := range folders {
		m, err := l.st.FindMessageByLocation(ctx, f.ID, uidl)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if m.BlobHash != "" {
			return true, nil
		}
	}
	return false, nil
}

// permanentErrors marks provider errors that must not be retried.
type permanentErrors struct {
	domain.MailProvider
	permanent func(error) bool
}

func (p permanentErrors) wrap(err error) error {
	if err != nil && p.permanent(err) {
		return Permanent(err)
	}
	return err
}

func (p permanentErrors) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	rc, err := p.MailProvider.FetchRaw(ctx, ref)
	return rc, p.wrap(err)
}

func (p permanentErrors) Move(ctx context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	out, err := p.MailProvider.Move(ctx, refs, to)
	return out, p.wrap(err)
}

func (p permanentErrors) Append(ctx context.Context, folder string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	r, err := p.MailProvider.Append(ctx, folder, raw, flags)
	return r, p.wrap(err)
}

func (p permanentErrors) RenameFolder(ctx context.Context, remoteID, newName string) (domain.Folder, error) {
	f, err := p.MailProvider.RenameFolder(ctx, remoteID, newName)
	return f, p.wrap(err)
}

func (p permanentErrors) DeleteFolder(ctx context.Context, remoteID string) error {
	return p.wrap(p.MailProvider.DeleteFolder(ctx, remoteID))
}
