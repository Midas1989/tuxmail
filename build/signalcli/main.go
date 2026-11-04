// SPDX-License-Identifier: AGPL-3.0-or-later

// Command signalcli bundles signal-cli (+ Java runtime) for a release
// package and updates the pins (internal/messenger/signalcli/pins.env).
// It is a build tool, called through build/signal-cli.sh:
//
//	go run ./build/signalcli bundle -os linux -arch amd64 -out DIR [-cache DIR]
//	go run ./build/signalcli plan   -os windows -arch arm64
//	go run ./build/signalcli pin    [-signal-cli VERSION|latest] [-temurin VERSION] [-file pins.env]
//	go run ./build/signalcli smoke  -dir DIR [-link]
//
// smoke locates signal-cli in DIR exactly like the Signal plugin does
// (signalcli.Locate), runs "signal-cli --version" and, with -link, starts
// "signal-cli link" until it prints the sgnl://linkdevice URI (proves the
// Java runtime, libsignal and the connection to the Signal servers work)
// and stops it without completing the link.
//
// bundle uses the same installer as the app's on-demand install: HTTPS
// downloads from the pinned URLs, SHA-256 verified before anything is
// unpacked. pin downloads every file and accepts its checksum only when it
// matches an independent source: GitHub's asset digest for signal-cli,
// Adoptium's published .sha256.txt for Temurin.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/signalcli"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "bundle":
		err = bundle(ctx, os.Args[2:])
	case "plan":
		err = plan(os.Args[2:])
	case "pin":
		err = pin(ctx, os.Args[2:])
	case "smoke":
		err = smoke(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "signalcli:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: signalcli bundle|plan|pin|smoke [flags] (see build/signal-cli.sh)")
	os.Exit(2)
}

