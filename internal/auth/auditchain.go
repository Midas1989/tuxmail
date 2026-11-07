// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/Midas1989/Mailclient/internal/auditchain"
)

// The audit log as an HMAC chain (Basis spec/BETRIEB.md). Each row is linked to
// its predecessor with a key that lies, sealed under the key ring, in server_keys:
// whoever can write to the database alone cannot forge, change or remove rows
// unnoticed; triggers refuse changes at the database level. Removing rows at
// the end shows only against a hash kept outside, which is why the server logs the
// newest hash once a day (AuditAnchor). Rows from before the chain became its
// beginning at the first start (sealAuditBacklog), once.
const auditKeyName = "audit_chain"

type auditRow struct {
	id            int64
	at, actorID   int64
	actor, action string
	target, ip    string
	success       bool
	details       string
}

// entry maps a row to the Basis format; everything the Basis has no field for is
// part of "new". The timestamp follows from the stored Unix time.
func (r auditRow) entry() auditchain.Entry {
	ip := r.ip
	return auditchain.Entry{
		Actor: r.actor, Action: r.action, Target: r.target, IP: &ip,
		TS:  time.Unix(r.at, 0).UTC().Format("2006-01-02T15:04:05.000+00:00"),
		New: map[string]any{"actor_id": r.actorID, "success": r.success, "details": r.details},
	}
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func chainHead(ctx context.Context, tx *sql.Tx) (string, error) {
	var head sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT hash FROM audit_log WHERE hash IS NOT NULL ORDER BY id DESC LIMIT 1`).Scan(&head)
	if err == sql.ErrNoRows || (err == nil && !head.Valid) {
		return auditchain.Genesis, nil
	}
	return head.String, err
}

// appendAudit writes one row. The write lock of the transaction (_txlock=immediate)
// keeps the chain in order, also across processes.
func (s *Service) appendAudit(ctx context.Context, r auditRow) error {
	key, err := s.purposeKey(ctx, auditKeyName)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		prev, err := chainHead(ctx, tx)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(at, actor_id, actor, action, target, ip, success, details, prev_hash, hash)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, r.at, nullableID(r.actorID), r.actor, r.action, r.target, r.ip, r.success, r.details, prev, r.entry().Hash(prev, key))
		return err
	})
}

const auditRowCols = `id, at, COALESCE(actor_id,0), actor, action, target, ip, success, details`

func scanAuditRow(rows *sql.Rows, extra ...any) (auditRow, error) {
	var r auditRow
	dest := append([]any{&r.id, &r.at, &r.actorID, &r.actor, &r.action, &r.target, &r.ip, &r.success, &r.details}, extra...)
	err := rows.Scan(dest...)
	return r, err
}

// sealAuditBacklog makes the rows from before the chain its beginning. This happens
// exactly once, in one transaction with the creation of the chain key: when the key
// exists there is nothing to seal any more. A row without hash that turns up later
// did not come from this program, it stays as it is and VerifyAuditChain reports it.
// Removing the key to get here again does not help: all earlier rows then fail.
func (s *Service) sealAuditBacklog(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_keys WHERE name = ?`, auditKeyName).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	key, err := randomServerKey()
	if err != nil {
		return err
	}
	defer clear(key)
	enc, err := s.seal(key, serverKeyContext(auditKeyName))
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO server_keys(name, key_enc) VALUES (?, ?)`, auditKeyName, enc)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // another process was faster
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+auditRowCols+` FROM audit_log WHERE hash IS NULL ORDER BY id`)
		if err != nil {
			return err
		}
		var todo []auditRow
		for rows.Next() {
			r, err := scanAuditRow(rows)
			if err != nil {
				rows.Close()
				return err
			}
			todo = append(todo, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		prev := auditchain.Genesis
		for _, r := range todo {
			h := r.entry().Hash(prev, key)
			if _, err := tx.ExecContext(ctx, `UPDATE audit_log SET prev_hash = ?, hash = ? WHERE id = ?`, prev, h, r.id); err != nil {
				return err
			}
			prev = h
		}
		return nil
	})
}

// AuditChainReport is the result of VerifyAuditChain.
type AuditChainReport struct {
	Entries int    `json:"entries"`
	Head    string `json:"head"`
	// BrokenAt is the id of the first row that does not fit, 0 = chain intact.
	BrokenAt int64  `json:"brokenAt,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// OK reports an intact chain.
func (r *AuditChainReport) OK() bool { return r.BrokenAt == 0 }

// VerifyAuditChain recomputes the whole chain. A changed row, a removed or
// inserted row in the middle and a row without hash all show.
func (s *Service) VerifyAuditChain(ctx context.Context) (*AuditChainReport, error) {
	key, err := s.purposeKey(ctx, auditKeyName)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+auditRowCols+`, prev_hash, hash FROM audit_log ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rep := &AuditChainReport{Head: auditchain.Genesis}
	for rows.Next() {
		var prev, hash sql.NullString
		r, err := scanAuditRow(rows, &prev, &hash)
		if err != nil {
			return nil, err
		}
		rep.Entries++
		switch {
		case !hash.Valid || !prev.Valid:
			rep.BrokenAt, rep.Reason = r.id, "row without hash"
		case prev.String != rep.Head:
			rep.BrokenAt, rep.Reason = r.id, "does not follow its predecessor (row removed or inserted)"
		case !r.entry().Valid(prev.String, hash.String, key):
			rep.BrokenAt, rep.Reason = r.id, "content changed"
		}
		if rep.BrokenAt != 0 {
			return rep, nil
		}
		rep.Head = hash.String
	}
	return rep, rows.Err()
}

// AuditAnchor verifies the chain and writes its newest hash to the log, which
// leaves the machine (journald, syslog, monitoring): the anchor against cutting
// off the end. The server calls it once a day.
func (s *Service) AuditAnchor(ctx context.Context) (*AuditChainReport, error) {
	rep, err := s.VerifyAuditChain(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: audit chain: %w", err)
	}
	if rep.OK() {
		slog.Info("audit chain anchor", "head", rep.Head, "entries", rep.Entries)
	} else {
		slog.Error("audit chain broken", "row", rep.BrokenAt, "reason", rep.Reason)
	}
	return rep, nil
}
