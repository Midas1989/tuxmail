// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/backup"
	"github.com/Midas1989/Mailclient/internal/store"
)

// BackupTarget is where an archive is written ("download": a download in
// the browser / save dialog, "local": a directory, "webdav").
type BackupTarget struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	// Password is only accepted on input; stored in the secret store.
	Password string `json:"password,omitempty"`
}

// BackupRequest starts a backup job.
type BackupRequest struct {
	Scope      backup.Scope `json:"scope"`
	Passphrase string       `json:"passphrase,omitempty"`
	Target     BackupTarget `json:"target"`
}

// BackupSource names an archive to inspect or restore.
type BackupSource struct {
	UploadToken string `json:"uploadToken,omitempty"`
	Path        string `json:"path,omitempty"` // desktop only
	// ScheduleID + Name: an archive at the target of a schedule.
	ScheduleID string `json:"scheduleId,omitempty"`
	Name       string `json:"name,omitempty"`
}

// RestoreRequest starts a restore job.
type RestoreRequest struct {
	Source     BackupSource   `json:"source"`
	Bases      []BackupSource `json:"bases,omitempty"`
	Passphrase string         `json:"passphrase,omitempty"`
	Mode       string         `json:"mode"` // replace | merge
	Accounts   []int64        `json:"accounts,omitempty"`
	ConfigOnly bool           `json:"configOnly,omitempty"`
	Secrets    bool           `json:"secrets,omitempty"`
}

// BackupInspection is the content of an archive (restore wizard).
type BackupInspection struct {
	Manifest  *backup.Manifest `json:"manifest"`
	Encrypted bool             `json:"encrypted"`
	// Messenger is set when messenger sessions are included: WhatsApp and
	// Signal may ask to pair the device again after a restore.
	Messenger bool `json:"messenger"`
}

func (a *App) backupWriteTarget(ctx context.Context, t BackupTarget, scheduleID string) (backup.Target, error) {
	switch t.Kind {
	case "local":
		if a.opts.Mode == ModeServer {
			return nil, Errorf(http.StatusForbidden, "forbidden", "server mode cannot write to local paths")
		}
		if t.Path == "" {
			return nil, invalid("target directory required")
		}
		return backup.LocalTarget{Dir: t.Path}, nil
	case "webdav":
		pw := t.Password
		if pw == "" && scheduleID != "" {
			if b, err := a.opts.Secrets.Get(scheduleSecretRef(scheduleID, "webdav")); err == nil {
				pw = string(b)
			}
		}
		return backup.WebDAVTarget{URL: t.URL, Username: t.Username, Password: pw, Client: a.opts.HTTPClient}, nil
	}
	return nil, invalid("unknown backup target %q", t.Kind)
}

func (a *App) secretRefs() []string { return []string{aiSecretRef} }

// CreateBackup starts a backup job.
func (a *App) CreateBackup(ctx context.Context, req BackupRequest) (IOJob, error) {
	if err := req.Scope.Validate(); err != nil {
		return IOJob{}, invalid("%v", err)
	}
	if req.Scope.Secrets && req.Passphrase == "" {
		return IOJob{}, invalid("a passphrase is required when credentials are included")
	}
	var tgt backup.Target
	if req.Target.Kind != "" && req.Target.Kind != "download" {
		var err error
		if tgt, err = a.backupWriteTarget(ctx, req.Target, ""); err != nil {
			return IOJob{}, err
		}
	}
	return a.startJob("backup", "txmb", nil, func(ctx context.Context, j *ioJob) error {
		m, name, tok, err := a.runBackup(ctx, j, req.Scope, req.Passphrase, tgt, nil)
		if err != nil {
			return err
		}
		j.update(func(x *IOJob) {
			x.Result = m
			if tok != "" {
				x.Download, x.DownloadName = tok, name
			}
		})
		return nil
	}), nil
}

