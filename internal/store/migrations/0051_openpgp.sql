-- OpenPGP key store (internal/app/comfort_pgp.go). Private key material never
-- lives here: secret_ref points into the secret store, like smime_certs.
CREATE TABLE pgp_keys (
  id          INTEGER PRIMARY KEY,
  fingerprint TEXT    NOT NULL UNIQUE,
  email       TEXT    NOT NULL DEFAULT '',
  user_id     TEXT    NOT NULL DEFAULT '',
  public_key  BLOB    NOT NULL,
  source      TEXT    NOT NULL DEFAULT 'manual', -- own | manual | wkd | keyserver
  secret_ref  TEXT,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX ix_pgp_keys_email ON pgp_keys(email);

-- Trust decisions per (address, key). Written only by user actions (import,
-- online lookup on request, fingerprint confirmed), never by incoming mail.
CREATE TABLE pgp_trust (
  address     TEXT    NOT NULL,
  fingerprint TEXT    NOT NULL,
  level       TEXT    NOT NULL DEFAULT 'unknown', -- unknown | tofu | verified
  source      TEXT    NOT NULL DEFAULT 'manual',
  first_seen  INTEGER,
  last_seen   INTEGER,
  verified_at INTEGER,
  seen_count  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (address, fingerprint)
) WITHOUT ROWID;
CREATE INDEX ix_pgp_trust_fingerprint ON pgp_trust(fingerprint);

-- Autocrypt Level 1 peer state, collected from incoming mail. Can be collected
-- again, so the daily settings backup empties it.
CREATE TABLE pgp_peers (
  addr           TEXT PRIMARY KEY,
  last_seen      INTEGER,
  autocrypt_ts   INTEGER,
  public_key     BLOB,
  prefer_encrypt TEXT NOT NULL DEFAULT 'nopreference',
  gossip_ts      INTEGER,
  gossip_key     BLOB,
  key_changed    INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
