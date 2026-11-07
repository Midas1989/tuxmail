// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	keySettingsMap = "server.settings.map"
	keySettingsLog = "server.settings.log"
	maxSettingsLog = 50
)

// settingsPair links one thing on both sides; the hashes are its state at the last sync.
type settingsPair struct {
	Local      string `json:"local,omitempty"`
	Remote     string `json:"remote,omitempty"`
	LocalHash  string `json:"localHash,omitempty"`
	RemoteHash string `json:"remoteHash,omitempty"`
	// Excluded: deselected during the set-up, the user does not want it here.
	Excluded bool `json:"excluded,omitempty"`
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
	name       string
	matchByID  bool
	listLocal  func(ctx context.Context) (map[string]syncObj, error)
	listRemote func(ctx context.Context) (map[string]syncObj, error)
	// put writes the other side's data; id "" creates. They return the id.
	putLocal   func(ctx context.Context, id string, from syncObj) (string, error)
	putRemote  func(ctx context.Context, id string, from syncObj) (string, error)
	delLocal   func(ctx context.Context, id string) error
	delRemote  func(ctx context.Context, id string) error
	noteRemote func() // called when a thing changed locally (UI refresh)
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
	var firstErr error
	for _, k := range a.settingsKinds(c) {
		pairs, err := a.reconcile(ctx, k, m[k.name], note)
		if err != nil {
			if errors.Is(err, errConnNetwork) || errors.Is(err, errConnLocked) || errors.Is(err, errRemoteRejected) {
				firstErr = err
				break
			}
			note(SettingsLogEntry{Kind: k.name, Action: "error", Detail: "not synchronised this time"})
			a.log.Warn("app: synchronising settings failed", "kind", k.name, "err", err)
			continue
		}
		m[k.name] = pairs
	}
	if err := a.st.SetAppSetting(ctx, keySettingsMap, m); err != nil {
		return log, err
	}
	if len(log) > 0 {
		all := append(a.settingsLog(ctx), log...)
		if len(all) > maxSettingsLog {
			all = all[len(all)-maxSettingsLog:]
		}
		if err := a.st.SetAppSetting(ctx, keySettingsLog, all); err != nil {
			return log, err
		}
		a.hub.Publish(Event{Type: EventSettingsSynced})
	}
	switch {
	case errors.Is(firstErr, errRemoteRejected):
		a.connMarkExpired(ctx)
		return log, nil
	case firstErr != nil:
		a.connMarkOffline(firstErr)
		return log, firstErr
	}
	if err := a.connUpdate(ctx, func(c *serverConnection) { c.LastSettingsSync = a.now().Unix() }); err != nil {
		return log, err
	}
	a.connNotify()
	return log, nil
}

func (a *App) reconcile(ctx context.Context, k settingsKind, old []settingsPair, note func(SettingsLogEntry)) ([]settingsPair, error) {
	local, err := k.listLocal(ctx)
	if err != nil {
		return nil, fmt.Errorf("local list: %w", err)
	}
	remote, err := k.listRemote(ctx)
	if err != nil {
		return nil, err
	}
	var out []settingsPair
	usedL, usedR := map[string]bool{}, map[string]bool{}
	logErr := func(name string, err error) {
		if errors.Is(err, errSkipSync) {
			note(SettingsLogEntry{Kind: k.name, Name: name, Action: "skipped", Detail: "refers to something that does not exist on the other side"})
			return
		}
		note(SettingsLogEntry{Kind: k.name, Name: name, Action: "error", Detail: "not synchronised this time"})
		a.log.Warn("app: synchronising a setting failed", "kind", k.name, "err", err)
	}
	fatal := func(err error) bool {
		return errors.Is(err, errConnNetwork) || errors.Is(err, errConnLocked) || errors.Is(err, errRemoteRejected)
	}
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
		lc, rc := hasL && l.Hash != p.LocalHash, hasR && r.Hash != p.RemoteHash
		var err error
		name := l.Name
		if name == "" {
			name = r.Name
		}
		switch {
		case !hasL && !hasR:
			continue
		case !hasL && rc: // deleted here, changed there: keep the work
			p.Local = ""
			err = pull(&p, r, true)
		case !hasL:
			if err = k.delRemote(ctx, p.Remote); err == nil {
				note(SettingsLogEntry{Kind: k.name, Name: name, Action: "deletedOnServer"})
				continue
			}
		case !hasR && lc:
			p.Remote = ""
			err = push(&p, l, true)
		case !hasR:
			if err = k.delLocal(ctx, p.Local); err == nil {
				note(SettingsLogEntry{Kind: k.name, Name: name, Action: "deletedHere"})
				continue
			}
		case lc && rc:
			if l.Updated > r.Updated {
				err = push(&p, l, true)
			} else {
				err = pull(&p, r, true)
			}
		case lc:
			err = push(&p, l, false)
		case rc:
			err = pull(&p, r, false)
		}
		if err != nil {
			if fatal(err) {
				return nil, err
			}
			logErr(name, err)
		}
		out = append(out, p)
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
			if l.Hash != r.Hash { // same name, other content
				if l.Updated > r.Updated {
					err = push(&p, l, true)
				} else {
					err = pull(&p, r, true)
				}
			}
		} else {
			err = pull(&p, r, false)
		}
		if err != nil {
			if fatal(err) {
				return nil, err
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
				return nil, err
			}
			logErr(l.Name, err)
			continue
		}
		out = append(out, p)
	}
	// Remember the state both sides have now, read back: each side stores
	// things in its own form (ids, sanitised HTML).
	if local, err = k.listLocal(ctx); err != nil {
		return nil, fmt.Errorf("local list: %w", err)
	}
	if remote, err = k.listRemote(ctx); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Excluded {
			continue
		}
		out[i].LocalHash, out[i].RemoteHash = local[out[i].Local].Hash, remote[out[i].Remote].Hash
	}
	return out, nil
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
	if out != nil && len(res.Body) > 0 {
		return json.Unmarshal(res.Body, out)
	}
	return nil
}

type errorEnvelope struct {
	Error string `json:"error"`
}

type remoteError struct {
	Status int
	Code   string
}

func (e *remoteError) Error() string { return fmt.Sprintf("server answered %d %s", e.Status, e.Code) }

func (a *App) settingsKinds(c *serverConnection) []settingsKind {
	return []settingsKind{a.signatureKind(c), a.workflowKind(c), a.profileKind(c), a.viewKind(c)}
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
		name: "signature",
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
// switched on here and on the server would act twice.
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

func (a *App) workflowKind(c *serverConnection) settingsKind {
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
		name:      "workflow",
		matchByID: true,
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
			if cur, ok := a.wf.Get(target); ok {
				n.Enabled = cur.Enabled
			}
			saved, err := a.SaveWorkflow(ctx, n)
			if e := AsError(err); err != nil && e.Code == CodeConfirmationRequired {
				n.Enabled = false // actions outside TuxMail changed: the user confirms by switching it on again
				saved, err = a.SaveWorkflow(ctx, n)
			}
			if err != nil {
				return "", err
			}
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
		name: "searchProfile",
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
		name:      "view",
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
