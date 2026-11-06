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
	"strings"
	"time"

	"filippo.io/age"

	"github.com/Midas1989/Mailclient/internal/auth"
)

const (
	backupKeyDirName     = "backup"
	backupRecipientsName = "recipients.txt"
	backupStatusName     = "status.json"
)

// backupRecipientLine is what the backup job accepts as well. It is wider than
// what can be entered here: a list taken over from an existing installation
// may hold other age recipient types.
var backupRecipientLine = regexp.MustCompile(`^age1[0-9a-z]+$`)

func (s *Server) backupRecipientsPath() string {
	return filepath.Join(s.cfg.DataDir, backupKeyDirName, backupRecipientsName)
}

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

// writeBackupRecipient replaces the stored public keys atomically: the job
// reads either the old or the new list, never a part.
func (s *Server) writeBackupRecipient(publicKey string) error {
	path := s.backupRecipientsPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+backupRecipientsName+".tmp-*")
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
	if _, err := f.WriteString(publicKey + "\n"); err != nil {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"keys":    keys,
		"invalid": invalid,
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

// storeBackupKey writes the new public key and audits it. Only fingerprints
// reach the audit trail. A key already stored needs req.Replace.
func (s *Server) storeBackupKey(w http.ResponseWriter, r *http.Request, action, publicKey string, replace bool) (backupKeyView, bool) {
	s.backupKeyMu.Lock()
	defer s.backupKeyMu.Unlock()
	view := backupKeyView{PublicKey: publicKey, Fingerprint: backupFingerprint(publicKey)}
	old, invalid, err := s.readBackupRecipients()
	if err != nil {
		s.log.Error("backup key: cannot read the public key", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_key_unreadable", "the backup key cannot be read")
		return view, false
	}
	if (len(old) > 0 || invalid) && !replace {
		writeError(w, http.StatusConflict, "backup_key_exists", "a backup key exists; confirm replacing it")
		return view, false
	}
	if err := s.writeBackupRecipient(publicKey); err != nil {
		s.log.Error("backup key: cannot store the public key", "err", err)
		writeError(w, http.StatusInternalServerError, "backup_key_unwritable", "the backup key cannot be stored")
		return view, false
	}
	details := "fingerprint=" + view.Fingerprint
	if len(old) > 0 {
		details += " replaced=" + backupFingerprints(old)
	}
	if err := s.auth.Audit(r.Context(), action, "backup", true, details); err != nil {
		s.log.Error("backup key: audit entry failed", "err", err, "fingerprint", view.Fingerprint)
	}
	return view, true
}

// handleAdminBackupGenerate creates a key pair and shows the private key this
// one time: the variable below is the only place it ever exists on the server.
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
	view, ok := s.storeBackupKey(w, r, "backup.key.generate", id.Recipient().String(), req.Replace)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, struct {
		backupKeyView
		PrivateKey string `json:"privateKey"`
	}{view, id.String()})
}

// handleAdminBackupImport stores the administrator's own public key. A private
// key pasted by mistake is refused and not repeated anywhere.
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
	view, ok := s.storeBackupKey(w, r, "backup.key.import", publicKey, req.Replace)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, view)
}
