// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

// Settings backup of a server: what the daily job puts into its encrypted
// archive. TuxMail is a mail client, the mail itself stays on the mail server,
// so this holds only what cannot be fetched again: accounts, rules, keys,
// signatures and preferences. CreateSettings writes a cleaned copy of the data
// directory; it works from consistent SQLite snapshots (VACUUM INTO), so the
// service keeps running and writing meanwhile.
//
// userStoreTables and serverDBTables below are the only place that decides what
// a table is. TestSettingsTablesAreClassified fails for a new table until it is
// listed here, and at run time an unlisted table stops the backup: nothing may
// silently travel along or go missing.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// TableClass says what the settings backup does with a table.
type TableClass int

const (
	// TableKeep is configuration and stays.
	TableKeep TableClass = iota + 1
	// TablePurge is data that can be fetched again or is not settings: emptied.
	TablePurge
	// TableIndex belongs to the search index: emptied through the FTS commands.
	TableIndex
)

// userStoreTables classifies tuxmail.db (of users and team mailboxes).
var userStoreTables = map[string]TableClass{
	// Accounts, folders and collections (sync state reset, see resetSyncState).
	"schema_version":          TableKeep,
	"accounts":                TableKeep,
	"account_folder_settings": TableKeep,
	"folder_role_overrides":   TableKeep,
	"folders":                 TableKeep,
	"identities":              TableKeep,
	"identity_chat":           TableKeep,
	"identity_delegation":     TableKeep,
	"calendars":               TableKeep,
	"calendar_meta":           TableKeep,
	"addressbooks":            TableKeep,
	"addressbook_meta":        TableKeep,
	"messenger_accounts":      TableKeep,
	"messenger_plugins":       TableKeep,
	"oauth_clients":           TableKeep,
	"search_index_policy":     TableKeep,
	// Rules, tags and organisation without their items.
	"workflows":         TableKeep,
	"workflow_meta":     TableKeep,
	"tags":              TableKeep,
	"projects":          TableKeep,
	"project_rules":     TableKeep,
	"project_templates": TableKeep,
	"search_folders":    TableKeep,
	"search_profiles":   TableKeep,
	"search_settings":   TableKeep,
	"chat_folders":      TableKeep,
	// Writing, keys and preferences.
	"signatures":           TableKeep,
	"signature_images":     TableKeep,
	"snippets":             TableKeep,
	"smime_certs":          TableKeep,
	"pgp_keys":             TableKeep, // private parts: secret store (secrets.vault)
	"pgp_trust":            TableKeep, // only user decisions
	"vacation_settings":    TableKeep,
	"remote_content_allow": TableKeep,
	"subscriptions":        TableKeep,
	"spell_words":          TableKeep,
	"app_settings":         TableKeep,
	"comfort_kv":           TableKeep,

	// Server connection of the desktop app: a read cache, an outbox of one
	// device, and the record of finished sends (migration 0060).
	"remote_cache":                 TablePurge,
	"remote_cache_messages":        TablePurge,
	"remote_cache_message_folders": TablePurge,
	"remote_outbox":                TablePurge,
	"remote_outbox_uploads":        TablePurge,
	"idempotency_keys":             TablePurge,
	"settings_tombstones":          TablePurge,

	// Mail.
	"messages":               TablePurge,
	"pgp_peers":              TablePurge, // Autocrypt: collected again from mail
	"message_addresses":      TablePurge,
	"message_counterparts":   TablePurge,
	"message_insights":       TablePurge,
	"message_locations":      TablePurge,
	"message_relations":      TablePurge,
	"attachments":            TablePurge,
	"conversations":          TablePurge,
	"conversation_overrides": TablePurge,
	"conversation_state":     TablePurge,
	"followups":              TablePurge,
	"scheduled_messages":     TablePurge,
	"meeting_proposals":      TablePurge,
	"merge_jobs":             TablePurge,
	"vacation_replies":       TablePurge,
	"item_hidden":            TablePurge,
	"item_pins":              TablePurge,
	"item_reactions":         TablePurge,
	"item_stars":             TablePurge,
	"item_tags":              TablePurge,
	"project_items":          TablePurge,
	"project_members":        TablePurge,
	"search_folder_hits":     TablePurge,
	"search_history":         TablePurge,
	"cloud_archive":          TablePurge,
	"cloud_link_meta":        TablePurge,
	"cloud_links":            TablePurge,
	// Chats.
	"chats":             TablePurge,
	"chat_messages":     TablePurge,
	"chat_counterparts": TablePurge,
	"chat_drafts":       TablePurge,
	"messenger_media":   TablePurge,
	// Calendar and contact data.
	"cal_objects":          TablePurge,
	"cal_instances":        TablePurge,
	"pim_changes":          TablePurge,
	"pim_invitations":      TablePurge,
	"pim_reminders":        TablePurge,
	"contacts":             TablePurge,
	"contact_cards":        TablePurge,
	"contact_emails":       TablePurge,
	"persons":              TablePurge,
	"person_conversations": TablePurge,
	"person_handles":       TablePurge,
	// Learned from mail text, queues and caches.
	"category_tokens":  TablePurge,
	"spam_tokens":      TablePurge,
	"spam_trained":     TablePurge,
	"oauth_pending":    TablePurge,
	"ops":              TablePurge,
	"proxy_cache":      TablePurge,
	"tag_sync_pending": TablePurge,
	"uploads":          TablePurge,
	"workflow_runs":    TablePurge,
	"fts_docs":         TablePurge,

	// Search index and its FTS5 shadow tables.
	"fts_items":           TableIndex,
	"fts_items_config":    TableIndex,
	"fts_items_data":      TableIndex,
	"fts_items_docsize":   TableIndex,
	"fts_items_idx":       TableIndex,
	"fts_trigram":         TableIndex,
	"fts_trigram_config":  TableIndex,
	"fts_trigram_data":    TableIndex,
	"fts_trigram_docsize": TableIndex,
	"fts_trigram_idx":     TableIndex,
	"fts_vocab":           TableIndex,
}

