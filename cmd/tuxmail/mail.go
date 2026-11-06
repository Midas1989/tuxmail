// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Terminal mode (docs/ENTWICKLUNG.md "Terminal-Modus"):
//
//	tuxmail mail folders|list|read|send|reply|search|mark|move [--json] [--data-dir DIR | --user NAME] ...
//	tuxmail term [--data-dir DIR | --user NAME]
//
// The command syntax lives in internal/mailcmd. Reading opens the database
// read-only and holds no lock. The sync engine is one per data directory: a
// change takes the engine lock (internal/instance) for its duration and is
// refused if it is held (exit code 3); on server data changes are always
// refused, the service owns that engine.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	xterm "github.com/charmbracelet/x/term"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/instance"
	"github.com/Midas1989/Mailclient/internal/mailcmd"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/termui"
	"github.com/Midas1989/Mailclient/locales"
)

const maxMailStdin = 10 << 20

// mailDrainTimeout is how long a one-shot change waits for its operations to
// reach the server before reporting them as pending.
var mailDrainTimeout = 20 * time.Second

// mailWriteTimeout bounds one change including that wait, so that a starting
// desktop window (engineLockWait) never has to run next to it.
var mailWriteTimeout = 25 * time.Second

// mailOptions are the options around a command (not part of its syntax).
type mailOptions struct {
	config, dataDir, user, vaultFile string
	portable, asJSON                 bool
}

// splitMailOptions separates the instance options from the command's words.
func splitMailOptions(args []string) (mailOptions, []string, error) {
	o := mailOptions{config: DefaultConfigPath}
	values := map[string]*string{"config": &o.config, "data-dir": &o.dataDir, "user": &o.user, "vault-passphrase-file": &o.vaultFile}
	bools := map[string]*bool{"portable": &o.portable, "json": &o.asJSON}
	var rest []string
	for i := 0; i < len(args); i++ {
		w := args[i]
		if w == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(w, "--"), "=")
		if !strings.HasPrefix(w, "--") {
			rest = append(rest, w)
			continue
		}
		if b, ok := bools[name]; ok && !hasVal {
			*b = true
			continue
		}
		v, ok := values[name]
		if !ok {
			rest = append(rest, w)
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("%w: option --%s needs a value", errUsage, name)
			}
			i++
			val = args[i]
		}
		*v = val
	}
	return o, rest, nil
}

// mailSession is opened data: reads go to a read-only App that is never
// started and holds no lock, so it neither delays nor disturbs a service.
type mailSession struct {
	svc *mailcmd.Service
	st  *store.Store
	a   *app.App
}

func (s *mailSession) close() {
	s.a.Close()
	s.st.Close()
}

// locateMail finds the data directory without keeping anything open. server
// says the sync engine there belongs to `tuxmail serve`.
func (o *mailOptions) locateMail(ctx context.Context) (dir string, loc dataLocation, server bool, err error) {
	if o.user != "" {
		cfg, err := config.Load(o.config)
		if err != nil {
			return "", loc, true, err
		}
		// Before anything else opens a file there (auth.Open writes).
		if err := checkDataOwner(cfg.DataDir); err != nil {
			return "", loc, true, err
		}
		svc, err := auth.Open(ctx, cfg)
		if err != nil {
			return "", loc, true, err
		}
		defer svc.Close()
		u, err := svc.GetUserByName(ctx, o.user)
		if err != nil {
			return "", loc, true, fmt.Errorf("user %q: %w", o.user, err)
		}
		return svc.UserDir(u.ID), loc, true, nil
	}
	exe, _ := os.Executable()
	if loc, err = resolveDataDir(o.dataDir, o.portable, exe); err != nil {
		return "", loc, false, err
	}
	if err := checkDataOwner(loc.Dir); err != nil {
		return "", loc, false, err
	}
	return loc.Dir, loc, isServerUserDir(loc.Dir), nil
}

