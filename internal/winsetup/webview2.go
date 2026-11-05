// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Trust decisions for the WebView2 bootstrapper, which the setup runs
// (often elevated). Plain functions so they can be tested on any OS; the
// Windows calls are in sys_windows.go and authenticode_windows.go
// (AUDIT-D D-13, re-audit ND-04).

// WebView2BootstrapperURL is Microsoft's Evergreen bootstrapper.
const WebView2BootstrapperURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

// WebView2PublisherName is the signer of Microsoft's bootstrapper.
const WebView2PublisherName = "Microsoft Corporation"

// WebView2PinFile is the name of the file next to the embedded bootstrapper
// in the setup's payload that holds the SHA-256 (hex) of the bootstrapper
// bundled by build/windows/package-setup.sh.
const WebView2PinFile = "MicrosoftEdgeWebview2Setup.exe.sha256"

// TrustedPublisher reports whether the signer name of the bootstrapper's
// Authenticode signature (the simple display name of the signer certificate)
// is Microsoft's.
func TrustedPublisher(name string) bool {
	return strings.TrimSpace(name) == WebView2PublisherName
}

// bootstrapperHost reports whether host may serve the bootstrapper: the
// fwlink redirector and Microsoft's download hosts.
func bootstrapperHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "aka.ms" || h == "microsoft.com" || strings.HasSuffix(h, ".microsoft.com")
}

// CheckBootstrapperURL accepts https URLs on Microsoft hosts only (the
// initial URL and every redirect target).
func CheckBootstrapperURL(u *url.URL) error {
	if u == nil || u.Scheme != "https" {
		return errors.New("only https is allowed for the WebView2 download")
	}
	if u.User != nil {
		return errors.New("credentials in the WebView2 download URL")
	}
	if !bootstrapperHost(u.Hostname()) {
		return fmt.Errorf("the WebView2 download redirects to %q, which is not a Microsoft host", u.Hostname())
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("unexpected port %s in the WebView2 download URL", p)
	}
	return nil
}

// CheckBootstrapperRedirect is the http.Client.CheckRedirect of the
// download: at most 5 hops, each https on a Microsoft host.
func CheckBootstrapperRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	return CheckBootstrapperURL(req.URL)
}

// VerifySHA256 compares data with the hex SHA-256 digest want (the pin of
// the bundled bootstrapper).
func VerifySHA256(data []byte, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	// "<hex>  name" lines (sha256sum format) are accepted.
	if f := strings.Fields(want); len(f) > 0 {
		want = f[0]
	}
	if len(want) != 64 {
		return errors.New("missing or malformed SHA-256 pin")
	}
	if _, err := hex.DecodeString(want); err != nil {
		return errors.New("malformed SHA-256 pin")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return fmt.Errorf("SHA-256 mismatch (got %s, pinned %s)", got, want)
	}
	return nil
}
