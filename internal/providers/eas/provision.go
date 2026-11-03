// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"fmt"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// Policy types ([MS-ASPROV] 2.2.2.42).
const (
	policyTypeWBXML = "MS-EAS-Provisioning-WBXML"
	policyTypeXML   = "MS-WAP-Provisioning-XML" // EAS 2.5
)

// Provision runs the two-phase provisioning of [MS-ASPROV] 3.1.5.1:
// download the policy (phase 1), then acknowledge it with the temporary
// key and receive the final policy key (phase 2). A remote wipe request is
// executed through ClientConfig.OnRemoteWipe and acknowledged; Provision
// then returns ErrRemoteWiped.
func (c *Client) Provision(ctx context.Context) (Policy, error) {
	c.provMu.Lock()
	defer c.provMu.Unlock()
	return c.provisionLocked(ctx)
}

// reprovision is called when the server demands provisioning (HTTP 449 or
// status 140-144). Concurrent callers wait for a single run.
func (c *Client) reprovision(ctx context.Context, reason int) error {
	c.mu.Lock()
	gen := c.provGen
	c.mu.Unlock()
	c.provMu.Lock()
	defer c.provMu.Unlock()
	c.mu.Lock()
	done := c.provGen != gen
	c.mu.Unlock()
	if done {
		return nil // someone else provisioned meanwhile
	}
	c.log.Info("eas: server requires provisioning", "reason", reason)
	_, err := c.provisionLocked(ctx)
	return err
}

func (c *Client) setPolicyKey(ctx context.Context, key string, final bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policyKey = key
	if final {
		c.provGen++
		c.saveLocked(ctx)
	}
}

func (c *Client) provisionLocked(ctx context.Context) (Policy, error) {
	ptype := policyTypeWBXML
	if !c.versionAtLeast(ctx, "12.0") {
		ptype = policyTypeXML
	}
	c.setPolicyKey(ctx, "", false)

	req := wbxml.E("Provision:Provision")
	if c.versionAtLeast(ctx, "14.1") {
		req.Add(c.deviceInformation())
	}
	req.Add(wbxml.E("Provision:Policies", wbxml.E("Provision:Policy", wbxml.S("Provision:PolicyType", ptype))))
	resp, err := c.provisionRequest(ctx, req)
	if err != nil {
		return Policy{}, err
	}
	if wiped, err := c.handleWipe(ctx, resp); wiped || err != nil {
		return Policy{}, err
	}
	if st := resp.IntOf("Provision:Status", 1); st != 1 {
		return Policy{}, fmt.Errorf("%w: %w", ErrProvisioning, &StatusError{Cmd: "Provision", Status: st})
	}
	pol := resp.Path("Provision:Policies/Provision:Policy")
	if pol == nil {
		// No policy element: the server does not use policies.
		c.setPolicyKey(ctx, "", true)
		return Policy{}, nil
	}
	switch st := pol.IntOf("Provision:Status", 1); st {
	case 1:
	case 2: // no policy for this client
		c.setPolicyKey(ctx, "", true)
		return Policy{}, nil
	default:
		return Policy{}, fmt.Errorf("%w: %w", ErrProvisioning, &StatusError{Cmd: "Provision", Status: st, Where: "Policy"})
	}
	temp := pol.TextOf("Provision:PolicyKey")
	policy := Policy{Settings: map[string]string{}}
	if doc := pol.Path("Provision:Data/Provision:EASProvisionDoc"); doc != nil {
		for _, s := range doc.Children {
			policy.Settings[s.Tag.Name()] = s.Value()
		}
	} else if data := pol.Child("Provision:Data"); data != nil && data.Value() != "" {
		policy.Settings["wap-provisioningdoc"] = data.Value()
	}

	// Phase 2: acknowledge with the temporary key.
	c.setPolicyKey(ctx, temp, false)
	ack := wbxml.E("Provision:Provision", wbxml.E("Provision:Policies", wbxml.E("Provision:Policy",
		wbxml.S("Provision:PolicyType", ptype), wbxml.S("Provision:PolicyKey", temp),
		wbxml.S("Provision:Status", c.cfg.PolicyAckStatus))))
	resp, err = c.provisionRequest(ctx, ack)
	if err != nil {
		return Policy{}, err
	}
	if wiped, err := c.handleWipe(ctx, resp); wiped || err != nil {
		return Policy{}, err
	}
	if st := resp.IntOf("Provision:Status", 1); st != 1 {
		return Policy{}, fmt.Errorf("%w: %w", ErrProvisioning, &StatusError{Cmd: "Provision", Status: st})
	}
	pol = resp.Path("Provision:Policies/Provision:Policy")
	if st := pol.IntOf("Provision:Status", 1); st != 1 {
		return Policy{}, fmt.Errorf("%w: %w", ErrProvisioning, &StatusError{Cmd: "Provision", Status: st, Where: "Policy"})
	}
	final := pol.TextOf("Provision:PolicyKey")
	if final == "" {
		final = temp
	}
	policy.Key = final
	c.setPolicyKey(ctx, final, true)
	c.mu.Lock()
	c.policy = policy
	c.mu.Unlock()
	c.log.Info("eas: device provisioned", "policies", len(policy.Settings))
	if c.cfg.OnPolicy != nil {
		c.cfg.OnPolicy(policy)
	}
	return policy, nil
}

