// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail is the TuxMail desktop application and server.
//
//	tuxmail              (desktop builds: opens the window)
//	tuxmail desktop      [--data-dir DIR] [--portable]
//	tuxmail version
//	tuxmail serve        --config /etc/tuxmail/config.yaml
//	tuxmail check-config --config /etc/tuxmail/config.yaml
//	tuxmail user add|list|disable|enable|reset-password|reset-2fa|unlock|set-role|delete --config ...
//	tuxmail import|export|backup|restore [--data-dir DIR | --user NAME] ...  (see io.go)
//	tuxmail backup-settings --data-dir DIR --out DIR
//	tuxmail mail folders|list|read|send|reply|search|mark|move ...  (see mail.go)
//	tuxmail term         [--data-dir DIR | --user NAME]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	// First: restrict the Windows DLL search path (AUDIT-D D-02).
	_ "github.com/Midas1989/Mailclient/internal/dllsafe"
)

var version = "0.0.0-dev"

// DefaultConfigPath is used when --config is not given.
const DefaultConfigPath = "/etc/tuxmail/config.yaml"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// errUsage marks errors caused by wrong invocation (exit code 2).
var errUsage = errors.New("usage error")

type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr}
	// Desktop builds open the window when started without a command (also
	// with only desktop flags, e.g. "tuxmail --portable").
	if desktopBuild && (len(args) == 0 || (strings.HasPrefix(args[0], "-") && !isHelpOrVersion(args[0]))) {
		args = append([]string{"desktop"}, args...)
	}
	if len(args) == 0 {
		fmt.Fprintln(stdout, "TuxMail", version)
		c.usage()
		return 0
	}
	var err error
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "TuxMail", version)
	case "serve":
		err = c.serve(ctx, args[1:])
	case "desktop":
		err = c.desktop(ctx, args[1:])
	case "demo":
		err = c.demoCmd(ctx, args[1:])
	case "devserver": // hidden: development mail server (docs/ENTWICKLUNG.md)
		err = c.devserverCmd(ctx, args[1:])
	case "check-config":
		err = c.checkConfig(args[1:])
	case "user":
		err = c.user(ctx, args[1:])
	case "keys":
		err = c.keys(ctx, args[1:])
	case "audit":
		err = c.audit(ctx, args[1:])
	case "import":
		err = c.importCmd(ctx, args[1:])
	case "export":
		err = c.exportCmd(ctx, args[1:])
	case "backup":
		err = c.backupCmd(ctx, args[1:])
	case "restore":
		err = c.restoreCmd(ctx, args[1:])
	case "backup-settings":
		err = c.backupSettingsCmd(ctx, args[1:])
	case "mail":
		err = c.mailCmd(ctx, args[1:])
	case "term":
		err = c.termCmd(ctx, args[1:])
	case "help", "-h", "--help":
		c.usage()
	default:
		fmt.Fprintf(stderr, "tuxmail: unknown command %q\n", args[0])
		c.usage()
		return 2
	}
	var exit *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		fmt.Fprintln(stderr, "tuxmail:", err)
		return exit.code
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, "tuxmail:", err)
		return 2
	default:
		fmt.Fprintln(stderr, "tuxmail:", err)
		return 1
	}
}

func isHelpOrVersion(a string) bool {
	switch a {
	case "-h", "-help", "--help", "-v", "--version":
		return true
	}
	return false
}

func (c *cli) usage() {
	fmt.Fprint(c.stderr, `Usage:
  tuxmail desktop      [--data-dir DIR] [--portable]   (desktop builds; default without arguments)
  tuxmail version
  tuxmail serve        [--config FILE]
  tuxmail demo seed    --user NAME [--config FILE] | --data-dir DIR
  tuxmail check-config [--config FILE]
  tuxmail user add            --username NAME [--email ADDR] [--display-name NAME] [--admin | --role ROLE]
                              [--password-stdin] [--if-not-exists] [--json]
  tuxmail user list           [--json]
  tuxmail user disable        USERNAME
  tuxmail user enable         USERNAME
  tuxmail user reset-password USERNAME [--password-stdin]
  tuxmail user reset-2fa      USERNAME               (removes TOTP, recovery codes and passkeys)
  tuxmail user unlock         USERNAME               (clears the sign-in lock and the lock of the security checks)
  tuxmail user set-role       USERNAME ROLE          (admin | user | readonly)
  tuxmail user delete         USERNAME --yes [--keep-data]
  tuxmail audit   verify [--json]   (checks the audit chain; exit status 1 if it is broken)
  tuxmail keys    status|rotate|rewrap|retire --kid K|retire-legacy|rollback --yes   (server keys, see docs)
  tuxmail import  --format eml|mbox|maildir|pst|thunderbird|ics|vcf|csv|ldif --source PATH
                  [--target local|folder] [--folder-id N] [--keep-structure] [--dedupe] [--inspect] [--resume JOB]
  tuxmail export  --format eml|mbox|maildir|ics|vcf|csv|ldif --out FILE [--folder-id N,..] [--query Q]
                  [--conversation N] [--project N] [--from DATE --to DATE] [--vcard 3.0|4.0] [--csv-preset outlook|google]
  tuxmail backup  --out FILE|DIR | --webdav URL [--mode config|local|full|custom] [--accounts ..] [--areas ..]
                  [--secrets] [--base PREV.txmb] [--passphrase-file F | --passphrase-stdin]
  tuxmail backup  --server --out FILE      (admin: whole data_dir incl. the server keys)
  tuxmail backup-settings --data-dir DIR --out DIR   (admin: settings only, unencrypted tree for the daily job)
  tuxmail restore --in FILE [--inspect | --verify] [--mode replace|merge] [--accounts ..] [--config-only]
                  [--secrets] [--base B.txmb] | --server --in FILE --target-dir DIR
  tuxmail mail    folders|list|read|send|reply|search|mark|move  (tuxmail mail help)  [--json]
  tuxmail term    [--data-dir DIR | --user NAME]   (full-screen terminal interface)
  import/export/backup/restore/mail/term work on --data-dir DIR (desktop) or --user NAME (server, with --config);
  the passphrase can also be given in TUXMAIL_BACKUP_PASSPHRASE.

All commands accept --config FILE (default `+DefaultConfigPath+`).
`)
}

func newFlagSet(name string, c *cli) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	cfg := fs.String("config", DefaultConfigPath, "path of the configuration file")
	return fs, cfg
}

// parseInterspersed parses flags and positional arguments in any order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}
