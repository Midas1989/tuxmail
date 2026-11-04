// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

import "golang.org/x/sys/windows"

// backdropSupported reports whether DWM system backdrops (Mica) are
// available: Windows 11 22H2 (build 22621) or later. Older systems keep an
// opaque window (no translucency, which would show the desktop through).
func backdropSupported() bool {
	v := windows.RtlGetVersion()
	return v.MajorVersion > 10 || (v.MajorVersion == 10 && v.BuildNumber >= 22621)
}
