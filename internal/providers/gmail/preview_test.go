// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"testing"
)

// Gmail's snippet (HTML-escaped) is the preview of mails kept with header only.
func TestSyncDeliversSnippet(t *testing.T) {
	srv, p := newProv(t)
	srv.AddMessage("From: a@example.org\r\nSubject: x\r\nMessage-ID: <s@x>\r\n\r\nTom & Jerry <3 \"quoted\"\r\n", "INBOX")
	d, _, err := p.Sync(context.Background(), "INBOX", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 || d.Added[0].Preview != `Tom & Jerry <3 "quoted"` {
		t.Fatalf("preview = %+v", d.Added)
	}
}
