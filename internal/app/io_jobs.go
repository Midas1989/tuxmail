// SPDX-License-Identifier: AGPL-3.0-or-later

package app

// Import/export/backup jobs (KONZEPT 13, 14). Long-running work runs in a
// background job with progress and cancel; the UI polls GetIOJob (and gets
// an "io.job" event on every state change). Jobs are kept in memory; the
// checkpoint of a mail import (number of processed messages) is also
// written to DataDir/io/jobs/<id>.json so that an interrupted import can be
// resumed after a restart (sources are walked in a stable order).
//
// The per-App state lives in a registry keyed by *App so that this feature
// does not need fields in App (merge-friendly; see doc.go).

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// EventIOJob is published when an import/export/backup job changes.
const EventIOJob = "io.job"

// IOJob is the state of an import, export, backup or restore job.
type IOJob struct {
	ID       string       `json:"id"`
	Kind     string       `json:"kind"`   // import | export | backup | restore
	Format   string       `json:"format"` // eml, mbox, ... , backup
	Status   string       `json:"status"` // running | done | failed | canceled
	Done     int          `json:"done"`
	Total    int          `json:"total"`
	Skipped  int          `json:"skipped"`
	Failed   int          `json:"failed"`
	Current  string       `json:"current,omitempty"`
	Started  string       `json:"started"`
	Finished string       `json:"finished,omitempty"`
	Error    string       `json:"error,omitempty"`
	Problems []ie.Problem `json:"problems,omitempty"`
	// Summary counts what was created (messages, folders, contacts ...).
	Summary map[string]int `json:"summary,omitempty"`
	// Download is set for exports: GET /api/io/downloads/{token}.
	Download     string `json:"download,omitempty"`
	DownloadName string `json:"downloadName,omitempty"`
	// Result carries job specific data (backup manifest, restore result).
	Result    any  `json:"result,omitempty"`
	Resumable bool `json:"resumable,omitempty"`
}

type ioJob struct {
	mu     sync.Mutex
	job    IOJob
	cancel context.CancelFunc
	req    any
}

type ioManager struct {
	mu        sync.Mutex
	jobs      map[string]*ioJob
	downloads map[string]ioDownload
}

type ioDownload struct {
	path, name, mime string
	created          time.Time
}

var ioRegistry sync.Map // *App -> *ioManager

func (a *App) io() *ioManager {
	if v, ok := ioRegistry.Load(a); ok {
		return v.(*ioManager)
	}
	v, _ := ioRegistry.LoadOrStore(a, &ioManager{jobs: map[string]*ioJob{}, downloads: map[string]ioDownload{}})
	return v.(*ioManager)
}

func (j *ioJob) snapshot() IOJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := j.job
	out.Problems = append([]ie.Problem(nil), j.job.Problems...)
	if j.job.Summary != nil {
		out.Summary = make(map[string]int, len(j.job.Summary))
		for k, v := range j.job.Summary {
			out.Summary[k] = v
		}
	}
	return out
}

func (j *ioJob) update(fn func(*IOJob)) {
	j.mu.Lock()
	fn(&j.job)
	j.mu.Unlock()
}

func (j *ioJob) count(key string, n int) {
	j.update(func(x *IOJob) {
		if x.Summary == nil {
			x.Summary = map[string]int{}
		}
		x.Summary[key] += n
	})
}

// startJob runs fn in the background and returns the new job.
func (a *App) startJob(kind, format string, req any, fn func(ctx context.Context, j *ioJob) error) IOJob {
	m := a.io()
	ctx, cancel := context.WithCancel(context.Background())
	j := &ioJob{cancel: cancel, req: req, job: IOJob{ID: newID("job_"), Kind: kind, Format: format, Status: "running", Started: rfc3339(a.now())}}
	m.mu.Lock()
	m.jobs[j.job.ID] = j
	m.mu.Unlock()
	a.hub.Publish(Event{Type: EventIOJob, ItemID: j.job.ID, State: "running"})
	go func() {
		defer cancel()
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("internal error: %v", r)
				}
			}()
			err = fn(ctx, j)
		}()
		j.update(func(x *IOJob) {
			x.Finished = rfc3339(a.now())
			switch {
			case errors.Is(err, context.Canceled):
				x.Status = "canceled"
			case err != nil:
				x.Status, x.Error = "failed", err.Error()
			default:
				x.Status, x.Resumable = "done", false
			}
		})
		if err == nil {
			a.removeCheckpoint(j.job.ID)
		}
		a.invalidateViews()
		snap := j.snapshot()
		a.hub.Publish(Event{Type: EventIOJob, ItemID: snap.ID, State: snap.Status})
		if snap.Kind == "import" {
			a.hub.Publish(Event{Type: EventAccountUpdated})
		}
	}()
	return j.snapshot()
}

