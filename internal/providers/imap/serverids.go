// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

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
	"unicode/utf16"

	"github.com/emersion/go-imap/v2"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// go-imap v2 rejects FETCH data items it does not know (EMAILID, THREADID,
// X-GM-MSGID, X-GM-THRID). To still obtain these move-stable identifiers a
// small secondary connection ("sidecar") speaking just enough IMAP is used.
// It is only opened when the server advertises OBJECTID or X-GM-EXT-1, is
// read-only (EXAMINE) and failures are never fatal for a sync.

type serverIDs struct {
	object string // EMAILID or X-GM-MSGID
	thread string // THREADID or X-GM-THRID
}

// serverIDsLocked returns object/thread IDs for uids in folder. p.mu is held.
func (p *Provider) serverIDsLocked(ctx context.Context, folder string, uids []uint32) map[uint32]serverIDs {
	if len(uids) == 0 {
		return nil
	}
	var items string
	switch {
	case p.caps.Has(capGmail):
		items = "X-GM-MSGID X-GM-THRID"
	case p.caps.Has(imap.CapObjectID):
		items = "EMAILID THREADID"
	default:
		return nil
	}
	if p.ids == nil {
		ic, err := p.dialIDConn(ctx)
		if err != nil {
			p.log.Warn("imap: object-id connection failed", "err", err)
			return nil
		}
		p.ids = ic
	}
	res, err := p.ids.fetchIDs(ctx, folder, uids, items)
	if err != nil {
		p.log.Warn("imap: fetching object ids failed", "folder", folder, "err", err)
		p.ids.close()
		p.ids = nil
		return nil
	}
	return res
}

// idConn is a minimal IMAP client for the ID fetches.
type idConn struct {
	conn     net.Conn
	br       *bufio.Reader
	tag      int
	caps     map[string]bool
	selected string
}

func (p *Provider) dialIDConn(ctx context.Context) (*idConn, error) {
	conn, err := p.dialRaw(ctx)
	if err != nil {
		return nil, err
	}
	ic := &idConn{conn: conn, br: bufio.NewReader(conn)}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	stop := ic.guard(ctx)
	defer stop()

	greeting, err := ic.readLine()
	if err != nil {
		return nil, ctxErr(ctx, err)
	}
	preauth := strings.HasPrefix(greeting, "* PREAUTH")
	if !preauth && !strings.HasPrefix(greeting, "* OK") {
		return nil, fmt.Errorf("imap: unexpected greeting %q", greeting)
	}
	if p.cfg.Server.TLS == domain.TLSStartTLS {
		if preauth {
			return nil, errors.New("imap: PREAUTH on unencrypted connection")
		}
		if _, err := ic.cmd("STARTTLS"); err != nil {
			return nil, ctxErr(ctx, err)
		}
		if ic.br.Buffered() > 0 {
			return nil, errors.New("imap: data injected before TLS handshake")
		}
		tc := tls.Client(conn, p.tlsConfig())
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		ic.conn, ic.br = tc, bufio.NewReader(tc)
	}
	if !preauth {
		if err := ic.capability(); err != nil {
			return nil, ctxErr(ctx, err)
		}
		if err := ic.login(ctx, p); err != nil {
			return nil, ctxErr(ctx, err)
		}
	}
	ok = true
	return ic, nil
}

// guard closes the connection when ctx ends; the returned func disarms it.
func (ic *idConn) guard(ctx context.Context) func() bool {
	conn := ic.conn
	return context.AfterFunc(ctx, func() { conn.Close() })
}

func (ic *idConn) close() {
	_ = ic.conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = ic.cmd("LOGOUT")
	ic.conn.Close()
}

