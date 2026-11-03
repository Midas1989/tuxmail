// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// accountConfig is the part of domain.Account that is stored in config_json.
type accountConfig struct {
	Incoming *domain.ServerConfig `json:"incoming,omitempty"`
	Outgoing *domain.ServerConfig `json:"outgoing,omitempty"`
	Extra    map[string]string    `json:"extra,omitempty"`
}

const accountCols = `id, kind, display_name, color, config_json, secret_ref, enabled, sort_order`

func scanAccount(sc scanner) (domain.Account, error) {
	var (
		a          domain.Account
		color, ref sql.NullString
		cfgJSON    string
	)
	if err := sc.Scan(&a.ID, &a.Kind, &a.DisplayName, &color, &cfgJSON, &ref, &a.Enabled, &a.SortOrder); err != nil {
		return a, err
	}
	var cfg accountConfig
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		return a, fmt.Errorf("store: account %d: bad config_json: %w", a.ID, err)
	}
	a.Color, a.SecretRef = color.String, ref.String
	a.Incoming, a.Outgoing, a.Extra = cfg.Incoming, cfg.Outgoing, cfg.Extra
	return a, nil
}

func accountConfigJSON(a domain.Account) (string, error) {
	b, err := json.Marshal(accountConfig{Incoming: a.Incoming, Outgoing: a.Outgoing, Extra: a.Extra})
	return string(b), err
}

// CreateAccount inserts a new account and returns its id (a.ID is ignored).
func (s *Store) CreateAccount(ctx context.Context, a domain.Account) (int64, error) {
	cfg, err := accountConfigJSON(a)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO accounts(kind, display_name, color, config_json, secret_ref, enabled, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.Kind, a.DisplayName, nullString(a.Color), cfg, nullString(a.SecretRef), boolInt(a.Enabled), a.SortOrder)
	if err != nil {
		return 0, fmt.Errorf("store: create account: %w", err)
	}
	return res.LastInsertId()
}

// UpdateAccount overwrites all fields of the account a.ID.
func (s *Store) UpdateAccount(ctx context.Context, a domain.Account) error {
	cfg, err := accountConfigJSON(a)
	if err != nil {
		return err
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE accounts SET kind = ?, display_name = ?, color = ?, config_json = ?,
		secret_ref = ?, enabled = ?, sort_order = ? WHERE id = ?`,
		a.Kind, a.DisplayName, nullString(a.Color), cfg, nullString(a.SecretRef), boolInt(a.Enabled), a.SortOrder, a.ID))
}

// GetAccount returns the account with the given id.
func (s *Store) GetAccount(ctx context.Context, id int64) (domain.Account, error) {
	a, err := scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = ?`, id))
	return a, notFound(err)
}

// ListAccounts returns all accounts ordered by sort order and id.
func (s *Store) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanAccount)
}

// DeleteAccount deletes an account and, via cascades, all its data.
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id))
}

// ==== Identities ==============================================================

const identityCols = `id, account_id, name, email, reply_to, bcc_self, smtp_account_id,
	default_signature_id, reply_signature_id, is_default`

func scanIdentity(sc scanner) (domain.Identity, error) {
	var (
		i                   domain.Identity
		replyTo             sql.NullString
		smtp, defSig, reSig sql.NullInt64
	)
	err := sc.Scan(&i.ID, &i.AccountID, &i.Name, &i.Email, &replyTo, &i.BccSelf, &smtp, &defSig, &reSig, &i.IsDefault)
	i.ReplyTo = replyTo.String
	i.SMTPAccountID, i.DefaultSignatureID, i.ReplySignatureID = smtp.Int64, defSig.Int64, reSig.Int64
	return i, err
}

// CreateIdentity inserts a new identity and returns its id. If it is marked as
// default, all other identities of the account lose their default flag.
func (s *Store) CreateIdentity(ctx context.Context, i domain.Identity) (int64, error) {
	var id int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO identities(account_id, name, email, reply_to, bcc_self, smtp_account_id,
			default_signature_id, reply_signature_id, is_default) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.AccountID, i.Name, i.Email, nullString(i.ReplyTo), boolInt(i.BccSelf), nullID(i.SMTPAccountID),
			nullID(i.DefaultSignatureID), nullID(i.ReplySignatureID), boolInt(i.IsDefault))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return fixDefaultIdentity(ctx, tx, id, i)
	})
	if err != nil {
		return 0, fmt.Errorf("store: create identity: %w", err)
	}
	return id, nil
}

