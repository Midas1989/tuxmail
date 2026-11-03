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
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/server/sealed"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// Desktop client: connect to a TuxMail server (KONZEPT 15.8). The device
// token and the X25519 key for the end-to-end encrypted secrets stay in the
// core (secret store); the UI only sees the profile.

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
		"client_name": "TuxMail Desktop", "client_public_key": base64.RawURLEncoding.EncodeToString(s.priv.PublicKey().Bytes())}, &res)
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
	if err := a.opts.Secrets.Set("server:"+base+":token", []byte(tok.AccessToken)); err != nil {
		a.log.Warn("app: storing device token failed", "err", err)
	}
	var p remoteProfile
	code, err := a.remoteCall(ctx, http.MethodGet, base+"/api/provisioning/profile", s.token, nil, &p)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	if code/100 != 2 {
		return DeviceTokenResult{Status: "denied", Error: fmt.Sprintf("profile request failed (%d)", code)}, nil
	}
	s.profile = &p
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
	return DeviceTokenResult{Status: "approved", Profile: prof}, nil
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
		"password": in.Password, "totp_code": in.TOTP, "device_name": "TuxMail Desktop",
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

// ImportProvisioning imports the selected parts of a server profile.
func (a *App) ImportProvisioning(ctx context.Context, in ProvisioningImport) error {
	base, err := normalizeServerURL(in.ServerURL)
	if err != nil {
		return err
	}
	s, err := a.session(base, false)
	if err != nil {
		return err
	}
	if s.profile == nil {
		return Errorf(http.StatusConflict, "not_connected", "log in first")
	}
	secretsByRef := map[string][]byte{}
	if s.profile.Secrets.Available && in.Mode == "local" {
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
	for _, sig := range s.profile.Signatures {
		if !slices.Contains(in.SignatureIDs, sig.ID) {
			continue
		}
		old := sig.ID
		sig.ID = 0
		id, err := a.st.CreateSignature(ctx, sig)
		if err != nil {
			return err
		}
		sigMap[old] = id
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
		if in.Mode == "thin" {
			acc.Enabled = false // the server syncs; this device is a thin client
		}
		id, err := a.st.CreateAccount(ctx, acc)
		if err != nil {
			return err
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
				w.Enabled = false
				_ = a.wf.Save(ctx, w, false)
			}
		}
	}
	for _, id := range accMap {
		if err := a.sync.AddAccount(ctx, id); err != nil {
			a.log.Debug("app: starting provisioned account", "account", id, "err", err)
		}
		a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	}
	a.invalidateViews()
	return nil
}
