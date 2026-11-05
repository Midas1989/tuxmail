-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Re-audit 2026-11-05, NA-06: networks a user logged in from successfully
-- (IPv4 /24, IPv6 /64), kept as a keyed hash (HMAC with the server key, no
-- address is stored) for 90 days. While a distributed guessing attack on an
-- account is detected, password logins are only accepted from these networks;
-- see the comment block in login.go.
CREATE TABLE login_known_networks (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    net_hash   BLOB NOT NULL,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (user_id, net_hash)
) WITHOUT ROWID;
CREATE INDEX login_known_networks_seen ON login_known_networks(last_seen);
