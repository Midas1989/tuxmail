// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Import/export and backup/restore commands (KONZEPT 13, 14) for scripting:
//
//	tuxmail import  --format mbox --source FILE [--data-dir DIR | --user NAME] [options]
//	tuxmail export  --format mbox --out FILE [--folder-id N,...] [--query Q] ...
//	tuxmail backup  --out FILE|DIR [--mode full] [--secrets] [--base PREV.txmb] ...
//	tuxmail backup  --server --out FILE [--config FILE]
//	tuxmail restore --in FILE [--inspect | --verify] [--mode replace|merge] ...
//	tuxmail restore --server --in FILE --target-dir DIR

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/backup"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/server"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/locales"
)

// ioTarget selects whose data a command works on.
type ioTarget struct {
	cfgPath  *string
	dataDir  *string
	user     *string
	portable *bool
}

func addTargetFlags(fs *flag.FlagSet, cfg *string) ioTarget {
	return ioTarget{cfgPath: cfg,
		dataDir:  fs.String("data-dir", "", "desktop data directory (default: the per-user data directory)"),
		user:     fs.String("user", "", "server mode: the user whose data is used (with --config)"),
		portable: fs.Bool("portable", false, "use the portable data directory next to the executable")}
}

// resolved is an opened data location.
type resolved struct {
	dir      string
	dataDir  string // server mode: the data_dir of the running service
	portable bool   // desktop: portable location (always the password vault)
	secrets  domain.SecretStore
	server   bool
	close    func()
}

// lockDir is where the engine lock of the location lives: one per data
// directory on the desktop, the whole data_dir on a server.
func (r *resolved) lockDir() string {
	if r.dataDir != "" {
		return r.dataDir
	}
	return r.dir
}

func (t ioTarget) resolve(ctx context.Context) (*resolved, error) {
	if *t.user != "" {
		cfg, err := config.Load(*t.cfgPath)
		if err != nil {
			return nil, err
		}
		svc, err := auth.Open(ctx, cfg)
		if err != nil {
			return nil, err
		}
		u, err := svc.GetUserByName(ctx, *t.user)
		if err != nil {
			svc.Close()
			return nil, fmt.Errorf("user %q: %w", *t.user, err)
		}
		sec, err := svc.UserSecrets(ctx, u.ID)
		if err != nil {
			svc.Close()
			return nil, err
		}
		return &resolved{dir: svc.UserDir(u.ID), dataDir: cfg.DataDir, secrets: sec, server: true, close: func() { svc.Close() }}, nil
	}
	exe, _ := os.Executable()
	loc, err := resolveDataDir(*t.dataDir, *t.portable, exe)
	if err != nil {
		return nil, err
	}
	return &resolved{dir: loc.Dir, portable: loc.Portable, close: func() {}}, nil
}

// openApp opens the application layer of the location (sync only when
// needed, i.e. for imports into server folders).
func (c *cli) openApp(ctx context.Context, r *resolved, withSync bool) (*app.App, *store.Store, error) {
	a, st, err := c.openAppOptions(ctx, r, true, withSync)
	if err == nil && withSync {
		a.Start()
	}
	return a, st, err
}

