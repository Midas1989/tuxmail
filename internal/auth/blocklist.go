// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// The list of common passwords (Basis spec/PARAMETER.md; source and licence in
// data/README.md, same file as the Basis). It is only checked for new passwords:
// who already has such a password can still sign in.
//
//go:embed data/passwords-blocklist.txt.gz
var blocklistGz []byte

var (
	blocklistOnce sync.Once
	blocklist     []string
	blocklistErr  error
)

// normalizeForBlocklist is Basis normalize_for_blocklist: NFKC, case folded,
// surrounding white space removed.
func normalizeForBlocklist(password string) string {
	return strings.TrimSpace(cases.Fold().String(norm.NFKC.String(password)))
}

func loadBlocklist() {
	gz, err := gzip.NewReader(bytes.NewReader(blocklistGz))
	if err != nil {
		blocklistErr = fmt.Errorf("auth: password blocklist: %w", err)
		return
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		blocklistErr = fmt.Errorf("auth: password blocklist: %w", err)
		return
	}
	lines := strings.Split(string(raw), "\n")
	out := lines[:0]
	for _, l := range lines {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	blocklist = out
}

// passwordBlocklisted reports whether password is on the list. The list is loaded
// when first needed and stays in memory (about 2 MiB).
func passwordBlocklisted(password string) (bool, error) {
	blocklistOnce.Do(loadBlocklist)
	if blocklistErr != nil {
		return false, blocklistErr
	}
	n := normalizeForBlocklist(password)
	i := sort.SearchStrings(blocklist, n)
	return i < len(blocklist) && blocklist[i] == n, nil
}
