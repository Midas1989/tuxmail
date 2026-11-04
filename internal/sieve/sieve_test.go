// SPDX-License-Identifier: AGPL-3.0-or-later

package sieve

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

var update = flag.Bool("update", false, "rewrite golden files")

// ==== fake ManageSieve server ==================================================

type fakeSieve struct {
	ln      net.Listener
	tlsCfg  *tls.Config
	mu      sync.Mutex
	scripts map[string]string
	active  string
	exts    string
}

func newFakeSieve(t *testing.T) *fakeSieve {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSieve{ln: ln, scripts: map[string]string{}, exts: "fileinto copy imap4flags regex vacation date relational include envelope"}
	f.tlsCfg = selfSigned(t)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return f
}

var rootPool = x509.NewCertPool()

func selfSigned(t *testing.T) *tls.Config {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sieve.test"}, DNSNames: []string{"sieve.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	rootPool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func (f *fakeSieve) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

var litRe = regexp.MustCompile(`\{(\d+)\+?\}$`)

func (f *fakeSieve) handle(nc net.Conn) {
	defer nc.Close()
	r, w := bufio.NewReader(nc), bufio.NewWriter(nc)
	out := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
	caps := func(tlsDone bool) {
		out(`"IMPLEMENTATION" "fake"`)
		f.mu.Lock()
		out(`"SIEVE" "` + f.exts + `"`)
		f.mu.Unlock()
		out(`"SASL" "PLAIN"`)
		if !tlsDone {
			out(`"STARTTLS"`)
		}
		out(`"VERSION" "1.0"`)
		out(`OK "ready"`)
	}
	caps(false)
	authed := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		var lit string
		if m := litRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			buf := make([]byte, n)
			io.ReadFull(r, buf)
			lit = string(buf)
			r.ReadString('\n')
			line = strings.TrimSpace(line[:len(line)-len(m[0])])
		}
		args := quotedArgs(line)
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		if verb != "STARTTLS" && verb != "AUTHENTICATE" && verb != "LOGOUT" && verb != "CAPABILITY" && !authed {
			out(`NO "not authenticated"`)
			continue
		}
		f.mu.Lock()
		switch verb {
		case "STARTTLS":
			out("OK")
			tc := tls.Server(nc, f.tlsCfg)
			if err := tc.Handshake(); err != nil {
				f.mu.Unlock()
				return
			}
			nc, r, w = tc, bufio.NewReader(tc), bufio.NewWriter(tc)
			f.mu.Unlock()
			caps(true)
			continue
		case "CAPABILITY":
			f.mu.Unlock()
			caps(true)
			continue
		case "AUTHENTICATE":
			b, _ := base64.StdEncoding.DecodeString(args[1])
			if string(b) == "\x00anna\x00pw" {
				authed = true
				out("OK")
			} else {
				out(`NO "auth failed"`)
			}
		case "LISTSCRIPTS":
			for n := range f.scripts {
				if n == f.active {
					out(fmt.Sprintf("%q ACTIVE", n))
				} else {
					out(fmt.Sprintf("%q", n))
				}
			}
			out("OK")
		case "GETSCRIPT":
			s, ok := f.scripts[args[0]]
			if !ok {
				out(`NO (NONEXISTENT) "no such script"`)
				break
			}
			out(fmt.Sprintf("{%d}", len(s)))
			w.WriteString(s)
			out("")
			out("OK")
		case "PUTSCRIPT", "CHECKSCRIPT":
			if strings.Contains(lit, "syntaxerror") {
				msg := "line 1: syntax error"
				out(fmt.Sprintf("NO {%d}", len(msg)))
				w.WriteString(msg + "\r\n")
				w.Flush()
				break
			}
			if verb == "PUTSCRIPT" {
				f.scripts[args[0]] = lit
			}
			if strings.Contains(lit, "warnme") {
				out(`OK (WARNINGS) "line 2: something odd"`)
			} else {
				out("OK")
			}
		case "SETACTIVE":
			if _, ok := f.scripts[args[0]]; !ok && args[0] != "" {
				out(`NO (NONEXISTENT) "no such script"`)
				break
			}
			f.active = args[0]
			out("OK")
		case "DELETESCRIPT":
			if args[0] == f.active {
				out(`NO (ACTIVE) "script is active"`)
				break
			}
			delete(f.scripts, args[0])
			out("OK")
		case "LOGOUT":
			out("OK")
			f.mu.Unlock()
			return
		default:
			out(`NO "unknown"`)
		}
		f.mu.Unlock()
	}
}