// readLine reads one response line, replacing literals by "{}".
func (ic *idConn) readLine() (string, error) {
	var sb strings.Builder
	for {
		line, err := ic.br.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if sb.Len()+len(line) > 1<<20 {
			return "", errors.New("imap: response line too long")
		}
		// literal: ... {n}\r\n<n bytes>
		if strings.HasSuffix(line, "}") {
			if i := strings.LastIndexByte(line, '{'); i >= 0 {
				n, err := strconv.ParseInt(strings.TrimSuffix(line[i+1:len(line)-1], "+"), 10, 64)
				if err == nil {
					sb.WriteString(line[:i])
					sb.WriteString(`""`)
					if _, err := io.CopyN(io.Discard, ic.br, n); err != nil {
						return "", err
					}
					continue
				}
			}
		}
		sb.WriteString(line)
		return sb.String(), nil
	}
}

// cmd sends a command and returns the untagged lines. A continuation request
// is answered with an empty line (aborting SASL exchanges).
func (ic *idConn) cmd(command string) ([]string, error) {
	return ic.cmdCont(command, nil)
}

func (ic *idConn) cmdCont(command string, cont func(challenge string) string) ([]string, error) {
	ic.tag++
	tag := "S" + strconv.Itoa(ic.tag)
	if _, err := io.WriteString(ic.conn, tag+" "+command+"\r\n"); err != nil {
		return nil, err
	}
	var untagged []string
	for {
		line, err := ic.readLine()
		if err != nil {
			return untagged, err
		}
		switch {
		case strings.HasPrefix(line, "+"):
			resp := ""
			if cont != nil {
				resp = cont(strings.TrimSpace(strings.TrimPrefix(line, "+")))
			}
			if _, err := io.WriteString(ic.conn, resp+"\r\n"); err != nil {
				return untagged, err
			}
		case strings.HasPrefix(line, "* "):
			untagged = append(untagged, line[2:])
		case strings.HasPrefix(line, tag+" "):
			status := line[len(tag)+1:]
			if strings.HasPrefix(strings.ToUpper(status), "OK") {
				return untagged, nil
			}
			return untagged, fmt.Errorf("imap: %s failed: %s", strings.Fields(command)[0], status)
		}
	}
}

func (ic *idConn) capability() error {
	lines, err := ic.cmd("CAPABILITY")
	if err != nil {
		return err
	}
	ic.caps = map[string]bool{}
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 && strings.EqualFold(f[0], "CAPABILITY") {
			for _, c := range f[1:] {
				ic.caps[strings.ToUpper(c)] = true
			}
		}
	}
	return nil
}

func (ic *idConn) authenticate(mech string, ir []byte) error {
	enc := base64.StdEncoding.EncodeToString(ir)
	if ic.caps["SASL-IR"] {
		_, err := ic.cmd("AUTHENTICATE " + mech + " " + enc)
		return err
	}
	sent := false
	_, err := ic.cmdCont("AUTHENTICATE "+mech, func(string) string {
		if sent {
			return "" // error challenge (XOAUTH2): abort
		}
		sent = true
		return enc
	})
	return err
}

func (ic *idConn) login(ctx context.Context, p *Provider) error {
	user := p.cfg.Server.Username
	switch p.cfg.Server.Auth {
	case domain.AuthOAuth2:
		token, err := p.cfg.TokenSource.Token(ctx)
		if err != nil {
			return err
		}
		if p.cfg.OAuthMechanism == "OAUTHBEARER" || (p.cfg.OAuthMechanism == "" && !ic.caps["AUTH=XOAUTH2"] && ic.caps["AUTH=OAUTHBEARER"]) {
			ir := "n,a=" + user + ",\x01host=" + p.cfg.Server.Host + "\x01port=" + strconv.Itoa(p.cfg.Server.Port) +
				"\x01auth=Bearer " + token + "\x01\x01"
			return ic.authenticate("OAUTHBEARER", []byte(ir))
		}
		return ic.authenticate("XOAUTH2", xoauth2IR(user, token))
	case domain.AuthClientCert:
		if ic.caps["AUTH=EXTERNAL"] {
			return ic.authenticate("EXTERNAL", []byte{})
		}
		return nil
	default:
		if ic.caps["AUTH=PLAIN"] {
			return ic.authenticate("PLAIN", []byte("\x00"+user+"\x00"+p.cfg.Password))
		}
		if !isQuotable(user) || !isQuotable(p.cfg.Password) {
			return errors.New("imap: credentials need AUTH=PLAIN")
		}
		_, err := ic.cmd("LOGIN " + quote(user) + " " + quote(p.cfg.Password))
		return err
	}
}