// serverDBTables classifies server.db. Sessions stay, so that a restored server
// keeps its devices signed in; what teams wrote about conversations is mail
// content and goes, like short-lived login state.
var serverDBTables = map[string]TableClass{
	"schema_version":         TableKeep,
	"users":                  TableKeep,
	"groups":                 TableKeep,
	"group_members":          TableKeep,
	"sessions":               TableKeep,
	"recovery_codes":         TableKeep,
	"invites":                TableKeep,
	"audit_log":              TableKeep,
	"id_highwater":           TableKeep,
	"login_known_networks":   TableKeep,
	"passkeys":               TableKeep,
	"passkey_users":          TableKeep,
	"device_retired_tokens":  TableKeep,
	"server_keys":            TableKeep,
	"push_subscriptions":     TableKeep,
	"notification_prefs":     TableKeep,
	"user_settings":          TableKeep,
	"provisioning_templates": TableKeep,
	"team_mailboxes":         TableKeep,
	"team_grants":            TableKeep,

	"team_conversations":    TablePurge,
	"team_comments":         TablePurge,
	"team_mentions":         TablePurge,
	"team_drafts":           TablePurge,
	"mfa_challenges":        TablePurge,
	"login_throttle":        TablePurge,
	"oidc_states":           TablePurge,
	"device_authorizations": TablePurge,
	"passkey_challenges":    TablePurge,
	"rate_limit":            TablePurge,
	// One-time secret links are short-lived ciphertexts; they never travel in a backup.
	"secret_links": TablePurge,
}

// UserStoreTables returns a copy of the classification of tuxmail.db (tests).
func UserStoreTables() map[string]TableClass { return copyClasses(userStoreTables) }

// ServerDBTables returns a copy of the classification of server.db (tests).
func ServerDBTables() map[string]TableClass { return copyClasses(serverDBTables) }

