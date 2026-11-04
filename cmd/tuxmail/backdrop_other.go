// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

// backdropSupported: system backdrops exist only on Windows 11.
func backdropSupported() bool { return false }
