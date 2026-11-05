// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix && !windows

package host

// checkTrusted: platforms other than Unix and Windows (Plan 9, js ...) do
// not host plugins in practice; there is no ownership model to check.
// Windows: trust_windows.go. Unix: trust_unix.go.
func checkTrusted(string) error { return nil }