func copyClasses(in map[string]TableClass) map[string]TableClass {
	out := make(map[string]TableClass, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ErrUnclassifiedTable stops the backup: a table nobody has judged yet.
var ErrUnclassifiedTable = errors.New("backup: table is not classified for the settings backup")

// SettingsReport says what CreateSettings wrote.
type SettingsReport struct {
	Users int
	Teams int
	Bytes int64
}

// CreateSettings writes the settings of the server in dataDir to
// outDir/<base name of dataDir>, a tree that can be unpacked as a data
// directory. On an error the caller removes outDir.
func CreateSettings(ctx context.Context, dataDir, outDir string) (*SettingsReport, error) {
	root := filepath.Join(outDir, filepath.Base(filepath.Clean(dataDir)))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	rep := &SettingsReport{}
	if err := cleanSnapshot(ctx, filepath.Join(dataDir, "server.db"), filepath.Join(root, "server.db"), serverDBTables, nil); err != nil {
		return nil, fmt.Errorf("backup: server.db: %w", err)
	}
	// The key ring and the older master.key (gone once retired). One of them must exist.
	keys := 0
	for _, name := range []string{"secretbox.json", "master.key"} {
		err := copyPrivate(filepath.Join(dataDir, name), filepath.Join(root, name))
		switch {
		case err == nil:
			keys++
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	if keys == 0 {
		return nil, fmt.Errorf("backup: neither secretbox.json nor master.key found in %s", dataDir)
	}
	for _, optional := range []string{"vapid.json", filepath.Join("backup", "recipients.txt")} {
		err := copyPrivate(filepath.Join(dataDir, optional), filepath.Join(root, optional))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	for _, kind := range []struct {
		dir string
		n   *int
	}{{"users", &rep.Users}, {"teams", &rep.Teams}} {
		list, err := os.ReadDir(filepath.Join(dataDir, kind.dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			if _, err := strconv.ParseInt(e.Name(), 10, 64); err != nil || !e.IsDir() {
				continue
			}
			rel := filepath.Join(kind.dir, e.Name())
			if err := settingsOfStore(ctx, filepath.Join(dataDir, rel), filepath.Join(root, rel)); err != nil {
				return nil, fmt.Errorf("backup: %s: %w", rel, err)
			}
			*kind.n++
		}
	}
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err == nil {
			rep.Bytes += fi.Size()
		}
		return err
	})
	return rep, err
}

// settingsOfStore handles one users/<id> or teams/<id> directory.
func settingsOfStore(ctx context.Context, src, dst string) error {
	if err := copyPrivate(filepath.Join(src, "secrets.vault"), filepath.Join(dst, "secrets.vault")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dbSrc := filepath.Join(src, "tuxmail.db")
	if _, err := os.Stat(dbSrc); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var images []string
	err := cleanSnapshot(ctx, dbSrc, filepath.Join(dst, "tuxmail.db"), userStoreTables, func(db *sql.Conn) error {
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT blob_hash FROM signature_images`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return err
			}
			images = append(images, h)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	return copySignatureImages(filepath.Join(src, "blobs"), filepath.Join(dst, "blobs"), images)
}

// copySignatureImages takes along the one kind of blob that is a setting. The
// images are part of the signatures, nothing else of the blob store comes along.
func copySignatureImages(src, dst string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	from, err := blob.New(src)
	if err != nil {
		return err
	}
	to, err := blob.New(dst)
	if err != nil {
		return err
	}
	for _, h := range hashes {
		r, err := from.Open(h)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, blob.ErrNotFound) {
			continue // a signature with a missing image stays as it was
		}
		if err != nil {
			return err
		}
		_, err = to.Put(r)
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// cleanSnapshot writes a consistent copy of src to dst and empties everything
// that classes does not keep, so that no byte of it remains in the file.
func cleanSnapshot(ctx context.Context, src, dst string, classes map[string]TableClass, inspect func(*sql.Conn) error) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := vacuumInto(ctx, src, dst); err != nil {
		return err
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dst)+"?_pragma=foreign_keys(1)&_pragma=secure_delete(1)&_pragma=journal_mode(DELETE)")
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := purgeTables(ctx, conn, classes); err != nil {
		return err
	}
	if inspect != nil {
		if err := inspect(conn); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `VACUUM`)
	return err
}

func purgeTables(ctx context.Context, conn *sql.Conn, classes map[string]TableClass) error {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var purge []string
	hasIndex := false
	for _, name := range tables {
		switch classes[name] {
		case TableKeep:
		case TablePurge:
			purge = append(purge, name)
		case TableIndex:
			hasIndex = true
		default:
			return fmt.Errorf("%w: %s", ErrUnclassifiedTable, name)
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The triggers of fts_docs would feed every deleted row back to the index;
	// the index is emptied as a whole instead. They are part of the schema and
	// come back unchanged.
	var triggers []string
	if hasIndex {
		rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'fts_docs'`)
		if err != nil {
			return err
		}
		var names []string
		for rows.Next() {
			var name, def string
			if err := rows.Scan(&name, &def); err != nil {
				rows.Close()
				return err
			}
			names = append(names, name)
			triggers = append(triggers, def)
		}
		rows.Close()
		for _, name := range names {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+quoteIdent(name)); err != nil {
				return err
			}
		}
	}
	for _, name := range purge {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+quoteIdent(name)); err != nil {
			return fmt.Errorf("empty %s: %w", name, err)
		}
	}
	if hasIndex {
		for _, q := range []string{`INSERT INTO fts_items(fts_items) VALUES('delete-all')`, `INSERT INTO fts_trigram(fts_trigram) VALUES('delete-all')`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("empty the search index: %w", err)
			}
		}
		for _, def := range triggers {
			if _, err := tx.ExecContext(ctx, def); err != nil {
				return err
			}
		}
		// Only the mail stores have an index, and sync positions.
		for _, q := range resetSyncState {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
	}
	return tx.Commit()
}

// resetSyncState makes the restored accounts fetch everything again: a kept
// sync position would tell them they already have the mail. Learned counters
// and the search history stored among the settings go as well.
var resetSyncState = []string{
	`UPDATE folders SET sync_state = NULL, unread = 0, total = 0`,
	`UPDATE calendars SET sync_token = NULL`,
	`UPDATE calendar_meta SET ctag = NULL, last_sync_at = NULL, last_error = NULL`,
	`UPDATE addressbooks SET sync_token = NULL`,
	`UPDATE addressbook_meta SET ctag = NULL, last_sync_at = NULL, last_error = NULL`,
	`UPDATE messenger_accounts SET state = NULL, last_error = NULL, synced_at = NULL`,
	`UPDATE search_folders SET cached_total = NULL, cached_unread = NULL, cached_at = NULL`,
	`DELETE FROM comfort_kv WHERE key LIKE 'spam.%' OR key LIKE 'category.%'`,
	`DELETE FROM app_settings WHERE key = 'search.history' OR key LIKE 'eas.state.%' OR key LIKE 'server.%'`,
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// copyPrivate copies one file, readable by the owner only. Files replaced by
// rename (vault) are read consistently through the open handle.
func copyPrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