func loadPins(file string) (signalcli.Pins, error) {
	if file == "" {
		return signalcli.LoadPins(), nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return signalcli.ParsePins(string(b))
}

func platformPlan(fs *flag.FlagSet, args []string) (signalcli.Plan, *flag.FlagSet, error) {
	goos := fs.String("os", "", "target GOOS")
	goarch := fs.String("arch", "", "target GOARCH")
	pinsFile := fs.String("pins", "", "pins file (default: the embedded pins.env)")
	if err := fs.Parse(args); err != nil {
		return signalcli.Plan{}, fs, err
	}
	pins, err := loadPins(*pinsFile)
	if err != nil {
		return signalcli.Plan{}, fs, err
	}
	pl, err := pins.PlanFor(*goos, *goarch)
	return pl, fs, err
}

func plan(args []string) error {
	pl, _, err := platformPlan(flag.NewFlagSet("plan", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	fmt.Printf("signal-cli %s for %s/%s\n", pl.Version, pl.OS, pl.Arch)
	for _, d := range pl.Downloads {
		sum := d.SHA256
		if sum == "" {
			sum = "(not pinned)"
		}
		fmt.Printf("%s\t%s\t%s\n", d.Kind, d.URL, sum)
	}
	if pl.Note != "" {
		fmt.Println("note:", pl.Note)
	}
	return nil
}

func bundle(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	out := fs.String("out", "", "signal-cli directory to create (e.g. <plugins>/signal-cli)")
	cache := fs.String("cache", "", "download cache directory")
	pl, _, err := platformPlan(fs, args)
	if err != nil {
		return err
	}
	if *out == "" {
		return errors.New("bundle: -out required")
	}
	if err := pl.Pinned(); err != nil {
		return fmt.Errorf("%w - run build/signal-cli.sh pin and commit pins.env", err)
	}
	last := ""
	in := &signalcli.Installer{CacheDir: *cache, Progress: func(p signalcli.Progress) {
		if line := p.Stage + " " + p.File; line != last {
			last = line
			fmt.Fprintf(os.Stderr, "signal-cli: %s %s\n", p.Stage, p.File)
		}
	}}
	if err := in.Install(ctx, pl, *out); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "signal-cli %s (%s/%s) -> %s\n", pl.Version, pl.OS, pl.Arch, *out)
	if pl.Note != "" {
		fmt.Fprintln(os.Stderr, "note:", pl.Note)
	}
	return nil
}

// ==== smoke ======================================================================

func smoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	dir := fs.String("dir", "", "signal-cli directory (e.g. <plugins>/signal-cli)")
	link := fs.Bool("link", false, "also start a device link and wait for the sgnl:// URI")
	timeout := fs.Duration("timeout", 90*time.Second, "timeout of the link start")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := signalcli.Locate(signalcli.Options{Dirs: []string{*dir}, LookPath: func(string) (string, error) {
		return "", errors.New("PATH lookup disabled")
	}, Getenv: func(string) string { return "" }})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "located %s signal-cli %s: %s %s\n", c.Mode, c.Version(), c.Path, strings.Join(c.Args, " "))
	out, err := c.Exec("--version").CombinedOutput()
	fmt.Fprintf(os.Stderr, "%s", out)
	if err != nil {
		return fmt.Errorf("signal-cli --version: %w", err)
	}
	if !*link {
		return nil
	}
	cfg, err := os.MkdirTemp("", "signal-cli-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cfg)
	cmd := c.Exec("--config", cfg, "link", "-n", "TuxMail smoke test")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	found := make(chan string, 1)
	go func() {
		b := make([]byte, 4096)
		var acc []byte
		for {
			n, err := stdout.Read(b)
			acc = append(acc, b[:n]...)
			if i := bytes.Index(acc, []byte("sgnl://linkdevice")); i >= 0 {
				line := acc[i:]
				if j := bytes.IndexAny(line, "\r\n"); j >= 0 {
					line = line[:j]
				}
				found <- string(line)
				return
			}
			if err != nil {
				close(found)
				return
			}
		}
	}()
	select {
	case uri, ok := <-found:
		if !ok {
			return errors.New("signal-cli link exited without a link URI")
		}
		q := uri
		if i := strings.Index(q, "?"); i >= 0 {
			q = q[:i] + "?…" // do not log the key material
		}
		fmt.Fprintf(os.Stderr, "signal-cli link: got %s (not completed)\n", q)
		return nil
	case <-time.After(*timeout):
		return errors.New("signal-cli link: no URI within the timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ==== pin =======================================================================

var client = &http.Client{Timeout: 30 * time.Minute}

func get(ctx context.Context, u string, api bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "TuxMail build/signalcli")
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp, nil
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func githubRelease(ctx context.Context, repo, tag string) (release, error) {
	u := "https://api.github.com/repos/" + repo + "/releases/latest"
	if tag != "latest" {
		u = "https://api.github.com/repos/" + repo + "/releases/tags/" + url.PathEscape(tag)
	}
	resp, err := get(ctx, u, true)
	if err != nil {
		return release{}, err
	}
	defer resp.Body.Close()
	var r release
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// fetch downloads u into memory and returns its SHA-256.
func fetch(ctx context.Context, u string) ([]byte, string, error) {
	resp, err := get(ctx, u, false)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	if err != nil {
		return nil, "", err
	}
	s := sha256.Sum256(b)
	return b, hex.EncodeToString(s[:]), nil
}

func pin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pin", flag.ExitOnError)
	scVersion := fs.String("signal-cli", "latest", "signal-cli version (e.g. 0.13.18) or latest")
	temurin := fs.String("temurin", "", "Temurin JRE version, e.g. 21.0.8+9 (default: keep the pinned one)")
	file := fs.String("file", "internal/messenger/signalcli/pins.env", "pins file to rewrite")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	old, err := signalcli.ParsePins(string(raw))
	if err != nil {
		return err
	}
	set := map[string]string{}

	// signal-cli: GitHub release, checksums against the asset digests.
	tag := *scVersion
	if tag != "latest" {
		tag = "v" + strings.TrimPrefix(tag, "v")
	}
	rel, err := githubRelease(ctx, "AsamK/signal-cli", tag)
	if err != nil {
		return err
	}
	ver := strings.TrimPrefix(rel.TagName, "v")
	set["SIGNAL_CLI_VERSION"] = ver
	want := map[string]string{
		"SIGNAL_CLI_JVM":    "signal-cli-" + ver + ".tar.gz",
		"SIGNAL_CLI_NATIVE": "signal-cli-" + ver + "-Linux-native.tar.gz",
	}
	keys := []string{"SIGNAL_CLI_JVM", "SIGNAL_CLI_NATIVE"}
	for _, key := range keys {
		name := want[key]
		found := false
		for _, a := range rel.Assets {
			if a.Name != name {
				continue
			}
			found = true
			body, sum, err := fetch(ctx, a.URL)
			if err != nil {
				return err
			}
			digest := strings.TrimPrefix(a.Digest, "sha256:")
			if digest == "" {
				return fmt.Errorf("%s: GitHub reports no digest; refusing to pin an unverified checksum", name)
			}
			if digest != sum {
				return fmt.Errorf("%s: downloaded SHA-256 %s != GitHub digest %s", name, sum, digest)
			}
			set[key+"_URL"], set[key+"_SHA256"] = a.URL, sum
			fmt.Fprintf(os.Stderr, "%s  %s (%.1f MB, matches GitHub digest)\n", sum, name, float64(len(body))/(1<<20))
			if key == "SIGNAL_CLI_JVM" {
				reportNatives(body)
			}
		}
		if !found {
			return fmt.Errorf("release %s has no asset %s", rel.TagName, name)
		}
	}

	// Temurin JRE: Adoptium's .sha256.txt next to every file.
	tv := *temurin
	if tv == "" {
		tv = old["TEMURIN_VERSION"]
	}
	m := regexp.MustCompile(`^(\d+)\.\d+\.\d+(\.\d+)?\+\d+$`).FindStringSubmatch(tv)
	if m == nil {
		return fmt.Errorf("bad Temurin version %q (want e.g. 21.0.8+9)", tv)
	}
	set["TEMURIN_VERSION"] = tv
	major := m[1]
	base := "https://github.com/adoptium/temurin" + major + "-binaries/releases/download/jdk-" + url.PathEscape(tv) + "/"
	for _, j := range []struct{ key, arch, os, ext string }{
		{"TEMURIN_LINUX_AARCH64", "aarch64", "linux", "tar.gz"},
		{"TEMURIN_WINDOWS_X64", "x64", "windows", "zip"},
	} {
		name := fmt.Sprintf("OpenJDK%sU-jre_%s_%s_hotspot_%s.%s", major, j.arch, j.os, strings.ReplaceAll(tv, "+", "_"), j.ext)
		u := base + name
		_, sum, err := fetch(ctx, u)
		if err != nil {
			return err
		}
		pub, _, err := fetch(ctx, u+".sha256.txt")
		if err != nil {
			return err
		}
		fields := strings.Fields(string(pub))
		if len(fields) == 0 || strings.ToLower(fields[0]) != sum {
			return fmt.Errorf("%s: downloaded SHA-256 %s != published %q", name, sum, strings.TrimSpace(string(pub)))
		}
		set[j.key+"_URL"], set[j.key+"_SHA256"] = u, sum
		fmt.Fprintf(os.Stderr, "%s  %s (matches Adoptium .sha256.txt)\n", sum, name)
	}

	out := rewrite(string(raw), set)
	if err := os.WriteFile(*file, []byte(out), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "updated %s (signal-cli %s, Temurin %s): review and commit it\n", *file, ver, tv)
	return nil
}

// rewrite replaces KEY=... lines (keeping comments and order).
func rewrite(s string, set map[string]string) string {
	lines := strings.Split(s, "\n")
	done := map[string]bool{}
	for i, l := range lines {
		k, _, ok := strings.Cut(l, "=")
		if v, has := set[k]; ok && has && !strings.HasPrefix(l, "#") {
			lines[i], done[k] = k+"="+v, true
		}
	}
	var missing []string
	for k := range set {
		if !done[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		lines = append(lines, k+"="+set[k])
	}
	return strings.Join(lines, "\n")
}

// reportNatives lists the native libsignal libraries of the JVM build
// (which platforms the bundled JRE can run on).
func reportNatives(tgz []byte) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return
		}
		if !strings.Contains(path.Base(h.Name), "libsignal-client") || !strings.HasSuffix(h.Name, ".jar") {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 256<<20))
		if err != nil {
			return
		}
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		var libs []string
		for _, f := range zr.File {
			n := strings.ToLower(f.Name)
			if strings.HasSuffix(n, ".so") || strings.HasSuffix(n, ".dll") || strings.HasSuffix(n, ".dylib") {
				libs = append(libs, f.Name)
			}
		}
		fmt.Fprintf(os.Stderr, "%s native libraries: %s\n", path.Base(h.Name), strings.Join(libs, ", "))
	}
}
