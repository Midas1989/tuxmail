// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ==== Workflows ==============================================================

// WorkflowRecord is a stored workflow definition (KONZEPT 8.5). The YAML text
// is the source of truth; Name and Enabled are denormalized for listing.
type WorkflowRecord struct {
	ID      string
	Name    string
	Enabled bool
	YAML    string
	// ExternalConfirmed records that the user confirmed the workflow's actions
	// with external effect (auto-reply, forward, webhook ...).
	ExternalConfirmed bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const workflowCols = `id, name, enabled, yaml, external_confirmed, created_at, updated_at`

func scanWorkflow(sc scanner) (WorkflowRecord, error) {
	var (
		w        WorkflowRecord
		en, ext  int
		cre, upd int64
	)
	err := sc.Scan(&w.ID, &w.Name, &en, &w.YAML, &ext, &cre, &upd)
	w.Enabled, w.ExternalConfirmed = en != 0, ext != 0
	w.CreatedAt, w.UpdatedAt = time.Unix(cre, 0).UTC(), time.Unix(upd, 0).UTC()
	return w, err
}

// PutWorkflow inserts or replaces a workflow. CreatedAt is kept for existing
// rows; UpdatedAt is set to now.
func (s *Store) PutWorkflow(ctx context.Context, w WorkflowRecord) error {
	if w.ID == "" {
		return errors.New("store: workflow id is empty")
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflows(`+workflowCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, enabled = excluded.enabled, yaml = excluded.yaml,
		  external_confirmed = excluded.external_confirmed, updated_at = excluded.updated_at`,
		w.ID, w.Name, boolInt(w.Enabled), w.YAML, boolInt(w.ExternalConfirmed), now, now)
	if err != nil {
		return fmt.Errorf("store: put workflow %s: %w", w.ID, err)
	}
	return nil
}

// GetWorkflow returns the workflow with the given id.
func (s *Store) GetWorkflow(ctx context.Context, id string) (WorkflowRecord, error) {
	w, err := scanWorkflow(s.db.QueryRowContext(ctx, `SELECT `+workflowCols+` FROM workflows WHERE id = ?`, id))
	return w, notFound(err)
}

// ListWorkflows returns all workflows ordered by name.
func (s *Store) ListWorkflows(ctx context.Context) ([]WorkflowRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workflowCols+` FROM workflows ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanWorkflow)
}

// SetWorkflowEnabled changes the enabled flag (and the confirmation flag).
func (s *Store) SetWorkflowEnabled(ctx context.Context, id string, enabled, externalConfirmed bool) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE workflows SET enabled = ?, external_confirmed = ?, updated_at = ? WHERE id = ?`,
		boolInt(enabled), boolInt(externalConfirmed), time.Now().Unix(), id))
}