// openAppOptions opens the application layer without starting it. configured
// wires sync and send in server mode (the desktop variant always has them);
// withMessenger adds the messenger plugins on the desktop.
func (c *cli) openAppOptions(ctx context.Context, r *resolved, withMessenger, configured bool) (*app.App, *store.Store, error) {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return nil, nil, err
	}
	if _, err := backup.ApplyPending(r.dir); err != nil {
		return nil, nil, err
	}
	log := slog.New(slog.NewTextHandler(c.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if !r.server {
		return openDesktopApp(ctx, dataLocation{Dir: r.dir, Portable: r.portable}, log, withMessenger)
	}
	st, err := store.Open(ctx, filepath.Join(r.dir, server.UserStoreFile))
	if err != nil {
		return nil, nil, err
	}
	opts := app.Options{Store: st, DataDir: r.dir, Mode: app.ModeServer, Version: version, Secrets: r.secrets, Logger: log, Locales: locales.FS}
	if configured {
		opts.Sync, opts.Send = &app.SyncOptions{}, &app.SendOptions{}
	}
	a, err := app.New(ctx, opts)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return a, st, nil
}

func splitIDs(s string) ([]int64, error) {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid id %q", errUsage, p)
		}
		out = append(out, n)
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// waitJob polls a job, printing progress to stderr, and returns it.
func (c *cli) waitJob(ctx context.Context, a *app.App, job app.IOJob, quiet bool) (app.IOJob, error) {
	last := ""
	for {
		j, err := a.GetIOJob(context.Background(), job.ID)
		if err != nil {
			return j, err
		}
		if !quiet {
			line := fmt.Sprintf("%s %s: %d", j.Kind, j.Format, j.Done)
			if j.Total > 0 {
				line += fmt.Sprintf("/%d", j.Total)
			}
			if j.Current != "" {
				line += " (" + j.Current + ")"
			}
			if line != last {
				fmt.Fprintf(c.stderr, "\r%s   ", line)
				last = line
			}
		}
		if j.Status != "running" {
			if !quiet {
				fmt.Fprintln(c.stderr)
			}
			return j, nil
		}
		select {
		case <-ctx.Done():
			_ = a.CancelIOJob(context.Background(), job.ID)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (c *cli) printJob(j app.IOJob, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(j); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(c.stdout, "%s %s: %s, %d processed, %d skipped, %d failed\n", j.Kind, j.Format, j.Status, j.Done, j.Skipped, j.Failed)
		for k, v := range j.Summary {
			fmt.Fprintf(c.stdout, "  %s: %d\n", k, v)
		}
		for _, p := range j.Problems {
			fmt.Fprintf(c.stdout, "  problem: %s: %s\n", p.Item, p.Error)
		}
		if j.Resumable {
			fmt.Fprintf(c.stdout, "  resume with: tuxmail import --resume %s\n", j.ID)
		}
	}
	switch j.Status {
	case "failed":
		return errors.New(j.Error)
	case "canceled":
		return errors.New("canceled")
	}
	return nil
}

// ==== import ===================================================================

func (c *cli) importCmd(ctx context.Context, args []string) error {
	fs, cfg := newFlagSet("import", c)
	t := addTargetFlags(fs, cfg)
	format := fs.String("format", "", "eml | mbox | maildir | pst | thunderbird | ics | vcf | csv | ldif")
	source := fs.String("source", "", "file or directory to import")
	target := fs.String("target", "local", "local (Local Folders) | folder (existing server folder, uploaded via IMAP)")
	folderID := fs.Int64("folder-id", 0, "target folder (folder) or parent folder (local)")
	folderName := fs.String("folder", "", "folder for all messages when the structure is not kept (default Imported)")
	keep := fs.Bool("keep-structure", true, "keep the folder structure")
	dedupe := fs.Bool("dedupe", true, "skip messages whose Message-ID already exists")
	flags := fs.Bool("keep-flags", true, "keep read/flagged/answered flags")
	dates := fs.Bool("keep-dates", true, "keep the received dates")
	variant := fs.String("mbox-variant", "mboxrd", "mboxrd | mboxo")
	throttle := fs.Int("throttle-ms", 0, "pause between uploads to a server folder")
	dups := fs.String("duplicates", "skip", "calendar UID duplicates: skip | overwrite | copy")
	csvPreset := fs.String("csv-preset", "", "CSV layout: outlook | google | thunderbird (default: detect)")
	csvMap := fs.String("csv-mapping", "", "CSV: comma-separated target field per column (see --inspect)")
	calID := fs.Int64("calendar-id", 0, "target calendar (default: a new local calendar)")
	abID := fs.Int64("addressbook-id", 0, "target address book (default: a new local one)")
	tbAcc := fs.Bool("tb-accounts", true, "thunderbird: create the mail accounts")
	tbMail := fs.Bool("tb-mail", true, "thunderbird: import the local folders")
	tbAB := fs.Bool("tb-addressbooks", true, "thunderbird: import the address books")
	resume := fs.String("resume", "", "resume an interrupted import job")
	inspect := fs.Bool("inspect", false, "only show what the file contains (CSV: header and suggested mapping)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	quiet := fs.Bool("quiet", false, "no progress output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *resume == "" && (*format == "" || *source == "") {
		fs.Usage()
		return fmt.Errorf("%w: --format and --source are required", errUsage)
	}
	r, err := t.resolve(ctx)
	if err != nil {
		return err
	}
	defer r.close()
	src := *source
	if src != "" {
		if src, err = filepath.Abs(src); err != nil {
			return err
		}
	}
	a, st, err := c.openApp(ctx, r, *target == "folder")
	if err != nil {
		return err
	}
	defer st.Close()
	defer a.Close()
	ios := app.IOSource{Format: *format, Path: src}
	if *inspect {
		p, err := a.InspectImport(ctx, ios)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	req := app.ImportRequest{Source: ios, ResumeJobID: *resume,
		Target: app.ImportTarget{Kind: *target, FolderID: *folderID, FolderName: *folderName, CalendarID: *calID, AddressbookID: *abID},
		Options: app.ImportOptions{KeepStructure: *keep, Dedupe: *dedupe, KeepFlags: *flags, KeepDates: *dates, MboxVariant: *variant,
			ThrottleMs: *throttle, DuplicateMode: *dups, CSVPreset: *csvPreset, CSVMapping: splitList(*csvMap),
			TBAccounts: *tbAcc, TBMail: *tbMail, TBAddressBooks: *tbAB}}
	job, err := a.StartImport(ctx, req)
	if err != nil {
		return err
	}
	j, err := c.waitJob(ctx, a, job, *quiet || *asJSON)
	if err != nil {
		return err
	}
	if *target == "folder" && j.Status == "done" {
		c.waitOps(ctx, st, *quiet)
	}
	return c.printJob(j, *asJSON)
}

// waitOps waits until the queued uploads (IMAP APPEND) are done.
func (c *cli) waitOps(ctx context.Context, st *store.Store, quiet bool) {
	deadline := time.Now().Add(2 * time.Hour)
	for time.Now().Before(deadline) {
		var n int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM ops WHERE state IN ('pending', 'running')`).Scan(&n); err != nil || n == 0 {
			return
		}
		if !quiet {
			fmt.Fprintf(c.stderr, "\rwaiting for %d uploads   ", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// ==== export ===================================================================

func (c *cli) exportCmd(ctx context.Context, args []string) error {
	fs, cfg := newFlagSet("export", c)
	t := addTargetFlags(fs, cfg)
	format := fs.String("format", "", "eml (zip) | mbox | maildir (zip) | ics | vcf | csv | ldif")
	out := fs.String("out", "", "output file")
	folders := fs.String("folder-id", "", "comma-separated folder ids")
	query := fs.String("query", "", "search query selecting the messages")
	conv := fs.Int64("conversation", 0, "conversation id")
	project := fs.Int64("project", 0, "project id")
	cals := fs.String("calendar-id", "", "comma-separated calendar ids (default: all)")
	from := fs.String("from", "", "calendar range start (YYYY-MM-DD)")
	to := fs.String("to", "", "calendar range end (YYYY-MM-DD)")
	books := fs.String("addressbook-id", "", "comma-separated address book ids (default: all)")
	vc := fs.String("vcard", "4.0", "vCard version: 3.0 | 4.0")
	csvPreset := fs.String("csv-preset", "outlook", "CSV layout: outlook | google")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	quiet := fs.Bool("quiet", false, "no progress output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format == "" || *out == "" {
		fs.Usage()
		return fmt.Errorf("%w: --format and --out are required", errUsage)
	}
	fids, err := splitIDs(*folders)
	if err != nil {
		return err
	}
	cids, err := splitIDs(*cals)
	if err != nil {
		return err
	}
	bids, err := splitIDs(*books)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	r, err := t.resolve(ctx)
	if err != nil {
		return err
	}
	defer r.close()
	a, st, err := c.openApp(ctx, r, false)
	if err != nil {
		return err
	}
	defer st.Close()
	defer a.Close()
	job, err := a.StartExport(ctx, app.ExportRequest{Format: *format, Path: abs,
		Selection:   app.MailSelection{FolderIDs: fids, Query: *query, ConversationID: *conv, ProjectID: *project},
		CalendarIDs: cids, From: *from, To: *to, AddressbookIDs: bids, VCardVersion: *vc, CSVPreset: *csvPreset})
	if err != nil {
		return err
	}
	j, err := c.waitJob(ctx, a, job, *quiet || *asJSON)
	if err != nil {
		return err
	}
	return c.printJob(j, *asJSON)
}

// ==== backup ===================================================================

// readPassphrase takes the passphrase from --passphrase-file, stdin or the
// environment variable TUXMAIL_BACKUP_PASSPHRASE.
func (c *cli) readPassphrase(file string, stdin bool) (string, error) {
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	case stdin:
		line, err := bufio.NewReader(c.stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	return os.Getenv("TUXMAIL_BACKUP_PASSPHRASE"), nil
}

// backupSettingsCmd writes the settings of a server (no mail, chats, calendar or
// contact data) as a tree into an existing private directory. The tree is not
// encrypted: tuxmail-backup puts it into its age archive and removes it. The
// service may keep running.
func (c *cli) backupSettingsCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup-settings", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	dataDir := fs.String("data-dir", "", "data directory of the server")
	out := fs.String("out", "", "existing private directory (mode 0700) for the result")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" || *out == "" {
		fs.Usage()
		return fmt.Errorf("%w: --data-dir and --out are required", errUsage)
	}
	fi, err := os.Stat(*out)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be a directory only its owner can access", *out)
	}
	rep, err := backup.CreateSettings(ctx, *dataDir, *out)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "settings of %d users and %d team mailboxes, %d bytes\n", rep.Users, rep.Teams, rep.Bytes)
	return nil
}

func (c *cli) backupCmd(ctx context.Context, args []string) error {
	fs, cfg := newFlagSet("backup", c)
	t := addTargetFlags(fs, cfg)
	out := fs.String("out", "", "archive file, or an existing directory (the archive is named automatically)")
	webdav := fs.String("webdav", "", "WebDAV collection URL instead of --out (user/password: --webdav-user, TUXMAIL_WEBDAV_PASSWORD)")
	webdavUser := fs.String("webdav-user", "", "WebDAV user name")
	mode := fs.String("mode", "full", "config | local | full | custom")
	accounts := fs.String("accounts", "", "custom: comma-separated account ids")
	areas := fs.String("areas", "", "custom: mail,calendar,contacts,chats,settings")
	withSecrets := fs.Bool("secrets", false, "include the stored credentials (encrypted only)")
	passFile := fs.String("passphrase-file", "", "read the passphrase from a file")
	passStdin := fs.Bool("passphrase-stdin", false, "read the passphrase from stdin")
	base := fs.String("base", "", "previous archive: write an incremental backup against it")
	srv := fs.Bool("server", false, "admin: full-server backup of data_dir (server.db, master.key, vapid.json, all users)")
	asJSON := fs.Bool("json", false, "print the manifest as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" && *webdav == "" {
		fs.Usage()
		return fmt.Errorf("%w: --out or --webdav is required", errUsage)
	}
	pass, err := c.readPassphrase(*passFile, *passStdin)
	if err != nil {
		return err
	}
	var m *backup.Manifest
	write := func(name string, fn func(w io.Writer) (*backup.Manifest, error)) error {
		if *webdav != "" {
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			tgt := backup.WebDAVTarget{URL: *webdav, Username: *webdavUser, Password: os.Getenv("TUXMAIL_WEBDAV_PASSWORD")}
			go func() { done <- tgt.Put(ctx, name, pr) }()
			var err error
			m, err = fn(pw)
			pw.CloseWithError(err)
			if perr := <-done; err == nil {
				err = perr
			}
			if err == nil {
				fmt.Fprintln(c.stderr, "written to", strings.TrimRight(*webdav, "/")+"/"+name)
			}
			return err
		}
		path := *out
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			path = filepath.Join(path, name)
		}
		tmp := path + ".partial"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		m, err = fn(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		fmt.Fprintln(c.stderr, "written to", path)
		return nil
	}
	if *srv {
		conf, err := config.Load(*cfg)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.stderr, "WARNING:", backup.ServerWarning)
		name := "tuxmail-server-backup-" + time.Now().Format("2006-01-02-1504") + backup.Extension
		err = write(name, func(w io.Writer) (*backup.Manifest, error) {
			return backup.CreateServer(ctx, w, backup.ServerOptions{DataDir: conf.DataDir, Passphrase: pass, AppVersion: version})
		})
		if err != nil {
			return err
		}
		return c.printManifest(m, *asJSON)
	}
	accs, err := splitIDs(*accounts)
	if err != nil {
		return err
	}
	var baseM *backup.Manifest
	if *base != "" {
		f, err := os.Open(*base)
		if err != nil {
			return err
		}
		baseM, err = backup.Inspect(f, pass)
		f.Close()
		if err != nil {
			return fmt.Errorf("base archive: %w", err)
		}
	}
	r, err := t.resolve(ctx)
	if err != nil {
		return err
	}
	defer r.close()
	a, st, err := c.openApp(ctx, r, false)
	if err != nil {
		return err
	}
	defer st.Close()
	defer a.Close()
	sec := r.secrets
	if sec == nil {
		sec = a.Secrets()
	}
	opts := backup.Options{Store: st, Blobs: a.Blobs(), Secrets: sec, SecretRefs: []string{"ai:api-key"},
		Scope:      backup.Scope{Mode: backup.Mode(*mode), Accounts: accs, Areas: splitList(*areas), Secrets: *withSecrets},
		Passphrase: pass, Base: baseM, AppVersion: version}
	// The name contains the manifest id, which is only known afterwards for
	// files; use a time-based name for --out directories and WebDAV.
	name := "tuxmail-backup-" + time.Now().Format("2006-01-02-1504") + backup.Extension
	if err := write(name, func(w io.Writer) (*backup.Manifest, error) { return backup.Create(ctx, w, opts) }); err != nil {
		return err
	}
	return c.printManifest(m, *asJSON)
}

func (c *cli) printManifest(m *backup.Manifest, asJSON bool) error {
	if m == nil {
		return nil
	}
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	}
	fmt.Fprintf(c.stdout, "backup %s (%s), schema %d, encrypted %v, secrets %v\n", m.ID, m.Scope.Mode, m.SchemaVersion, m.Encrypted, m.Secrets)
	if m.BaseID != "" {
		fmt.Fprintf(c.stdout, "  incremental, base %s\n", m.BaseID)
	}
	for _, acc := range m.Accounts {
		fmt.Fprintf(c.stdout, "  account %d: %s (%s) %d messages\n", acc.ID, acc.Name, acc.Kind, acc.Messages)
	}
	fmt.Fprintf(c.stdout, "  files %d, blobs %d (%d in this archive)\n", len(m.Files), len(m.Blobs), m.BlobsIncluded)
	if m.HasMessenger() {
		fmt.Fprintln(c.stdout, "  note: messenger sessions included; WhatsApp/Signal may require pairing again after a restore on another device")
	}
	if m.Server {
		fmt.Fprintln(c.stdout, "  WARNING:", backup.ServerWarning)
	}
	return nil
}

// ==== restore ==================================================================

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func (c *cli) restoreCmd(ctx context.Context, args []string) error {
	fs, cfg := newFlagSet("restore", c)
	t := addTargetFlags(fs, cfg)
	in := fs.String("in", "", "archive to restore")
	var bases multiFlag
	fs.Var(&bases, "base", "base archive of an incremental backup (repeatable)")
	inspect := fs.Bool("inspect", false, "only show the content of the archive")
	verify := fs.Bool("verify", false, "verify all checksums and exit")
	mode := fs.String("mode", "replace", "replace (rebuild the data directory) | merge (add accounts to the existing data)")
	accounts := fs.String("accounts", "", "restore only these account ids (as listed by --inspect)")
	configOnly := fs.Bool("config-only", false, "restore only accounts, identities, signatures and settings")
	withSecrets := fs.Bool("secrets", false, "restore the stored credentials (if included)")
	passFile := fs.String("passphrase-file", "", "read the passphrase from a file")
	passStdin := fs.Bool("passphrase-stdin", false, "read the passphrase from stdin")
	srv := fs.Bool("server", false, "admin: restore a full-server backup into --target-dir")
	targetDir := fs.String("target-dir", "", "server: empty data directory to restore into")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		fs.Usage()
		return fmt.Errorf("%w: --in is required", errUsage)
	}
	pass, err := c.readPassphrase(*passFile, *passStdin)
	if err != nil {
		return err
	}
	src := backup.FileSource(*in)
	var bs []backup.Source
	for _, b := range bases {
		bs = append(bs, backup.FileSource(b))
	}
	if *inspect {
		f, err := os.Open(*in)
		if err != nil {
			return err
		}
		defer f.Close()
		m, err := backup.Inspect(f, pass)
		if err != nil {
			return err
		}
		return c.printManifest(m, *asJSON)
	}
	if *verify {
		m, err := backup.Verify(ctx, src, pass, bs, "")
		if err != nil {
			return err
		}
		fmt.Fprintln(c.stderr, "archive OK")
		return c.printManifest(m, *asJSON)
	}
	if *srv {
		if *targetDir == "" {
			return fmt.Errorf("%w: --target-dir is required with --server", errUsage)
		}
		m, err := backup.RestoreServer(ctx, src, pass, *targetDir)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.stderr, "server data restored to", *targetDir, "- point data_dir there and start tuxmail serve")
		return c.printManifest(m, *asJSON)
	}
	accs, err := splitIDs(*accounts)
	if err != nil {
		return err
	}
	r, err := t.resolve(ctx)
	if err != nil {
		return err
	}
	defer r.close()
	ex, err := backup.Extract(ctx, src, pass, bs, "")
	if err != nil {
		return err
	}
	defer ex.Close()
	opts := backup.RestoreOptions{Mode: backup.RestoreMode(*mode), Accounts: accs, ConfigOnly: *configOnly, Secrets: *withSecrets, DataDir: r.dir}
	var res *backup.RestoreResult
	switch opts.Mode {
	case backup.Replace:
		// The data directory must not be in use.
		sec := r.secrets
		if sec == nil && *withSecrets {
			a, st, err := c.openApp(ctx, r, false)
			if err != nil {
				return err
			}
			sec = a.Secrets()
			a.Close()
			st.Close()
		}
		opts.SecretStore = sec
		res, err = backup.Restore(ctx, ex, opts)
	case backup.Merge:
		a, st, oerr := c.openApp(ctx, r, false)
		if oerr != nil {
			return oerr
		}
		defer st.Close()
		defer a.Close()
		bl, berr := blob.New(filepath.Join(r.dir, "blobs"))
		if berr != nil {
			return berr
		}
		opts.Store, opts.Blobs, opts.SecretStore = st, bl, a.Secrets()
		res, err = backup.Restore(ctx, ex, opts)
	default:
		return fmt.Errorf("%w: --mode must be replace or merge", errUsage)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprintf(c.stdout, "restored (%s): %d accounts, %d messages, %d blobs, %d secrets; schema %d -> %d\n", res.Mode, res.Accounts,
		res.Messages, res.Blobs, res.Secrets, res.SchemaFrom, res.SchemaTo)
	for _, w := range res.Warnings {
		if w == "messenger" {
			fmt.Fprintln(c.stdout, "  note: WhatsApp/Signal sessions may require pairing this device again")
			continue
		}
		fmt.Fprintln(c.stdout, "  warning:", w)
	}
	return nil
}