// runBackup writes an archive to the target or a download.
func (a *App) runBackup(ctx context.Context, j *ioJob, sc backup.Scope, pass string, tgt backup.Target, base *backup.Manifest) (*backup.Manifest, string, string, error) {
	tmpDir, err := a.ioDir("tmp")
	if err != nil {
		tmpDir = ""
	}
	f, err := os.CreateTemp(tmpDir, "backup-*.txmb")
	if err != nil {
		return nil, "", "", err
	}
	defer os.Remove(f.Name())
	m, err := backup.Create(ctx, f, backup.Options{Store: a.st, Blobs: a.blobs, Secrets: a.opts.Secrets, SecretRefs: a.secretRefs(),
		Scope: sc, Passphrase: pass, Base: base, AppVersion: a.opts.Version, TempDir: tmpDir, Now: a.opts.Now,
		Progress: func(stage string, done, total int) {
			if j != nil {
				j.update(func(x *IOJob) { x.Current, x.Done, x.Total = stage, done, total })
			}
		}})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, backup.ErrNeedPassphrase) {
			return nil, "", "", invalid("%v", err)
		}
		return nil, "", "", err
	}
	name := backup.ArchiveName(m, a.opts.Location)
	if tgt == nil {
		out, tok, err := a.newDownload(name, "application/octet-stream")
		if err != nil {
			return nil, "", "", err
		}
		if err := os.Rename(f.Name(), out); err != nil {
			if err := copyPath(f.Name(), out); err != nil {
				return nil, "", "", err
			}
		}
		return m, name, tok, nil
	}
	rf, err := os.Open(f.Name())
	if err != nil {
		return nil, "", "", err
	}
	defer rf.Close()
	if err := tgt.Put(ctx, name, rf); err != nil {
		return nil, "", "", err
	}
	return m, name, "", nil
}