// DeleteWorkflow removes a workflow together with its run log.
func (s *Store) DeleteWorkflow(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := checkAffected(tx.ExecContext(ctx, `DELETE FROM workflows WHERE id = ?`, id)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM workflow_runs WHERE workflow_id = ?`, id)
		return err
	})
}

// ==== Workflow runs ==========================================================

// RunStatus is the state of a workflow run.
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunSkipped   RunStatus = "skipped"
)

// WorkflowRun is an entry of the workflow execution log.
type WorkflowRun struct {
	ID         int64
	WorkflowID string
	EventID    string
	EventType  string
	EventRef   string
	Status     RunStatus
	StepsJSON  string // JSON array of step results, owned by the workflow engine
	Error      string
	OpID       int64 // operations queue entry executing this run (0 = synchronous run)
	Attempts   int
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

const runCols = `id, workflow_id, event_id, event_type, event_ref, status, steps_json, error, op_id, attempts, created_at, started_at, finished_at`

func scanRun(sc scanner) (WorkflowRun, error) {
	var (
		r                    WorkflowRun
		evID, ref, errMsg    sql.NullString
		opID, started, finis sql.NullInt64
		created              int64
	)
	err := sc.Scan(&r.ID, &r.WorkflowID, &evID, &r.EventType, &ref, &r.Status, &r.StepsJSON, &errMsg,
		&opID, &r.Attempts, &created, &started, &finis)
	r.EventID, r.EventRef, r.Error, r.OpID = evID.String, ref.String, errMsg.String, opID.Int64
	r.CreatedAt = time.Unix(created, 0).UTC()
	r.StartedAt, r.FinishedAt = timeFromNull(started), timeFromNull(finis)
	return r, err
}

func insertRun(ctx context.Context, q querier, r WorkflowRun) (int64, error) {
	if r.StepsJSON == "" {
		r.StepsJSON = "[]"
	}
	if r.Status == "" {
		r.Status = RunQueued
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	res, err := q.ExecContext(ctx, `INSERT INTO workflow_runs(workflow_id, event_id, event_type, event_ref, status, steps_json,
		error, op_id, attempts, created_at, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.WorkflowID, nullString(r.EventID), r.EventType, nullString(r.EventRef), r.Status, r.StepsJSON,
		nullString(r.Error), nullID(r.OpID), r.Attempts, r.CreatedAt.Unix(), unixOrNull(r.StartedAt), unixOrNull(r.FinishedAt))
	if err != nil {
		return 0, fmt.Errorf("store: insert workflow run: %w", err)
	}
	return res.LastInsertId()
}

// InsertWorkflowRun appends a run log entry (used for synchronous runs that
// do not go through the operations queue) and returns its id.
func (s *Store) InsertWorkflowRun(ctx context.Context, r WorkflowRun) (int64, error) {
	return insertRun(ctx, s.db, r)
}

// EnqueueWorkflowRun atomically creates a queued run log entry and the
// operations queue entry that executes it. payload is called with the new run
// id and returns the op payload. The returned run has ID and OpID set.
func (s *Store) EnqueueWorkflowRun(ctx context.Context, r WorkflowRun, opKind string, payload func(runID int64) (string, error)) (WorkflowRun, error) {
	r.Status = RunQueued
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		id, err := insertRun(ctx, tx, r)
		if err != nil {
			return err
		}
		p, err := payload(id)
		if err != nil {
			return err
		}
		now := r.CreatedAt.Unix() // due immediately (relative to the caller's clock)
		res, err := tx.ExecContext(ctx, `INSERT INTO ops(account_id, kind, payload, next_try_at, created_at) VALUES (NULL, ?, ?, ?, ?)`,
			opKind, p, now, now)
		if err != nil {
			return fmt.Errorf("store: enqueue workflow op: %w", err)
		}
		opID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET op_id = ? WHERE id = ?`, opID, id); err != nil {
			return err
		}
		r.ID, r.OpID = id, opID
		return nil
	})
	return r, err
}

// UpdateWorkflowRun stores status, steps, error, attempts and timestamps of a run.
func (s *Store) UpdateWorkflowRun(ctx context.Context, r WorkflowRun) error {
	if r.StepsJSON == "" {
		r.StepsJSON = "[]"
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE workflow_runs SET status = ?, steps_json = ?, error = ?, attempts = ?,
		started_at = ?, finished_at = ? WHERE id = ?`,
		r.Status, r.StepsJSON, nullString(r.Error), r.Attempts, unixOrNull(r.StartedAt), unixOrNull(r.FinishedAt), r.ID))
}

// GetWorkflowRun returns a run log entry.
func (s *Store) GetWorkflowRun(ctx context.Context, id int64) (WorkflowRun, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE id = ?`, id))
	return r, notFound(err)
}

// ListWorkflowRuns returns the newest runs of a workflow (all workflows if
// workflowID is ""), at most limit entries (100 if limit <= 0).
func (s *Store) ListWorkflowRuns(ctx context.Context, workflowID string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE ?1 = '' OR workflow_id = ?1
		ORDER BY id DESC LIMIT ?2`, workflowID, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanRun)
}

