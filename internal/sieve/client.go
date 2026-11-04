// SPDX-License-Identifier: AGPL-3.0-or-later

package sieve

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// DefaultPort is the ManageSieve port (RFC 5804).
const DefaultPort = 4190

// ErrNo wraps NO responses of the server.
var ErrNo = errors.New("managesieve: NO")

// ServerError is a NO/BYE response with optional response code.
type ServerError struct {
	Status string // NO or BYE
	Code   string // e.g. "NONEXISTENT", "QUOTA/MAXSIZE"
	Text   string
}

func (e *ServerError) Error() string {
	s := "managesieve: " + e.Status
	if e.Code != "" {
		s += " (" + e.Code + ")"
	}
	if e.Text != "" {
		s += ": " + e.Text
	}
	return s
}

// Is makes errors.Is(err, ErrNo) work for NO responses.
func (e *ServerError) Is(target error) bool { return target == ErrNo && e.Status == "NO" }

// Config are the connection parameters of a ManageSieve server.
type Config struct {
	Host     string
	Port     int            // default 4190
	TLS      domain.TLSMode // default STARTTLS
	Username string
	Password string
	// Token is used with AUTHENTICATE XOAUTH2 when set (OAuth2 accounts).
	Token       func(ctx context.Context) (string, error)
	TLSConfig   *tls.Config
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	Timeout     time.Duration // per command, default 1 min
}

// ScriptInfo is one entry of LISTSCRIPTS.
type ScriptInfo struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// Client is a ManageSieve (RFC 5804) client connection.
type Client struct {
	nc      net.Conn
	r       *bufio.Reader
	w       *bufio.Writer
	caps    map[string]string
	tls     bool
	timeout time.Duration
}

// Dial connects, upgrades with STARTTLS (unless TLS is "none" or
// implicit) and authenticates.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("managesieve: missing host")
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.TLS == "" {
		cfg.TLS = domain.TLSStartTLS
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Minute
	}
	d := cfg.DialContext
	if d == nil {
		d = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	}
	nc, err := d(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	tcfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.TLSConfig != nil {
		tcfg = cfg.TLSConfig.Clone()
	}
	if tcfg.ServerName == "" {
		tcfg.ServerName = cfg.Host
	}
	c := &Client{nc: nc, timeout: cfg.Timeout}
	if cfg.TLS == domain.TLSImplicit {
		tc := tls.Client(nc, tcfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, fmt.Errorf("managesieve: TLS: %w", err)
		}
		c.nc, c.tls = tc, true
	}
	c.r, c.w = bufio.NewReader(c.nc), bufio.NewWriter(c.nc)
	if err := c.readCaps(); err != nil {
		c.nc.Close()
		return nil, fmt.Errorf("managesieve: greeting: %w", err)
	}
	if cfg.TLS == domain.TLSStartTLS {
		if !c.HasCap("STARTTLS") {
			c.nc.Close()
			return nil, errors.New("managesieve: server does not offer STARTTLS")
		}
		if _, err := c.simple("STARTTLS"); err != nil {
			c.nc.Close()
			return nil, err
		}
		tc := tls.Client(c.nc, tcfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			c.nc.Close()
			return nil, fmt.Errorf("managesieve: TLS: %w", err)
		}
		c.nc, c.tls = tc, true
		c.r, c.w = bufio.NewReader(tc), bufio.NewWriter(tc)
		// RFC 5804 2.2: the server re-sends its capabilities after STARTTLS.
		if err := c.readCaps(); err != nil {
			c.nc.Close()
			return nil, err
		}
	}
	if err := c.auth(ctx, cfg); err != nil {
		c.nc.Close()
		return nil, err
	}
	return c, nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	if c == nil || c.nc == nil {
		return nil
	}
	_, _ = c.simple("LOGOUT")
	return c.nc.Close()
}

// Capabilities returns the capability map (upper-case names).
func (c *Client) Capabilities() map[string]string { return c.caps }

// HasCap reports whether a capability is announced.
func (c *Client) HasCap(name string) bool {
	_, ok := c.caps[strings.ToUpper(name)]
	return ok
}

// Extensions lists the Sieve extensions of the SIEVE capability.
func (c *Client) Extensions() []string { return strings.Fields(strings.ToLower(c.caps["SIEVE"])) }

// HasExtension reports whether the server supports a Sieve extension.
func (c *Client) HasExtension(ext string) bool {
	for _, e := range c.Extensions() {
		if e == strings.ToLower(ext) {
			return true
		}
	}
	return false
}