// Secrets returns the secret store of the user (backups, CLI).
func (a *App) Secrets() domain.SecretStore { return a.opts.Secrets }

// ListIOJobs returns the jobs of this session, newest first.
func (a *App) ListIOJobs(ctx context.Context) ([]IOJob, error) {
	m := a.io()
	m.mu.Lock()
	out := make([]IOJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.snapshot())
	}
	m.mu.Unlock()
	for _, cp := range a.listCheckpoints() {
		if _, ok := m.jobs[cp.Job.ID]; !ok {
			job := cp.Job
			job.Status, job.Resumable = "canceled", true
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	return out, nil
}

// GetIOJob returns one job.
func (a *App) GetIOJob(ctx context.Context, id string) (IOJob, error) {
	m := a.io()
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		for _, cp := range a.listCheckpoints() {
			if cp.Job.ID == id {
				job := cp.Job
				job.Status, job.Resumable = "canceled", true
				return job, nil
			}
		}
		return IOJob{}, errNotFound
	}
	return j.snapshot(), nil
}

// CancelIOJob stops a running job (an import can be resumed later).
func (a *App) CancelIOJob(ctx context.Context, id string) error {
	m := a.io()
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		a.removeCheckpoint(id)
		return nil
	}
	j.cancel()
	return nil
}

// ==== checkpoints (resumable imports) ==========================================

type ioCheckpoint struct {
	Job     IOJob         `json:"job"`
	Request ImportRequest `json:"request"`
}

func (a *App) checkpointDir() string {
	if a.opts.DataDir == "" {
		return ""
	}
	return filepath.Join(a.opts.DataDir, "io", "jobs")
}

func (a *App) saveCheckpoint(j *ioJob, req ImportRequest) {
	dir := a.checkpointDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	snap := j.snapshot()
	snap.Problems = nil
	b, _ := json.Marshal(ioCheckpoint{Job: snap, Request: req})
	tmp := filepath.Join(dir, snap.ID+".json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, snap.ID+".json"))
	}
}

func (a *App) removeCheckpoint(id string) {
	if dir := a.checkpointDir(); dir != "" && !strings.ContainsAny(id, `/\.`) {
		_ = os.Remove(filepath.Join(dir, id+".json"))
	}
}

func (a *App) listCheckpoints() []ioCheckpoint {
	dir := a.checkpointDir()
	if dir == "" {
		return nil
	}
	ents, _ := os.ReadDir(dir)
	var out []ioCheckpoint
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var cp ioCheckpoint
		if json.Unmarshal(b, &cp) == nil && cp.Job.ID != "" {
			out = append(out, cp)
		}
	}
	return out
}

// ==== uploads & downloads ======================================================

// IOUpload is a file uploaded for an import or restore (server mode).
type IOUpload struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
}

// MaxIOUpload limits uploaded import files and archives.
const MaxIOUpload = 8 << 30

func (a *App) ioDir(sub string) (string, error) {
	dir, err := a.userDataDir(filepath.Join("io", sub))
	if err != nil {
		return "", Errorf(http.StatusNotImplemented, CodeNotConfigured, "no data directory for import files")
	}
	return dir, nil
}