// UpdateIdentity overwrites the identity i.ID (the folder assignments set via
// SetIdentityFolders are kept).
func (s *Store) UpdateIdentity(ctx context.Context, i domain.Identity) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		err := checkAffected(tx.ExecContext(ctx, `UPDATE identities SET account_id = ?, name = ?, email = ?, reply_to = ?,
			bcc_self = ?, smtp_account_id = ?, default_signature_id = ?, reply_signature_id = ?, is_default = ?
			WHERE id = ?`,
			i.AccountID, i.Name, i.Email, nullString(i.ReplyTo), boolInt(i.BccSelf), nullID(i.SMTPAccountID),
			nullID(i.DefaultSignatureID), nullID(i.ReplySignatureID), boolInt(i.IsDefault), i.ID))
		if err != nil {
			return err
		}
		return fixDefaultIdentity(ctx, tx, i.ID, i)
	})
}

func fixDefaultIdentity(ctx context.Context, tx *sql.Tx, id int64, i domain.Identity) error {
	if !i.IsDefault {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE identities SET is_default = 0 WHERE account_id = ? AND id <> ?`, i.AccountID, id)
	return err
}

// GetIdentity returns the identity with the given id.
func (s *Store) GetIdentity(ctx context.Context, id int64) (domain.Identity, error) {
	i, err := scanIdentity(s.db.QueryRowContext(ctx, `SELECT `+identityCols+` FROM identities WHERE id = ?`, id))
	return i, notFound(err)
}

// ListIdentities returns the identities of an account, default identity first.
func (s *Store) ListIdentities(ctx context.Context, accountID int64) ([]domain.Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+identityCols+` FROM identities WHERE account_id = ?
		ORDER BY is_default DESC, id`, accountID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanIdentity)
}

// DeleteIdentity deletes an identity.
func (s *Store) DeleteIdentity(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM identities WHERE id = ?`, id))
}

// IdentityFolders returns the sent and drafts folder of an identity (0 = account default).
func (s *Store) IdentityFolders(ctx context.Context, identityID int64) (sent, drafts int64, err error) {
	var sn, dn sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT sent_folder_id, drafts_folder_id FROM identities WHERE id = ?`, identityID).
		Scan(&sn, &dn)
	return sn.Int64, dn.Int64, notFound(err)
}

// SetIdentityFolders sets the sent and drafts folder of an identity (0 = account default).
func (s *Store) SetIdentityFolders(ctx context.Context, identityID, sent, drafts int64) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE identities SET sent_folder_id = ?, drafts_folder_id = ? WHERE id = ?`,
		nullID(sent), nullID(drafts), identityID))
}

// ==== Signatures ==============================================================

const signatureCols = `id, name, html, plain, placement`

func scanSignature(sc scanner) (domain.Signature, error) {
	var sig domain.Signature
	err := sc.Scan(&sig.ID, &sig.Name, &sig.HTML, &sig.Plain, &sig.Placement)
	return sig, err
}

func placementOrDefault(p string) string {
	if p == "" {
		return "below_reply"
	}
	return p
}

// CreateSignature inserts a signature and returns its id.
func (s *Store) CreateSignature(ctx context.Context, sig domain.Signature) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO signatures(name, html, plain, placement) VALUES (?, ?, ?, ?)`,
		sig.Name, sig.HTML, sig.Plain, placementOrDefault(sig.Placement))
	if err != nil {
		return 0, fmt.Errorf("store: create signature: %w", err)
	}
	return res.LastInsertId()
}

// UpdateSignature overwrites the signature sig.ID.
func (s *Store) UpdateSignature(ctx context.Context, sig domain.Signature) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE signatures SET name = ?, html = ?, plain = ?, placement = ? WHERE id = ?`,
		sig.Name, sig.HTML, sig.Plain, placementOrDefault(sig.Placement), sig.ID))
}

// GetSignature returns the signature with the given id.
func (s *Store) GetSignature(ctx context.Context, id int64) (domain.Signature, error) {
	sig, err := scanSignature(s.db.QueryRowContext(ctx, `SELECT `+signatureCols+` FROM signatures WHERE id = ?`, id))
	return sig, notFound(err)
}

// ListSignatures returns all signatures ordered by name.
func (s *Store) ListSignatures(ctx context.Context) ([]domain.Signature, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+signatureCols+` FROM signatures ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanSignature)
}

// DeleteSignature deletes a signature; identities referencing it fall back to none.
func (s *Store) DeleteSignature(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM signatures WHERE id = ?`, id))
}

// collect scans all rows with fn and closes rows.
func collect[T any](rows *sql.Rows, fn func(scanner) (T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := fn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
