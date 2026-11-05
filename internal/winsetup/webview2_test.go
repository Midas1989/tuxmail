// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Re-audit ND-04: the bootstrapper is only fetched over https from
// Microsoft hosts, across every redirect hop.
func TestCheckBootstrapperURL(t *testing.T) {
	for _, ok := range []string{
		WebView2BootstrapperURL,
		"https://msedge.sf.dl.delivery.mp.microsoft.com/filestreamingservice/files/x/MicrosoftEdgeWebview2Setup.exe",
		"https://aka.ms/webview2",
		"https://go.microsoft.com:443/fwlink/?LinkId=2124703",
	} {
		if err := CheckBootstrapperURL(mustURL(t, ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://go.microsoft.com/fwlink/p/?LinkId=2124703", // downgrade
		"https://evil.example/MicrosoftEdgeWebview2Setup.exe",
		"https://microsoft.com.evil.example/x",
		"https://evilmicrosoft.com/x",
		"https://go.microsoft.com@evil.example/x",
		"https://user:pw@go.microsoft.com/x",
		"https://go.microsoft.com:8443/x",
		"ftp://go.microsoft.com/x",
		"file:///C:/x.exe",
	} {
		if err := CheckBootstrapperURL(mustURL(t, bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	// Redirect policy: scheme/host per hop and the hop limit.
	req := &http.Request{URL: mustURL(t, "http://msedge.sf.dl.delivery.mp.microsoft.com/x")}
	if err := CheckBootstrapperRedirect(req, nil); err == nil {
		t.Error("redirect to http accepted")
	}
	req = &http.Request{URL: mustURL(t, "https://download.microsoft.com/x")}
	if err := CheckBootstrapperRedirect(req, make([]*http.Request, 4)); err != nil {
		t.Error(err)
	}
	if err := CheckBootstrapperRedirect(req, make([]*http.Request, 5)); err == nil {
		t.Error("redirect chain not limited")
	}
}

func TestTrustedPublisher(t *testing.T) {
	if !TrustedPublisher("Microsoft Corporation") || !TrustedPublisher(" Microsoft Corporation ") {
		t.Error("Microsoft not trusted")
	}
	for _, n := range []string{"", "Microsoft", "Microsoft Corporation Ltd", "microsoft corporation", "Not Microsoft Corporation", "Microsoft Corporation\x00evil"} {
		if TrustedPublisher(n) {
			t.Errorf("%q trusted", n)
		}
	}
}

func TestVerifySHA256(t *testing.T) {
	data := []byte("bootstrapper")
	sum := sha256.Sum256(data)
	pin := hex.EncodeToString(sum[:])
	if err := VerifySHA256(data, pin); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(data, pin+"  MicrosoftEdgeWebview2Setup.exe\n"); err != nil {
		t.Fatal("sha256sum format:", err)
	}
	if err := VerifySHA256([]byte("tampered"), pin); err == nil {
		t.Error("tampered data accepted")
	}
	for _, bad := range []string{"", "abc", "zz" + pin[2:]} {
		if err := VerifySHA256(data, bad); err == nil {
			t.Errorf("pin %q accepted", bad)
		}
	}
}
