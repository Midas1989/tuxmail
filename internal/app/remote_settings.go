// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/signature"
)

// Settings synchronisation in the mode "Auf diesem Gerät" (KONZEPT 15.8):
// signatures, workflows, saved searches and view settings stay in step with
// the server. Every thing is compared with its state at the last sync, per
// side: changed on one side is copied, changed on both means the newer state
// wins (and is noted in the log). Account passwords are not part of this:
// those only ever come sealed to the client key, at set-up.

const (
	keySettingsMap       = "server.settings.map"
	keySettingsLog       = "server.settings.log"
	keySettingsAttention = "server.settings.attention"
	maxSettingsLog       = 50

	settingsKindSignature = "signature"
	settingsKindWorkflow  = "workflow"
	settingsKindProfile   = "searchProfile"
	settingsKindView      = "view"

	// massDeleteMin: this many deletions at once are not carried out without asking.
	massDeleteMin = 3
)

// settingsPair links one thing on both sides; the hashes are its state at the last sync.
type settingsPair struct {
	Local      string `json:"local,omitempty"`
	Remote     string `json:"remote,omitempty"`
	LocalHash  string `json:"localHash,omitempty"`
	RemoteHash string `json:"remoteHash,omitempty"`
	// Excluded: deselected during the set-up, the user does not want it here.
	Excluded bool `json:"excluded,omitempty"`
	// Conflict: a local rule and a server rule with the same id and different
	// content met on the first sync. Nothing happens until the user decides;
	// Prefer holds the decision (local | server) until it is carried out.
	Conflict bool   `json:"conflict,omitempty"`
	Prefer   string `json:"prefer,omitempty"`
	// Missing: absent from the last answer of the server, without a record of
	// deleting it. Once is no reason to act: only absent again it is restored.
	Missing bool `json:"missing,omitempty"`
}

type settingsMap map[string][]settingsPair

