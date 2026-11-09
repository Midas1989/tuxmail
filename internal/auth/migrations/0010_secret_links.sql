-- One-time secret links (internal/server/secretlink): text that the sender's browser
-- encrypted with a random key. The key only exists in the link fragment (#...), the
-- server stores nothing but the ciphertext, the expiry and a view counter. Rows are
-- deleted when the last permitted view happened, when they expire or with their owner.
CREATE TABLE secret_links (
    id         TEXT PRIMARY KEY,                                   -- 128 random bits, base64url
    owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    max_views  INTEGER NOT NULL CHECK (max_views BETWEEN 1 AND 10),
    views      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_secret_links_owner ON secret_links(owner_id);
CREATE INDEX ix_secret_links_expires ON secret_links(expires_at);
