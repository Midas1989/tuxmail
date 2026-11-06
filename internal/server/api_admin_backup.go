// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Key of the encrypted server backups (Settings -> Administration -> Backup).
//
// The age key pair is created here, not in a configuration: the private key
// exists in memory for the one answer that shows it and is never written to
// disk, the database, a log or the audit trail. Only the public key stays on
// the server, in data_dir/backup/recipients.txt, where the backup job
// (deploy/.../tuxmail-backup, runs as root) reads it. The job in turn leaves
// the outcome of its last run in data_dir/backup/status.json.
//
// A generated key only counts once the administrator confirmed having saved
// the private key. Until then its public part waits in recipients.txt.pending,
// which the job does not read: if the answer gets lost or the tab is closed,
// backups stay readable with the old key (or the job stays visibly red), and
// the retention does not replace readable archives with unreadable ones.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/Midas1989/Mailclient/internal/auth"
)

const (
	backupKeyDirName     = "backup"
	backupRecipientsName = "recipients.txt"
	backupStatusName     = "status.json"
	backupPendingSuffix  = ".pending"
	// backupStaleAfter is a missed daily run plus margin.
	backupStaleAfter = 48 * time.Hour
)

// backupRecipientLine is what the backup job accepts as well. It is wider than
// what can be entered here: a list taken over from an existing installation
// may hold other age recipient types.
var backupRecipientLine = regexp.MustCompile(`^age1[0-9a-z]+$`)

func (s *Server) backupRecipientsPath() string {
	return filepath.Join(s.cfg.DataDir, backupKeyDirName, backupRecipientsName)
}

func (s *Server) backupPendingPath() string { return s.backupRecipientsPath() + backupPendingSuffix }

type backupKeyView struct {
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
}

// backupFingerprint names a public key in the UI and the audit trail.
func backupFingerprint(publicKey string) string {
	sum := sha256.Sum256([]byte(publicKey))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func backupFingerprints(keys []backupKeyView) string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.Fingerprint
	}
	return strings.Join(out, ",")
}

// readBackupRecipients returns the stored public keys; invalid means the file
// has a line the backup job will refuse (it then does not run at all).
func (s *Server) readBackupRecipients() (keys []backupKeyView, invalid bool, err error) {
	raw, err := os.ReadFile(s.backupRecipientsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !backupRecipientLine.MatchString(line) {
			invalid = true
			continue
		}
		keys = append(keys, backupKeyView{PublicKey: line, Fingerprint: backupFingerprint(line)})
	}
	return keys, invalid, nil
}

// writeBackupKeyFile replaces a key file atomically: the job reads either the
// old or the new list, never a part.
func writeBackupKeyFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	if err := f.Chmod(0o640); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	done = true
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// backupRun is the outcome of the last run of the backup job. Result is "ok",
// "failed", "no_key" (no public key, nothing was written) or "invalid_key".
type backupRun struct {
	FinishedAt time.Time `json:"finishedAt"`
	Label      string    `json:"label"`
	Result     string    `json:"result"`
	Encrypted  bool      `json:"encrypted"`
}

func (s *Server) readBackupRun() *backupRun {
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, backupKeyDirName, backupStatusName))
	if err != nil {
		return nil
	}
	var run backupRun
	if json.Unmarshal(raw, &run) != nil || run.Result == "" {
		return nil
	}
	return &run
}

// readBackupPending returns the generated key waiting for its confirmation and
// the session that created it (the only one that has seen the private key).
func (s *Server) readBackupPending() (publicKey string, sessionID int64) {
	raw, err := os.ReadFile(s.backupPendingPath())
	if err != nil {
		return "", 0
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "# session="); ok {
			sessionID, _ = strconv.ParseInt(v, 10, 64)
		} else if backupRecipientLine.MatchString(line) {
			publicKey = line
		}
	}
	return publicKey, sessionID
}