func copyPath(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (a *App) backupSource(ctx context.Context, s BackupSource) (backup.Source, error) {
	switch {
	case s.UploadToken != "":
		p, err := a.uploadPath(s.UploadToken)
		if err != nil {
			return backup.Source{}, err
		}
		return backup.FileSource(p), nil
	case s.Path != "":
		if a.opts.Mode == ModeServer {
			return backup.Source{}, Errorf(http.StatusForbidden, "forbidden", "server mode only restores uploaded archives")
		}
		return backup.FileSource(s.Path), nil
	case s.ScheduleID != "":
		sch, err := a.getSchedule(ctx, s.ScheduleID)
		if err != nil {
			return backup.Source{}, err
		}
		tgt, err := a.backupWriteTarget(ctx, scheduleTarget(sch), sch.ID)
		if err != nil {
			return backup.Source{}, err
		}
		name := s.Name
		return backup.Source{Name: name, Open: func() (io.ReadCloser, error) { return tgt.Get(context.Background(), name) }}, nil
	}
	return backup.Source{}, invalid("no archive selected")
}

func backupErr(err error) error {
	switch {
	case errors.Is(err, backup.ErrWrongPassphrase), errors.Is(err, backup.ErrNeedPassphrase):
		return Errorf(http.StatusUnauthorized, CodeInvalidPassword, "%v", err)
	case errors.Is(err, backup.ErrTampered), errors.Is(err, backup.ErrNotArchive), errors.Is(err, backup.ErrMissingBase),
		errors.Is(err, backup.ErrTooNew), errors.Is(err, backup.ErrNotEncrypted):
		return invalid("%v", err)
	}
	return err
}

// InspectBackup reads the manifest of an archive.
func (a *App) InspectBackup(ctx context.Context, s BackupSource, passphrase string) (BackupInspection, error) {
	src, err := a.backupSource(ctx, s)
	if err != nil {
		return BackupInspection{}, err
	}
	rc, err := src.Open()
	if err != nil {
		return BackupInspection{}, err
	}
	defer rc.Close()
	m, err := backup.Inspect(rc, passphrase)
	if err != nil {
		if errors.Is(err, backup.ErrNeedPassphrase) {
			return BackupInspection{Encrypted: true}, nil
		}
		return BackupInspection{}, backupErr(err)
	}
	return BackupInspection{Manifest: m, Encrypted: m.Encrypted, Messenger: m.HasMessenger()}, nil
}

// StartRestore starts a restore job. "merge" adds the selected accounts to
// the running installation; "replace" stages the database for the next
// start (the result says restartRequired) and restores blobs and secrets.
func (a *App) StartRestore(ctx context.Context, req RestoreRequest) (IOJob, error) {
	src, err := a.backupSource(ctx, req.Source)
	if err != nil {
		return IOJob{}, err
	}
	var bases []backup.Source
	for _, b := range req.Bases {
		bs, err := a.backupSource(ctx, b)
		if err != nil {
			return IOJob{}, err
		}
		bases = append(bases, bs)
	}
	mode := backup.RestoreMode(req.Mode)
	if mode != backup.Replace && mode != backup.Merge {
		return IOJob{}, invalid("restore mode must be replace or merge")
	}
	if mode == backup.Replace && a.opts.DataDir == "" {
		return IOJob{}, Errorf(http.StatusNotImplemented, CodeNotConfigured, "replace needs a data directory")
	}
	return a.startJob("restore", "txmb", nil, func(ctx context.Context, j *ioJob) error {
		tmpDir, _ := a.ioDir("tmp")
		j.update(func(x *IOJob) { x.Current = "verify" })
		ex, err := backup.Extract(ctx, src, req.Passphrase, bases, tmpDir)
		if err != nil {
			return backupErr(err)
		}
		defer ex.Close()
		j.update(func(x *IOJob) { x.Current = "restore" })
		opts := backup.RestoreOptions{Mode: mode, Accounts: req.Accounts, ConfigOnly: req.ConfigOnly, Secrets: req.Secrets,
			DataDir: a.opts.DataDir, Store: a.st, Blobs: a.blobs, SecretStore: a.opts.Secrets, Now: a.opts.Now,
			OnMessage: func(ctx context.Context, id int64) { a.reindexMessage(ctx, id) }}
		var res *backup.RestoreResult
		if mode == backup.Replace {
			res, err = backup.Stage(ctx, ex, opts)
		} else {
			res, err = backup.Restore(ctx, ex, opts)
		}
		if err != nil {
			return backupErr(err)
		}
		j.update(func(x *IOJob) {
			x.Result = res
			x.Done = res.Messages
			x.Summary = map[string]int{"accounts": res.Accounts, "messages": res.Messages, "blobs": res.Blobs, "secrets": res.Secrets}
		})
		a.invalidateViews()
		return nil
	}), nil
}

// reindexMessage indexes a merged message from its stored body.
func (a *App) reindexMessage(ctx context.Context, id int64) {
	m, err := a.st.GetMessage(ctx, id)
	if err != nil || m.BlobHash == "" || a.blobs == nil {
		return
	}
	r, err := a.blobs.Open(m.BlobHash)
	if err != nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxRenderBytes))
	r.Close()
	if err != nil {
		return
	}
	ct, err := ParseExtractor{}.Extract(raw)
	if err != nil {
		return
	}
	addrs, _ := a.st.MessageAddresses(ctx, id)
	var ab strings.Builder
	for _, x := range addrs {
		ab.WriteString(x.Name + " " + x.Email + " ")
	}
	_ = a.st.IndexMessage(ctx, id, store.SearchDoc{Subject: m.Subject, Body: ct.Text, Addresses: strings.TrimSpace(ab.String())})
}

// ==== schedules ================================================================

const scheduleSettingKey = "backup.schedules"

func scheduleSecretRef(id, what string) string { return "backup:" + id + ":" + what }

// BackupScheduleInput is a schedule with the secrets to store.
type BackupScheduleInput struct {
	backup.Schedule
	Passphrase     string `json:"passphrase,omitempty"`
	TargetPassword string `json:"targetPassword,omitempty"`
}

func scheduleTarget(s backup.Schedule) BackupTarget {
	return BackupTarget{Kind: s.Target.Kind, Path: s.Target.Path, URL: s.Target.URL, Username: s.Target.Username}
}