func (c *Client) provisionRequest(ctx context.Context, req *wbxml.Node) (*wbxml.Node, error) {
	data, err := wbxml.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, &request{cmd: "Provision", body: data, contentType: wbxml.ContentType, noProvision: true})
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.Is("Provision:Provision") {
		return nil, fmt.Errorf("%w: unexpected response", ErrProvisioning)
	}
	return resp, nil
}

// handleWipe executes and acknowledges a remote wipe request.
func (c *Client) handleWipe(ctx context.Context, resp *wbxml.Node) (bool, error) {
	elem := ""
	switch {
	case resp.Has("Provision:AccountOnlyRemoteWipe"):
		elem = "Provision:AccountOnlyRemoteWipe"
	case resp.Has("Provision:RemoteWipe"):
		elem = "Provision:RemoteWipe"
	default:
		return false, nil
	}
	accountOnly := elem == "Provision:AccountOnlyRemoteWipe"
	c.log.Warn("eas: server requested remote wipe", "accountOnly", accountOnly)
	if c.cfg.OnRemoteWipe == nil {
		return true, fmt.Errorf("%w (no wipe handler configured, not acknowledged)", ErrRemoteWiped)
	}
	if err := c.cfg.OnRemoteWipe(ctx, accountOnly); err != nil {
		return true, fmt.Errorf("%w: local wipe failed: %v", ErrRemoteWiped, err)
	}
	ack := wbxml.E("Provision:Provision", wbxml.E(elem, wbxml.S("Provision:Status", "1")))
	if _, err := c.provisionRequest(ctx, ack); err != nil {
		c.log.Warn("eas: remote wipe acknowledgment failed", "err", err)
	}
	c.updateState(ctx, func(st *accountState) { *st = accountState{} })
	// A wiped device stops talking to the server; a new client (account
	// re-enabled by the user) starts from scratch.
	c.mu.Lock()
	c.wiped = true
	c.mu.Unlock()
	return true, ErrRemoteWiped
}

func (c *Client) deviceInformation() *wbxml.Node {
	di := c.cfg.DeviceInfo
	if di.Model == "" {
		di.Model = "TuxMail"
	}
	if di.FriendlyName == "" {
		di.FriendlyName = "TuxMail"
	}
	if di.UserAgent == "" {
		di.UserAgent = c.cfg.UserAgent
	}
	set := wbxml.E("Settings:Set", wbxml.S("Settings:Model", di.Model), wbxml.S("Settings:FriendlyName", di.FriendlyName))
	if di.OS != "" {
		set.Add(wbxml.S("Settings:OS", di.OS))
	}
	if di.OSLanguage != "" {
		set.Add(wbxml.S("Settings:OSLanguage", di.OSLanguage))
	}
	set.Add(wbxml.S("Settings:UserAgent", di.UserAgent))
	return wbxml.E("Settings:DeviceInformation", set)
}

// CurrentPolicy returns the last provisioned policy.
func (c *Client) CurrentPolicy() Policy {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.policy
}