var qRe = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

func quotedArgs(line string) []string {
	var out []string
	for _, m := range qRe.FindAllStringSubmatch(line, -1) {
		out = append(out, strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(m[1]))
	}
	return append(out, "", "")
}

func (f *fakeSieve) manager(tlsMode domain.TLSMode) *Manager {
	return &Manager{Dial: func(ctx context.Context) (*Client, error) {
		return Dial(ctx, Config{Host: "127.0.0.1", Port: f.port(), TLS: tlsMode, Username: "anna", Password: "pw",
			TLSConfig: &tls.Config{RootCAs: rootPool, ServerName: "sieve.test"}})
	}}
}

// ==== tests ====================================================================

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func testWorkflows() []*workflow.Workflow {
	rule := func(r workflow.Rule) workflow.Condition { return workflow.Condition{Rule: &r} }
	return []*workflow.Workflow{
		{ID: "invoices", Name: "Rechnungen", Enabled: true, Trigger: workflow.Trigger{Event: events.MailReceived},
			Condition: rule(workflow.Rule{All: []workflow.Rule{
				{Field: "mail.fromDomain", Op: "domain_is", Value: "telekom.de"},
				{Any: []workflow.Rule{{Field: "mail.subject", Op: "contains", Value: "Rechnung"}, {Field: "mail.hasAttachments", Op: "is_true"}}},
				{Not: &workflow.Rule{Field: "mail.headers.X-Spam", Op: "eq", Value: "yes"}},
			}}),
			Actions: []workflow.ActionSpec{{Type: "move", Params: map[string]any{"folder": "Rechnungen/Telekom"}},
				{Type: "flag", Params: map[string]any{"add": []any{"seen"}}}}},
		{ID: "big", Name: "Große Mails", Enabled: true, Trigger: workflow.Trigger{Event: events.MailReceived, Folders: []string{"INBOX"}},
			Condition: rule(workflow.Rule{Field: "mail.size", Op: "gt", Value: "10MB"}),
			Actions:   []workflow.ActionSpec{{Type: "label", Params: map[string]any{"add": []any{"Gross"}}}, {Type: "forward", Params: map[string]any{"to": []any{"archiv@example.org"}}}}},
		{ID: "news", Name: "Newsletter", Enabled: true, Trigger: workflow.Trigger{Event: events.MailReceived},
			Condition: rule(workflow.Rule{Field: "mail.from", Op: "ends_with", Value: "@news.example.com"}),
			Actions:   []workflow.ActionSpec{{Type: "delete", Params: map[string]any{"permanent": true}}}},
		{ID: "expr", Name: "Expression", Enabled: true, Trigger: workflow.Trigger{Event: events.MailReceived},
			Condition: workflow.Condition{Expr: "mail.isInvoice"}, Actions: []workflow.ActionSpec{{Type: "move", Params: map[string]any{"folder": "X"}}}},
		{ID: "notify", Name: "Notify", Enabled: true, Trigger: workflow.Trigger{Event: events.MailReceived},
			Actions: []workflow.ActionSpec{{Type: "notify", Params: map[string]any{"text": "x"}}}},
		{ID: "sent", Name: "Sent", Enabled: true, Trigger: workflow.Trigger{Event: events.MailSent},
			Actions: []workflow.ActionSpec{{Type: "move", Params: map[string]any{"folder": "X"}}}},
		{ID: "disabled", Name: "Off", Enabled: false, Trigger: workflow.Trigger{Event: events.MailReceived},
			Actions: []workflow.ActionSpec{{Type: "move", Params: map[string]any{"folder": "X"}}}},
	}
}

func TestGenerateGolden(t *testing.T) {
	res := Generate(testWorkflows(), GenerateOptions{AccountID: 1})
	golden(t, "rules.sieve", res.Script)
	if strings.Join(res.Included, ",") != "big,invoices,news" {
		t.Errorf("included %v", res.Included)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.ID] = s.Reason
	}
	if len(res.Skipped) != 2 || !strings.Contains(reasons["expr"], "expression") || !strings.Contains(reasons["notify"], "notify") {
		t.Errorf("skipped %+v", res.Skipped)
	}
	if r := Parse(res.Script, false); r.Error != "" {
		t.Fatalf("generated script does not parse: %s (line %d)", r.Error, r.ErrorLine)
	}
	// Without the regex extension a regex rule is skipped.
	ws := []*workflow.Workflow{{ID: "re", Name: "re", Enabled: true, Trigger: workflow.Trigger{Event: events.MailReceived},
		Condition: workflow.Condition{Rule: &workflow.Rule{Field: "mail.subject", Op: "matches", Value: "^A"}},
		Actions:   []workflow.ActionSpec{{Type: "move", Params: map[string]any{"folder": "A"}}}}}
	if r := Generate(ws, GenerateOptions{Extensions: []string{"fileinto"}}); len(r.Skipped) != 1 {
		t.Errorf("regex without extension: %+v", r)
	}
}

