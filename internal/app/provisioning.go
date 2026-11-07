// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/server/sealed"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// Desktop client: connect to a TuxMail server (KONZEPT 15.8). The device
// token and the X25519 key for the end-to-end encrypted secrets stay in the
// core; the UI only sees the profile. During the sign-in they live in the
// session here, and when the user has chosen the mode connCommit makes them
// lasting (remote_conn.go).

// extraProvisionedRemoteID is the account's id at the server (next to
// provisionedFrom): it links both sides for repeated set-ups and the settings sync.
const extraProvisionedRemoteID = "provisionedRemoteId"

type remoteSession struct {
	priv    *ecdh.PrivateKey
	token   string
	profile *remoteProfile
}

// remoteProfile is the server's GET /api/provisioning/profile response.
type remoteProfile struct {
	Server struct {
		PublicURL string `json:"publicUrl"`
		Version   string `json:"version"`
	} `json:"server"`
	User struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"user"`
	Accounts   []domain.Account   `json:"accounts"`
	Identities []domain.Identity  `json:"identities"`
	Signatures []domain.Signature `json:"signatures"`
	Templates  []struct {
		ID   int64           `json:"id"`
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	} `json:"templates"`
	Secrets struct {
		Available bool `json:"available"`
	} `json:"secrets"`
}

func normalizeServerURL(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", invalid("invalid server URL")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

func (a *App) session(serverURL string, create bool) (*remoteSession, error) {
	a.remoteMu.Lock()
	defer a.remoteMu.Unlock()
	s, ok := a.remote[serverURL]
	if !ok && create {
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		s = &remoteSession{priv: priv}
		a.remote[serverURL] = s
	}
	if s == nil {
		return nil, Errorf(http.StatusConflict, "not_connected", "not connected to %s", serverURL)
	}
	return s, nil
}

func (a *App) remoteCall(ctx context.Context, method, u, token string, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := a.opts.HTTPClient.Do(req)
	if err != nil {
		return 0, Errorf(http.StatusBadGateway, "network", "server not reachable: %v", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return res.StatusCode, err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil && res.StatusCode/100 == 2 {
			return res.StatusCode, fmt.Errorf("invalid server response: %w", err)
		}
	}
	return res.StatusCode, nil
}

// DeviceAuthorize starts the device flow at a server.
func (a *App) DeviceAuthorize(ctx context.Context, serverURL string) (DeviceAuthorization, error) {
	base, err := normalizeServerURL(serverURL)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	s, err := a.session(base, true)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	var res struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
		Error                   string `json:"error"`
		Message                 string `json:"error_description"`
	}
	code, err := a.remoteCall(ctx, http.MethodPost, base+"/api/device/authorize", "", map[string]string{
		"client_name": connDeviceName(), "client_public_key": base64.RawURLEncoding.EncodeToString(s.priv.PublicKey().Bytes())}, &res)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	if code/100 != 2 {
		return DeviceAuthorization{}, Errorf(http.StatusBadGateway, "server_error", "server refused the device authorization: %s %s", res.Error, res.Message)
	}
	return DeviceAuthorization{DeviceCode: res.DeviceCode, UserCode: res.UserCode, VerificationURI: res.VerificationURI,
		VerificationURIComplete: res.VerificationURIComplete, ExpiresIn: res.ExpiresIn, Interval: res.Interval}, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Desc        string `json:"error_description"`
}

func (a *App) finishLogin(ctx context.Context, base string, s *remoteSession, tok tokenResponse) (DeviceTokenResult, error) {
	s.token = tok.AccessToken
	var p remoteProfile
	code, err := a.remoteCall(ctx, http.MethodGet, base+"/api/provisioning/profile", s.token, nil, &p)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	if code/100 != 2 {
		return DeviceTokenResult{Status: "denied", Error: fmt.Sprintf("profile request failed (%d)", code)}, nil
	}
	s.profile = &p
	return DeviceTokenResult{Status: "approved", Profile: a.profileForUI(base, &p)}, nil
}

// profileForUI is what the wizard shows of a server profile.
func (a *App) profileForUI(base string, p *remoteProfile) *ProvisioningProfile {
	prof := &ProvisioningProfile{Accounts: p.Accounts, Signatures: p.Signatures, Identities: []Identity{}, Workflows: []ProvisioningWorkflow{}}
	prof.Server.URL, prof.Server.Name, prof.Server.Version = base, hostOf(base), p.Server.Version
	prof.User.Name, prof.User.Email = p.User.DisplayName, p.User.Email
	if prof.User.Name == "" {
		prof.User.Name = p.User.Username
	}
	for _, i := range p.Identities {
		prof.Identities = append(prof.Identities, Identity{Identity: i})
	}
	for _, t := range p.Templates {
		prof.Workflows = append(prof.Workflows, ProvisioningWorkflow{ID: fmt.Sprint(t.ID), Name: t.Name})
	}
	if prof.Accounts == nil {
		prof.Accounts = []Account{}
	}
	if prof.Signatures == nil {
		prof.Signatures = []Signature{}
	}
	return prof
}

// ServerProfile loads what the connected server offers, with the stored token
// (changing the mode). Fails offline.
func (a *App) ServerProfile(ctx context.Context) (ProvisioningProfile, error) {
	c := a.connCurrent()
	if c == nil {
		return ProvisioningProfile{}, Errorf(http.StatusConflict, "not_connected", "not connected")
	}
	s, err := a.storedSession(ctx, c)
	if err != nil {
		return ProvisioningProfile{}, err
	}
	return *a.profileForUI(c.URL, s.profile), nil
}

// storedSession rebuilds the provisioning session from the lasting connection.
func (a *App) storedSession(ctx context.Context, c *serverConnection) (*remoteSession, error) {
	tok, err := a.connToken(c.URL)
	if err != nil {
		return nil, Errorf(http.StatusConflict, "secret_store", "the secret store is not available")
	}
	key, err := a.connClientKey(c.URL)
	if err != nil {
		return nil, Errorf(http.StatusConflict, "secret_store", "the secret store is not available")
	}
	s := &remoteSession{priv: key, token: tok}
	var p remoteProfile
	code, err := a.remoteCall(ctx, http.MethodGet, c.URL+"/api/provisioning/profile", tok, nil, &p)
	if err != nil {
		return nil, Errorf(http.StatusServiceUnavailable, "offline", "the server cannot be reached")
	}
	if code == http.StatusUnauthorized {
		a.connMarkExpired(ctx)
		return nil, Errorf(http.StatusUnauthorized, "expired", "the sign-in at the server has expired")
	}
	if code/100 != 2 {
		return nil, Errorf(http.StatusBadGateway, "server_error", "the server could not provide the set-up (%d)", code)
	}
	s.profile = &p
	return s, nil
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return u
}

// DeviceToken polls the device flow.
func (a *App) DeviceToken(ctx context.Context, serverURL, deviceCode string) (DeviceTokenResult, error) {
	base, err := normalizeServerURL(serverURL)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	s, err := a.session(base, false)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	var tok tokenResponse
	if _, err := a.remoteCall(ctx, http.MethodPost, base+"/api/device/token", "", map[string]string{"device_code": deviceCode}, &tok); err != nil {
		return DeviceTokenResult{}, err
	}
	switch tok.Error {
	case "":
	case "authorization_pending":
		return DeviceTokenResult{Status: "pending"}, nil
	case "slow_down":
		return DeviceTokenResult{Status: "slow_down"}, nil
	case "access_denied":
		return DeviceTokenResult{Status: "denied"}, nil
	default:
		return DeviceTokenResult{Status: "expired", Error: tok.Error}, nil
	}
	return a.finishLogin(ctx, base, s, tok)
}

// ServerLogin logs in at a server with username and password (+TOTP).
func (a *App) ServerLogin(ctx context.Context, serverURL string, in ServerLoginInput) (DeviceTokenResult, error) {
	base, err := normalizeServerURL(serverURL)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	s, err := a.session(base, true)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	var tok tokenResponse
	code, err := a.remoteCall(ctx, http.MethodPost, base+"/api/device/login", "", map[string]string{"username": in.Username,
		"password": in.Password, "totp_code": in.TOTP, "device_name": connDeviceName(),
		"client_public_key": base64.RawURLEncoding.EncodeToString(s.priv.PublicKey().Bytes())}, &tok)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	if code/100 != 2 || tok.AccessToken == "" {
		msg := tok.Desc
		if msg == "" {
			msg = tok.Error
		}
		return DeviceTokenResult{Status: "denied", Error: msg}, nil
	}
	return a.finishLogin(ctx, base, s, tok)
}

// ImportProvisioning finishes the set-up in the chosen mode and makes the
// connection lasting. "thin": nothing is copied, the interface works through
// the server from now on. "local": the selected parts are set up on this
// device; it also serves changing the mode of an existing connection.
func (a *App) ImportProvisioning(ctx context.Context, in ProvisioningImport) error {
	base, err := normalizeServerURL(in.ServerURL)
	if err != nil {
		return err
	}
	if in.Mode != ConnModeThin && in.Mode != ConnModeLocal {
		return invalid("invalid mode")
	}
	s, err := a.session(base, false)
	if err != nil {
		cur := a.connCurrent()
		if cur == nil || cur.URL != base {
			return err
		}
		if s, err = a.storedSession(ctx, cur); err != nil {
			return err
		}
	}
	if s.profile == nil {
		return Errorf(http.StatusConflict, "not_connected", "log in first")
	}
	if cur := a.connCurrent(); cur != nil && (cur.URL != base || cur.Username != s.profile.User.Username) {
		return Errorf(http.StatusConflict, "already_connected", "this device is already connected to %s; disconnect first", cur.URL)
	}
	if in.Mode == ConnModeThin {
		if err := a.connCommit(ctx, base, s, in.Mode); err != nil {
			return err
		}
		a.dropSession(base)
		// Copies of the old "thin" set-up are not needed any more; enabled
		// accounts and accounts with mail stay untouched.
		return a.removeStaleThinAccounts(ctx, base)
	}
	if err := a.importLocal(ctx, base, s, in); err != nil {
		return err
	}
	if err := a.connCommit(ctx, base, s, in.Mode); err != nil {
		return err
	}
	a.dropSession(base)
	a.invalidateViews()
	return nil
}

// importLocal copies the selected parts of the profile. Doing it again (after
// the mode changed) finds what is there already instead of doubling it.
func (a *App) importLocal(ctx context.Context, base string, s *remoteSession, in ProvisioningImport) error {
	secretsByRef := map[string][]byte{}
	if s.profile.Secrets.Available {
		var box sealed.Box
		if code, err := a.remoteCall(ctx, http.MethodGet, base+"/api/provisioning/secrets", s.token, nil, &box); err == nil && code/100 == 2 {
			if plain, err := sealed.Open(s.priv, &box); err == nil {
				var p struct {
					Secrets map[string][]byte `json:"secrets"`
				}
				_ = json.Unmarshal(plain, &p)
				secretsByRef = p.Secrets
				clear(plain)
			}
		}
	}
	sigMap := map[int64]int64{}
	localSigs, err := a.st.ListSignatures(ctx)
	if err != nil {
		return err
	}
	excluded := []settingsPair{}
	for _, sig := range s.profile.Signatures {
		if !slices.Contains(in.SignatureIDs, sig.ID) {
			excluded = append(excluded, settingsPair{Remote: strconv.FormatInt(sig.ID, 10), Excluded: true})
			continue
		}
		old := sig.ID
		if i := slices.IndexFunc(localSigs, func(l domain.Signature) bool { return l.Name == sig.Name }); i >= 0 {
			sigMap[old] = localSigs[i].ID
			continue
		}
		id, err := a.createImportedSignature(ctx, sig) // images arrive as data: URIs
		if err != nil {
			return err
		}
		sigMap[old] = id
	}
	if err := a.excludeFromSync(ctx, "signature", excluded); err != nil {
		return err
	}
	existing, err := a.st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	accMap := map[int64]int64{}
	for _, acc := range s.profile.Accounts {
		if !slices.Contains(in.AccountIDs, acc.ID) {
			continue
		}
		old, ref := acc.ID, acc.SecretRef
		acc.ID, acc.SecretRef = 0, ""
		if acc.Extra == nil {
			acc.Extra = map[string]string{}
		}
		acc.Extra["provisionedFrom"] = base
		acc.Extra[extraProvisionedRemoteID] = strconv.FormatInt(old, 10)
		acc.Enabled = true
		var id int64
		if cur := findProvisionedAccount(existing, base, old, acc); cur != nil {
			id = cur.ID
			acc.ID, acc.SecretRef = cur.ID, cur.SecretRef
			if err := a.st.UpdateAccount(ctx, acc); err != nil {
				return err
			}
		} else {
			if id, err = a.st.CreateAccount(ctx, acc); err != nil {
				return err
			}
		}
		accMap[old] = id
		if v, ok := secretsByRef[ref]; ok {
			nr := accountSecretRef(id)
			if err := a.opts.Secrets.Set(nr, v); err == nil {
				acc.ID, acc.SecretRef = id, nr
				_ = a.st.UpdateAccount(ctx, acc)
			}
			clear(v)
		}
	}
	for _, i := range s.profile.Identities {
		if !slices.Contains(in.IdentityIDs, i.ID) {
			continue
		}
		acc, ok := accMap[i.AccountID]
		if !ok {
			continue
		}
		have, err := a.st.ListIdentities(ctx, acc)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(have, func(l domain.Identity) bool { return strings.EqualFold(l.Email, i.Email) }) {
			continue
		}
		i.ID, i.AccountID, i.SMTPAccountID = 0, acc, accMap[i.SMTPAccountID]
		i.DefaultSignatureID, i.ReplySignatureID = sigMap[i.DefaultSignatureID], sigMap[i.ReplySignatureID]
		if _, err := a.st.CreateIdentity(ctx, i); err != nil {
			return err
		}
	}
	for _, t := range s.profile.Templates {
		if !slices.Contains(in.WorkflowIDs, fmt.Sprint(t.ID)) {
			continue
		}
		var data struct {
			Workflows []string `json:"workflows"`
		}
		if json.Unmarshal(t.Data, &data) != nil {
			continue
		}
		for _, src := range data.Workflows {
			if w, err := workflow.Parse([]byte(src)); err == nil {
				if _, exists := a.wf.Get(w.ID); exists {
					continue
				}
				w.Enabled = false
				_ = a.wf.Save(ctx, w, false)
			}
		}
	}
	// Accounts are started when the mode is committed (connResumeLocal) or now, if sync runs.
	for _, id := range accMap {
		if err := a.sync.AddAccount(ctx, id); err != nil {
			a.log.Debug("app: starting provisioned account", "account", id, "err", err)
		}
		a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	}
	return nil
}

// findProvisionedAccount finds this device's copy of a server account: by its
// recorded id, or (copies made before that id was recorded) by kind and address.
func findProvisionedAccount(existing []domain.Account, base string, remoteID int64, acc domain.Account) *domain.Account {
	for i := range existing {
		cur := &existing[i]
		if cur.Extra["provisionedFrom"] != base {
			continue
		}
		if cur.Extra[extraProvisionedRemoteID] == strconv.FormatInt(remoteID, 10) {
			return cur
		}
		if cur.Extra[extraProvisionedRemoteID] == "" && cur.Kind == acc.Kind && cur.Extra["address"] == acc.Extra["address"] && cur.DisplayName == acc.DisplayName {
			return cur
		}
	}
	return nil
}

// excludeFromSync remembers what the user did not want on this device.
func (a *App) excludeFromSync(ctx context.Context, kind string, pairs []settingsPair) error {
	if len(pairs) == 0 {
		return nil
	}
	m := settingsMap{}
	if _, err := a.st.GetAppSetting(ctx, keySettingsMap, &m); err != nil {
		return err
	}
	for _, p := range pairs {
		if !slices.ContainsFunc(m[kind], func(o settingsPair) bool { return o.Remote == p.Remote }) {
			m[kind] = append(m[kind], p)
		}
	}
	return a.st.SetAppSetting(ctx, keySettingsMap, m)
}
