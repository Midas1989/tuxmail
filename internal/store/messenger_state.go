// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Messenger state (migration 0008_messenger.sql, used by internal/messenger).

// MessengerPlugin is the opt-in state of a messenger plugin.
type MessengerPlugin struct {
	Name           string
	Enabled        bool
	RiskAcceptedAt time.Time
	Settings       map[string]string
	UpdatedAt      time.Time
}

func scanMessengerPlugin(sc scanner) (MessengerPlugin, error) {
	var (
		p        MessengerPlugin
		risk     sql.NullInt64
		settings string
		updated  int64
	)
	if err := sc.Scan(&p.Name, &p.Enabled, &risk, &settings, &updated); err != nil {
		return p, err
	}
	p.RiskAcceptedAt, p.UpdatedAt = timeFromNull(risk), time.Unix(updated, 0).UTC()
	p.Settings = map[string]string{}
	if settings != "" {
		if err := json.Unmarshal([]byte(settings), &p.Settings); err != nil {
			return p, fmt.Errorf("store: messenger plugin %s: bad settings: %w", p.Name, err)
		}
	}
	return p, nil
}

const messengerPluginCols = `name, enabled, risk_accepted_at, settings_json, updated_at`

// GetMessengerPlugin returns the state of a plugin; a plugin without row is
// disabled (no error).
func (s *Store) GetMessengerPlugin(ctx context.Context, name string) (MessengerPlugin, error) {
	p, err := scanMessengerPlugin(s.q(ctx).QueryRowContext(ctx, `SELECT `+messengerPluginCols+` FROM messenger_plugins WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return MessengerPlugin{Name: name, Settings: map[string]string{}}, nil
	}
	return p, err
}

// ListMessengerPlugins returns all stored plugin states.
func (s *Store) ListMessengerPlugins(ctx context.Context) ([]MessengerPlugin, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+messengerPluginCols+` FROM messenger_plugins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessengerPlugin)
}

// PutMessengerPlugin stores the state of a plugin.
func (s *Store) PutMessengerPlugin(ctx context.Context, p MessengerPlugin) error {
	if p.Name == "" {
		return errors.New("store: messenger plugin name required")
	}
	if p.Settings == nil {
		p.Settings = map[string]string{}
	}
	b, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now()
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO messenger_plugins(name, enabled, risk_accepted_at, settings_json, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET enabled = excluded.enabled,
		risk_accepted_at = excluded.risk_accepted_at, settings_json = excluded.settings_json, updated_at = excluded.updated_at`,
		p.Name, boolInt(p.Enabled), unixOrNull(p.RiskAcceptedAt), string(b), p.UpdatedAt.Unix())
	return err
}

// Messenger account modes.
const (
	MessengerDirect = "direct"
	MessengerBridge = "bridge"
)

// MessengerAccount links a TuxMail account to a plugin session.
type MessengerAccount struct {
	AccountID     int64
	Plugin        string
	PluginAccount string
	Mode          string
	SelfID        string
	State         string
	LastError     string
	SyncedAt      time.Time
}

const messengerAccountCols = `account_id, plugin, plugin_account, mode, self_id, state, last_error, synced_at`

func scanMessengerAccount(sc scanner) (MessengerAccount, error) {
	var (
		m                    MessengerAccount
		self, state, lastErr sql.NullString
		synced               sql.NullInt64
	)
	err := sc.Scan(&m.AccountID, &m.Plugin, &m.PluginAccount, &m.Mode, &self, &state, &lastErr, &synced)
	m.SelfID, m.State, m.LastError, m.SyncedAt = self.String, state.String, lastErr.String, timeFromNull(synced)
	return m, err
}

// CreateMessengerAccount creates the account row and its plugin mapping.
func (s *Store) CreateMessengerAccount(ctx context.Context, a domain.Account, m MessengerAccount) (int64, error) {
	if m.Plugin == "" || m.PluginAccount == "" {
		return 0, errors.New("store: messenger account: plugin and plugin account required")
	}
	if m.Mode == "" {
		m.Mode = MessengerDirect
	}
	cfg, err := accountConfigJSON(a)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `INSERT INTO accounts(kind, display_name, color, config_json, secret_ref, enabled, sort_order)
			VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, a.Kind, a.DisplayName, nullString(a.Color), cfg, nullString(a.SecretRef),
			boolInt(a.Enabled), a.SortOrder).Scan(&id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO messenger_accounts(account_id, plugin, plugin_account, mode, self_id, state)
			VALUES (?, ?, ?, ?, ?, ?)`, id, m.Plugin, m.PluginAccount, m.Mode, nullString(m.SelfID), nullString(m.State))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: create messenger account: %w", err)
	}
	return id, nil
}

// GetMessengerAccount returns the plugin mapping of an account.
func (s *Store) GetMessengerAccount(ctx context.Context, accountID int64) (MessengerAccount, error) {
	m, err := scanMessengerAccount(s.q(ctx).QueryRowContext(ctx, `SELECT `+messengerAccountCols+` FROM messenger_accounts
		WHERE account_id = ?`, accountID))
	return m, notFound(err)
}

// MessengerAccountByPlugin finds the account of a plugin session.
func (s *Store) MessengerAccountByPlugin(ctx context.Context, plugin, pluginAccount string) (MessengerAccount, error) {
	m, err := scanMessengerAccount(s.q(ctx).QueryRowContext(ctx, `SELECT `+messengerAccountCols+` FROM messenger_accounts
		WHERE plugin = ? AND plugin_account = ?`, plugin, pluginAccount))
	return m, notFound(err)
}

// ListMessengerAccounts returns all messenger accounts (plugin "" = all).
func (s *Store) ListMessengerAccounts(ctx context.Context, plugin string) ([]MessengerAccount, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+messengerAccountCols+` FROM messenger_accounts
		WHERE ?1 = '' OR plugin = ?1 ORDER BY account_id`, plugin)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessengerAccount)
}

// SetMessengerAccountState records the connection state of an account.
func (s *Store) SetMessengerAccountState(ctx context.Context, accountID int64, state, lastError string) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE messenger_accounts SET state = ?, last_error = ? WHERE account_id = ?`,
		nullString(state), nullString(lastError), accountID))
}