func TestVacationGolden(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	s := vacation.Settings{Enabled: true, Start: time.Date(2026, 12, 20, 0, 0, 0, 0, loc), End: time.Date(2027, 1, 6, 0, 0, 0, 0, loc),
		Subject: "Nicht im Büro", ExternalText: "Hallo,\nich bin bis zum 6. Januar nicht erreichbar.\n.Punkt am Anfang", OncePerDays: 7,
		Identities: []string{"anna@example.org"}, OwnAddresses: []string{"Anna@Example.org", "a@example.org"}}
	p, err := VacationPart(s, GenerateOptions{}, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), loc)
	if err != nil {
		t.Fatal(err)
	}
	sec := Section{}
	sec.SetPart(PartVacation, p)
	golden(t, "vacation.sieve", sec.Render())
	if r := Parse(sec.Render(), false); r.Error != "" {
		t.Fatal(r.Error)
	}
	if p, _ := VacationPart(vacation.Settings{}, GenerateOptions{}, time.Now(), loc); p.Body != "" {
		t.Fatal("disabled vacation produced a script")
	}
}

func TestReplaceSectionPreservesUserContent(t *testing.T) {
	user := "# my rules\nrequire [\"fileinto\"];\nrequire \"envelope\";\n\nif header :contains \"subject\" \"x\" {\n  fileinto \"X\";\n}\n"
	sec := Section{}
	sec.SetPart(PartRules, Part{Requires: []string{"imap4flags"}, Body: "addflag \"\\\\Seen\";\n"})
	out := ReplaceSection(user, sec)
	if !strings.HasPrefix(out, "# my rules\nrequire [\"fileinto\"];\nrequire \"envelope\";\n"+beginMarker) {
		t.Fatalf("section not after requires:\n%s", out)
	}
	if !strings.HasSuffix(out, "\nif header :contains \"subject\" \"x\" {\n  fileinto \"X\";\n}\n") {
		t.Fatalf("user content changed:\n%s", out)
	}
	// Update in place and parse back.
	parsed := ParseSection(out)
	if parsed.Parts[PartRules].Body != "addflag \"\\\\Seen\";" || parsed.Parts[PartRules].Requires[0] != "imap4flags" {
		t.Fatalf("parsed %+v", parsed)
	}
	sec.SetPart(PartVacation, Part{Requires: []string{"vacation"}, Body: "vacation \"x\";"})
	out2 := ReplaceSection(out, sec)
	if strings.Count(out2, beginMarker) != 1 || !strings.Contains(out2, `require ["imap4flags", "vacation"];`) {
		t.Fatalf("update:\n%s", out2)
	}
	if strings.Index(out2, "part vacation") > strings.Index(out2, "part rules") {
		t.Fatal("vacation must come first")
	}
	if StripSection(out2) != user {
		t.Fatalf("strip:\n%q\nwant\n%q", StripSection(out2), user)
	}
}

func TestParseOutline(t *testing.T) {
	src := "require [\"fileinto\", \"vacation\"];\n# spam\nif header :contains \"x-spam\" \"yes\" {\n  fileinto \"Junk\";\n  stop;\n} elsif size :over 1M { discard; }\nvacation :days 3 text:\nHallo\n.\n;\n"
	r := Parse(src, true)
	if r.Error != "" {
		t.Fatal(r.Error)
	}
	if len(r.Commands) != 4 || r.Commands[1].Name != "if" || r.Commands[1].Comment != "spam" || len(r.Commands[1].Children) != 2 {
		t.Fatalf("%+v", r.Commands)
	}
	if strings.Join(r.Requires, ",") != "fileinto,vacation" || r.Commands[3].Args[2] != "Hallo" {
		t.Fatalf("%+v", r)
	}
	if r := Parse("if true {\n fileinto \"x\";\n", false); r.Error == "" {
		t.Fatal("missing brace not reported")
	}
	if r := Parse("fileinto \"x;\n", false); r.Error == "" || r.ErrorLine != 1 {
		t.Fatal("unterminated string not reported", r)
	}
}

