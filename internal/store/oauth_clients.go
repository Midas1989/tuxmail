// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Persistence of OAuth client overrides, pending web-redirect flows and
// identity delegation (migration 0009_oauth.sql).

// OAuthClient is a user's own OAuth client registration.
type OAuthClient struct {
	Provider  string
	ClientID  string
	Tenant    string
	SecretRef string // secret store reference of the client secret ("" = none)
	UpdatedAt time.Time
}

// ListOAuthClients returns the user's own client registrations.
func (s *Store) ListOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT provider, client_id, tenant, secret_ref, updated_at FROM oauth_clients ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthClient
	for rows.Next() {
		var c OAuthClient
		var tenant, ref sql.NullString
		var upd int64
		if err := rows.Scan(&c.Provider, &c.ClientID, &tenant, &ref, &upd); err != nil {
			return nil, err
		}
		c.Tenant, c.SecretRef, c.UpdatedAt = tenant.String, ref.String, time.Unix(upd, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutOAuthClient stores (replaces) a client registration.
func (s *Store) PutOAuthClient(ctx context.Context, c OAuthClient) error {
	if c.Provider == "" || c.ClientID == "" {
		return errors.New("store: oauth client: provider and client id are required")
	}
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO oauth_clients(provider, client_id, tenant, secret_ref, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET client_id = excluded.client_id, tenant = excluded.tenant,
		secret_ref = excluded.secret_ref, updated_at = excluded.updated_at`,
		c.Provider, c.ClientID, nullString(c.Tenant), nullString(c.SecretRef), time.Now().Unix())
	return err
}

// DeleteOAuthClient removes a client registration.
func (s *Store) DeleteOAuthClient(ctx context.Context, provider string) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM oauth_clients WHERE provider = ?`, provider)
	return err
}

// PutOAuthPending stores a pending web-redirect flow (JSON) and drops
// entries older than maxAge.
func (s *Store) PutOAuthPending(ctx context.Context, state, provider, flowJSON string, maxAge time.Duration) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_pending WHERE created_at < ?`, time.Now().Add(-maxAge).Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO oauth_pending(state, provider, flow_json, created_at) VALUES (?, ?, ?, ?)`,
			state, provider, flowJSON, time.Now().Unix())
		return err
	})
}

// TakeOAuthPending returns and deletes a pending flow (ErrNotFound if unknown).
func (s *Store) TakeOAuthPending(ctx context.Context, state string) (string, error) {
	var js string
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT flow_json FROM oauth_pending WHERE state = ?`, state).Scan(&js); err != nil {
			return notFound(err)
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM oauth_pending WHERE state = ?`, state)
		return err
	})
	return js, err
}

// IdentityDelegation is the send mode of a shared-mailbox identity.
type IdentityDelegation struct {
	IdentityID     int64  `json:"identityId"`
	Mode           string `json:"mode"` // send_as | send_on_behalf
	Mailbox        string `json:"mailbox"`
	OwnerAccountID int64  `json:"ownerAccountId,omitempty"`
}

// SetIdentityDelegation stores (or with an empty mode removes) the
// delegation of an identity.
func (s *Store) SetIdentityDelegation(ctx context.Context, d IdentityDelegation) error {
	if d.Mode == "" {
		_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM identity_delegation WHERE identity_id = ?`, d.IdentityID)
		return err
	}
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO identity_delegation(identity_id, mode, mailbox, owner_account_id) VALUES (?, ?, ?, ?)
		ON CONFLICT(identity_id) DO UPDATE SET mode = excluded.mode, mailbox = excluded.mailbox, owner_account_id = excluded.owner_account_id`,
		d.IdentityID, d.Mode, d.Mailbox, nullID(d.OwnerAccountID))
	return err
}

// IdentityDelegations returns all delegations by identity id.
func (s *Store) IdentityDelegations(ctx context.Context) (map[int64]IdentityDelegation, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT identity_id, mode, mailbox, owner_account_id FROM identity_delegation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]IdentityDelegation{}
	for rows.Next() {
		var d IdentityDelegation
		var owner sql.NullInt64
		if err := rows.Scan(&d.IdentityID, &d.Mode, &d.Mailbox, &owner); err != nil {
			return nil, err
		}
		d.OwnerAccountID = owner.Int64
		out[d.IdentityID] = d
	}
	return out, rows.Err()
}
