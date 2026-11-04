// SPDX-License-Identifier: AGPL-3.0-or-later

package oauth

import (
	"context"
	"testing"
)

// AUDIT-C: the browser opener must not start local programs, and a tenant
// must not change the host or path of the endpoints.
func TestOpenBrowserRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{`C:\Windows\System32\calc.exe`, "file:///etc/passwd", "javascript:alert(1)", "--help", "smb://host/share", ""} {
		if err := OpenBrowser(context.Background(), u); err == nil {
			t.Errorf("OpenBrowser(%q) accepted", u)
		}
	}
}

func TestTenantValidated(t *testing.T) {
	for _, tenant := range []string{"evil.example/x", "a?b", "a#b", "x@evil.example", "..", "a/../b"} {
		if _, err := endpoint(Microsoft, tenant); err == nil {
			t.Errorf("tenant %q accepted", tenant)
		}
	}
	for _, tenant := range []string{"", "common", "organizations", "contoso.onmicrosoft.com", "72f988bf-86f1-41af-91ab-2d7cd011db47"} {
		if _, err := endpoint(Microsoft, tenant); err != nil {
			t.Errorf("tenant %q: %v", tenant, err)
		}
	}
}
