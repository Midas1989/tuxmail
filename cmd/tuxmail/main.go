// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail is the TuxMail desktop application and server.
//
//	tuxmail version
//	tuxmail serve        --config /etc/tuxmail/config.yaml
//	tuxmail check-config --config /etc/tuxmail/config.yaml
//	tuxmail user add|list|disable|enable|reset-password|reset-2fa|set-role|delete --config ...
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
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
	if len(args) == 0 {
		fmt.Fprintln(stdout, "TuxMail", version, "- noch in Entwicklung (Phase 0)")
		c.usage()
		return 0
	}
	var err error
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "TuxMail", version)
	case "serve":
		err = c.serve(ctx, args[1:])
	case "check-config":
		err = c.checkConfig(args[1:])
	case "user":
		err = c.user(ctx, args[1:])
	case "help", "-h", "--help":
		c.usage()
	default:
		fmt.Fprintf(stderr, "tuxmail: unknown command %q\n", args[0])
		c.usage()
		return 2
	}
	switch {
	case err == nil:
		return 0
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

func (c *cli) usage() {
	fmt.Fprint(c.stderr, `Usage:
  tuxmail version
  tuxmail serve        [--config FILE]
  tuxmail check-config [--config FILE]
  tuxmail user add            --username NAME [--email ADDR] [--display-name NAME] [--admin | --role ROLE]
                              [--password-stdin] [--if-not-exists] [--json]
  tuxmail user list           [--json]
  tuxmail user disable        USERNAME
  tuxmail user enable         USERNAME
  tuxmail user reset-password USERNAME [--password-stdin]
  tuxmail user reset-2fa      USERNAME
  tuxmail user set-role       USERNAME ROLE          (admin | user | readonly)
  tuxmail user delete         USERNAME --yes [--keep-data]

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