func isQuotable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (ic *idConn) fetchIDs(ctx context.Context, folder string, uids []uint32, items string) (map[uint32]serverIDs, error) {
	stop := ic.guard(ctx)
	defer stop()
	if ic.selected != folder {
		ic.selected = ""
		if _, err := ic.cmd("EXAMINE " + quote(encodeMailboxUTF7(folder))); err != nil {
			return nil, ctxErr(ctx, err)
		}
		ic.selected = folder
	}
	lines, err := ic.cmd("UID FETCH " + uidSet(uids).String() + " (UID " + items + ")")
	if err != nil {
		return nil, ctxErr(ctx, err)
	}
	out := make(map[uint32]serverIDs, len(uids))
	for _, l := range lines {
		uid, ids, ok := parseIDFetch(l)
		if _, seen := out[uid]; ok && (!seen || ids != serverIDs{}) {
			out[uid] = ids
		}
	}
	return out, nil
}

// parseIDFetch parses `<seq> FETCH (UID n EMAILID (x) THREADID (y))` or the
// Gmail variant `<seq> FETCH (X-GM-THRID n X-GM-MSGID m UID u)`.
func parseIDFetch(line string) (uint32, serverIDs, bool) {
	f := strings.Fields(line)
	if len(f) < 3 || !strings.EqualFold(f[1], "FETCH") {
		return 0, serverIDs{}, false
	}
	i := strings.IndexByte(line, '(')
	j := strings.LastIndexByte(line, ')')
	if i < 0 || j <= i {
		return 0, serverIDs{}, false
	}
	toks := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ").Replace(line[i+1 : j]))
	var uid uint64
	var ids serverIDs
	value := func(k int) (string, int) {
		if k >= len(toks) {
			return "", k
		}
		if toks[k] == "(" {
			v := ""
			k++
			for k < len(toks) && toks[k] != ")" {
				v = toks[k]
				k++
			}
			return v, k + 1
		}
		if strings.EqualFold(toks[k], "NIL") {
			return "", k + 1
		}
		return toks[k], k + 1
	}
	for k := 0; k < len(toks); {
		name := strings.ToUpper(toks[k])
		v, nk := value(k + 1)
		switch name {
		case "UID":
			uid, _ = strconv.ParseUint(v, 10, 32)
		case "EMAILID", "X-GM-MSGID":
			ids.object = v
		case "THREADID", "X-GM-THRID":
			ids.thread = v
		}
		k = nk
	}
	if uid == 0 {
		return 0, serverIDs{}, false
	}
	return uint32(uid), ids, true
}

// encodeMailboxUTF7 encodes a mailbox name in IMAP modified UTF-7 (RFC 3501
// section 5.1.3).
func encodeMailboxUTF7(s string) string {
	const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,"
	enc := base64.NewEncoding(b64).WithPadding(base64.NoPadding)
	var sb strings.Builder
	var pending []rune
	flush := func() {
		if len(pending) == 0 {
			return
		}
		u := utf16.Encode(pending)
		b := make([]byte, 0, 2*len(u))
		for _, c := range u {
			b = append(b, byte(c>>8), byte(c))
		}
		sb.WriteByte('&')
		sb.WriteString(enc.EncodeToString(b))
		sb.WriteByte('-')
		pending = pending[:0]
	}
	for _, r := range s {
		if r >= 0x20 && r <= 0x7e {
			flush()
			if r == '&' {
				sb.WriteString("&-")
			} else {
				sb.WriteRune(r)
			}
			continue
		}
		pending = append(pending, r)
	}
	flush()
	return sb.String()
}
