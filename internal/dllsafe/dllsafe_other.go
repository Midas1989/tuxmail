// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package dllsafe

// Err is always nil outside Windows.
var Err error
