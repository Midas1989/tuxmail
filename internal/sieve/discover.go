// SPDX-License-Identifier: AGPL-3.0-or-later

package sieve

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Discovery is the result of probing a ManageSieve server.
type Discovery struct {
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	STARTTLS   bool     `json:"starttls"`
	Extensions []string `json:"extensions"`
	SASL       []string `json:"sasl"`
}

// Discover probes ManageSieve on port 4190 of the given hosts (normally the
// IMAP host) and returns the first one that answers with a capability list.
// No credentials are sent.
func Discover(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), hosts ...string) (*Discovery, error) {
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	var last error = ErrNoServer
	for _, h := range hosts {
		if h == "" {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		d, err := probe(pctx, dial, h, DefaultPort)
		cancel()
		if err == nil {
			return d, nil
		}
		last = err
	}
	return nil, last
}

// ErrNoServer is returned when no ManageSieve server was found.
var ErrNoServer = &ServerError{Status: "BYE", Text: "no ManageSieve server found"}

func probe(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), host string, port int) (*Discovery, error) {
	nc, err := dial(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	c := &Client{nc: nc, r: bufio.NewReader(nc), w: bufio.NewWriter(nc), timeout: 10 * time.Second}
	if err := c.readCaps(); err != nil {
		return nil, err
	}
	_, _ = c.simple("LOGOUT")
	return &Discovery{Host: host, Port: port, STARTTLS: c.HasCap("STARTTLS"), Extensions: c.Extensions(),
		SASL: strings.Fields(strings.ToUpper(c.caps["SASL"]))}, nil
}

// ConfigFor derives ManageSieve settings from an IMAP account: same host
// and credentials, port 4190 with STARTTLS (Extra "sieveHost", "sievePort",
// "sieveTLS" override).
func ConfigFor(acc domain.Account) (Config, bool) {
	if acc.Incoming == nil || acc.Incoming.Host == "" {
		return Config{}, false
	}
	cfg := Config{Host: acc.Incoming.Host, Port: DefaultPort, TLS: domain.TLSStartTLS, Username: acc.Incoming.Username}
	if acc.Extra != nil {
		if h := strings.TrimSpace(acc.Extra["sieveHost"]); h != "" {
			cfg.Host = h
		}
		if p, err := strconv.Atoi(acc.Extra["sievePort"]); err == nil && p > 0 {
			cfg.Port = p
		}
		switch domain.TLSMode(acc.Extra["sieveTLS"]) {
		case domain.TLSImplicit, domain.TLSNone, domain.TLSStartTLS:
			cfg.TLS = domain.TLSMode(acc.Extra["sieveTLS"])
		}
		if acc.Extra["sieve"] == "off" {
			return Config{}, false
		}
	}
	if acc.Incoming.TLS == domain.TLSNone && (acc.Extra == nil || acc.Extra["sieveTLS"] == "") {
		cfg.TLS = domain.TLSNone // test/lab setups without TLS
	}
	return cfg, true
}
