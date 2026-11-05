// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import "net/http"

// maxBulkIDs caps the number of ids (messages, items, tags, contacts) one
// bulk request may name. The body limit of the HTTP API alone allowed several
// hundred thousand ids, each of which became a sync operation or a store
// round trip (audit NH-07). The UI works in chunks below this size.
const maxBulkIDs = 5000

// CodeTooManyItems is the error code of a bulk request above maxBulkIDs.
const CodeTooManyItems = "too_many_items"

// checkBulk fails with a 413 when a bulk request names more than maxBulkIDs.
func checkBulk(n int) error {
	if n > maxBulkIDs {
		return Errorf(http.StatusRequestEntityTooLarge, CodeTooManyItems, "too many items in one request (%d, at most %d); split the request", n, maxBulkIDs)
	}
	return nil
}
