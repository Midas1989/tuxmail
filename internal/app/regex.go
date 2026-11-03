// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import "regexp"

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	rePhone = regexp.MustCompile(`\+?\d[\d /-]{7,}\d`)
)
