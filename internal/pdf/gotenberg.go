// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// GotenbergURL returns TUXMAIL_GOTENBERG_URL (server deployments run a
// Gotenberg container next to TuxMail, KONZEPT 8.11), "" if unset.
func GotenbergURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("TUXMAIL_GOTENBERG_URL")), "/")
}

// GotenbergToPDF converts an HTML document with Gotenberg
// (POST {base}/forms/chromium/convert/html, file "index.html").
func GotenbergToPDF(ctx context.Context, client *http.Client, base string, html []byte) ([]byte, error) {
	if base == "" {
		return nil, fmt.Errorf("pdf: no Gotenberg URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(html); err != nil {
		return nil, err
	}
	_ = mw.WriteField("printBackground", "true")
	_ = mw.WriteField("preferCssPageSize", "true")
	if err := mw.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/forms/chromium/convert/html", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pdf: Gotenberg: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(data, []byte("%PDF")) {
		msg := string(data)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("pdf: Gotenberg: HTTP %d: %s", resp.StatusCode, msg)
	}
	return data, nil
}
