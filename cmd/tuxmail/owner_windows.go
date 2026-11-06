// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

// dataOwner: per-user data with NTFS rights, no root-like caller to guard.
func dataOwner(string) (int, bool) { return 0, false }