// isServerUserDir recognises <data_dir>/users/<id> given as --data-dir.
func isServerUserDir(dir string) bool {
	users := filepath.Dir(dir)
	return filepath.Base(users) == "users" && exists(filepath.Join(filepath.Dir(users), instance.FileName))
}

// checkDataOwner refuses to work on data of another system user: files created
// here (database -wal/-shm, locks) would belong to the caller, typically root,
// and the service could no longer open them.
func checkDataOwner(dir string) error {
	owner, ok := dataOwner(dir)
	if !ok {
		return nil
	}
	return ownerError(os.Geteuid(), owner, dir)
}

func ownerError(euid, owner int, dir string) error {
	if euid == owner {
		return nil
	}
	return fmt.Errorf("%s belongs to another system user (uid %d, you are %d); run this as that user, e.g. sudo -u tuxmail tuxmail ...", dir, owner, euid)
}

// openMail opens the data for reading and prepares changes: never on server
// data, otherwise each change takes the engine lock and runs the sync engine
// only for its duration.
func (c *cli) openMail(ctx context.Context, o *mailOptions, notice io.Writer) (*mailSession, error) {
	dir, loc, server, err := o.locateMail(ctx)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "tuxmail.db")
	if !exists(dbPath) {
		return nil, fmt.Errorf("no TuxMail data in %s (--data-dir or --user select another one)", dir)
	}
	st, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	mode := app.ModeDesktop
	if server {
		mode = app.ModeServer // keeps the server rules (audit ND-01), also here
	}
	quietLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(ctx, app.Options{Store: st, DataDir: dir, Mode: mode, ReadOnly: true, Version: version, Logger: quietLog, Locales: locales.FS})
	if err != nil {
		st.Close()
		return nil, err
	}
	svc := &mailcmd.Service{B: a}
	if server {
		svc.ReadOnly, svc.Refusal = true, mailcmd.ErrServerData
	} else {
		svc.Writer = c.mailWriter(loc, o.vaultFile, notice)
	}
	return &mailSession{svc: svc, st: st, a: a}, nil
}

// mailWriter runs one change: engine lock, sync engine (without messengers),
// the change, time for it to reach the server, everything released again.
func (c *cli) mailWriter(loc dataLocation, vaultFile string, notice io.Writer) func(context.Context, func(mailcmd.Backend) error) error {
	return func(ctx context.Context, change func(mailcmd.Backend) error) error {
		ctx, cancel := context.WithTimeout(ctx, mailWriteTimeout)
		defer cancel()
		lock, err := instance.Acquire(loc.Dir)
		if errors.Is(err, instance.ErrHeld) {
			return mailcmd.ErrBusy
		}
		if err != nil {
			return err
		}
		defer lock.Release()
		quiet := *c
		quiet.stderr = io.Discard
		a, st, err := quiet.openAppOptions(ctx, &resolved{dir: loc.Dir, portable: loc.Portable}, false, true)
		if err != nil {
			return err
		}
		defer st.Close()
		defer a.Close()
		if err := c.unlockVault(ctx, a, vaultFile); err != nil {
			return err
		}
		a.Start()
		if err := change(a); err != nil {
			return err
		}
		drainOps(ctx, a, notice)
		return nil
	}
}

// unlockVault opens an existing password vault with the passphrase from a
// file; passwords are never taken from arguments. A vault is never created.
func (c *cli) unlockVault(ctx context.Context, a *app.App, file string) error {
	if file == "" {
		return nil
	}
	st, err := a.VaultStatus(ctx)
	if err != nil || !st.Locked || !st.Initialized {
		return err
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return a.UnlockVault(ctx, strings.TrimRight(string(b), "\r\n"))
}

func (c *cli) mailUsage() {
	fmt.Fprintln(c.stderr, "Usage: tuxmail mail COMMAND [--json] [--data-dir DIR | --portable | --user NAME [--config FILE]] [--vault-passphrase-file FILE]")
	for _, s := range mailcmd.Specs {
		if !s.UI {
			fmt.Fprintln(c.stderr, "  "+s.Usage)
		}
	}
	fmt.Fprintln(c.stderr, "send and reply read the text from standard input unless --text is given.")
	fmt.Fprintln(c.stderr, "Exit codes: 0 ok, 1 error, 2 usage, 3 another TuxMail is running (changes refused), 4 not found, 5 blocked by send checks.")
}

// exitError carries a specific process exit code to run().
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// mailCmd runs one `tuxmail mail` command.
func (c *cli) mailCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		c.mailUsage()
		if len(args) == 0 {
			return &exitError{code: mailcmd.ExitUsage, err: errors.New("missing mail command")}
		}
		return nil
	}
	err := c.mailRun(ctx, args[0], args[1:])
	if err == nil {
		return nil
	}
	// Server or mail text can end up in an error message.
	return &exitError{code: mailcmd.ExitCode(err), err: errors.New(mailcmd.CleanLine(err.Error()))}
}

