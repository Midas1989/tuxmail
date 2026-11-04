// SPDX-License-Identifier: AGPL-3.0-or-later

// Command mkpayload packs the application files for the setup program
// (build/windows/package-setup.sh):
//
//	go run ./cmd/tuxmail-setup/mkpayload -o cmd/tuxmail-setup/payload/payload.zip [-mtime UNIX] DIR
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Midas1989/Mailclient/internal/winsetup"
)

func main() {
	out := flag.String("o", "", "output zip")
	mtime := flag.Int64("mtime", 0, "modification time of all entries (unix seconds, default: now)")
	flag.Parse()
	if *out == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mkpayload -o payload.zip [-mtime UNIX] DIR")
		os.Exit(2)
	}
	t := time.Now()
	if *mtime > 0 {
		t = time.Unix(*mtime, 0)
	}
	if err := run(*out, flag.Arg(0), t); err != nil {
		fmt.Fprintln(os.Stderr, "mkpayload:", err)
		os.Exit(1)
	}
}

func run(out, dir string, t time.Time) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := winsetup.WritePayload(f, dir, t); err != nil {
		f.Close()
		os.Remove(out)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	p, err := winsetup.OpenPayload(data) // validates names and TuxMail.exe
	if err != nil {
		os.Remove(out)
		return err
	}
	fmt.Fprintf(os.Stderr, "mkpayload: %d files, %.1f MB -> %.1f MB\n", len(p.Files()), float64(p.Size())/(1<<20), float64(len(data))/(1<<20))
	return nil
}
