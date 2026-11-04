// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/autoconfig"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// ListAccounts returns all accounts in display order.
func (a *App) ListAccounts(ctx context.Context) ([]Account, error) {
	accs, err := a.st.ListAccounts(ctx)
	if accs == nil {
		accs = []Account{}
	}
	return accs, err
}

// ListIdentities returns the identities of all accounts.
func (a *App) ListIdentities(ctx context.Context) ([]Identity, error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	chat, err := a.st.IdentityChatSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := []Identity{}
	for _, acc := range accs {
		ids, err := a.st.ListIdentities(ctx, acc.ID)
		if err != nil {
			return nil, err
		}
		for _, i := range ids {
			c := chat[i.ID]
			out = append(out, Identity{Identity: i, ChatSignatureMode: c.SignatureMode, ChatSignatureID: c.SignatureID})
		}
	}
	return out, nil
}

// ListSignatures returns all signatures.
func (a *App) ListSignatures(ctx context.Context) ([]Signature, error) {
	sigs, err := a.st.ListSignatures(ctx)
	if sigs == nil {
		sigs = []Signature{}
	}
	return sigs, err
}

// SaveSignature creates (ID 0) or updates a signature. A missing HTML
// version is generated from the plain text.
func (a *App) SaveSignature(ctx context.Context, sig Signature) (Signature, error) {
	sig.Name = strings.TrimSpace(sig.Name)
	if sig.Name == "" {
		return Signature{}, invalid("signature name required")
	}
	switch sig.Placement {
	case "", "below_reply", "above_quote":
	default:
		return Signature{}, invalid("invalid placement %q", sig.Placement)
	}
	if strings.TrimSpace(sig.HTML) == "" && sig.Plain != "" {
		sig.HTML = strings.ReplaceAll(html.EscapeString(strings.TrimRight(sig.Plain, "\n")), "\n", "<br>")
	}
	if sig.ID == 0 {
		id, err := a.st.CreateSignature(ctx, sig)
		if err != nil {
			return Signature{}, err
		}
		sig.ID = id
	} else if err := a.st.UpdateSignature(ctx, sig); err != nil {
		return Signature{}, err
	}
	a.hub.Publish(Event{Type: EventAccountUpdated})
	return a.st.GetSignature(ctx, sig.ID)
}

// DeleteSignature deletes a signature; identities using it fall back to none.
func (a *App) DeleteSignature(ctx context.Context, id int64) error {
	if err := a.st.DeleteSignature(ctx, id); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventAccountUpdated})
	return nil
}

var frontendHandleKinds = map[string]bool{"email": true, "phone": true, "whatsapp": true, "signal": true, "telegram": true}

func toPerson(p store.PersonWithHandles) Person {
	out := Person{ID: p.ID, DisplayName: p.DisplayName, IsMe: p.IsMe, Handles: []PersonHandle{}}
	for _, h := range p.Handles {
		if frontendHandleKinds[h.Kind] {
			out.Handles = append(out.Handles, PersonHandle{Kind: h.Kind, Value: h.Value})
		}
	}
	if out.DisplayName == "" && len(out.Handles) > 0 {
		out.DisplayName = out.Handles[0].Value
	}
	return out
}

// ListPersons returns all persons with their handles.
func (a *App) ListPersons(ctx context.Context) ([]Person, error) {
	if err := a.ensureIndexed(ctx); err != nil {
		return nil, err
	}
	ps, err := a.st.ListPersonsWithHandles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Person, 0, len(ps))
	for _, p := range ps {
		out = append(out, toPerson(p))
	}
	return out, nil
}

// EnvISPDBURL overrides the ISPDB base URL of the default Discoverer.
const EnvISPDBURL = "TUXMAIL_ISPDB_URL"

// autoconfigDiscoverer is the default Discoverer (internal/autoconfig).
type autoconfigDiscoverer struct{}

