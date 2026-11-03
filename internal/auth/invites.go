// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultInviteLifetime is used when CreateInvite gets a zero lifetime.
const DefaultInviteLifetime = 7 * 24 * time.Hour

// Invite is a single-use registration link (auth.registration: invite).
type Invite struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email,omitempty"` // if set, registration must use this address
	Role      Role      `json:"role"`
	CreatedBy int64     `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	UsedAt    time.Time `json:"usedAt,omitzero"`
	UsedBy    int64     `json:"usedBy,omitempty"`
}

// CreateInvite creates an invite and returns it with its secret token.
func (s *Service) CreateInvite(ctx context.Context, email string, role Role, lifetime time.Duration) (*Invite, string, error) {
	if role == "" {
		role = RoleUser
	}
	if !role.Valid() {
		return nil, "", fmt.Errorf("%w: unknown role %q", ErrInvalidInput, role)
	}
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, "", err
	}
	if lifetime <= 0 {
		lifetime = DefaultInviteLifetime
	}
	tok := randomToken(24)
	now := s.now()
	var by any
	if a := ActorFrom(ctx); a.UserID != 0 {
		by = a.UserID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO invites(token_hash, email, role, created_by, created_at, expires_at)
		VALUES (?,?,?,?,?,?)`, hashToken(tok), email, role, by, now.Unix(), now.Add(lifetime).Unix())
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	s.Audit(ctx, "invite.create", email, true, "role="+string(role))
	inv, err := s.getInvite(ctx, `id = ?`, id)
	return inv, tok, err
}

func (s *Service) getInvite(ctx context.Context, where string, args ...any) (*Invite, error) {
	var inv Invite
	var by, usedAt, usedBy sql.NullInt64
	var c, e int64
	err := s.db.QueryRowContext(ctx, `SELECT id, email, role, created_by, created_at, expires_at, used_at, used_by
		FROM invites WHERE `+where, args...).Scan(&inv.ID, &inv.Email, &inv.Role, &by, &c, &e, &usedAt, &usedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	inv.CreatedBy, inv.UsedBy = by.Int64, usedBy.Int64
	inv.CreatedAt, inv.ExpiresAt, inv.UsedAt = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC(), nullTime(usedAt)
	return &inv, nil
}

// ListInvites returns all invites, newest first.
func (s *Service) ListInvites(ctx context.Context) ([]*Invite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM invites ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []*Invite{}
	for _, id := range ids {
		inv, err := s.getInvite(ctx, `id = ?`, id)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, nil
}

// DeleteInvite revokes an invite.
func (s *Service) DeleteInvite(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.Audit(ctx, "invite.delete", fmt.Sprint(id), true, "")
	return nil
}

// LookupInvite returns a usable invite by token.
func (s *Service) LookupInvite(ctx context.Context, token string) (*Invite, error) {
	inv, err := s.getInvite(ctx, `token_hash = ?`, hashToken(token))
	if err != nil {
		return nil, ErrInvalidCode
	}
	if !inv.UsedAt.IsZero() || !s.now().Before(inv.ExpiresAt) {
		return nil, ErrInvalidCode
	}
	return inv, nil
}

// Register creates a user from an invite (only with auth.registration: invite).
func (s *Service) Register(ctx context.Context, token string, nu NewUser) (*User, error) {
	if s.cfg.Auth.Registration != "invite" {
		return nil, ErrRegistration
	}
	inv, err := s.LookupInvite(ctx, token)
	if err != nil {
		return nil, err
	}
	if inv.Email != "" && !strings.EqualFold(inv.Email, strings.TrimSpace(nu.Email)) {
		return nil, fmt.Errorf("%w: this invite is for a different email address", ErrInvalidInput)
	}
	if nu.Password == "" {
		return nil, fmt.Errorf("%w: password required", ErrWeakPassword)
	}
	// Claim the invite first so it cannot be used twice concurrently.
	res, err := s.db.ExecContext(ctx, `UPDATE invites SET used_at = ? WHERE id = ? AND used_at IS NULL`, s.now().Unix(), inv.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrInvalidCode
	}
	nu.Role = inv.Role
	u, err := s.CreateUser(ctx, nu)
	if err != nil {
		s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE invites SET used_at = NULL WHERE id = ?`, inv.ID)
		return nil, err
	}
	s.db.ExecContext(ctx, `UPDATE invites SET used_by = ? WHERE id = ?`, u.ID, inv.ID)
	s.Audit(ctx, "invite.use", u.Username, true, fmt.Sprintf("invite=%d", inv.ID))
	return u, nil
}
