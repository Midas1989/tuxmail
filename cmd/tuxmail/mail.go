// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Terminal mode (docs/ENTWICKLUNG.md "Terminal-Modus"):
//
//	tuxmail mail folders|list|read|send|reply|search|mark|move [--json] [--data-dir DIR | --user NAME] ...
//	tuxmail term [--data-dir DIR | --user NAME]
//
// The command syntax lives in internal/mailcmd. Who may do what: reading uses
// the database next to a running TuxMail (SQLite WAL with busy timeout); the
// sync engine is one per data directory, so changes need the engine lock
// (internal/instance) and are refused otherwise (exit code 3).

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
	"github.com/Midas1989/Mailclient/internal/instance"
	"github.com/Midas1989/Mailclient/internal/mailcmd"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/termui"
)

const maxMailStdin = 10 << 20

// mailDrainTimeout is how long a one-shot change waits for its operations to
// reach the server before reporting them as pending.
var mailDrainTimeout = 20 * time.Second

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

func (o *mailOptions) target() ioTarget {
	return ioTarget{cfgPath: &o.config, dataDir: &o.dataDir, user: &o.user, portable: &o.portable}
}

// mailSession is an opened data location.
type mailSession struct {
	a       *app.App
	st      *store.Store
	lock    *instance.Lock
	release func()
}

func (s *mailSession) close() {
	if s.a != nil {
		s.a.Close()
	}
	if s.st != nil {
		s.st.Close()
	}
	s.lock.Release()
	if s.release != nil {
		s.release()
	}
}

// openMail opens the data of the options. With engine the sync engine runs in
// this process; that needs the engine lock (errors.Is(err, mailcmd.ErrBusy)
// when another process holds it). Without engine nothing but the database is
// touched. Log output is dropped: it would disturb scripts and the screen.
func (c *cli) openMail(ctx context.Context, o *mailOptions, engine bool) (*mailSession, error) {
	r, err := o.target().resolve(ctx)
	if err != nil {
		return nil, err
	}
	s := &mailSession{release: r.close}
	fail := func(err error) (*mailSession, error) {
		s.close()
		return nil, err
	}
	if !exists(filepath.Join(r.dir, "tuxmail.db")) {
		return fail(fmt.Errorf("no TuxMail data in %s (--data-dir or --user select another one)", r.dir))
	}
	if engine {
		s.lock, err = instance.Acquire(r.lockDir())
		if errors.Is(err, instance.ErrHeld) {
			return fail(mailcmd.ErrBusy)
		}
		if err != nil {
			return fail(err)
		}
	}
	quiet := *c
	quiet.stderr = io.Discard
	s.a, s.st, err = quiet.openAppOptions(ctx, r, false, engine)
	if err != nil {
		return fail(err)
	}
	if err := c.unlockVault(ctx, s.a, o.vaultFile); err != nil {
		return fail(err)
	}
	if engine {
		s.a.Start()
	}
	return s, nil
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
	write := cmd.Name == "send" || cmd.Name == "reply" || cmd.Name == "mark" || cmd.Name == "move"
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
	s, err := c.openMail(ctx, &o, write)
	if err != nil {
		return err
	}
	defer s.close()
	svc := &mailcmd.Service{B: s.a}
	res, err := svc.Execute(ctx, cmd, 0)
	if err != nil && !errors.Is(err, mailcmd.ErrBlocked) {
		return err
	}
	if write && err == nil {
		c.drainOps(ctx, s.a)
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
func (c *cli) drainOps(ctx context.Context, a *app.App) {
	deadline := time.Now().Add(mailDrainTimeout)
	for {
		n, err := a.PendingMailOps(ctx)
		if err != nil || n == 0 {
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			fmt.Fprintf(c.stderr, "tuxmail: %d change(s) not on the server yet; they run with the next sync\n", n)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// termCmd runs the full-screen interface. With another TuxMail running on the
// same data it shows the mail read-only; otherwise it runs the sync itself.
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
	readOnly := false
	s, err := c.openMail(ctx, o, true)
	if errors.Is(err, mailcmd.ErrBusy) {
		readOnly = true
		s, err = c.openMail(ctx, o, false)
	}
	if err != nil {
		return err
	}
	defer s.close()
	return termui.Run(ctx, &mailcmd.Service{B: s.a, ReadOnly: readOnly}, in, out)
}
