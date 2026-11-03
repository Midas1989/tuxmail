// SPDX-License-Identifier: AGPL-3.0-or-later

package smtp

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/emersion/go-sasl"
)

// xoauth2Client implements the Google/Microsoft XOAUTH2 SASL mechanism.
// go-sasl dropped its XOAUTH2 client, so it is implemented here.
type xoauth2Client struct {
	username, token string
}

func newXOAuth2Client(username, token string) sasl.Client {
	return &xoauth2Client{username: username, token: token}
}

func (c *xoauth2Client) Start() (string, []byte, error) {
	ir := []byte("user=" + c.username + "\x01auth=Bearer " + c.token + "\x01\x01")
	return "XOAUTH2", ir, nil
}

// Next is only called when the server rejected the token: it sends a JSON
// error as challenge and expects an empty response before the final failure.
func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}

// loginClient implements the obsolete LOGIN mechanism. Unlike the go-sasl
// implementation it does not send an initial response and accepts the usual
// "Username:"/"Password:" prompts in any order.
type loginClient struct {
	username, password string
	step               int
}

func newLoginClient(username, password string) sasl.Client {
	return &loginClient{username: username, password: password}
}

func (c *loginClient) Start() (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (c *loginClient) Next(challenge []byte) ([]byte, error) {
	c.step++
	lc := bytes.ToLower(challenge)
	switch {
	case bytes.HasPrefix(lc, []byte("user")):
		return []byte(c.username), nil
	case bytes.HasPrefix(lc, []byte("pass")):
		return []byte(c.password), nil
	case c.step == 1:
		return []byte(c.username), nil
	case c.step == 2:
		return []byte(c.password), nil
	}
	return nil, errors.New("smtp: unexpected LOGIN challenge")
}

// errNoAuthMechanism is returned when the server offers no usable mechanism.
func errNoAuthMechanism(method string, offered string) error {
	return fmt.Errorf("smtp: server offers no supported mechanism for %s authentication (offered: %q)", method, offered)
}
