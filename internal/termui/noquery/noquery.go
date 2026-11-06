// SPDX-License-Identifier: AGPL-3.0-or-later

// Package noquery stops the terminal query that bubbletea v1 sends from its
// package init (lipgloss.HasDarkBackground). Without this every start of the
// binary on a terminal, also `tuxmail version` or `tuxmail serve`, would write
// an escape sequence and could wait seconds for an answer.
//
// It works through the specified package initialization order (Go 1.21+): the
// ready package with the smallest import path goes first, and this path
// ("github.com/Midas1989/...") sorts before "github.com/charmbracelet/bubbletea".
package noquery

import "github.com/charmbracelet/lipgloss"

func init() { lipgloss.SetHasDarkBackground(true) }