func TestManagerOverManageSieve(t *testing.T) {
	f := newFakeSieve(t)
	m := f.manager(domain.TLSStartTLS)
	ctx := context.Background()

	// No script yet: TuxMail creates and activates its own.
	res, err := m.SyncWorkflows(ctx, testWorkflows(), GenerateOptions{AccountID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Included) != 3 || f.active != DefaultScriptName || !strings.Contains(f.scripts[DefaultScriptName], "fileinto \"Rechnungen/Telekom\"") {
		t.Fatalf("active=%q scripts=%v", f.active, f.scripts)
	}
	// The user activates an own script: the section is inserted there.
	user := "require \"fileinto\";\nif header :is \"from\" \"boss@x\" { fileinto \"Boss\"; }\n"
	if err := m.Put(ctx, "mine", user, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SyncWorkflows(ctx, testWorkflows()[:1], GenerateOptions{AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	vs := &VacationSync{M: m, Loc: time.UTC, Now: func() time.Time { return time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) }}
	if !vs.Supported() {
		t.Fatal("vacation unsupported")
	}
	if err := vs.Push(ctx, vacation.Settings{Enabled: true, ExternalText: "Bin weg"}); err != nil {
		t.Fatal(err)
	}
	mine := f.scripts["mine"]
	if !strings.Contains(mine, "if header :is \"from\" \"boss@x\" { fileinto \"Boss\"; }") || !strings.Contains(mine, "vacation :days 4 \"Bin weg\";") ||
		!strings.Contains(mine, "workflow invoices") || strings.Contains(mine, "workflow big") {
		t.Fatalf("mine:\n%s", mine)
	}
	// Switching vacation off and removing workflows restores the user's script.
	if err := vs.Push(ctx, vacation.Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveWorkflows(ctx); err != nil {
		t.Fatal(err)
	}
	if f.scripts["mine"] != user {
		t.Fatalf("not restored:\n%s", f.scripts["mine"])
	}

	// Raw editor.
	list, err := m.Scripts(ctx)
	if err != nil || len(list) != 2 {
		t.Fatal(err, list)
	}
	if _, err := m.Check(ctx, "syntaxerror"); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("check: %v", err)
	}
	if w, err := m.Check(ctx, "keep; # warnme"); err != nil || !strings.Contains(w, "odd") {
		t.Fatal(w, err)
	}
	if err := m.Delete(ctx, "mine"); err == nil {
		t.Fatal("deleted the active script")
	}
	if err := m.Activate(ctx, DefaultScriptName); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "mine"); err != nil {
		t.Fatal(err)
	}
	if src, err := m.Script(ctx, DefaultScriptName); err != nil || !strings.Contains(src, beginMarker) {
		t.Fatal(err, src)
	}
}

func TestSeparateIncludeStrategy(t *testing.T) {
	f := newFakeSieve(t)
	m := f.manager(domain.TLSStartTLS)
	m.Separate = true
	ctx := context.Background()
	if err := m.Put(ctx, "mine", "keep;\n", true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SyncWorkflows(ctx, testWorkflows(), GenerateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.scripts["mine"], `include :personal "tuxmail";`) || !strings.HasSuffix(f.scripts["mine"], "keep;\n") {
		t.Fatalf("mine:\n%s", f.scripts["mine"])
	}
	if !strings.Contains(f.scripts[DefaultScriptName], "workflow news") || f.active != "mine" {
		t.Fatal(f.scripts, f.active)
	}
}

func TestDialFailuresAndDiscover(t *testing.T) {
	f := newFakeSieve(t)
	ctx := context.Background()
	if _, err := Dial(ctx, Config{Host: "127.0.0.1", Port: f.port(), TLS: domain.TLSStartTLS, Username: "anna", Password: "no",
		TLSConfig: &tls.Config{RootCAs: rootPool, ServerName: "sieve.test"}}); err == nil {
		t.Fatal("bad password accepted")
	}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, f.ln.Addr().String())
	}
	d, err := Discover(ctx, dial, "imap.example.org")
	if err != nil || !d.STARTTLS || d.Port != 4190 || len(d.Extensions) < 5 {
		t.Fatal(err, d)
	}
	cfg, ok := ConfigFor(domain.Account{Incoming: &domain.ServerConfig{Host: "imap.example.org", Username: "anna", TLS: domain.TLSImplicit}})
	if !ok || cfg.Port != 4190 || cfg.TLS != domain.TLSStartTLS {
		t.Fatal(cfg)
	}
}
