// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/providers/eas/easstore"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Account extras used by EAS accounts (see the eas package).
const (
	ExtraEASURL        = eas.ExtraURL
	ExtraEASDeviceID   = eas.ExtraDeviceID
	ExtraEASVersion    = eas.ExtraVersion
	ExtraEASMaxVersion = eas.ExtraMaxVersion
	ExtraEASFilter     = eas.ExtraFilter
)

// EASOptions configure the EAS provider factory.
type EASOptions struct {
	// State persists the EAS account state (policy key, folder hierarchy,
	// sync keys). Use EASStoreState; nil keeps it in process memory only,
	// which forces a full resync after every restart.
	State eas.StateStore
	// OnRemoteWipe wipes the local data of the account when the server
	// requests it (default: none — the wipe is then not acknowledged).
	// WipeEASAccountData is a suitable implementation.
	OnRemoteWipe func(ctx context.Context, acc domain.Account, accountOnly bool) error
	// KeywordMapper maps categories to tag keywords (default
	// store.DefaultKeywordMapper); it must match Config.KeywordMapper.
	KeywordMapper *store.KeywordMapper
	// Heartbeat overrides the Ping heartbeat (default 9 min).
	Heartbeat time.Duration
}

// EASFactory builds EAS providers without persistent account state.
func EASFactory(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	return NewEASFactory(EASOptions{})(ctx, acc, creds, opts)
}

// RegisterEAS registers kind "eas" with the given options.
func RegisterEAS(r *Registry, o EASOptions) { r.Register(domain.KindEAS, NewEASFactory(o)) }

// NewEASFactory returns a ProviderFactory for domain.KindEAS.
func NewEASFactory(o EASOptions) ProviderFactory {
	return func(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
		cfg, err := EASConfig(ctx, acc, creds, opts, o)
		if err != nil {
			return nil, err
		}
		p, err := eas.New(cfg)
		if err != nil {
			return nil, Permanent(err)
		}
		return &easProvider{Provider: p}, nil
	}
}

// EASDeviceID returns the stable device id of an account.
func EASDeviceID(acc domain.Account) string { return eas.AccountDeviceID(acc) }

// EASURL returns the ActiveSync endpoint of an account.
func EASURL(acc domain.Account) (string, error) { return eas.AccountURL(acc) }

// EASConfig builds the provider configuration of an account.
func EASConfig(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions, o EASOptions) (eas.Config, error) {
	cc, err := eas.AccountClientConfig(acc)
	if err != nil {
		return eas.Config{}, Permanent(err)
	}
	cc.State, cc.Logger, cc.HTTPClient = o.State, opts.Logger, easHTTPClient(opts)
	extra := func(k string) string {
		if acc.Extra == nil {
			return ""
		}
		return acc.Extra[k]
	}
	if acc.Incoming != nil && acc.Incoming.Auth == domain.AuthOAuth2 {
		ts, err := creds.OAuthToken(ctx, acc)
		if err != nil {
			return eas.Config{}, err
		}
		cc.TokenSource = eas.TokenSourceFunc(ts.Token)
	} else {
		pw, err := creds.Password(ctx, acc)
		if err != nil {
			return eas.Config{}, err
		}
		cc.Password = pw
	}
	if o.OnRemoteWipe != nil {
		cc.OnRemoteWipe = func(ctx context.Context, accountOnly bool) error { return o.OnRemoteWipe(ctx, acc, accountOnly) }
	}
	km := store.DefaultKeywordMapper
	if o.KeywordMapper != nil {
		km = *o.KeywordMapper
	}
	cfg := eas.Config{
		Client: cc, FilterType: extra(ExtraEASFilter), Heartbeat: o.Heartbeat,
		MinBackoff: opts.WatchMinBackoff, MaxBackoff: opts.WatchMaxBackoff, Logger: opts.Logger,
		CategoryToKeyword: func(cat string) (string, bool) {
			path, ok := store.TagFromOutlookCategory(cat)
			if !ok {
				return "", false
			}
			kw, err := km.ToKeyword(path)
			return kw, err == nil
		},
		KeywordToCategory: func(kw string) (string, bool) {
			path, ok := km.FromKeyword(kw)
			if !ok {
				return "", false
			}
			cat, err := store.TagToOutlookCategory(path)
			return cat, err == nil
		},
	}
	return cfg, nil
}

const (
	easDialTimeout     = 30 * time.Second
	easIdleConnTimeout = 90 * time.Second
)

func easHTTPClient(opts ProviderOptions) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if opts.TLSConfig != nil {
		tr.TLSClientConfig = opts.TLSConfig.Clone()
	}
	if opts.DialContext != nil {
		dial := opts.DialContext
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, easDialTimeout)
			defer cancel()
			return dial(ctx, network, addr)
		}
	}
	tr.TLSHandshakeTimeout = easDialTimeout
	tr.IdleConnTimeout = easIdleConnTimeout
	tr.ResponseHeaderTimeout = 0 // Ping blocks for the heartbeat; no overall client timeout either
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// easProvider maps EAS errors to the sync engine's error classes.
type easProvider struct{ *eas.Provider }

func easErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, eas.ErrNotSupported) || errors.Is(err, eas.ErrItemNotFound) || errors.Is(err, eas.ErrRemoteWiped) ||
		errors.Is(err, eas.ErrForbidden) {
		return Permanent(err)
	}
	var se *eas.StatusError
	if errors.As(err, &se) && !se.Temporary() && se.Status != eas.SyncStatusServerError && se.Status != eas.SyncStatusRetry &&
		se.Status != eas.SyncStatusHierarchyChanged {
		return Permanent(err)
	}
	return err
}

func (p *easProvider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	f, err := p.Provider.ListFolders(ctx)
	return f, easErr(err)
}

func (p *easProvider) CreateFolder(ctx context.Context, parent, name string) (domain.Folder, error) {
	f, err := p.Provider.CreateFolder(ctx, parent, name)
	return f, easErr(err)
}

func (p *easProvider) RenameFolder(ctx context.Context, remoteID, name string) (domain.Folder, error) {
	f, err := p.Provider.RenameFolder(ctx, remoteID, name)
	return f, easErr(err)
}

func (p *easProvider) DeleteFolder(ctx context.Context, remoteID string) error {
	return easErr(p.Provider.DeleteFolder(ctx, remoteID))
}

func (p *easProvider) Sync(ctx context.Context, folder string, st domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	d, s, err := p.Provider.Sync(ctx, folder, st)
	return d, s, easErr(err)
}

func (p *easProvider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	r, err := p.Provider.FetchRaw(ctx, ref)
	return r, easErr(err)
}

func (p *easProvider) Move(ctx context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	r, err := p.Provider.Move(ctx, refs, to)
	return r, easErr(err)
}

func (p *easProvider) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	return easErr(p.Provider.SetFlags(ctx, refs, add, remove))
}

func (p *easProvider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	return easErr(p.Provider.Delete(ctx, refs))
}

func (p *easProvider) Append(ctx context.Context, folder string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	r, err := p.Provider.Append(ctx, folder, raw, flags)
	return r, easErr(err)
}

func (p *easProvider) Watch(ctx context.Context, folder string) (<-chan domain.ChangeHint, error) {
	ch, err := p.Provider.Watch(ctx, folder)
	return ch, easErr(err)
}

// ==== persistence and wipe ======================================================

// EASStoreState persists the EAS account state in the app settings table.
type EASStoreState struct{ Store *store.Store }

const easStatePrefix = "eas.state."

// LoadEASState implements eas.StateStore.
func (s EASStoreState) LoadEASState(ctx context.Context, key string) ([]byte, error) {
	var v string
	ok, err := s.Store.GetAppSetting(ctx, easStatePrefix+key, &v)
	if err != nil || !ok {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

// SaveEASState implements eas.StateStore.
func (s EASStoreState) SaveEASState(ctx context.Context, key string, data []byte) error {
	return s.Store.SetAppSetting(ctx, easStatePrefix+key, base64.StdEncoding.EncodeToString(data))
}

// WipeEASAccountData deletes the local data of one account after a remote
// wipe request: folders (with their messages), calendars and address
// books. Other accounts and the account configuration are not touched; the
// account is disabled so it does not resynchronize without the user.
func WipeEASAccountData(ctx context.Context, st *store.Store, accountID int64) error {
	folders, err := st.ListFolders(ctx, accountID)
	if err != nil {
		return err
	}
	for _, f := range folders {
		if err := st.DeleteFolder(ctx, f.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	cals, err := st.ListAccountCalendars(ctx, accountID)
	if err != nil {
		return err
	}
	for _, c := range cals {
		if err := st.DeleteCalendar(ctx, c.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	books, err := st.ListAccountAddressbooks(ctx, accountID)
	if err != nil {
		return err
	}
	for _, b := range books {
		if err := st.DeleteAddressbook(ctx, b.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	acc, err := st.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if acc.Enabled {
		acc.Enabled = false
		if err := st.UpdateAccount(ctx, acc); err != nil {
			return fmt.Errorf("sync: disable wiped account: %w", err)
		}
	}
	return nil
}

// SyncEASPIM synchronizes the calendars, contacts and tasks of an EAS
// account two-way into the store tables (easstore conventions). It builds
// its own connection; run it periodically next to the mail worker.
func SyncEASPIM(ctx context.Context, st *store.Store, acc domain.Account, creds Credentials, opts ProviderOptions,
	o EASOptions, po eas.PIMOptions) (eas.PIMResult, error) {
	cfg, err := EASConfig(ctx, acc, creds, opts, o)
	if err != nil {
		return eas.PIMResult{}, err
	}
	c, err := eas.NewClient(cfg.Client)
	if err != nil {
		return eas.PIMResult{}, err
	}
	return c.SyncPIM(ctx, easstore.New(st), acc.ID, po)
}