func (a *App) loadSchedules(ctx context.Context) ([]backup.Schedule, error) {
	var list []backup.Schedule
	if _, err := a.st.GetAppSetting(ctx, scheduleSettingKey, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (a *App) saveSchedules(ctx context.Context, list []backup.Schedule) error {
	if list == nil {
		list = []backup.Schedule{}
	}
	return a.st.SetAppSetting(ctx, scheduleSettingKey, list)
}

func (a *App) getSchedule(ctx context.Context, id string) (backup.Schedule, error) {
	list, err := a.loadSchedules(ctx)
	if err != nil {
		return backup.Schedule{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return backup.Schedule{}, errNotFound
}

// ListBackupSchedules returns the backup schedules.
func (a *App) ListBackupSchedules(ctx context.Context) ([]backup.Schedule, error) {
	list, err := a.loadSchedules(ctx)
	if list == nil {
		list = []backup.Schedule{}
	}
	return list, err
}

// SaveBackupSchedule creates (no id) or updates a schedule.
func (a *App) SaveBackupSchedule(ctx context.Context, in BackupScheduleInput) (backup.Schedule, error) {
	s := in.Schedule
	if err := s.Scope.Validate(); err != nil {
		return s, invalid("%v", err)
	}
	if s.Frequency != "daily" && s.Frequency != "weekly" {
		return s, invalid("frequency must be daily or weekly")
	}
	if s.Hour < 0 || s.Hour > 23 || s.Minute < 0 || s.Minute > 59 {
		return s, invalid("invalid time")
	}
	if _, err := a.backupWriteTarget(ctx, scheduleTarget(s), ""); err != nil {
		return s, err
	}
	if s.KeepDaily <= 0 {
		s.KeepDaily = 7
	}
	if s.KeepWeekly <= 0 {
		s.KeepWeekly = 4
	}
	list, err := a.loadSchedules(ctx)
	if err != nil {
		return s, err
	}
	idx := -1
	for i, x := range list {
		if x.ID == s.ID && s.ID != "" {
			idx = i
			s.LastRun, s.LastResult, s.LastError, s.PassphraseRef = x.LastRun, x.LastResult, x.LastError, x.PassphraseRef
			s.Target.PasswordRef = x.Target.PasswordRef
		}
	}
	if s.ID == "" {
		s.ID = newID("bk_")
	}
	if in.Passphrase != "" {
		ref := scheduleSecretRef(s.ID, "passphrase")
		if err := a.opts.Secrets.Set(ref, []byte(in.Passphrase)); err != nil {
			return s, err
		}
		s.PassphraseRef = ref
	}
	if s.Scope.Secrets && s.PassphraseRef == "" {
		return s, invalid("a passphrase is required when credentials are included")
	}
	if in.TargetPassword != "" {
		ref := scheduleSecretRef(s.ID, "webdav")
		if err := a.opts.Secrets.Set(ref, []byte(in.TargetPassword)); err != nil {
			return s, err
		}
		s.Target.PasswordRef = ref
	}
	s.NextRun = s.Next(a.now(), a.opts.Location)
	if idx >= 0 {
		list[idx] = s
	} else {
		list = append(list, s)
	}
	return s, a.saveSchedules(ctx, list)
}

// DeleteBackupSchedule removes a schedule (archives are kept).
func (a *App) DeleteBackupSchedule(ctx context.Context, id string) error {
	list, err := a.loadSchedules(ctx)
	if err != nil {
		return err
	}
	out := list[:0]
	for _, s := range list {
		if s.ID != id {
			out = append(out, s)
		}
	}
	_ = a.opts.Secrets.Delete(scheduleSecretRef(id, "passphrase"))
	_ = a.opts.Secrets.Delete(scheduleSecretRef(id, "webdav"))
	return a.saveSchedules(ctx, out)
}

// ListBackupArchives lists the archives at the target of a schedule.
func (a *App) ListBackupArchives(ctx context.Context, scheduleID string) ([]backup.Entry, error) {
	s, err := a.getSchedule(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	tgt, err := a.backupWriteTarget(ctx, scheduleTarget(s), s.ID)
	if err != nil {
		return nil, err
	}
	ents, err := tgt.List(ctx)
	if ents == nil {
		ents = []backup.Entry{}
	}
	return ents, err
}

// RunBackupSchedule runs a schedule now.
func (a *App) RunBackupSchedule(ctx context.Context, id string) (IOJob, error) {
	s, err := a.getSchedule(ctx, id)
	if err != nil {
		return IOJob{}, err
	}
	return a.startJob("backup", "txmb", nil, func(ctx context.Context, j *ioJob) error {
		return a.runSchedule(ctx, j, s)
	}), nil
}

type scheduleState struct {
	Full *backup.Manifest `json:"full"`
}

func (a *App) scheduleStatePath(id string) string {
	if a.opts.DataDir == "" || strings.ContainsAny(id, `/\.`) {
		return ""
	}
	return filepath.Join(a.opts.DataDir, "io", "backup-state", id+".json")
}

func (a *App) runSchedule(ctx context.Context, j *ioJob, s backup.Schedule) error {
	pass := ""
	if s.PassphraseRef != "" {
		b, err := a.opts.Secrets.Get(s.PassphraseRef)
		if err != nil {
			return a.finishSchedule(ctx, s, errors.New("backup passphrase is not available (vault locked?)"))
		}
		pass = string(b)
	}
	tgt, err := a.backupWriteTarget(ctx, scheduleTarget(s), s.ID)
	if err != nil {
		return a.finishSchedule(ctx, s, err)
	}
	// Incremental: against the newest full archive of this schedule as long
	// as it is younger than FullEvery days and still at the target.
	var base *backup.Manifest
	statePath := a.scheduleStatePath(s.ID)
	if s.Incremental && statePath != "" {
		var st scheduleState
		if b, err := os.ReadFile(statePath); err == nil && json.Unmarshal(b, &st) == nil && st.Full != nil {
			every := s.FullEvery
			if every <= 0 {
				every = 7
			}
			if a.now().Sub(st.Full.Created) < time.Duration(every)*24*time.Hour {
				if ents, err := tgt.List(ctx); err == nil {
					for _, e := range ents {
						if p, ok := backup.ParseArchiveName(e.Name, a.opts.Location); ok && p.ID == st.Full.ID {
							base = st.Full
						}
					}
				}
			}
		}
	}
	m, name, _, err := a.runBackup(ctx, j, s.Scope, pass, tgt, base)
	if err == nil && base == nil && statePath != "" {
		if b, err := json.Marshal(scheduleState{Full: m}); err == nil {
			_ = os.MkdirAll(filepath.Dir(statePath), 0o700)
			_ = os.WriteFile(statePath, b, 0o600)
		}
	}
	if err == nil {
		if ents, lerr := tgt.List(ctx); lerr == nil {
			for _, del := range backup.Retention(ents, s.KeepDaily, s.KeepWeekly, a.opts.Location) {
				_ = tgt.Delete(ctx, del)
			}
		}
		s.LastResult = name
		if j != nil {
			j.update(func(x *IOJob) { x.Result = m })
		}
	}
	return a.finishSchedule(ctx, s, err)
}

func (a *App) finishSchedule(ctx context.Context, s backup.Schedule, runErr error) error {
	list, err := a.loadSchedules(ctx)
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID == s.ID {
			list[i].LastRun = a.now()
			list[i].LastError = ""
			if runErr != nil {
				list[i].LastError = runErr.Error()
			} else {
				list[i].LastResult = s.LastResult
			}
			list[i].NextRun = list[i].Next(a.now(), a.opts.Location)
		}
	}
	if err := a.saveSchedules(ctx, list); err != nil {
		return err
	}
	return runErr
}

// RunDueBackups runs the schedules that are due (the scheduler hook; the
// application timer calls it, and so can an external scheduler).
func (a *App) RunDueBackups(ctx context.Context) {
	list, err := a.loadSchedules(ctx)
	if err != nil {
		return
	}
	m := a.io()
	for _, s := range list {
		if !s.Due(a.now(), a.opts.Location) {
			continue
		}
		running := false
		m.mu.Lock()
		for _, j := range m.jobs {
			snap := j.snapshot()
			if snap.Kind == "backup" && snap.Status == "running" {
				running = true
			}
		}
		m.mu.Unlock()
		if running {
			return
		}
		s := s
		a.startJob("backup", "txmb", nil, func(ctx context.Context, j *ioJob) error { return a.runSchedule(ctx, j, s) })
		return // one at a time
	}
}