// SaveIOUpload stores an uploaded import file or backup archive.
func (a *App) SaveIOUpload(ctx context.Context, name string, r io.Reader) (IOUpload, error) {
	dir, err := a.ioDir("uploads")
	if err != nil {
		return IOUpload{}, err
	}
	a.purgeIOFiles(dir, 24*time.Hour)
	name = ie.SafeName(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	tok := newID("iou_")
	d := filepath.Join(dir, tok)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return IOUpload{}, err
	}
	f, err := os.Create(filepath.Join(d, name))
	if err != nil {
		return IOUpload{}, err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxIOUpload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxIOUpload {
		err = Errorf(http.StatusRequestEntityTooLarge, "too_large", "file is larger than %d GB", MaxIOUpload>>30)
	}
	if err != nil {
		os.RemoveAll(d)
		return IOUpload{}, err
	}
	return IOUpload{Token: tok, Name: name, Size: n}, nil
}

// uploadPath returns the file of an upload token.
func (a *App) uploadPath(tok string) (string, error) {
	if !strings.HasPrefix(tok, "iou_") || strings.ContainsAny(tok, `/\.`) {
		return "", invalid("invalid upload token")
	}
	dir, err := a.ioDir("uploads")
	if err != nil {
		return "", err
	}
	ents, err := os.ReadDir(filepath.Join(dir, tok))
	if err != nil || len(ents) != 1 {
		return "", errNotFound
	}
	return filepath.Join(dir, tok, ents[0].Name()), nil
}

func (a *App) purgeIOFiles(dir string, maxAge time.Duration) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && a.now().Sub(info.ModTime()) > maxAge {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// newDownload reserves a file for an export result.
func (a *App) newDownload(name, mime string) (path, token string, err error) {
	dir, err := a.ioDir("downloads")
	if err != nil {
		return "", "", err
	}
	a.purgeIOFiles(dir, 24*time.Hour)
	token = newID("iod_")
	if err := os.MkdirAll(filepath.Join(dir, token), 0o700); err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, token, ie.SafeName(name))
	m := a.io()
	m.mu.Lock()
	m.downloads[token] = ioDownload{path: path, name: name, mime: mime, created: a.now()}
	m.mu.Unlock()
	return path, token, nil
}

// IODownload is an export result to download.
type IODownload struct {
	Path     string
	Filename string
	MIMEType string
}

// OpenIODownload returns the file of a finished export.
func (a *App) OpenIODownload(ctx context.Context, token string) (IODownload, error) {
	m := a.io()
	m.mu.Lock()
	d, ok := m.downloads[token]
	m.mu.Unlock()
	if !ok {
		return IODownload{}, errNotFound
	}
	if _, err := os.Stat(d.path); err != nil {
		return IODownload{}, errNotFound
	}
	return IODownload{Path: d.path, Filename: d.name, MIMEType: d.mime}, nil
}

// ==== source resolution ========================================================

// IOSource names an import file: a local path (desktop mode only) or an
// upload token (POST /api/io/uploads).
type IOSource struct {
	Format      string `json:"format"`
	Path        string `json:"path,omitempty"`
	UploadToken string `json:"uploadToken,omitempty"`
}

// resolve returns a local path for the source. ZIP uploads of directory
// formats (maildir, thunderbird, mbox trees) are unpacked; cleanup removes
// temporary files.
func (a *App) resolveSource(src IOSource) (path string, cleanup func(), err error) {
	cleanup = func() {}
	switch {
	case src.UploadToken != "":
		if path, err = a.uploadPath(src.UploadToken); err != nil {
			return "", cleanup, err
		}
	case src.Path != "":
		if a.opts.Mode == ModeServer {
			return "", cleanup, Errorf(http.StatusForbidden, "forbidden", "server mode only imports uploaded files")
		}
		path = src.Path
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
	default:
		return "", cleanup, invalid("no file selected")
	}
	if _, err := os.Stat(path); err != nil {
		return "", cleanup, invalid("file not found: %s", filepath.Base(path))
	}
	switch src.Format {
	case "maildir", "thunderbird", "mbox":
		if st, _ := os.Stat(path); st != nil && !st.IsDir() && isZip(path) {
			dir, err := os.MkdirTemp(filepath.Dir(path), "unzip-*")
			if err != nil {
				return "", cleanup, err
			}
			cleanup = func() { os.RemoveAll(dir) }
			if err := unzip(path, dir); err != nil {
				cleanup()
				return "", func() {}, err
			}
			path = singleRoot(dir)
		}
	}
	return path, cleanup, nil
}

// importRoot is the directory an import may read from: uploads (and
// everything in server mode) are confined to the uploaded/unpacked files,
// so a crafted Thunderbird profile cannot point at server files such as
// master.key (AUDIT-C). Local paths in desktop mode are the user's own
// files and are not confined ("").
func (a *App) importRoot(src IOSource, path string) string {
	if src.UploadToken != "" || a.opts.Mode == ModeServer {
		return path
	}
	return ""
}

func isZip(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	_, _ = io.ReadFull(f, b)
	return string(b) == "PK\x03\x04"
}

// unzip extracts a ZIP archive safely (no path traversal, size limit).
func unzip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return ie.Corrupt("zip: %v", err)
	}
	defer zr.Close()
	var total uint64
	for _, f := range zr.File {
		// Backslashes are treated as separators on every OS (some Windows
		// tools write them); IsLocal also rejects drive/UNC and reserved
		// names (AUDIT-C).
		slash := strings.ReplaceAll(f.Name, `\`, "/")
		name := filepath.FromSlash(slash)
		if strings.Contains(slash, "..") || !filepath.IsLocal(name) {
			return ie.Corrupt("zip: unsafe path %q", f.Name)
		}
		p := filepath.Join(dst, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o700); err != nil {
				return err
			}
			continue
		}
		total += f.UncompressedSize64
		if total > MaxIOUpload {
			return ie.Corrupt("zip: content too large")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return ie.Corrupt("zip: %v", err)
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			rc.Close()
			return err
		}
		// #nosec G110 -- archive/zip fails reads beyond UncompressedSize64; the total is checked against MaxIOUpload
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return ie.Corrupt("zip: %v", err)
		}
	}
	return nil
}

// singleRoot descends into a ZIP's single top-level directory.
func singleRoot(dir string) string {
	ents, err := os.ReadDir(dir)
	if err == nil && len(ents) == 1 && ents[0].IsDir() {
		return filepath.Join(dir, ents[0].Name())
	}
	return dir
}
