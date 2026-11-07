// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO accounts(kind, display_name, color, config_json, secret_ref, enabled, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.Kind, a.DisplayName, nullString(a.Color), cfg, nullString(a.SecretRef), boolInt(a.Enabled), a.SortOrder)
	if err != nil {
		return 0, fmt.Errorf("store: create account: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := denyLegacyAI(ctx, tx, id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// denyLegacyAI marks a new account as not shared in the AI settings of
// TuxMail <= 1.0.1 ("accountPermissions", absent = allowed): an older
// version opening this data after a downgrade must not send its content,
// which starts "off" here (internal/app/ai_access.go). Without stored AI
// settings nothing is configured and nothing is written.
func denyLegacyAI(ctx context.Context, tx *sql.Tx, id int64) error {
	k := strconv.FormatInt(id, 10)
	_, err := tx.ExecContext(ctx, `UPDATE app_settings SET value_json = CASE
		WHEN json_type(value_json, '$.accountPermissions') = 'object' THEN json_set(value_json, '$.accountPermissions."' || ? || '"', json('false'))
		ELSE json_set(value_json, '$.accountPermissions', json_object(?, json('false'))) END
		WHERE key = 'ai' AND json_valid(value_json)`, k, k)
	if err != nil {
		return fmt.Errorf("store: create account: ai settings: %w", err)
	}
	return nil
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

const signatureCols = `id, name, html, plain, placement, plain_override, updated_at`

func scanSignature(sc scanner) (domain.Signature, error) {
	var sig domain.Signature
	var override int
	err := sc.Scan(&sig.ID, &sig.Name, &sig.HTML, &sig.Plain, &sig.Placement, &override, &sig.UpdatedAt)
	sig.PlainOverride = override != 0
	return sig, err
}

func placementOrDefault(p string) string {
	if p == "" {
		return "below_reply"
	}
	return p
}

// CreateSignature inserts a signature (with its images) and returns its id.
func (s *Store) CreateSignature(ctx context.Context, sig domain.Signature) (int64, error) {
	var id int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO signatures(name, html, plain, placement, plain_override, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			sig.Name, sig.HTML, sig.Plain, placementOrDefault(sig.Placement), boolInt(sig.PlainOverride), time.Now().Unix())
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return putSignatureImages(ctx, tx, id, sig.Images)
	})
	if err != nil {
		return 0, fmt.Errorf("store: create signature: %w", err)
	}
	return id, nil
}

// UpdateSignature overwrites the signature sig.ID including its image set.
func (s *Store) UpdateSignature(ctx context.Context, sig domain.Signature) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := checkAffected(tx.ExecContext(ctx, `UPDATE signatures SET name = ?, html = ?, plain = ?, placement = ?, plain_override = ?, updated_at = ? WHERE id = ?`,
			sig.Name, sig.HTML, sig.Plain, placementOrDefault(sig.Placement), boolInt(sig.PlainOverride), time.Now().Unix(), sig.ID)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM signature_images WHERE signature_id = ?`, sig.ID); err != nil {
			return err
		}
		return putSignatureImages(ctx, tx, sig.ID, sig.Images)
	})
}

func putSignatureImages(ctx context.Context, tx *sql.Tx, id int64, imgs []domain.SignatureImage) error {
	for _, im := range imgs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO signature_images(signature_id, content_id, blob_hash, mime_type, filename, size)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(signature_id, content_id) DO NOTHING`,
			id, im.ContentID, im.BlobHash, im.MIMEType, im.Filename, im.Size); err != nil {
			return err
		}
	}
	return nil
}

// signatureImages loads the images of the given signatures.
func (s *Store) signatureImages(ctx context.Context, sigs []domain.Signature) error {
	if len(sigs) == 0 {
		return nil
	}
	idx := make(map[int64]int, len(sigs))
	for i, sig := range sigs {
		idx[sig.ID] = i
	}
	q := `SELECT signature_id, content_id, blob_hash, mime_type, filename, size FROM signature_images`
	var args []any
	if len(sigs) == 1 {
		q += ` WHERE signature_id = ?`
		args = append(args, sigs[0].ID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY signature_id, content_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sid int64
		var im domain.SignatureImage
		if err := rows.Scan(&sid, &im.ContentID, &im.BlobHash, &im.MIMEType, &im.Filename, &im.Size); err != nil {
			return err
		}
		if i, ok := idx[sid]; ok {
			sigs[i].Images = append(sigs[i].Images, im)
		}
	}
	return rows.Err()
}

// GetSignature returns the signature with the given id.
func (s *Store) GetSignature(ctx context.Context, id int64) (domain.Signature, error) {
	sig, err := scanSignature(s.db.QueryRowContext(ctx, `SELECT `+signatureCols+` FROM signatures WHERE id = ?`, id))
	if err != nil {
		return sig, notFound(err)
	}
	one := []domain.Signature{sig}
	err = s.signatureImages(ctx, one)
	return one[0], err
}

// ListSignatures returns all signatures ordered by name.
func (s *Store) ListSignatures(ctx context.Context) ([]domain.Signature, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+signatureCols+` FROM signatures ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	sigs, err := collect(rows, scanSignature)
	if err != nil {
		return nil, err
	}
	return sigs, s.signatureImages(ctx, sigs)
}

// SignatureImageByCID returns a stored signature image with the given
// Content-ID (of any signature).
func (s *Store) SignatureImageByCID(ctx context.Context, cid string) (domain.SignatureImage, error) {
	var im domain.SignatureImage
	err := s.db.QueryRowContext(ctx, `SELECT content_id, blob_hash, mime_type, filename, size FROM signature_images WHERE content_id = ? LIMIT 1`, cid).
		Scan(&im.ContentID, &im.BlobHash, &im.MIMEType, &im.Filename, &im.Size)
	return im, notFound(err)
}

// DeleteSignature deletes a signature; identities referencing it fall back to none.
func (s *Store) DeleteSignature(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM signatures WHERE id = ?`, id))
}

// blobRefsQuery counts the references to a blob hash from all tables that
// store blob hashes.
const blobRefsQuery = `SELECT (SELECT COUNT(*) FROM messages WHERE blob_hash = ?1)
	+ (SELECT COUNT(*) FROM attachments WHERE blob_hash = ?1)
	+ (SELECT COUNT(*) FROM uploads WHERE blob_hash = ?1)
	+ (SELECT COUNT(*) FROM proxy_cache WHERE blob_hash = ?1)
	+ (SELECT COUNT(*) FROM signature_images WHERE blob_hash = ?1)
	+ (SELECT COUNT(*) FROM chat_messages WHERE media_blob = ?1)
	+ (SELECT COUNT(*) FROM persons WHERE avatar_blob = ?1)`

// UnreferencedBlobs returns the hashes of hashes that no row references any
// more (safe to delete from the blob store).
func (s *Store) UnreferencedBlobs(ctx context.Context, hashes []string) ([]string, error) {
	var out []string
	for _, h := range hashes {
		var n int
		if err := s.db.QueryRowContext(ctx, blobRefsQuery, h).Scan(&n); err != nil {
			return out, err
		}
		if n == 0 {
			out = append(out, h)
		}
	}
	return out, nil
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
