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

// Group is a named set of users, used for shared mailbox permissions
// (KONZEPT 8.14) and provisioning templates.
type Group struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	Members     []int64   `json:"members"`
}

// CreateGroup creates a group.
func (s *Service) CreateGroup(ctx context.Context, name, description string) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, fmt.Errorf("%w: group name must be 1-100 characters", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO groups(name, description, created_at) VALUES (?,?,?)`,
		name, description, s.now().Unix())
	if isUniqueErr(err) {
		return nil, fmt.Errorf("%w: group %q", ErrExists, name)
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.Audit(ctx, "group.create", name, true, "")
	return s.GetGroup(ctx, id)
}

// GetGroup returns a group with its member ids.
func (s *Service) GetGroup(ctx context.Context, id int64) (*Group, error) {
	var g Group
	var c int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, created_at FROM groups WHERE id = ?`, id).
		Scan(&g.ID, &g.Name, &g.Description, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.CreatedAt = time.Unix(c, 0).UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM group_members WHERE group_id = ? ORDER BY user_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g.Members = []int64{}
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		g.Members = append(g.Members, uid)
	}
	return &g, rows.Err()
}

// GetGroupByName returns a group by its (case-insensitive) name.
func (s *Service) GetGroupByName(ctx context.Context, name string) (*Group, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM groups WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetGroup(ctx, id)
}

// ListGroups returns all groups with members.
func (s *Service) ListGroups(ctx context.Context) ([]*Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM groups ORDER BY name`)
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
	out := []*Group{}
	for _, id := range ids {
		g, err := s.GetGroup(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// DeleteGroup deletes a group.
func (s *Service) DeleteGroup(ctx context.Context, id int64) error {
	g, err := s.GetGroup(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id); err != nil {
		return err
	}
	s.Audit(ctx, "group.delete", g.Name, true, "")
	return nil
}

// AddGroupMember adds a user to a group (idempotent).
func (s *Service) AddGroupMember(ctx context.Context, groupID, userID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO group_members(group_id, user_id) VALUES (?,?)`, groupID, userID)
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
		return ErrNotFound
	}
	if err == nil {
		s.Audit(ctx, "group.add_member", fmt.Sprintf("group=%d user=%d", groupID, userID), true, "")
	}
	return err
}

// RemoveGroupMember removes a user from a group (idempotent).
func (s *Service) RemoveGroupMember(ctx context.Context, groupID, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM group_members WHERE group_id = ? AND user_id = ?`, groupID, userID)
	if err == nil {
		s.Audit(ctx, "group.remove_member", fmt.Sprintf("group=%d user=%d", groupID, userID), true, "")
	}
	return err
}

// UserGroupIDs returns the ids of all groups the user belongs to.
func (s *Service) UserGroupIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id FROM group_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
