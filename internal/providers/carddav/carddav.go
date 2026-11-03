// SPDX-License-Identifier: AGPL-3.0-or-later

// Package carddav is TuxMail's CardDAV client (RFC 6352, RFC 6764) on top
// of internal/providers/dav: discovery of the address book home, listing,
// sync (sync-collection with ETag fallback) and conditional writes.
package carddav

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/Midas1989/Mailclient/internal/providers/dav"
)

// ContentType of address objects.
const ContentType = "text/vcard; charset=utf-8"

// Account is the result of discovery.
type Account struct {
	Principal    string
	Home         string
	AddressBooks []dav.Collection
}

// Discover finds the address books reachable from start.
func Discover(ctx context.Context, c *dav.Client, start string) (*Account, error) {
	principal, err := c.Principal(ctx, start, "carddav")
	if err != nil {
		return nil, err
	}
	acc := &Account{Principal: principal}
	acc.Home, _ = c.HomeSet(ctx, principal, dav.NSCardDAV, "addressbook-home-set")
	home := acc.Home
	if home == "" {
		home = principal
	}
	cols, err := c.ListCollections(ctx, home)
	if err != nil {
		return nil, err
	}
	for _, col := range cols {
		if col.AddressBook {
			acc.AddressBooks = append(acc.AddressBooks, col)
		}
	}
	if len(acc.AddressBooks) == 0 && acc.Home == "" {
		return nil, errors.New("carddav: no address book home or address books found")
	}
	return acc, nil
}

// Pull returns the changes of an address book with the card data.
func Pull(ctx context.Context, c *dav.Client, col, token string, useSync bool, local map[string]string) (*dav.Delta, error) {
	return c.Pull(ctx, col, token, useSync, local, dav.NSCardDAV, "addressbook-multiget", "address-data")
}

// ObjectURL returns the URL for a new card.
func ObjectURL(collection, uid string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, uid)
	if !strings.HasSuffix(collection, "/") {
		collection += "/"
	}
	return dav.Resolve(collection, url.PathEscape(name)+".vcf")
}

// Put stores a card ("" etag creates it).
func Put(ctx context.Context, c *dav.Client, href string, data []byte, etag string) (string, error) {
	newTag, err := c.Put(ctx, href, ContentType, data, etag, etag == "")
	if err != nil {
		return "", err
	}
	if newTag == "" {
		newTag, _ = c.ETagOf(ctx, href)
	}
	return newTag, nil
}