// backupHealth sums up the state for administrators and their monitoring (GET
// /api/admin/backup, also with an admin's device token). It stays out of the
// public /healthz: "no backups here" must not be readable by everyone. "ok"
// needs a proven, recent, encrypted run; a key alone is only "warn".
func (s *Server) backupHealth() string {
	keys, invalid, err := s.readBackupRecipients()
	if err != nil || invalid {
		return "fail"
	}
	run := s.readBackupRun()
	switch {
	case run == nil || run.Result == "no_key" || run.Result == "invalid_key":
		// A refusal is history once a valid key is there.
		if len(keys) == 0 {
			return "fail"
		}
		return "warn"
	case run.Result != "ok":
		return "fail"
	case !run.Encrypted || time.Since(run.FinishedAt) > backupStaleAfter:
		return "warn"
	}
	return "ok"
}

func (s *Server) handleAdminBackupStatus(w http.ResponseWriter, r *http.Request, a *authInfo) {
	keys, invalid, err := s.readBackupRecipients()
	if err != nil {
		s.log.Error("backup key: cannot read the public key", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_key_unreadable", "the backup key cannot be read")
		return
	}
	if keys == nil {
		keys = []backupKeyView{}
	}
	var pending *backupKeyView
	if key, _ := s.readBackupPending(); key != "" {
		pending = &backupKeyView{PublicKey: key, Fingerprint: backupFingerprint(key)}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"keys":    keys,
		"invalid": invalid,
		"pending": pending,
		"health":  s.backupHealth(),
		"lastRun": s.readBackupRun(),
		// Which proofs the confirmation asks for (see backupKeyStepUp).
		"reauth": map[string]bool{"password": a.User.HasPassword, "code": a.User.TOTPEnabled},
	})
}

type backupKeyReq struct {
	reauthReq
	// Replace confirms that an existing key is given up; backups made with it
	// keep needing the old private key.
	Replace bool `json:"replace"`
	// PublicKey is the administrator's own key (import only).
	PublicKey string `json:"publicKey"`
}

// backupKeyStepUp asks for a fresh confirmation. It is the strict variant used
// for passkeys: an account without password and second factor has to come from
// a recent login, a long-lived session alone is not enough. Device tokens stay
// out, this belongs to the web administration.
func (s *Server) backupKeyStepUp(w http.ResponseWriter, r *http.Request, a *authInfo, req backupKeyReq) bool {
	if a.Bearer || a.Session.Kind != auth.KindWeb {
		writeError(w, http.StatusForbidden, "forbidden", "the backup key can only be managed in the web client")
		return false
	}
	if !s.limitLogin(w, r, a.User.Username) {
		return false
	}
	if err := s.auth.VerifyPasskeyStepUp(r.Context(), a.User.ID, a.Session.ID, req.Password, req.Code, req.RecoveryCode); err != nil {
		s.writeAuthError(w, err)
		return false
	}
	return true
}

