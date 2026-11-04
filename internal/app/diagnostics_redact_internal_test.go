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