// PurgeWorkflowRuns deletes finished runs created before the given time.
func (s *Store) PurgeWorkflowRuns(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workflow_runs WHERE status IN ('succeeded', 'failed', 'skipped') AND created_at < ?`,
		before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClaimNextOpOfKind works like ClaimNextOp but only considers operations of
// the given kinds. This lets the workflow engine run its own worker without
// taking operations that belong to the sync engine.
func (s *Store) ClaimNextOpOfKind(ctx context.Context, now time.Time, kinds ...string) (op Op, ok bool, err error) {
	if len(kinds) == 0 {
		return s.ClaimNextOp(ctx, now)
	}
	args := []any{now.Unix()}
	ph := make([]string, len(kinds))
	for i, k := range kinds {
		ph[i] = "?"
		args = append(args, k)
	}
	op, err = scanOp(s.db.QueryRowContext(ctx, `UPDATE ops SET state = 'running', attempts = attempts + 1
		WHERE id = (
			SELECT o.id FROM ops o
			WHERE o.state = 'pending' AND o.next_try_at <= ? AND o.kind IN (`+strings.Join(ph, ",")+`)
			  AND NOT EXISTS (SELECT 1 FROM ops p WHERE p.account_id = o.account_id AND p.id <> o.id
			                  AND (p.state = 'running' OR (p.state = 'pending' AND p.id < o.id)))
			ORDER BY o.next_try_at, o.id LIMIT 1)
		RETURNING `+opCols, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Op{}, false, nil
	}
	if err != nil {
		return Op{}, false, fmt.Errorf("store: claim op: %w", err)
	}
	return op, true, nil
}

// ==== Follow-ups ("waiting for reply", KONZEPT 8.12) =========================

// FollowupState is the state of a follow-up reminder.
type FollowupState string

const (
	FollowupPending   FollowupState = "pending"
	FollowupFired     FollowupState = "fired"
	FollowupCancelled FollowupState = "cancelled"
)

// Followup is a "remind me if there is no reply" entry.
type Followup struct {
	ID             int64
	AccountID      int64 // 0 = none
	MessageKey     string
	ConversationID int64 // 0 = none
	Subject        string
	Recipients     []string
	SentAt         time.Time
	DueAt          time.Time
	State          FollowupState
	ReplyKey       string // message key of the reply that cancelled the reminder
	CreatedAt      time.Time
	ClosedAt       time.Time
}

const followupCols = `id, account_id, message_key, conversation_id, subject, recipients_json, sent_at, due_at, state, reply_key, created_at, closed_at`

func scanFollowup(sc scanner) (Followup, error) {
	var (
		f                  Followup
		acc, conv, closed  sql.NullInt64
		subj, reply        sql.NullString
		recips             string
		sent, due, created int64
	)
	err := sc.Scan(&f.ID, &acc, &f.MessageKey, &conv, &subj, &recips, &sent, &due, &f.State, &reply, &created, &closed)
	if err != nil {
		return f, err
	}
	f.AccountID, f.ConversationID, f.Subject, f.ReplyKey = acc.Int64, conv.Int64, subj.String, reply.String
	f.SentAt, f.DueAt, f.CreatedAt = time.Unix(sent, 0).UTC(), time.Unix(due, 0).UTC(), time.Unix(created, 0).UTC()
	f.ClosedAt = timeFromNull(closed)
	if err := json.Unmarshal([]byte(recips), &f.Recipients); err != nil {
		return f, fmt.Errorf("store: followup %d: bad recipients: %w", f.ID, err)
	}
	return f, nil
}

// UpsertFollowup creates a pending reminder for f.MessageKey or, if one is
// already pending, updates its due date and details. It returns the id.
func (s *Store) UpsertFollowup(ctx context.Context, f Followup) (int64, error) {
	if f.MessageKey == "" {
		return 0, errors.New("store: followup without message key")
	}
	recips, err := json.Marshal(nonNil(f.Recipients))
	if err != nil {
		return 0, err
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM followups WHERE message_key = ? AND state = 'pending'`, f.MessageKey).Scan(&id)
		switch {
		case err == nil:
			_, err = tx.ExecContext(ctx, `UPDATE followups SET account_id = ?, conversation_id = ?, subject = ?, recipients_json = ?,
				sent_at = ?, due_at = ? WHERE id = ?`,
				nullID(f.AccountID), nullID(f.ConversationID), nullString(f.Subject), string(recips),
				f.SentAt.Unix(), f.DueAt.Unix(), id)
			return err
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx, `INSERT INTO followups(account_id, message_key, conversation_id, subject, recipients_json,
				sent_at, due_at, state, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
				nullID(f.AccountID), f.MessageKey, nullID(f.ConversationID), nullString(f.Subject), string(recips),
				f.SentAt.Unix(), f.DueAt.Unix(), f.CreatedAt.Unix())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		default:
			return err
		}
	})
	if err != nil {
		return 0, fmt.Errorf("store: upsert followup: %w", err)
	}
	return id, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// GetFollowup returns a follow-up by id.
func (s *Store) GetFollowup(ctx context.Context, id int64) (Followup, error) {
	f, err := scanFollowup(s.db.QueryRowContext(ctx, `SELECT `+followupCols+` FROM followups WHERE id = ?`, id))
	return f, notFound(err)
}

// ListFollowups returns follow-ups in the given state (all if "") ordered by due date.
func (s *Store) ListFollowups(ctx context.Context, state FollowupState) ([]Followup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+followupCols+` FROM followups WHERE ?1 = '' OR state = ?1 ORDER BY due_at, id`,
		string(state))
	if err != nil {
		return nil, err
	}
	return collect(rows, scanFollowup)
}

// DueFollowups returns pending follow-ups with due_at <= now (at most limit; 100 if <= 0).
func (s *Store) DueFollowups(ctx context.Context, now time.Time, limit int) ([]Followup, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+followupCols+` FROM followups WHERE state = 'pending' AND due_at <= ?
		ORDER BY due_at, id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanFollowup)
}

