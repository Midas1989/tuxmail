// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Account extras used by EAS accounts.
const (
	// ExtraURL is the full ActiveSync URL (default
	// https://<incoming host>/Microsoft-Server-ActiveSync).
	ExtraURL = "eas_url"
	// ExtraDeviceID overrides the derived device id.
	ExtraDeviceID = "eas_device_id"
	// ExtraVersion forces a protocol version (e.g. "14.1").
	ExtraVersion = "eas_version"
	// ExtraMaxVersion limits version negotiation.
	ExtraMaxVersion = "eas_max_version"
	// ExtraFilter is the mail FilterType ("0" = all).
	ExtraFilter = "eas_filter"
)

func extra(acc domain.Account, k string) string {
	if acc.Extra == nil {
		return ""
	}
	return acc.Extra[k]
}

// AccountDeviceID derives a stable device id (alphanumeric, 23
// characters) for an account; mail sync and sending must use the same id.
func AccountDeviceID(acc domain.Account) string {
	if v := extra(acc, ExtraDeviceID); v != "" {
		return v
	}
	user := ""
	if acc.Incoming != nil {
		user = acc.Incoming.Username
	}
	h := sha256.Sum256([]byte("tuxmail-eas|" + strconv.FormatInt(acc.ID, 10) + "|" + strings.ToLower(user)))
	return "TuxMail" + strings.ToUpper(hex.EncodeToString(h[:8]))
}

// AccountURL returns the ActiveSync endpoint of an account.
func AccountURL(acc domain.Account) (string, error) {
	if v := extra(acc, ExtraURL); v != "" {
		return v, nil
	}
	if acc.Incoming == nil || acc.Incoming.Host == "" {
		return "", errors.New("eas: account has neither eas_url nor incoming host")
	}
	scheme := "https"
	if acc.Incoming.TLS == domain.TLSNone {
		scheme = "http"
	}
	host := acc.Incoming.Host
	if p := acc.Incoming.Port; p != 0 && !(p == 443 && scheme == "https") && !(p == 80 && scheme == "http") {
		host = net.JoinHostPort(host, strconv.Itoa(p))
	}
	return scheme + "://" + host + "/Microsoft-Server-ActiveSync", nil
}

// AccountClientConfig returns the account-derived part of a ClientConfig
// (URL, user, device id, version overrides); credentials are added by the
// caller.
func AccountClientConfig(acc domain.Account) (ClientConfig, error) {
	u, err := AccountURL(acc)
	if err != nil {
		return ClientConfig{}, err
	}
	cc := ClientConfig{URL: u, DeviceID: AccountDeviceID(acc), ProtocolVersion: extra(acc, ExtraVersion),
		MaxProtocolVersion: extra(acc, ExtraMaxVersion)}
	if acc.Incoming != nil {
		cc.Username = acc.Incoming.Username
	}
	return cc, nil
}
