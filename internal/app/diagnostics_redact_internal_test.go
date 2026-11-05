// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"strings"
	"testing"
)

// AUDIT-C: diagnostics exports must not contain credentials in any of the
// usual spellings.
func TestRedactTextSecrets(t *testing.T) {
	secrets := []string{"S3cr3tValue1", "ya29.A0ARrdaM-tok", "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJl", "AbCdEf123", "sk-proj-123456"}
	for _, line := range []string{
		`level=WARN msg="login failed" password=S3cr3tValue1`,
		`Authorization: Bearer ya29.A0ARrdaM-tok`,
		`authorization="Basic AbCdEf123"`,
		`{"access_token":"ya29.A0ARrdaM-tok","refresh_token":"S3cr3tValue1"}`,
		`client_secret=S3cr3tValue1&code=AbCdEf123&state=x`,
		`GET https://host/cb?code=AbCdEf123&state=1`,
		`device_code: AbCdEf123`,
		`x-api-key: sk-proj-123456`,
		`api_key=sk-proj-123456`,
		`Cookie: sessionid=S3cr3tValue1`,
		`token eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJl in header`,
		`dial https://anna:S3cr3tValue1@dav.example.org/remote.php`,
		`"passphrase": "S3cr3tValue1"`,
	} {
		out := redactText(line)
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("redactText(%q) = %q still contains %q", line, out, s)
			}
		}
	}
	if got := redactText("synced 12 messages in 3s, status_code=200"); got != "synced 12 messages in 3s, status_code=200" {
		t.Errorf("over-redaction: %q", got)
	}
}

// NC-05: protocol lines without a key/value separator.
func TestRedactTextProtocolSecrets(t *testing.T) {
	pgp := "-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQVYBFxxxxxSECRETKEYBYTES\nabcdefghij\n-----END PGP PRIVATE KEY BLOCK-----"
	for _, tc := range []struct{ line, secret string }{
		{`imap: C: a001 LOGIN "anna@example.org" "S3cr3tValue1"`, "S3cr3tValue1"},
		{`imap: C: a001 LOGIN anna S3cr3tValue1`, "S3cr3tValue1"},
		{`imap: C: a002 LOGIN "anna" "pa\"S3cr3tValue1"`, "S3cr3tValue1"},
		{`smtp: C: AUTH PLAIN AGFubmEAUzNjcjN0VmFsdWUx`, "AGFubmEAUzNjcjN0VmFsdWUx"},
		{`smtp: C: AUTH LOGIN YW5uYQ==`, "YW5uYQ=="},
		{`smtp: C: AUTH XOAUTH2 dXNlcj1hbm5hAWF1dGg9QmVhcmVyIHlhMjkuQTBBUnJkYU0tdG9rAQE=`, "dXNlcj1hbm5hAWF1dGg9QmVhcmVyIHlhMjkuQTBBUnJkYU0tdG9rAQE="},
		{`imap: C: a003 AUTHENTICATE XOAUTH2 dXNlcj1hbm5hAWF1dGg9QmVhcmVyIHlhMjkuQTBBUnJkYU0tdG9rAQE=`, "dXNlcj1hbm5hAWF1dGg9"},
		{"level=DEBUG msg=" + pgp + " next=1", "SECRETKEYBYTES"},
		{"level=DEBUG msg=\"-----BEGIN PGP PRIVATE KEY BLOCK-----\\nlQVYBFSECRETKEYBYTES\\n-----END PGP PRIVATE KEY BLOCK-----\" x=1", "SECRETKEYBYTES"},
		{"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAASECRETKEYBYTES", "SECRETKEYBYTES"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEASECRETKEYBYTES\n-----END RSA PRIVATE KEY-----", "SECRETKEYBYTES"},
	} {
		out := redactText(tc.line)
		if strings.Contains(out, tc.secret) {
			t.Errorf("redactText(%q) = %q still contains %q", tc.line, out, tc.secret)
		}
	}
	// Surrounding text and ordinary lines are kept.
	if got := redactText("before " + "-----BEGIN PGP PRIVATE KEY BLOCK-----\nAAAA\n-----END PGP PRIVATE KEY BLOCK-----" + " after"); got != "before [redacted-private-key] after" {
		t.Errorf("block: %q", got)
	}
	for _, line := range []string{
		"login failed for user anna",
		"imap: S: a001 OK LOGIN completed",
		"smtp: auth plain not offered",
		"-----BEGIN PGP PUBLIC KEY BLOCK-----\nmQENB\n-----END PGP PUBLIC KEY BLOCK-----",
		"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
	} {
		if got := redactText(line); got != line {
			t.Errorf("over-redaction: %q -> %q", line, got)
		}
	}
}