func (c *Client) auth(ctx context.Context, cfg Config) error {
	if !c.tls && cfg.TLS != domain.TLSNone {
		return errors.New("managesieve: refusing to authenticate without TLS")
	}
	mechs := strings.Fields(strings.ToUpper(c.caps["SASL"]))
	has := func(m string) bool {
		for _, x := range mechs {
			if x == m {
				return true
			}
		}
		return false
	}
	var mech string
	var ir []byte
	if cfg.Token != nil {
		tok, err := cfg.Token(ctx)
		if err != nil {
			return fmt.Errorf("managesieve: OAuth2 token: %w", err)
		}
		mech, ir = "XOAUTH2", []byte("user="+cfg.Username+"\x01auth=Bearer "+tok+"\x01\x01")
		if !has("XOAUTH2") && has("OAUTHBEARER") {
			mech, ir = "OAUTHBEARER", []byte("n,a="+cfg.Username+",\x01auth=Bearer "+tok+"\x01\x01")
		}
	} else {
		if len(mechs) > 0 && !has("PLAIN") {
			return fmt.Errorf("managesieve: server offers no PLAIN authentication (%s)", c.caps["SASL"])
		}
		mech, ir = "PLAIN", []byte("\x00"+cfg.Username+"\x00"+cfg.Password)
	}
	if err := c.send("AUTHENTICATE " + quote(mech) + " " + quote(base64.StdEncoding.EncodeToString(ir))); err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		lines, status, err := c.readResponseMode(true)
		if err != nil {
			return err
		}
		if status == nil {
			// Challenge (XOAUTH2 error details): answer with an empty response.
			_ = lines
			if err := c.send(quote("")); err != nil {
				return err
			}
			continue
		}
		if status.Status != "OK" {
			return fmt.Errorf("managesieve: authentication failed: %w", status)
		}
		return nil
	}
	return errors.New("managesieve: authentication: too many challenges")
}

// ==== commands =================================================================

// ListScripts returns the scripts and which one is active.
func (c *Client) ListScripts() ([]ScriptInfo, error) {
	lines, err := c.simple("LISTSCRIPTS")
	if err != nil {
		return nil, err
	}
	out := []ScriptInfo{}
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		si := ScriptInfo{Name: l[0]}
		if len(l) > 1 && strings.EqualFold(l[1], "ACTIVE") {
			si.Active = true
		}
		out = append(out, si)
	}
	return out, nil
}

// GetScript downloads a script.
func (c *Client) GetScript(name string) (string, error) {
	lines, err := c.simple("GETSCRIPT " + quote(name))
	if err != nil {
		return "", err
	}
	if len(lines) == 0 || len(lines[0]) == 0 {
		return "", nil
	}
	return lines[0][0], nil
}

// PutScript uploads (creates or replaces) a script. The server validates it.
func (c *Client) PutScript(name, script string) error {
	_, err := c.simple("PUTSCRIPT " + quote(name) + " " + literal(script))
	return err
}

// CheckScript validates a script without storing it. Warnings (OK with
// WARNINGS code) are returned as text.
func (c *Client) CheckScript(script string) (warnings string, err error) {
	if !c.HasCap("VERSION") {
		// RFC 5804 servers (VERSION "1.0") support CHECKSCRIPT; older
		// ones only validate on PUTSCRIPT.
		return "", errors.New("managesieve: CHECKSCRIPT not supported by the server")
	}
	if err := c.send("CHECKSCRIPT " + literal(script)); err != nil {
		return "", err
	}
	_, st, err := c.readResponse()
	if err != nil {
		return "", err
	}
	if st.Status != "OK" {
		return "", st
	}
	if strings.HasPrefix(strings.ToUpper(st.Code), "WARNINGS") {
		return st.Text, nil
	}
	return "", nil
}

// SetActive activates a script ("" deactivates all).
func (c *Client) SetActive(name string) error {
	_, err := c.simple("SETACTIVE " + quote(name))
	return err
}

// DeleteScript deletes a (non-active) script.
func (c *Client) DeleteScript(name string) error {
	_, err := c.simple("DELETESCRIPT " + quote(name))
	return err
}

// Capability re-reads the capabilities.
func (c *Client) Capability() (map[string]string, error) {
	if err := c.send("CAPABILITY"); err != nil {
		return nil, err
	}
	if err := c.readCaps(); err != nil {
		return nil, err
	}
	return c.caps, nil
}

// ==== protocol =================================================================