func (autoconfigDiscoverer) Discover(ctx context.Context, email, password string) (DiscoveryResult, error) {
	d := autoconfig.New()
	d.UseAutodiscover = true // Microsoft Autodiscover -> EAS (KONZEPT 7.5)
	d.AutodiscoverPassword = func(string) string { return password }
	// TUXMAIL_ISPDB_URL replaces Thunderbird's ISPDB, e.g. with an
	// organization's own mirror or the dev server (docs/ENTWICKLUNG.md).
	if u := os.Getenv(EnvISPDBURL); u != "" {
		d.ISPDBURL = u
	}
	cands, err := d.Discover(ctx, email)
	res := DiscoveryResult{Source: "none", Kind: string(domain.KindIMAP)}
	if _, dom, e := autoconfig.SplitAddress(email); e == nil {
		res.DisplayName = dom
	}
	if err != nil || len(cands) == 0 {
		if j := discoverJMAP(ctx, d, email, password); j != nil { // mailproto.go
			return *j, nil
		}
		return res, nil //nolint:nilerr // "none" is a valid result the UI handles
	}
	c := cands[0]
	switch c.Source {
	case autoconfig.SourceSRV:
		res.Source = "srv"
	case autoconfig.SourceAutodiscover:
		res.Source = "autodiscover"
	case autoconfig.SourceHeuristic, autoconfig.SourceMX:
		res.Source = "guess"
	default:
		res.Source = "autoconfig"
	}
	res.Kind = string(c.Kind)
	if c.DisplayName != "" {
		res.DisplayName = c.DisplayName
	}
	in, out := c.Incoming, c.Outgoing
	res.Incoming = &in
	if out.Host != "" {
		res.Outgoing = &out
	}
	res.OAuthAvailable = c.OAuthProvider != ""
	res.EASURL = c.EASURL
	res.OAuthProvider = c.OAuthProvider
	return res, nil
}

// DiscoverAccount looks up the server settings for an address.
func (a *App) DiscoverAccount(ctx context.Context, email, password string) (DiscoveryResult, error) {
	if !strings.Contains(email, "@") {
		return DiscoveryResult{}, invalid("invalid e-mail address")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return a.opts.Discoverer.Discover(ctx, strings.TrimSpace(email), password)
}

func accountSecretRef(id int64) string { return fmt.Sprintf("account:%d", id) }

// CreateAccount creates an account with its primary identity and stores
// the password in the secret store.
func (a *App) CreateAccount(ctx context.Context, in NewAccountInput) (Account, error) {
	if in.Kind == "" {
		in.Kind = string(domain.KindIMAP)
	}
	if in.DisplayName == "" {
		in.DisplayName = in.Email
	}
	if in.DisplayName == "" {
		return Account{}, invalid("display name or e-mail address required")
	}
	acc := domain.Account{Kind: domain.AccountKind(in.Kind), DisplayName: in.DisplayName, Incoming: in.Incoming,
		Outgoing: in.Outgoing, Enabled: true, Extra: map[string]string{}}
	if in.Email != "" {
		acc.Extra["address"] = in.Email
	}
	if err := applyEASInput(&acc, in); err != nil {
		return Account{}, err
	}
	applyMailProtoInput(&acc, in) // mailproto.go
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return Account{}, err
	}
	acc.SortOrder = len(accs)
	id, err := a.st.CreateAccount(ctx, acc)
	if err != nil {
		return Account{}, err
	}
	acc.ID = id
	if in.Password != "" {
		ref := accountSecretRef(id)
		if err := a.opts.Secrets.Set(ref, []byte(in.Password)); err != nil {
			_ = a.st.DeleteAccount(ctx, id)
			return Account{}, err
		}
		acc.SecretRef = ref
		if err := a.st.UpdateAccount(ctx, acc); err != nil {
			return Account{}, err
		}
	}
	if in.Email != "" {
		name := in.Name
		if name == "" {
			name = in.DisplayName
		}
		if _, err := a.st.CreateIdentity(ctx, domain.Identity{AccountID: id, Name: name, Email: in.Email, IsDefault: true}); err != nil {
			return Account{}, err
		}
	}
	if err := a.sync.AddAccount(ctx, id); err != nil {
		a.log.Warn("app: starting sync failed", "account", id, "err", err)
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	a.kickTimers() // e.g. first EAS calendar/contacts sync right away
	return a.st.GetAccount(ctx, id)
}

// UpdateIdentity updates an identity including its chat-mode signature settings.
func (a *App) UpdateIdentity(ctx context.Context, in Identity) (Identity, error) {
	cur, err := a.st.GetIdentity(ctx, in.ID)
	if err != nil {
		return Identity{}, err
	}
	in.Identity.AccountID = cur.AccountID
	if err := a.st.UpdateIdentity(ctx, in.Identity); err != nil {
		return Identity{}, err
	}
	switch in.ChatSignatureMode {
	case "", "full", "short", "off":
	default:
		return Identity{}, invalid("invalid chat signature mode %q", in.ChatSignatureMode)
	}
	if err := a.st.PutIdentityChat(ctx, store.IdentityChat{IdentityID: in.ID, SignatureMode: in.ChatSignatureMode, SignatureID: in.ChatSignatureID}); err != nil {
		return Identity{}, err
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: cur.AccountID})
	got, err := a.st.GetIdentity(ctx, in.ID)
	return Identity{Identity: got, ChatSignatureMode: in.ChatSignatureMode, ChatSignatureID: in.ChatSignatureID}, err
}