// SettingsLogEntry is one line of the synchronisation log in the status.
type SettingsLogEntry struct {
	At       string `json:"at"`
	Kind     string `json:"kind"` // signature | workflow | searchProfile | view
	Name     string `json:"name"`
	Action   string `json:"action"` // toServer | fromServer | deletedOnServer | deletedHere | skipped | error
	Conflict bool   `json:"conflict,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// PendingWorkflow is a rule from the server waiting for the user's release. It
// is switched off here until then, whatever it was before.
type PendingWorkflow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsNew      bool   `json:"isNew"`
	WasEnabled bool   `json:"wasEnabled,omitempty"`
	Trigger    string `json:"trigger,omitempty"`
	// What the rule does, line by line with all parameters: as it was when the
	// user last released it, and now.
	Before   []string `json:"before,omitempty"`
	After    []string `json:"after"`
	External bool     `json:"external,omitempty"`
	// Hash of what the user is looking at: releasing a newer version needs a new look.
	Hash string `json:"hash"`
}

// WorkflowConflict: same id, different content, on the first sync.
type WorkflowConflict struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	LocalAfter  []string `json:"local"`
	ServerAfter []string `json:"server"`
}

// DeleteDecision: many deletions at once wait for a yes or no.
type DeleteDecision struct {
	Kind  string   `json:"kind"`
	Names []string `json:"names"`
	// Decided: "" (open), "approve" or "reject"; carried out by the next sync.
	Decided string `json:"decided,omitempty"`
}

// SettingsAttention is what the synchronisation will not do without the user.
type SettingsAttention struct {
	PendingWorkflows []PendingWorkflow  `json:"pendingWorkflows"`
	Conflicts        []WorkflowConflict `json:"conflicts"`
	Deletes          []DeleteDecision   `json:"deletes"`
}

func emptyAttention() SettingsAttention {
	return SettingsAttention{PendingWorkflows: []PendingWorkflow{}, Conflicts: []WorkflowConflict{}, Deletes: []DeleteDecision{}}
}

func (a *App) loadAttention(ctx context.Context) SettingsAttention {
	at := emptyAttention()
	if _, err := a.st.GetAppSetting(ctx, keySettingsAttention, &at); err != nil {
		a.log.Warn("app: reading the synchronisation state failed", "err", err)
	}
	return at
}

// settingsAttentionNow leaves out rules the user meanwhile switched on or deleted.
func (a *App) settingsAttentionNow(ctx context.Context) SettingsAttention {
	at := a.loadAttention(ctx)
	at.PendingWorkflows = slices.DeleteFunc(at.PendingWorkflows, func(p PendingWorkflow) bool {
		w, ok := a.wf.Get(p.ID)
		return !ok || w.Enabled
	})
	return at
}

// noteSaved: it exists (again), an older deletion no longer speaks for it.
func (a *App) noteSaved(ctx context.Context, kind, id string) {
	if err := a.st.RemoveSettingsTombstone(ctx, kind, id); err != nil {
		a.log.Warn("app: updating the record of deletions failed", "kind", kind, "err", err)
	}
}

// noteDeleted records a deletion on purpose, for the other side of the
// synchronisation. Written on the server too: a device learns of it there.
func (a *App) noteDeleted(ctx context.Context, kind, id string) {
	if err := a.st.AddSettingsTombstone(ctx, kind, id, a.now()); err != nil {
		a.log.Warn("app: recording a deletion failed", "kind", kind, "err", err)
	}
}

// ServerWorkflowApprove switches a waiting rule on: the user has seen what it does.
func (a *App) ServerWorkflowApprove(ctx context.Context, id string) error {
	at := a.loadAttention(ctx)
	for _, p := range at.PendingWorkflows {
		if p.ID == id {
			return a.ServerWorkflowApproveVersion(ctx, id, p.Hash)
		}
	}
	return Errorf(http.StatusConflict, "changed", "this rule is not waiting for approval")
}

// ServerWorkflowApproveVersion releases exactly the version the user looked at.
func (a *App) ServerWorkflowApproveVersion(ctx context.Context, id, hash string) error {
	at := a.loadAttention(ctx)
	i := slices.IndexFunc(at.PendingWorkflows, func(p PendingWorkflow) bool { return p.ID == id })
	if i < 0 || at.PendingWorkflows[i].Hash != hash {
		return Errorf(http.StatusConflict, "changed", "this rule has changed meanwhile; please look at it again")
	}
	if _, err := a.SetWorkflowEnabled(ctx, id, true, true); err != nil {
		return err
	}
	return a.ServerWorkflowDismiss(ctx, id)
}

// ServerWorkflowDismiss leaves a waiting rule switched off and stops asking.
func (a *App) ServerWorkflowDismiss(ctx context.Context, id string) error {
	at := a.loadAttention(ctx)
	at.PendingWorkflows = slices.DeleteFunc(at.PendingWorkflows, func(p PendingWorkflow) bool { return p.ID == id })
	if err := a.st.SetAppSetting(ctx, keySettingsAttention, at); err != nil {
		return err
	}
	a.connNotify()
	return nil
}

// ServerWorkflowResolve decides a conflict of the first sync.
func (a *App) ServerWorkflowResolve(ctx context.Context, id string, keepLocal bool) error {
	a.cr.syncMu.Lock()
	m := settingsMap{}
	_, err := a.st.GetAppSetting(ctx, keySettingsMap, &m)
	if err == nil {
		for i := range m[settingsKindWorkflow] {
			p := &m[settingsKindWorkflow][i]
			if p.Conflict && p.Remote == id {
				p.Conflict, p.Prefer = false, "server"
				if keepLocal {
					p.Prefer = "local"
				}
			}
		}
		err = a.st.SetAppSetting(ctx, keySettingsMap, m)
	}
	if err == nil {
		at := a.loadAttention(ctx)
		at.Conflicts = slices.DeleteFunc(at.Conflicts, func(c WorkflowConflict) bool { return c.ID == id })
		err = a.st.SetAppSetting(ctx, keySettingsAttention, at)
	}
	a.cr.syncMu.Unlock()
	if err != nil {
		return err
	}
	_, err = a.ServerSyncNow(ctx)
	return err
}

// ServerSettingsDeletes decides on many deletions at once. A no brings the
// things back on the side that lost them.
func (a *App) ServerSettingsDeletes(ctx context.Context, kind string, approve bool) error {
	a.cr.syncMu.Lock()
	at := a.loadAttention(ctx)
	for i := range at.Deletes {
		if at.Deletes[i].Kind == kind {
			at.Deletes[i].Decided = "reject"
			if approve {
				at.Deletes[i].Decided = "approve"
			}
		}
	}
	err := a.st.SetAppSetting(ctx, keySettingsAttention, at)
	a.cr.syncMu.Unlock()
	if err != nil {
		return err
	}
	_, err = a.ServerSyncNow(ctx)
	return err
}

func (a *App) settingsLog(ctx context.Context) []SettingsLogEntry {
	out := []SettingsLogEntry{}
	if _, err := a.st.GetAppSetting(ctx, keySettingsLog, &out); err != nil {
		a.log.Warn("app: reading the synchronisation log failed", "err", err)
	}
	return out
}

// syncObj is one thing as listed on one side.
type syncObj struct {
	ID      string
	Name    string
	Hash    string
	Updated int64
	Data    any
}

// settingsKind connects one kind of thing on both sides.
type settingsKind struct {
	name      string
	matchByID bool
	// protectFirst: on the first sync a local thing is never overwritten; a
	// difference is shown as a conflict (rules can act on mail).
	protectFirst bool
	listLocal    func(ctx context.Context) (map[string]syncObj, error)
	listRemote   func(ctx context.Context) (map[string]syncObj, error)
	// put writes the other side's data; id "" creates. They return the id.
	putLocal  func(ctx context.Context, id string, from syncObj) (string, error)
	putRemote func(ctx context.Context, id string, from syncObj) (string, error)
	delLocal  func(ctx context.Context, id string) error
	delRemote func(ctx context.Context, id string) error
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// errSkipSync: this thing cannot be carried over; note it, leave it alone.
var errSkipSync = errors.New("skip")

// syncSettings runs one synchronisation. It returns the new log lines.
func (a *App) syncSettings(ctx context.Context) ([]SettingsLogEntry, error) {
	a.cr.syncMu.Lock()
	defer a.cr.syncMu.Unlock()
	return a.syncSettingsLocked(ctx)
}

// syncSettingsIfFree is the timer's way in: if one runs right now, this one is
// not due. It must not queue up and run a second time just after.
func (a *App) syncSettingsIfFree(ctx context.Context) ([]SettingsLogEntry, error) {
	if !a.cr.syncMu.TryLock() {
		return nil, nil
	}
	defer a.cr.syncMu.Unlock()
	return a.syncSettingsLocked(ctx)
}

func (a *App) syncSettingsLocked(ctx context.Context) ([]SettingsLogEntry, error) {
	c := a.connCurrent()
	if c == nil || c.Mode != ConnModeLocal || c.Expired {
		return nil, nil
	}
	m := settingsMap{}
	if _, err := a.st.GetAppSetting(ctx, keySettingsMap, &m); err != nil {
		return nil, err
	}
	var log []SettingsLogEntry
	note := func(e SettingsLogEntry) {
		e.At = a.now().UTC().Format("2006-01-02T15:04:05Z")
		log = append(log, e)
	}
	at := a.loadAttention(ctx)
	before, _ := json.Marshal(at)
	tombs, err := a.remoteTombstones(ctx, c)
	if err != nil {
		if errors.Is(err, errRemoteRejected) {
			a.connMarkExpired(ctx)
			return nil, nil
		}
		if errors.Is(err, errConnNetwork) || errors.Is(err, errConnLocked) {
			a.connMarkOffline(err)
		}
		return nil, err
	}
	localTombs, err := a.st.ListSettingsTombstones(ctx)
	if err != nil {
		return nil, err
	}
	var fatalErr error
	failed := false
	for _, k := range a.settingsKinds(c, &at) {
		pairs, kindFailed, err := a.reconcile(ctx, k, m[k.name], note, &at, localTombs[k.name], tombs[k.name])
		failed = failed || kindFailed
		if err != nil {
			failed = true
			if connSyncFatal(err) {
				fatalErr = err
				break
			}
			// An answer that makes no sense changes nothing here.
			note(SettingsLogEntry{Kind: k.name, Action: "error", Detail: "not synchronised this time"})
			a.log.Warn("app: synchronising settings failed", "kind", k.name, "err", err)
			continue
		}
		m[k.name] = pairs
	}
	if gone, err := a.saveSyncState(ctx, m, at, before, log); err != nil || gone {
		return log, err
	}
	return a.settingsSyncDone(ctx, log, fatalErr, failed)
}

// saveSyncState stores the result. Disconnected meanwhile: nothing of this
// connection may be written back (gone).
func (a *App) saveSyncState(ctx context.Context, m settingsMap, at SettingsAttention, before []byte, log []SettingsLogEntry) (gone bool, err error) {
	a.cr.persistMu.Lock()
	defer a.cr.persistMu.Unlock()
	if a.connCurrent() == nil {
		return true, nil
	}
	if err := a.st.SetAppSetting(ctx, keySettingsMap, m); err != nil {
		return false, err
	}
	if after, _ := json.Marshal(at); !bytes.Equal(before, after) {
		if err := a.st.SetAppSetting(ctx, keySettingsAttention, at); err != nil {
			return false, err
		}
	}
	if len(log) > 0 {
		all := a.settingsLog(ctx)
		// A setting that keeps failing fills the log once, not every minute.
		for _, e := range log {
			if e.Action == "error" && slices.ContainsFunc(all, func(o SettingsLogEntry) bool { return o.Action == "error" && o.Kind == e.Kind && o.Name == e.Name }) {
				continue
			}
			all = append(all, e)
		}
		if len(all) > maxSettingsLog {
			all = all[len(all)-maxSettingsLog:]
		}
		if err := a.st.SetAppSetting(ctx, keySettingsLog, all); err != nil {
			return false, err
		}
		a.hub.Publish(Event{Type: EventSettingsSynced})
	}
	return false, nil
}

// settingsSyncDone reports the end of a synchronisation to the rest of the app.
func (a *App) settingsSyncDone(ctx context.Context, log []SettingsLogEntry, fatalErr error, failed bool) ([]SettingsLogEntry, error) {
	switch {
	case errors.Is(fatalErr, errRemoteRejected):
		a.connMarkExpired(ctx)
		return log, nil
	case fatalErr != nil:
		a.connMarkOffline(fatalErr)
		return log, fatalErr
	case failed:
		// The time stays: the next check tries again what failed.
		a.connNotify()
		return log, nil
	}
	if err := a.connUpdate(ctx, func(c *serverConnection) { c.LastSettingsSync = a.now().Unix() }); err != nil {
		return log, err
	}
	a.connNotify()
	return log, nil
}

// remoteTombstones reads the server's record of deletions. A server without
// the route has no such record: nothing is deleted here on its word.
func (a *App) remoteTombstones(ctx context.Context, c *serverConnection) (map[string][]string, error) {
	out := map[string][]string{}
	err := a.remoteAPI(ctx, c, http.MethodGet, "/api/settings-sync/tombstones", nil, &out)
	var re *remoteError
	if errors.As(err, &re) && re.Status == http.StatusNotFound {
		return map[string][]string{}, nil
	}
	return out, err
}

// connSyncFatal: no point in going on, and nothing may be concluded from what is missing.
func connSyncFatal(err error) bool {
	return errors.Is(err, errConnNetwork) || errors.Is(err, errConnLocked) || errors.Is(err, errConnNoToken) || errors.Is(err, errRemoteRejected)
}

func (a *App) reconcile(ctx context.Context, k settingsKind, old []settingsPair, note func(SettingsLogEntry), at *SettingsAttention, localTombs, remoteTombs []string) ([]settingsPair, bool, error) {
	local, err := k.listLocal(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("local list: %w", err)
	}
	remote, err := k.listRemote(ctx)
	if err != nil {
		return nil, false, err
	}
	var out []settingsPair
	usedL, usedR := map[string]bool{}, map[string]bool{}
	failedAny := false
	// Pairs that failed keep their old state, so the next sync sees the change again.
	retry := map[int]bool{}
	logErr := func(name string, err error) (skipped bool) {
		if errors.Is(err, errSkipSync) {
			note(SettingsLogEntry{Kind: k.name, Name: name, Action: "skipped", Detail: "refers to something that does not exist on the other side"})
			return true
		}
		failedAny = true
		note(SettingsLogEntry{Kind: k.name, Name: name, Action: "error", Detail: "not synchronised this time"})
		a.log.Warn("app: synchronising a setting failed", "kind", k.name, "err", err)
		return false
	}
	fatal := connSyncFatal
	pull := func(p *settingsPair, r syncObj, conflict bool) error {
		id, err := k.putLocal(ctx, p.Local, r)
		if err != nil {
			return err
		}
		p.Local = id
		note(SettingsLogEntry{Kind: k.name, Name: r.Name, Action: "fromServer", Conflict: conflict})
		return nil
	}
	push := func(p *settingsPair, l syncObj, conflict bool) error {
		id, err := k.putRemote(ctx, p.Remote, l)
		if err != nil {
			return err
		}
		p.Remote = id
		note(SettingsLogEntry{Kind: k.name, Name: l.Name, Action: "toServer", Conflict: conflict})
		return nil
	}
	// Local wins a tie with an unknown time: it is what the user has in front of them.
	localNewer := func(l, r syncObj) bool { return l.Updated > r.Updated || l.Updated == 0 }

	// Deletions: only with a record of it, and many at once only after asking.
	decision := ""
	di := slices.IndexFunc(at.Deletes, func(d DeleteDecision) bool { return d.Kind == k.name })
	if di >= 0 {
		decision = at.Deletes[di].Decided
	}
	isDeleteLocal := func(p settingsPair) bool { // deleted on the server, delete here
		_, hasL := local[p.Local]
		_, hasR := remote[p.Remote]
		return !p.Excluded && !p.Conflict && hasL && !hasR && local[p.Local].Hash == p.LocalHash && slices.Contains(remoteTombs, p.Remote)
	}
	isDeleteRemote := func(p settingsPair) bool { // deleted here, delete on the server
		_, hasL := local[p.Local]
		_, hasR := remote[p.Remote]
		return !p.Excluded && !p.Conflict && !hasL && hasR && remote[p.Remote].Hash == p.RemoteHash && slices.Contains(localTombs, p.Local)
	}
	var planned []string
	for _, p := range old {
		switch {
		case isDeleteLocal(p):
			planned = append(planned, local[p.Local].Name)
		case isDeleteRemote(p):
			planned = append(planned, remote[p.Remote].Name)
		}
	}
	hold := len(planned) >= massDeleteMin && decision == ""
	if hold {
		if di < 0 {
			at.Deletes = append(at.Deletes, DeleteDecision{Kind: k.name, Names: planned})
		} else {
			at.Deletes[di].Names = planned
		}
	}
	reject := decision == "reject"
	// A decision stays until it has been carried out completely: after a "no"
	// and a failed restore, the rest must not fall below the threshold and go.
	finishDecision := func() {
		if !hold && !failedAny {
			at.Deletes = slices.DeleteFunc(at.Deletes, func(d DeleteDecision) bool { return d.Kind == k.name })
		}
	}

	// Known pairs.
	for _, p := range old {
		if p.Excluded {
			if _, ok := remote[p.Remote]; ok {
				usedR[p.Remote] = true
				out = append(out, p)
			}
			continue
		}
		l, hasL := local[p.Local]
		r, hasR := remote[p.Remote]
		usedL[p.Local], usedR[p.Remote] = true, true
		if p.Conflict {
			if hasL && hasR {
				out = append(out, p)
				retry[len(out)-1] = true
			}
			continue
		}
		lc, rc := hasL && l.Hash != p.LocalHash, hasR && r.Hash != p.RemoteHash
		if hasR {
			p.Missing = false
		}
		var err error
		name := l.Name
		if name == "" {
			name = r.Name
		}
		switch {
		case !hasL && !hasR:
			continue
		case hold && (isDeleteLocal(p) || isDeleteRemote(p)):
			out = append(out, p)
			retry[len(out)-1] = true
			continue
		case hasL && hasR && p.Prefer != "":
			prefer := p.Prefer
			p.Prefer = ""
			if prefer == "local" {
				err = push(&p, l, true)
			} else {
				err = pull(&p, r, true)
			}
		case isDeleteRemote(p) && !reject:
			if err = k.delRemote(ctx, p.Remote); err == nil {
				note(SettingsLogEntry{Kind: k.name, Name: name, Action: "deletedOnServer"})
				continue
			}
		case isDeleteLocal(p) && !reject:
			if err = k.delLocal(ctx, p.Local); err == nil {
				note(SettingsLogEntry{Kind: k.name, Name: name, Action: "deletedHere"})
				continue
			}
		case !hasL:
			// Gone here without a deletion, changed there, or the deletion was
			// refused: it comes back. Losing it is the one thing not to do.
			p.Local = ""
			err = pull(&p, r, rc)
		case !hasR && reject:
			p.Remote, p.Missing = "", false
			err = push(&p, l, lc)
		case !hasR && !lc && !p.Missing:
			// Absent once from a valid answer, no record of deleting it: wait.
			// Restoring now could overwrite what the server really has.
			p.Missing = true
			out = append(out, p)
			retry[len(out)-1] = true
			continue
		case !hasR:
			p.Remote, p.Missing = "", false
			err = push(&p, l, lc)
		case lc && rc:
			if localNewer(l, r) {
				err = push(&p, l, true)
			} else {
				err = pull(&p, r, true)
			}
		case lc:
			err = push(&p, l, false)
		case rc:
			err = pull(&p, r, false)
		}
		out = append(out, p)
		if err != nil {
			if fatal(err) {
				return nil, failedAny, err
			}
			if !logErr(name, err) {
				retry[len(out)-1] = true
			}
		}
	}
	// New on the server; the same thing may already exist here under its name.
	byName := map[string]string{}
	for id, l := range local {
		if !usedL[id] {
			key := l.Name
			if k.matchByID {
				key = id
			}
			byName[key] = id
		}
	}
	for rid, r := range remote {
		if usedR[rid] {
			continue
		}
		key := r.Name
		if k.matchByID {
			key = rid
		}
		p := settingsPair{Remote: rid}
		var err error
		if lid, ok := byName[key]; ok {
			l := local[lid]
			usedL[lid] = true
			p.Local = lid
			switch {
			case l.Hash == r.Hash:
			case k.protectFirst:
				p.Conflict = true
				if !slices.ContainsFunc(at.Conflicts, func(c WorkflowConflict) bool { return c.ID == rid }) {
					at.Conflicts = append(at.Conflicts, WorkflowConflict{ID: rid, Name: l.Name, LocalAfter: workflowDescribe(l.Data), ServerAfter: workflowDescribe(r.Data)})
				}
				note(SettingsLogEntry{Kind: k.name, Name: l.Name, Action: "skipped", Conflict: true, Detail: "same rule, different content: waiting for a decision"})
				out = append(out, p)
				retry[len(out)-1] = true
				continue
			case localNewer(l, r):
				err = push(&p, l, true)
			default:
				err = pull(&p, r, true)
			}
		} else {
			err = pull(&p, r, false)
		}
		if err != nil {
			if fatal(err) {
				return nil, failedAny, err
			}
			logErr(r.Name, err)
			continue
		}
		out = append(out, p)
	}
	// New here.
	for lid, l := range local {
		if usedL[lid] {
			continue
		}
		p := settingsPair{Local: lid}
		if err := push(&p, l, false); err != nil {
			if fatal(err) {
				return nil, failedAny, err
			}
			logErr(l.Name, err)
			continue
		}
		out = append(out, p)
	}
	// Remember the state both sides have now, read back: each side stores
	// things in its own form (ids, sanitised HTML).
	if local, err = k.listLocal(ctx); err != nil {
		return nil, failedAny, fmt.Errorf("local list: %w", err)
	}
	if remote, err = k.listRemote(ctx); err != nil {
		return nil, failedAny, err
	}
	for i := range out {
		if out[i].Excluded || retry[i] {
			continue
		}
		out[i].LocalHash, out[i].RemoteHash = local[out[i].Local].Hash, remote[out[i].Remote].Hash
	}
	finishDecision()
	return out, failedAny, nil
}

// errRemoteRejected: the token was rejected in the middle of a sync.
var errRemoteRejected = errors.New("device token rejected")

// remoteAPI is a small JSON call of the settings sync.
func (a *App) remoteAPI(ctx context.Context, c *serverConnection, method, path string, in, out any) error {
	var body []byte
	hdr := http.Header{"Accept": {"application/json"}}
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
		hdr.Set("Content-Type", "application/json")
	}
	res, err := a.connDo(ctx, c.URL, method, path, hdr, body, a.cr.timing.WriteTimeout)
	if err != nil {
		return err
	}
	if connGatewayDown(res.Status) {
		return errConnNetwork
	}
	if connIsTokenRejected(res) {
		return errRemoteRejected
	}
	if res.Status/100 != 2 {
		var e errorEnvelope
		_ = json.Unmarshal(res.Body, &e)
		return &remoteError{Status: res.Status, Code: e.Error}
	}
	if out != nil {
		// A captive portal or misconfigured proxy answers 200 with anything:
		// that is an error, never "the server has nothing".
		if len(bytes.TrimSpace(res.Body)) == 0 || !strings.Contains(res.Header.Get("Content-Type"), "json") {
			return fmt.Errorf("%w: %s", errRemoteInvalid, path)
		}
		if err := json.Unmarshal(res.Body, out); err != nil {
			return fmt.Errorf("%w: %s: %w", errRemoteInvalid, path, err)
		}
	}
	return nil
}

// errRemoteInvalid: the answer is not what this server would send.
var errRemoteInvalid = errors.New("invalid answer from the server")

type errorEnvelope struct {
	Error string `json:"error"`
}

type remoteError struct {
	Status int
	Code   string
}

func (e *remoteError) Error() string { return fmt.Sprintf("server answered %d %s", e.Status, e.Code) }

func (a *App) settingsKinds(c *serverConnection, at *SettingsAttention) []settingsKind {
	return []settingsKind{a.signatureKind(c), a.workflowKind(c, at), a.profileKind(c), a.viewKind(c)}
}

// ==== Signatures ===============================================================

// neutralSignature is a signature without ids, with its images embedded.
type neutralSignature struct {
	Name          string `json:"name"`
	HTML          string `json:"html"`
	Plain         string `json:"plain"`
	Placement     string `json:"placement"`
	PlainOverride bool   `json:"plainOverride,omitempty"`
}

func (a *App) embedSignature(sig domain.Signature) string {
	if len(sig.Images) == 0 || a.blobs == nil {
		return sig.HTML
	}
	byCID := map[string]domain.SignatureImage{}
	for _, im := range sig.Images {
		byCID[im.ContentID] = im
	}
	return signature.RewriteImages(sig.HTML, func(src string) (string, bool) {
		im, ok := byCID[strings.Trim(strings.TrimPrefix(src, "cid:"), "<> ")]
		if !ok || !strings.HasPrefix(src, "cid:") {
			return src, true
		}
		rc, err := a.blobs.Open(im.BlobHash)
		if err != nil {
			return src, true
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, signature.MaxImageSize+1))
		if err != nil {
			return src, true
		}
		return signature.DataURI(im.MIMEType, data), true
	})
}

func signatureObj(id, name string, n neutralSignature, updated int64) syncObj {
	return syncObj{ID: id, Name: name, Hash: hashJSON(n), Updated: updated, Data: n}
}

func (a *App) signatureKind(c *serverConnection) settingsKind {
	return settingsKind{
		name: settingsKindSignature,
		listLocal: func(ctx context.Context) (map[string]syncObj, error) {
			sigs, err := a.st.ListSignatures(ctx)
			if err != nil {
				return nil, err
			}
			out := map[string]syncObj{}
			for _, s := range sigs {
				id := strconv.FormatInt(s.ID, 10)
				out[id] = signatureObj(id, s.Name, neutralSignature{Name: s.Name, HTML: a.embedSignature(s), Plain: s.Plain, Placement: s.Placement, PlainOverride: s.PlainOverride}, s.UpdatedAt)
			}
			return out, nil
		},
		listRemote: func(ctx context.Context) (map[string]syncObj, error) {
			// The profile carries signature images as data: URIs (the plain list does not).
			var p remoteProfile
			if err := a.remoteAPI(ctx, c, http.MethodGet, "/api/provisioning/profile", nil, &p); err != nil {
				return nil, err
			}
			// Another server or user behind the address: nothing of it counts.
			if p.Signatures == nil || p.User.Username != c.Username {
				return nil, fmt.Errorf("%w: profile", errRemoteInvalid)
			}
			out := map[string]syncObj{}
			for _, s := range p.Signatures {
				id := strconv.FormatInt(s.ID, 10)
				out[id] = signatureObj(id, s.Name, neutralSignature{Name: s.Name, HTML: s.HTML, Plain: s.Plain, Placement: s.Placement, PlainOverride: s.PlainOverride}, s.UpdatedAt)
			}
			return out, nil
		},
		putLocal: func(ctx context.Context, id string, from syncObj) (string, error) {
			n := from.Data.(neutralSignature)
			sig := domain.Signature{Name: n.Name, HTML: n.HTML, Plain: n.Plain, Placement: n.Placement, PlainOverride: n.PlainOverride}
			if id != "" {
				sig.ID, _ = strconv.ParseInt(id, 10, 64)
			}
			saved, err := a.SaveSignature(ctx, sig)
			if err != nil {
				return "", err
			}
			return strconv.FormatInt(saved.ID, 10), nil
		},
		putRemote: func(ctx context.Context, id string, from syncObj) (string, error) {
			n := from.Data.(neutralSignature)
			method, path := http.MethodPost, "/api/signatures"
			body := domain.Signature{Name: n.Name, HTML: n.HTML, Plain: n.Plain, Placement: n.Placement, PlainOverride: n.PlainOverride}
			if id != "" {
				method, path = http.MethodPut, "/api/signatures/"+id
				body.ID, _ = strconv.ParseInt(id, 10, 64)
			}
			var saved domain.Signature
			if err := a.remoteAPI(ctx, c, method, path, body, &saved); err != nil {
				return "", err
			}
			return strconv.FormatInt(saved.ID, 10), nil
		},
		delLocal: func(ctx context.Context, id string) error {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return err
			}
			return a.DeleteSignature(ctx, n)
		},
		delRemote: func(ctx context.Context, id string) error {
			return a.remoteAPI(ctx, c, http.MethodDelete, "/api/signatures/"+url.PathEscape(id), nil, nil)
		},
	}
}

// ==== Workflows ================================================================

// neutralWorkflow leaves out what belongs to one device. Whether a workflow is
// switched on stays per device: it should not start forwarding mail from a
// second place just because it was switched on somewhere else, and a rule
// switched on here and on the server would act twice. See also putLocal below.
func neutralWorkflow(w Workflow, accounts []int64) Workflow {
	w.ID, w.Enabled, w.ExternalEffects, w.LastRun, w.UpdatedAt, w.ConfirmExternal, w.Author, w.TemplateID, w.ProjectID = "", false, false, nil, "", false, "", "", 0
	w.Trigger.Accounts = accounts
	return w
}

// accountMaps links this device's accounts to the server's through provisioning.
func (a *App) accountMaps(ctx context.Context, base string) (toRemote, toLocal map[int64]int64, err error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	toRemote, toLocal = map[int64]int64{}, map[int64]int64{}
	for _, acc := range accs {
		if acc.Extra["provisionedFrom"] != base {
			continue
		}
		if r, err := strconv.ParseInt(acc.Extra[extraProvisionedRemoteID], 10, 64); err == nil {
			toRemote[acc.ID], toLocal[r] = r, acc.ID
		}
	}
	return toRemote, toLocal, nil
}

func mapIDs(ids []int64, m map[int64]int64) ([]int64, bool) {
	if len(ids) == 0 {
		return nil, true
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		v, ok := m[id]
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

func workflowUpdated(w Workflow) int64 {
	if t, err := parseTime(w.UpdatedAt); err == nil {
		return t.Unix()
	}
	return 0
}

// workflowDescribe says what a rule really does, for the user's decision: when,
// under which condition, each step with its parameters.
func workflowDescribe(data any) []string {
	w, ok := data.(Workflow)
	if !ok {
		return []string{}
	}
	js := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			return "?"
		}
		return string(b)
	}
	when := w.Trigger.Event
	if len(w.Trigger.Accounts)+len(w.Trigger.Folders)+len(w.Trigger.Channels) > 0 || w.Trigger.Schedule != "" || w.Trigger.Name != "" {
		t := w.Trigger
		t.Event = ""
		when += " " + js(t)
	}
	out := []string{"WHEN " + when}
	if w.Condition != nil {
		out = append(out, "IF "+js(w.Condition))
	}
	for _, act := range w.Actions {
		line := "DO " + act.Type
		if len(act.Params) > 0 {
			line += " " + js(act.Params)
		}
		if act.If != "" {
			line += " IF " + act.If
		}
		out = append(out, line)
	}
	if len(w.Options) > 0 {
		out = append(out, "OPTIONS "+js(w.Options))
	}
	return out
}

func (a *App) workflowKind(c *serverConnection, at *SettingsAttention) settingsKind {
	build := func(list []Workflow, translate map[int64]int64) map[string]syncObj {
		out := map[string]syncObj{}
		for _, w := range list {
			if w.ProjectID != 0 {
				continue // belongs to a project, which only exists on one side
			}
			// The hash uses the other side's numbers where they are known.
			ids, ok := mapIDs(w.Trigger.Accounts, translate)
			if !ok {
				ids = w.Trigger.Accounts
			}
			n := neutralWorkflow(w, ids)
			out[w.ID] = syncObj{ID: w.ID, Name: w.Name, Hash: hashJSON(n), Updated: workflowUpdated(w), Data: w}
		}
		return out
	}
	return settingsKind{
		name:         settingsKindWorkflow,
		matchByID:    true,
		protectFirst: true,
		listLocal: func(ctx context.Context) (map[string]syncObj, error) {
			list, err := a.ListWorkflows(ctx, 0)
			if err != nil {
				return nil, err
			}
			toRemote, _, err := a.accountMaps(ctx, c.URL)
			if err != nil {
				return nil, err
			}
			return build(list, toRemote), nil
		},
		listRemote: func(ctx context.Context) (map[string]syncObj, error) {
			var list []Workflow
			if err := a.remoteAPI(ctx, c, http.MethodGet, "/api/workflows", nil, &list); err != nil {
				return nil, err
			}
			if list == nil {
				return nil, fmt.Errorf("%w: workflows", errRemoteInvalid)
			}
			return build(list, identityMap(list)), nil
		},
		putLocal: func(ctx context.Context, id string, from syncObj) (string, error) {
			w := from.Data.(Workflow)
			_, toLocal, err := a.accountMaps(ctx, c.URL)
			if err != nil {
				return "", err
			}
			ids, ok := mapIDs(w.Trigger.Accounts, toLocal)
			if !ok {
				return "", errSkipSync
			}
			target := id
			if target == "" {
				target = w.ID
			}
			n := neutralWorkflow(w, ids)
			n.ID = target
			// A rule from the server is off until the user has seen it, also
			// when the rule it replaces was on: it can act on mail, forward it,
			// delete it. neutralWorkflow leaves Enabled false.
			pending := PendingWorkflow{ID: target, Name: n.Name, IsNew: true, Trigger: n.Trigger.Event, After: workflowDescribe(w)}
			if prev, err := a.GetWorkflow(ctx, target); err == nil {
				pending.IsNew, pending.WasEnabled, pending.Before = false, prev.Enabled, workflowDescribe(prev)
			}
			// Changed again before the user decided: the comparison stays with
			// what they last released, and that it was switched on is not forgotten.
			if i := slices.IndexFunc(at.PendingWorkflows, func(p PendingWorkflow) bool { return p.ID == target }); i >= 0 {
				old := at.PendingWorkflows[i]
				pending.IsNew, pending.WasEnabled, pending.Before = old.IsNew, old.WasEnabled, old.Before
			}
			saved, err := a.SaveWorkflow(ctx, n)
			if err != nil {
				return "", err
			}
			pending.ID, pending.External, pending.Hash = saved.ID, saved.ExternalEffects, hashJSON(pending.After)
			at.PendingWorkflows = slices.DeleteFunc(at.PendingWorkflows, func(p PendingWorkflow) bool { return p.ID == saved.ID })
			at.PendingWorkflows = append(at.PendingWorkflows, pending)
			return saved.ID, nil
		},
		putRemote: func(ctx context.Context, id string, from syncObj) (string, error) {
			w := from.Data.(Workflow)
			toRemote, _, err := a.accountMaps(ctx, c.URL)
			if err != nil {
				return "", err
			}
			ids, ok := mapIDs(w.Trigger.Accounts, toRemote)
			if !ok {
				return "", errSkipSync
			}
			target := id
			if target == "" {
				target = w.ID
			}
			n := neutralWorkflow(w, ids)
			n.ID = target
			var cur Workflow
			if err := a.remoteAPI(ctx, c, http.MethodGet, "/api/workflows/"+url.PathEscape(target), nil, &cur); err == nil {
				n.Enabled = cur.Enabled
			}
			var saved Workflow
			err = a.remoteAPI(ctx, c, http.MethodPut, "/api/workflows/"+url.PathEscape(target), n, &saved)
			var re *remoteError
			if errors.As(err, &re) && re.Code == CodeConfirmationRequired {
				n.Enabled = false
				err = a.remoteAPI(ctx, c, http.MethodPut, "/api/workflows/"+url.PathEscape(target), n, &saved)
			}
			if err != nil {
				return "", err
			}
			return saved.ID, nil
		},
		delLocal: func(ctx context.Context, id string) error { return a.DeleteWorkflow(ctx, id) },
		delRemote: func(ctx context.Context, id string) error {
			return a.remoteAPI(ctx, c, http.MethodDelete, "/api/workflows/"+url.PathEscape(id), nil, nil)
		},
	}
}

// identityMap keeps the server's account numbers as they are.
func identityMap(list []Workflow) map[int64]int64 {
	m := map[int64]int64{}
	for _, w := range list {
		for _, id := range w.Trigger.Accounts {
			m[id] = id
		}
	}
	return m
}

// ==== Saved searches ===========================================================

// neutralProfile drops the account and folder filters: their numbers mean
// something else on the other side.
func neutralProfile(p SearchProfile) SearchProfile {
	p.ID, p.UpdatedAt = "", 0
	p.Query.AccountIDs, p.Query.FolderIDs = []int64{}, []int64{}
	return p
}

func (a *App) profileKind(c *serverConnection) settingsKind {
	build := func(list []SearchProfile) map[string]syncObj {
		out := map[string]syncObj{}
		for _, p := range list {
			n := neutralProfile(p)
			out[p.ID] = syncObj{ID: p.ID, Name: p.Name, Hash: hashJSON(n), Updated: p.UpdatedAt, Data: n}
		}
		return out
	}
	return settingsKind{
		name: settingsKindProfile,
		listLocal: func(ctx context.Context) (map[string]syncObj, error) {
			list, err := a.ListSearchProfiles(ctx)
			if err != nil {
				return nil, err
			}
			return build(list), nil
		},
		listRemote: func(ctx context.Context) (map[string]syncObj, error) {
			var list []SearchProfile
			if err := a.remoteAPI(ctx, c, http.MethodGet, "/api/search/profiles", nil, &list); err != nil {
				return nil, err
			}
			if list == nil {
				return nil, fmt.Errorf("%w: search profiles", errRemoteInvalid)
			}
			return build(list), nil
		},
		putLocal: func(ctx context.Context, id string, from syncObj) (string, error) {
			p := from.Data.(SearchProfile)
			p.ID = id
			saved, err := a.SaveSearchProfile(ctx, p)
			if err != nil {
				return "", err
			}
			return saved.ID, nil
		},
		putRemote: func(ctx context.Context, id string, from syncObj) (string, error) {
			p := from.Data.(SearchProfile)
			p.ID = id
			target := id
			if target == "" {
				target = "new"
			}
			var saved SearchProfile
			if err := a.remoteAPI(ctx, c, http.MethodPut, "/api/search/profiles/"+url.PathEscape(target), p, &saved); err != nil {
				return "", err
			}
			return saved.ID, nil
		},
		delLocal: func(ctx context.Context, id string) error { return a.DeleteSearchProfile(ctx, id) },
		delRemote: func(ctx context.Context, id string) error {
			return a.remoteAPI(ctx, c, http.MethodDelete, "/api/search/profiles/"+url.PathEscape(id), nil, nil)
		},
	}
}

// ==== View settings and language ===============================================

const viewSettingsID = "preferences"

func (a *App) viewKind(c *serverConnection) settingsKind {
	obj := func(p UserPreferences) map[string]syncObj {
		updated := p.UpdatedAt
		p.UpdatedAt = 0
		return map[string]syncObj{viewSettingsID: {ID: viewSettingsID, Name: viewSettingsID, Hash: hashJSON(p), Updated: updated, Data: p}}
	}
	return settingsKind{
		name:      settingsKindView,
		matchByID: true,
		listLocal: func(ctx context.Context) (map[string]syncObj, error) {
			p, err := a.GetPreferences(ctx)
			if err != nil {
				return nil, err
			}
			return obj(p), nil
		},
		listRemote: func(ctx context.Context) (map[string]syncObj, error) {
			var p UserPreferences
			if err := a.remoteAPI(ctx, c, http.MethodGet, "/api/preferences", nil, &p); err != nil {
				return nil, err
			}
			if p.Locale == "" {
				return nil, fmt.Errorf("%w: preferences", errRemoteInvalid)
			}
			return obj(p), nil
		},
		putLocal: func(ctx context.Context, _ string, from syncObj) (string, error) {
			_, err := a.SetPreferences(ctx, from.Data.(UserPreferences))
			return viewSettingsID, err
		},
		putRemote: func(ctx context.Context, _ string, from syncObj) (string, error) {
			return viewSettingsID, a.remoteAPI(ctx, c, http.MethodPut, "/api/preferences", from.Data, nil)
		},
		delLocal:  func(context.Context, string) error { return nil },
		delRemote: func(context.Context, string) error { return nil },
	}
}