func quote(s string) string {
	if strings.ContainsAny(s, "\r\n") || len(s) > 1024 {
		return literal(s)
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// literal is a non-synchronizing literal {n+} (RFC 5804 section 1.6).
func literal(s string) string { return "{" + strconv.Itoa(len(s)) + "+}\r\n" + s }

func (c *Client) send(line string) error {
	_ = c.nc.SetWriteDeadline(time.Now().Add(c.timeout))
	if _, err := c.w.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *Client) simple(cmd string) ([][]string, error) {
	if err := c.send(cmd); err != nil {
		return nil, err
	}
	lines, st, err := c.readResponse()
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("managesieve: unexpected continuation")
	}
	if st.Status != "OK" {
		return nil, st
	}
	return lines, nil
}

func (c *Client) readCaps() error {
	lines, st, err := c.readResponse()
	if err != nil {
		return err
	}
	if st == nil || st.Status != "OK" {
		if st != nil {
			return st
		}
		return errors.New("managesieve: unexpected continuation")
	}
	c.caps = map[string]string{}
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		v := ""
		if len(l) > 1 {
			v = l[1]
		}
		c.caps[strings.ToUpper(l[0])] = v
	}
	return nil
}

// readResponse reads data lines up to the final OK/NO/BYE. A response
// consisting of a single string line without status is a SASL challenge
// (status nil).
func (c *Client) readResponse() ([][]string, *ServerError, error) { return c.readResponseMode(false) }

func (c *Client) readResponseMode(auth bool) ([][]string, *ServerError, error) {
	var lines [][]string
	for {
		_ = c.nc.SetReadDeadline(time.Now().Add(c.timeout))
		toks, atoms, err := c.readLine()
		if err != nil {
			return nil, nil, err
		}
		if len(toks) > 0 && atoms[0] {
			switch s := strings.ToUpper(toks[0]); s {
			case "OK", "NO", "BYE":
				st := &ServerError{Status: s}
				rest := toks[1:]
				ra := atoms[1:]
				if len(rest) > 0 && ra[0] && strings.HasPrefix(rest[0], "(") {
					st.Code = strings.Trim(rest[0], "()")
					rest = rest[1:]
				}
				if len(rest) > 0 {
					st.Text = rest[0]
				}
				return lines, st, nil
			}
		}
		if auth && len(toks) == 1 && !atoms[0] && len(lines) == 0 {
			return [][]string{toks}, nil, nil
		}
		lines = append(lines, toks)
	}
}

// readLine tokenizes one response line. atoms[i] reports whether token i
// was an atom (not a string).
func (c *Client) readLine() ([]string, []bool, error) {
	var toks []string
	var atoms []bool
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return nil, nil, err
		}
		switch {
		case b == ' ':
			continue
		case b == '\r':
			continue
		case b == '\n':
			return toks, atoms, nil
		case b == '"':
			s, err := c.readQuoted()
			if err != nil {
				return nil, nil, err
			}
			toks, atoms = append(toks, s), append(atoms, false)
		case b == '{':
			s, err := c.readLiteral()
			if err != nil {
				return nil, nil, err
			}
			toks, atoms = append(toks, s), append(atoms, false)
		case b == '(':
			var sb strings.Builder
			sb.WriteByte('(')
			inQ := false
			for {
				x, err := c.r.ReadByte()
				if err != nil {
					return nil, nil, err
				}
				sb.WriteByte(x)
				if x == '"' {
					inQ = !inQ
				}
				if x == ')' && !inQ {
					break
				}
				if x == '\n' {
					return nil, nil, errors.New("managesieve: unterminated response code")
				}
			}
			toks, atoms = append(toks, sb.String()), append(atoms, true)
		default:
			var sb strings.Builder
			sb.WriteByte(b)
			for {
				x, err := c.r.ReadByte()
				if err != nil {
					return nil, nil, err
				}
				if x == ' ' || x == '\r' || x == '\n' {
					_ = c.r.UnreadByte()
					break
				}
				sb.WriteByte(x)
			}
			toks, atoms = append(toks, sb.String()), append(atoms, true)
		}
	}
}

func (c *Client) readQuoted() (string, error) {
	var sb strings.Builder
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '\\':
			n, err := c.r.ReadByte()
			if err != nil {
				return "", err
			}
			sb.WriteByte(n)
		case '"':
			return sb.String(), nil
		case '\n':
			return "", errors.New("managesieve: newline in quoted string")
		default:
			sb.WriteByte(b)
		}
		if sb.Len() > 1<<20 {
			return "", errors.New("managesieve: string too long")
		}
	}
}

func (c *Client) readLiteral() (string, error) {
	spec, err := c.r.ReadString('}')
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(spec, "}"), "+"))
	if err != nil || n < 0 || n > 16<<20 {
		return "", fmt.Errorf("managesieve: bad literal %q", spec)
	}
	// CRLF after the length.
	if b, _ := c.r.ReadByte(); b == '\r' {
		_, _ = c.r.ReadByte()
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}