// SaveContactCard stores the card of a person and adds its addresses and
// phone numbers as handles.
func (a *App) SaveContactCard(ctx context.Context, personID int64, card ContactCard) error {
	p, err := a.st.GetPerson(ctx, personID)
	if err != nil {
		return err
	}
	if card.Name != "" && card.Name != p.DisplayName {
		p.DisplayName = card.Name
		if err := a.st.UpdatePerson(ctx, p); err != nil {
			return err
		}
	}
	for _, e := range card.Emails {
		if err := a.st.AddHandle(ctx, store.Handle{PersonID: personID, Kind: store.HandleEmail, Value: e}); err != nil && err != store.ErrHandleInUse {
			return err
		}
	}
	for _, ph := range card.Phones {
		if err := a.st.AddHandle(ctx, store.Handle{PersonID: personID, Kind: store.HandlePhone, Value: ph}); err != nil && err != store.ErrHandleInUse {
			return err
		}
	}
	b, err := json.Marshal(card)
	if err != nil {
		return err
	}
	return a.st.PutContactCard(ctx, personID, string(b))
}

// ListFolders returns the folders of an account (inbox first, then by name).
func (a *App) ListFolders(ctx context.Context, accountID int64) ([]Folder, error) {
	if _, err := a.st.GetAccount(ctx, accountID); err != nil {
		return nil, err
	}
	fs, err := a.st.ListFolders(ctx, accountID)
	if err != nil {
		return nil, err
	}
	rank := map[domain.FolderRole]int{domain.RoleInbox: 0, domain.RoleDrafts: 1, domain.RoleSent: 2, domain.RoleArchive: 3, domain.RoleJunk: 4, domain.RoleTrash: 5}
	sort.SliceStable(fs, func(i, j int) bool {
		ri, iok := rank[fs[i].Role]
		rj, jok := rank[fs[j].Role]
		if fs[i].ParentID != 0 || fs[j].ParentID != 0 {
			return false
		}
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return false
	})
	if fs == nil {
		fs = []Folder{}
	}
	return fs, nil
}

// FrequentContacts returns persons the user writes to frequently.
func (a *App) FrequentContacts(ctx context.Context) ([]int64, error) {
	if err := a.ensureIndexed(ctx); err != nil {
		return nil, err
	}
	ids, err := a.st.OutgoingCountsByPerson(ctx, a.now().AddDate(0, -6, 0), 20)
	if ids == nil {
		ids = []int64{}
	}
	return ids, err
}
