// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package host

// checkTrusted: on Windows the plugins live in the installation folder,
// whose ACL the setup protects (Program Files or a protected DACL for other
// all-users folders, AUDIT-D D-05); per-user installations are only
// writable by that user.
func checkTrusted(string) error { return nil }