// NextFollowupDue returns the earliest due date of all pending follow-ups.
func (s *Store) NextFollowupDue(ctx context.Context) (due time.Time, ok bool, err error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(due_at) FROM followups WHERE state = 'pending'`).Scan(&v); err != nil {
		return time.Time{}, false, err
	}
	if !v.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(v.Int64, 0).UTC(), true, nil
}

// MarkFollowupFired closes a pending follow-up as fired. It returns
// ErrNotFound if the follow-up is not pending (e.g. cancelled concurrently).
func (s *Store) MarkFollowupFired(ctx context.Context, id int64, now time.Time) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE followups SET state = 'fired', closed_at = ? WHERE id = ? AND state = 'pending'`,
		now.Unix(), id))
}

// CancelFollowups cancels the pending follow-up of messageKey (if any),
// recording replyKey as the reason. It returns the number of cancelled rows.
func (s *Store) CancelFollowups(ctx context.Context, messageKey, replyKey string, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE followups SET state = 'cancelled', reply_key = ?, closed_at = ?
		WHERE message_key = ? AND state = 'pending'`, nullString(replyKey), now.Unix(), messageKey)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CancelFollowup cancels a single pending follow-up by id.
func (s *Store) CancelFollowup(ctx context.Context, id int64, now time.Time) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE followups SET state = 'cancelled', closed_at = ? WHERE id = ? AND state = 'pending'`,
		now.Unix(), id))
}

// ==== Out-of-office ("Abwesenheitsnotiz") ====================================

// GetVacationSettings returns the stored settings JSON of an id.
func (s *Store) GetVacationSettings(ctx context.Context, id string) (string, error) {
	var js string
	err := s.db.QueryRowContext(ctx, `SELECT json FROM vacation_settings WHERE id = ?`, id).Scan(&js)
	return js, notFound(err)
}

// PutVacationSettings stores the settings JSON of an id.
func (s *Store) PutVacationSettings(ctx context.Context, id, js string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO vacation_settings(id, json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET json = excluded.json, updated_at = excluded.updated_at`, id, js, time.Now().Unix())
	return err
}

// ClaimVacationReply atomically decides whether an automatic reply to sender
// may be sent in scope: it returns true and records now if there was no reply
// within window, false otherwise.
func (s *Store) ClaimVacationReply(ctx context.Context, scope, sender string, now time.Time, window time.Duration) (bool, error) {
	sender = strings.ToLower(strings.TrimSpace(sender))
	var ok bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var last int64
		err := tx.QueryRowContext(ctx, `SELECT last_reply_at FROM vacation_replies WHERE scope = ? AND sender = ?`, scope, sender).Scan(&last)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case now.Unix()-last < int64(window/time.Second):
			return nil
		}
		ok = true
		_, err = tx.ExecContext(ctx, `INSERT INTO vacation_replies(scope, sender, last_reply_at) VALUES (?, ?, ?)
			ON CONFLICT(scope, sender) DO UPDATE SET last_reply_at = excluded.last_reply_at`, scope, sender, now.Unix())
		return err
	})
	return ok, err
}

// ForgetVacationReply removes a sent-to entry (e.g. after a failed send).
func (s *Store) ForgetVacationReply(ctx context.Context, scope, sender string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM vacation_replies WHERE scope = ? AND sender = ?`,
		scope, strings.ToLower(strings.TrimSpace(sender)))
	return err
}

// ResetVacationReplies clears the sent-to log (all scopes if scope is "").
// Call it when a new absence period starts.
func (s *Store) ResetVacationReplies(ctx context.Context, scope string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM vacation_replies WHERE ?1 = '' OR scope = ?1`, scope)
	return err
}
