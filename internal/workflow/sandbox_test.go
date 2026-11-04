// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// AUDIT-C: condition expressions must not be able to exhaust memory or CPU,
// and must not reach the file system, processes or reflection.
func TestSandboxRejectsResourceExhaustion(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	env := &Env{Mail: MailEnv{Body: big, To: []string{"a@x", "b@y"}}}
	for _, src := range []string{
		`len(repeat(repeat("x", 100000), 100000)) > 0`,
		`len(reduce(1..64, #acc + #acc, "x")) > 0`,
		`len(join(map(1..5000, {mail.body}), "")) > 0`,
		`len(join(map(1..4000, {mail.body + "y"}))) > 0`,
		`all(1..1000, {all(1..1000, {# > 0})})`,
		`len(map(1..100000000, {#})) > 0`,
		`let a = mail.body + mail.body; let b = a + a; let c = b + b; let d = c + c; let e = d + d; len(e + e) > 0`,
		`len(string(map(1..4000, {mail.body}))) > 0`,
	} {
		p, err := compileExpr(src)
		if err != nil {
			continue // rejected at compile time: fine
		}
		done := make(chan error, 1)
		go func() { _, err := p.Eval(env); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: evaluated without hitting a limit", src)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: still running after 10s", src)
		}
	}
}

func TestSandboxNoDangerousBuiltins(t *testing.T) {
	for _, src := range []string{
		`toJSON(mail) != ""`,
		`fromJSON("{}") != nil`,
		`toBase64(mail.body) != ""`,
		`os.Getenv("HOME") != ""`,
		`exec("id") != ""`,
		`$env.WithBackends(nil, nil, nil) != nil`,
		`readFile("/etc/passwd") != ""`,
	} {
		if p, err := compileExpr(src); err == nil {
			if _, err := p.Eval(&Env{}); err == nil {
				t.Errorf("%s: compiled and evaluated", src)
			}
		}
	}
}

// AUDIT-C: mail content reached through field access or helpers must not
// add directories or make a path template absolute.
func TestRenderPathSanitizesFieldAccess(t *testing.T) {
	env := &Env{Mail: MailEnv{Subject: "/etc/../../x/y", FromName: `..\..\evil`, Size: 7}}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for tpl, want := range map[string]string{
		`{{.Mail.Subject}}.pdf`:                        `_etc_.._.._x_y.pdf`,
		`Archiv/{{lower .Mail.Subject}}`:               `Archiv/_etc_.._.._x_y`,
		`A/{{replace "_" "/" betreff}}`:                `A/_etc_.._.._x_y`,
		`A/{{absender}}/{{.Mail.Size}}`:                `A/_.._evil/7`,
		`/Archiv/{{jahr}}/{{datum}}_{{betreff}}`:       `/Archiv/2026/2026-01-02__etc_.._.._x_y`,
		`{{if .Mail.Subject}}{{.Mail.Subject}}{{end}}`: `_etc_.._.._x_y`,
	} {
		got, err := RenderPath(env, nil, now, tpl)
		if err != nil || got != want {
			t.Errorf("RenderPath(%q) = %q, %v; want %q", tpl, got, err, want)
		}
	}
}

// AUDIT-C: CR/LF from mail content must not split webhook headers.
func TestWebhookHeaderCRLF(t *testing.T) {
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() }))
	defer srv.Close()
	act, err := newWebhookAction(map[string]any{"url": srv.URL, "headers": map[string]any{"X-Subject": "{{betreff}}"}})
	if err != nil {
		t.Fatal(err)
	}
	rc := &RunContext{Env: &Env{Mail: MailEnv{Subject: "hi\r\nX-Injected: 1\r\n\r\nbody"}}, Vars: map[string]string{}, Now: time.Now()}
	if _, err := act.Execute(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	h := <-got
	if h.Get("X-Injected") != "" || strings.ContainsAny(h.Get("X-Subject"), "\r\n") || !strings.HasPrefix(h.Get("X-Subject"), "hi") {
		t.Fatalf("header injection: %v", h)
	}
}

func TestMaxDepthIsCapped(t *testing.T) {
	var o Options
	o.LoopProtection.MaxDepth = 1 << 30
	if o.maxDepth() != MaxAllowedDepth {
		t.Fatalf("maxDepth = %d", o.maxDepth())
	}
}

func TestSandboxKeepsSemantics(t *testing.T) {
	env := &Env{Mail: MailEnv{Subject: "Rechnung 42", To: []string{"a@x.de", "b@y.de"}, Size: 3000}}
	for _, src := range []string{
		`"a" + "b" == "ab"`,
		`1 + 2 == 3`,
		`mail.size + 1 == 3001`,
		`mail.subject + "!" == "Rechnung 42!"`,
		`join(mail.to, ",") == "a@x.de,b@y.de"`,
		`join(map(mail.to, {domain(#)})) == "x.dey.de"`,
		`repeat("ab", 2) == "abab"`,
		`string(42) == "42"`,
		`reduce(1..4, #acc + #, 0) == 10`,
		`reduce(["a", "b"], #acc + #, "") == "ab"`,
		`all(mail.to, {# contains "@"})`,
		`len(filter(mail.to, {# endsWith ".de"})) == 2`,
		`time.now + duration("1h") > time.now`,
	} {
		p, err := compileExpr(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		ok, err := p.Eval(env)
		if err != nil || !ok {
			t.Errorf("%s = %v, %v", src, ok, err)
		}
	}
	// The budget is per evaluation.
	p, err := compileExpr(`all(1..1000, {# > 0})`)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		if ok, err := p.Eval(env); err != nil || !ok {
			t.Fatalf("repeated eval: %v %v", ok, err)
		}
	}
}