// backupKeyExists refuses to touch an existing key without req.Replace.
func (s *Server) backupKeyExists(w http.ResponseWriter, replace bool) (old []backupKeyView, ok bool) {
	old, invalid, err := s.readBackupRecipients()
	if err != nil {
		s.log.Error("backup key: cannot read the public key", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_key_unreadable", "the backup key cannot be read")
		return nil, false
	}
	if (len(old) > 0 || invalid) && !replace {
		writeError(w, http.StatusConflict, "backup_key_exists", "a backup key exists; confirm replacing it")
		return nil, false
	}
	return old, true
}

// activateBackupKey makes publicKey the key of the job and audits it. Only
// fingerprints reach the audit trail. Caller holds backupKeyMu.
func (s *Server) activateBackupKey(w http.ResponseWriter, r *http.Request, action, publicKey string, old []backupKeyView) (backupKeyView, bool) {
	view := backupKeyView{PublicKey: publicKey, Fingerprint: backupFingerprint(publicKey)}
	if err := writeBackupKeyFile(s.backupRecipientsPath(), publicKey+"\n"); err != nil {
		s.log.Error("backup key: cannot store the public key", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_key_unwritable", "the backup key cannot be stored")
		return view, false
	}
	if err := os.Remove(s.backupPendingPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Error("backup key: cannot remove the pending key", "err", err)
	}
	details := "fingerprint=" + view.Fingerprint
	if len(old) > 0 {
		details += " replaced=" + backupFingerprints(old)
	}
	s.auditBackupKey(r, action, details)
	return view, true
}

func (s *Server) auditBackupKey(r *http.Request, action, details string) {
	if err := s.auth.Audit(r.Context(), action, "backup", true, details); err != nil {
		s.log.Error("backup key: audit entry failed", "err", err, "details", details)
	}
}

// handleAdminBackupGenerate creates a key pair and shows the private key this
// one time: the variable below is the only place it ever exists on the server.
// The public key only becomes pending; generating again just overwrites that.
func (s *Server) handleAdminBackupGenerate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req backupKeyReq
	if !readJSON(w, r, &req) || !s.backupKeyStepUp(w, r, a, req) {
		return
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		s.log.Error("backup key: generation failed", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_key_failed", "the backup key could not be created")
		return
	}
	publicKey := id.Recipient().String()
	s.backupKeyMu.Lock()
	defer s.backupKeyMu.Unlock()
	if _, ok := s.backupKeyExists(w, req.Replace); !ok {
		return
	}
	content := "# session=" + strconv.FormatInt(a.Session.ID, 10) + "\n" + publicKey + "\n"
	if err := writeBackupKeyFile(s.backupPendingPath(), content); err != nil {
		s.log.Error("backup key: cannot store the pending key", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_key_unwritable", "the backup key cannot be stored")
		return
	}
	view := backupKeyView{PublicKey: publicKey, Fingerprint: backupFingerprint(publicKey)}
	s.auditBackupKey(r, "backup.key.generate", "pending fingerprint="+view.Fingerprint)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, struct {
		backupKeyView
		PrivateKey string `json:"privateKey"`
	}{view, id.String()})
}

// handleAdminBackupConfirm activates the pending key: "saved in the vault",
// from the session that was shown the private key and for that very key. It
// needs no new step-up, generating did. Repeating it after a lost answer is
// harmless: a fingerprint that is active already is answered with success.
func (s *Server) handleAdminBackupConfirm(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
		Replace     bool   `json:"replace"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.Bearer || a.Session.Kind != auth.KindWeb {
		writeError(w, http.StatusForbidden, "forbidden", "the backup key can only be managed in the web client")
		return
	}
	s.backupKeyMu.Lock()
	defer s.backupKeyMu.Unlock()
	pending, sessionID := s.readBackupPending()
	if pending == "" || backupFingerprint(pending) != req.Fingerprint || sessionID != a.Session.ID {
		active, _, _ := s.readBackupRecipients()
		for _, k := range active {
			if k.Fingerprint == req.Fingerprint {
				writeJSON(w, http.StatusOK, k)
				return
			}
		}
		writeError(w, http.StatusConflict, "backup_key_not_pending", "this key is no longer waiting for confirmation; create it again")
		return
	}
	old, ok := s.backupKeyExists(w, req.Replace)
	if !ok {
		return
	}
	if view, ok := s.activateBackupKey(w, r, "backup.key.confirm", pending, old); ok {
		writeJSON(w, http.StatusOK, view)
	}
}

// handleAdminBackupImport stores the administrator's own public key. A private
// key pasted by mistake is refused and not repeated anywhere. Its owner has the
// private key already, so it counts at once and replaces a pending key.
func (s *Server) handleAdminBackupImport(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req backupKeyReq
	if !readJSON(w, r, &req) {
		return
	}
	publicKey := strings.TrimSpace(req.PublicKey)
	if strings.Contains(strings.ToUpper(publicKey), "AGE-SECRET-KEY") {
		writeError(w, http.StatusBadRequest, "backup_key_private", "this is a private key; enter the public key (age1...)")
		return
	}
	// The error of the parser is not passed on: it could quote the input.
	if _, err := age.ParseX25519Recipient(publicKey); err != nil {
		writeError(w, http.StatusBadRequest, "backup_key_invalid", "not a valid age public key (age1...)")
		return
	}
	if !s.backupKeyStepUp(w, r, a, req) {
		return
	}
	s.backupKeyMu.Lock()
	defer s.backupKeyMu.Unlock()
	old, ok := s.backupKeyExists(w, req.Replace)
	if !ok {
		return
	}
	if view, ok := s.activateBackupKey(w, r, "backup.key.import", publicKey, old); ok {
		writeJSON(w, http.StatusOK, view)
	}
}