// SetMessengerAccountSynced records the time of the last full sync and the
// own user id.
func (s *Store) SetMessengerAccountSynced(ctx context.Context, accountID int64, selfID string, at time.Time) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE messenger_accounts SET synced_at = ?, self_id = COALESCE(?, self_id)
		WHERE account_id = ?`, at.Unix(), nullString(selfID), accountID))
}

// PutChatMedia stores the plugin media reference of a chat message.
func (s *Store) PutChatMedia(ctx context.Context, chatMessageID int64, ref string, size int64) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO messenger_media(chat_message_id, ref, size) VALUES (?, ?, ?)
		ON CONFLICT(chat_message_id) DO UPDATE SET ref = excluded.ref, size = excluded.size`, chatMessageID, ref, size)
	return err
}

// ChatMedia returns the plugin media reference of a chat message.
func (s *Store) ChatMedia(ctx context.Context, chatMessageID int64) (ref string, size int64, err error) {
	var sz sql.NullInt64
	err = s.q(ctx).QueryRowContext(ctx, `SELECT ref, size FROM messenger_media WHERE chat_message_id = ?`, chatMessageID).Scan(&ref, &sz)
	return ref, sz.Int64, notFound(err)
}

// SetChatMessageMedia stores the downloaded media of a chat message.
func (s *Store) SetChatMessageMedia(ctx context.Context, id int64, blobHash, mime, name string) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE chat_messages SET media_blob = ?, media_mime = COALESCE(?, media_mime),
		media_name = COALESCE(?, media_name) WHERE id = ?`, nullString(blobHash), nullString(mime), nullString(name), id))
}

// chatStatusRank orders delivery states; states never move back.
const chatStatusRank = `CASE %s WHEN 'failed' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 0 END`

// UpgradeChatStatus raises the delivery status of own messages of a chat
// (receipts). With remoteIDs empty, all own messages sent until upTo are
// raised. It returns the number of changed messages.
func (s *Store) UpgradeChatStatus(ctx context.Context, chatID int64, remoteIDs []string, upTo time.Time, status string) (int, error) {
	newRank := fmt.Sprintf(chatStatusRank, "?")
	oldRank := fmt.Sprintf(chatStatusRank, "COALESCE(status, '')")
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	q := `UPDATE chat_messages SET status = ? WHERE chat_id = ? AND from_me = 1 AND ` + oldRank + ` < ` + newRank
	args := []any{status, chatID, status}
	if len(remoteIDs) > 0 {
		q += ` AND remote_id IN (` + placeholders(len(remoteIDs)) + `)`
		for _, r := range remoteIDs {
			args = append(args, r)
		}
	} else {
		q += ` AND COALESCE(sent_at, 0) <= ?`
		args = append(args, upTo.Unix())
	}
	res, err := s.q(ctx).ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// LatestIncomingChatMessage returns the newest incoming message of a chat
// (ErrNotFound if there is none).
func (s *Store) LatestIncomingChatMessage(ctx context.Context, chatID int64) (ChatMessage, error) {
	m, err := scanChatMessage(s.q(ctx).QueryRowContext(ctx, `SELECT `+chatMessageCols+` FROM chat_messages
		WHERE chat_id = ? AND from_me = 0 AND deleted = 0 ORDER BY COALESCE(sent_at, 0) DESC, id DESC LIMIT 1`, chatID))
	return m, notFound(err)
}

// UnreadIncomingRemoteIDs returns the remote ids of incoming messages after
// the read marker of a chat (oldest first), for read receipts.
func (s *Store) UnreadIncomingRemoteIDs(ctx context.Context, chatID int64, limit int) ([]string, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT m.remote_id FROM chat_messages m JOIN chats c ON c.id = m.chat_id
		WHERE m.chat_id = ? AND `+unreadChatCond+` ORDER BY COALESCE(m.sent_at, 0), m.id LIMIT ?`, chatID, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (string, error) {
		var v string
		return v, sc.Scan(&v)
	})
}
