// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cloud defines the storage and sharing abstractions of TuxMail's
// cloud integration (KONZEPT 12): Nextcloud, OpenCloud/oCIS and generic
// WebDAV implement Storage (and Sharer), Vaultwarden/Bitwarden Send only
// shares files. The providers live in the sub-packages webdav, nextcloud,
// opencloud and vaultwarden; internal/app wires them to accounts.
//
// Paths are slash-separated and absolute ("/Documents/a.pdf"); "/" is the
// root of the provider (for OpenCloud the list of spaces).
package cloud

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"path"
	"strings"
	"time"
)

// Errors shared by all providers.
var (
	ErrNotFound     = errors.New("cloud: not found")
	ErrExists       = errors.New("cloud: already exists")
	ErrUnauthorized = errors.New("cloud: authentication failed")
	ErrForbidden    = errors.New("cloud: permission denied")
	ErrInsufficient = errors.New("cloud: insufficient storage")
	ErrUnsupported  = errors.New("cloud: not supported by this provider")
)

// Entry is a file or directory.
type Entry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Dir         bool      `json:"dir"`
	Size        int64     `json:"size"`
	Modified    time.Time `json:"modified,omitempty"`
	ContentType string    `json:"contentType,omitempty"`
	ETag        string    `json:"etag,omitempty"`
	// ID is the provider's file id (Nextcloud oc:fileid, OpenCloud item id).
	ID string `json:"id,omitempty"`
}

// Quota of the storage; -1 means unknown/unlimited.
type Quota struct {
	Used      int64 `json:"used"`
	Available int64 `json:"available"`
}

// Storage is a browsable file store (KONZEPT 4.3 StorageProvider).
type Storage interface {
	List(ctx context.Context, dir string) ([]Entry, error)
	Stat(ctx context.Context, p string) (Entry, error)
	Download(ctx context.Context, p string) (io.ReadCloser, Entry, error)
	// Upload stores r at p (overwriting); size may be -1 when unknown.
	Upload(ctx context.Context, p string, r io.Reader, size int64) (Entry, error)
	Mkdir(ctx context.Context, p string) error
	Move(ctx context.Context, from, to string) error
	Delete(ctx context.Context, p string) error
	Quota(ctx context.Context) (Quota, error)
}

// ShareOptions configure a public link.
type ShareOptions struct {
	Password string
	Expires  time.Time // zero = no expiry (or the server's default)
	// Upload allows uploads into a shared folder (default read-only).
	Upload bool
	Note   string
	Label  string
	// Vaultwarden Send only.
	MaxAccessCount int
	HideEmail      bool
	DeletionDate   time.Time
}

// Link is a created share link.
type Link struct {
	ID       string    `json:"id"` // provider share id (for revoking)
	URL      string    `json:"url"`
	Password string    `json:"password,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	Path     string    `json:"path,omitempty"`
}

// Sharer creates and revokes public links.
type Sharer interface {
	Share(ctx context.Context, p string, opts ShareOptions) (Link, error)
	Revoke(ctx context.Context, id string) error
}

// PolicyProvider reports the server's password policy for share passwords.
type PolicyProvider interface {
	PasswordPolicy(ctx context.Context) (PasswordPolicy, error)
}

// ==== paths ===================================================================

// Clean normalizes a path to the absolute slash form ("/" for empty).
func Clean(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return path.Clean("/" + p)
}

// Join joins path elements and cleans the result.
func Join(elem ...string) string { return Clean(path.Join(elem...)) }

// SafeName replaces characters that are invalid in file names on common
// servers and file systems and trims the result to 120 runes.
func SafeName(s string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "", "?", "", "\"", "'", "<", "", ">", "", "|", "-", "\x00", "")
	s = strings.TrimSpace(r.Replace(s))
	s = strings.Trim(s, ". ")
	if rs := []rune(s); len(rs) > 120 {
		s = strings.TrimSpace(string(rs[:120]))
	}
	return s
}

// MkdirAll creates p and all missing parents.
func MkdirAll(ctx context.Context, s Storage, p string) error {
	p = Clean(p)
	if p == "/" {
		return nil
	}
	if e, err := s.Stat(ctx, p); err == nil {
		if !e.Dir {
			return fmt.Errorf("cloud: %s is a file: %w", p, ErrExists)
		}
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := MkdirAll(ctx, s, path.Dir(p)); err != nil {
		return err
	}
	if err := s.Mkdir(ctx, p); err != nil && !errors.Is(err, ErrExists) {
		return err
	}
	return nil
}

// UniquePath returns p, or "name (2).ext", "name (3).ext" ... if p exists.
func UniquePath(ctx context.Context, s Storage, p string) (string, error) {
	p = Clean(p)
	ext := path.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; i < 1000; i++ {
		cand := p
		if i > 1 {
			cand = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		_, err := s.Stat(ctx, cand)
		if errors.Is(err, ErrNotFound) {
			return cand, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", ErrExists
}

// StatusError maps an HTTP status to the shared errors.
func StatusError(op string, status int, body string) error {
	var base error
	switch status {
	case http.StatusNotFound:
		base = ErrNotFound
	case http.StatusUnauthorized:
		base = ErrUnauthorized
	case http.StatusForbidden:
		base = ErrForbidden
	case http.StatusInsufficientStorage:
		base = ErrInsufficient
	case http.StatusMethodNotAllowed, http.StatusPreconditionFailed:
		base = ErrExists
	default:
		body = strings.TrimSpace(body)
		if len(body) > 200 {
			body = body[:200]
		}
		return fmt.Errorf("cloud: %s: HTTP %d %s", op, status, body)
	}
	return fmt.Errorf("cloud: %s: %w", op, base)
}

// ==== passwords ===============================================================

// PasswordPolicy describes the requirements for generated share passwords
// (Nextcloud password_policy capability).
type PasswordPolicy struct {
	MinLength    int    `json:"minLength"`
	Upper        bool   `json:"upper"`
	Lower        bool   `json:"lower"`
	Digit        bool   `json:"digit"`
	Special      bool   `json:"special"`
	SpecialChars string `json:"specialChars,omitempty"`
	// Enforced: links must have a password.
	Enforced bool `json:"enforced,omitempty"`
}

// DefaultPasswordLength is used when the policy demands less (KONZEPT 12.2).
const DefaultPasswordLength = 16

const (
	lowerChars = "abcdefghijkmnopqrstuvwxyz" // no l
	upperChars = "ABCDEFGHJKLMNPQRSTUVWXYZ"  // no I, O
	digitChars = "23456789"                  // no 0, 1
	// Specials that survive copying from mails and URLs.
	defaultSpecials = "#+-_!?%=@*"
)

// GeneratePassword returns a random password that satisfies the policy; it
// always contains lower, upper case letters, digits and special characters,
// avoiding ambiguous characters.
func GeneratePassword(p PasswordPolicy) string {
	n := p.MinLength
	if n < DefaultPasswordLength {
		n = DefaultPasswordLength
	}
	specials := p.SpecialChars
	if specials == "" {
		specials = defaultSpecials
	}
	classes := []string{lowerChars, upperChars, digitChars, specials}
	out := make([]byte, 0, n)
	for _, c := range classes {
		out = append(out, c[randInt(len(c))])
	}
	all := strings.Join(classes, "")
	for len(out) < n {
		out = append(out, all[randInt(len(all))])
	}
	for i := len(out) - 1; i > 0; i-- { // Fisher-Yates
		j := randInt(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("cloud: crypto/rand failed: " + err.Error())
	}
	return int(v.Int64())
}
