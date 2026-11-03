// SPDX-License-Identifier: AGPL-3.0-or-later

// Package locales embeds the language files (KONZEPT 16.4) shared by the
// frontend and the Go core.
package locales

import "embed"

// FS holds de.json, en.json, ... at its root.
//
//go:embed *.json
var FS embed.FS
