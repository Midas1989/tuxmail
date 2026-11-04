-- SPDX-License-Identifier: AGPL-3.0-or-later
-- Large mailboxes: LinkAddressesToPerson matches lower(email); without an
-- expression index every new person scanned all message_addresses, which
-- made indexing the counterparts of a 2500-mail initial sync quadratic.
-- (Version 30 leaves room for the migrations of parallel feature branches.)
CREATE INDEX IF NOT EXISTS ix_addr_email_lower ON message_addresses(lower(email));