func (c *cli) mailRun(ctx context.Context, name string, args []string) error {
	o, words, err := splitMailOptions(args)
	if err != nil {
		return &mailcmd.ParseError{Code: mailcmd.ErrNoValue, Detail: err.Error()}
	}
	cmd, err := mailcmd.ParseArgs(name, words)
	if err != nil {
		return err
	}
	if spec, _ := mailcmd.Lookup(cmd.Name); spec.UI {
		return &mailcmd.ParseError{Code: mailcmd.ErrUnknown, Detail: name}
	}
	if cmd.Name == "send" || cmd.Name == "reply" {
		if _, given := cmd.Flags["text"]; !given {
			// Read standard input before touching the data, like `user add`.
			b, err := io.ReadAll(io.LimitReader(c.stdin, maxMailStdin+1))
			if err != nil {
				return err
			}
			if len(b) > maxMailStdin {
				return errors.New("message text on standard input is too large")
			}
			cmd.Flags["text"] = string(b)
		}
	}
	s, err := c.openMail(ctx, &o, c.stderr)
	if err != nil {
		return err
	}
	defer s.close()
	res, err := s.svc.Execute(ctx, cmd, 0)
	if err != nil && !errors.Is(err, mailcmd.ErrBlocked) {
		return err
	}
	var out error
	if o.asJSON {
		out = res.JSON(c.stdout)
	} else {
		out = res.Text(c.stdout)
	}
	if err != nil {
		return err
	}
	return out
}

// drainOps gives the queued operations time to reach the server. What is left
// stays queued in the database and runs with the next sync.
func drainOps(ctx context.Context, a *app.App, notice io.Writer) {
	deadline := time.Now().Add(mailDrainTimeout)
	for {
		n, err := a.PendingMailOps(context.WithoutCancel(ctx))
		if err != nil || n == 0 {
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			fmt.Fprintf(notice, "tuxmail: %d change(s) not on the server yet; they run with the next sync\n", n)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// termCmd runs the full-screen interface. It only reads and holds no lock, so
// a service starts and runs undisturbed next to it. Changes work like one
// `tuxmail mail` call each and are refused on server data.
func (c *cli) termCmd(ctx context.Context, args []string) error {
	fs, cfgPath := newFlagSet("term", c)
	t := addTargetFlags(fs, cfgPath)
	vault := fs.String("vault-passphrase-file", "", "file with the passphrase of an existing password vault")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: term takes no arguments", errUsage)
	}
	in, inOK := c.stdin.(*os.File)
	out, outOK := c.stdout.(*os.File)
	if !inOK || !outOK || !xterm.IsTerminal(in.Fd()) || !xterm.IsTerminal(out.Fd()) {
		return errors.New("tuxmail term needs an interactive terminal; use `tuxmail mail` in scripts")
	}
	// Nothing may write on the screen behind the interface's back.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	o := &mailOptions{config: *t.cfgPath, dataDir: *t.dataDir, user: *t.user, portable: *t.portable, vaultFile: *vault}
	s, err := c.openMail(ctx, o, io.Discard)
	if err != nil {
		return err
	}
	defer s.close()
	return termui.Run(ctx, s.svc, in, out)
}
