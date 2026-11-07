// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package server

func diskFreePercent(string) (int, bool) { return 0, false }
