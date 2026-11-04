// SPDX-License-Identifier: AGPL-3.0-or-later

package signalcli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type entry struct {
	name, body, link string
	mode             int64
	dir              bool
}

func tgz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		switch {
		case e.dir:
			h.Typeflag, h.Size, h.Mode = tar.TypeDir, 0, 0o755
		case e.link != "":
			h.Typeflag, h.Size, h.Linkname = tar.TypeSymlink, 0, e.link
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipped(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// server serves files over HTTPS (httptest TLS) and counts requests.
type server struct {
	*httptest.Server
	files map[string][]byte
	hits  atomic.Int32
}

func newServer(t *testing.T, files map[string][]byte) *server {
	s := &server{files: files}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if r.URL.Path == "/redirect-http" {
			http.Redirect(w, r, "http://example.invalid/x.tar.gz", http.StatusFound)
			return
		}
		b, ok := s.files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) download(kind, name string, body []byte) Download {
	return Download{Kind: kind, Name: name, URL: s.URL + "/" + name, SHA256: sum(body), Version: "1.0"}
}

var (
	jvmTar = []entry{
		{name: "signal-cli-1.0/", dir: true},
		{name: "signal-cli-1.0/bin/signal-cli", body: "#!/bin/sh\n", mode: 0o755},
		{name: "signal-cli-1.0/bin/signal-cli.bat", body: "@echo off\r\n"},
		{name: "signal-cli-1.0/lib/signal-cli-1.0.jar", body: "jar"},
		{name: "signal-cli-1.0/lib/libsignal-client-0.1.jar", body: "jar"},
	}
	jreTar = []entry{
		{name: "jdk-21-jre/bin/java", body: "ELF", mode: 0o755},
		{name: "jdk-21-jre/legal/java.base/LICENSE", body: "GPLv2+CPE"},
		{name: "jdk-21-jre/legal/java.xml/LICENSE", link: "../java.base/LICENSE"},
		{name: "jdk-21-jre/legal/evil", link: "../../../../etc/passwd"},
		{name: "jdk-21-jre/NOTICE", body: "notice"},
	}
)

func TestInstallLinuxNative(t *testing.T) {
	native := tgz(t, entry{name: "signal-cli", body: "#!/bin/sh\necho signal-cli 1.0\n", mode: 0o755})
	s := newServer(t, map[string][]byte{"native.tar.gz": native})
	var stages []string
	in := &Installer{Client: s.Client(), Progress: func(p Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
	}}
	dest := filepath.Join(t.TempDir(), "plugins", DirName)
	plan := Plan{OS: "linux", Arch: "amd64", Version: "1.0", Downloads: []Download{s.download(KindNative, "native.tar.gz", native)}}
	if err := in.Install(context.Background(), plan, dest); err != nil {
		t.Fatal(err)
	}
	if strings.Join(stages, ",") != "download,verify,unpack,done" {
		t.Errorf("stages %v", stages)
	}
	for _, f := range []string{"signal-cli", "VERSION", "LICENSE-signal-cli.txt", "NOTICE.txt"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Error(err)
		}
	}
	if runtime.GOOS != "windows" {
		c, err := Locate(Options{Dirs: []string{dest}, LookPath: noLookPath})
		if err != nil || c.Mode != ModeNative || c.Version() != "1.0" {
			t.Fatalf("locate: %+v %v", c, err)
		}
		out, err := c.Exec("--version").Output()
		if err != nil || strings.TrimSpace(string(out)) != "signal-cli 1.0" {
			t.Fatalf("run: %q %v", out, err)
		}
	}
	lic, _ := os.ReadFile(filepath.Join(dest, "LICENSE-signal-cli.txt"))
	if !strings.Contains(string(lic), "GNU GENERAL PUBLIC LICENSE") {
		t.Error("GPL text missing")
	}
	// No leftovers next to dest.
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("leftovers: %v", entries)
	}
}

func TestInstallJVMWithJRE(t *testing.T) {
	jvm, jre := tgz(t, jvmTar...), tgz(t, jreTar...)
	s := newServer(t, map[string][]byte{"jvm.tar.gz": jvm, "jre.tar.gz": jre})
	dest := filepath.Join(t.TempDir(), DirName)
	plan := Plan{OS: "linux", Arch: "arm64", Version: "1.0",
		Downloads: []Download{s.download(KindJVM, "jvm.tar.gz", jvm), s.download(KindJRE, "jre.tar.gz", jre)}}
	cache := t.TempDir()
	in := &Installer{Client: s.Client(), CacheDir: cache}
	if err := in.Install(context.Background(), plan, dest); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bin/signal-cli", "lib/signal-cli-1.0.jar", "jre/bin/java", "jre/legal/java.base/LICENSE", "jre/NOTICE"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, "jre/legal/evil")); err == nil {
		t.Error("escaping symlink was created")
	}
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(filepath.Join(dest, "jre/legal/java.xml/LICENSE")); err != nil || target != "../java.base/LICENSE" {
			t.Errorf("inner symlink: %q %v", target, err)
		}
		c, err := Locate(Options{Dirs: []string{dest}, LookPath: noLookPath, Getenv: func(string) string { return "" }})
		if err != nil || c.Mode != ModeJVM || c.Path != filepath.Join(dest, "jre", "bin", "java") {
			t.Fatalf("locate: %+v %v", c, err)
		}
	}
	notice, _ := os.ReadFile(filepath.Join(dest, "NOTICE.txt"))
	if !strings.Contains(string(notice), "Temurin") || !strings.Contains(string(notice), plan.Downloads[1].SHA256) {
		t.Errorf("NOTICE.txt: %s", notice)
	}

	// Second install: served from the verified cache, replaces dest.
	before := s.hits.Load()
	if err := os.WriteFile(filepath.Join(dest, "stale"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.Install(context.Background(), plan, dest); err != nil {
		t.Fatal(err)
	}
	if s.hits.Load() != before {
		t.Error("cache not used")
	}
	if _, err := os.Stat(filepath.Join(dest, "stale")); err == nil {
		t.Error("old directory not replaced")
	}
}

func TestInstallWindowsZipJRE(t *testing.T) {
	jvm := tgz(t, jvmTar...)
	jre := zipped(t, entry{name: "jdk-21-jre/bin/java.exe", body: "MZ"}, entry{name: "jdk-21-jre/legal/java.base/LICENSE", body: "GPLv2+CPE"})
	s := newServer(t, map[string][]byte{"jvm.tar.gz": jvm, "jre.zip": jre})
	dest := filepath.Join(t.TempDir(), DirName)
	plan := Plan{OS: "windows", Arch: "arm64", Version: "1.0", Note: "x64 emulation",
		Downloads: []Download{s.download(KindJVM, "jvm.tar.gz", jvm), s.download(KindJRE, "jre.zip", jre)}}
	if err := (&Installer{Client: s.Client()}).Install(context.Background(), plan, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "jre", "bin", "java.exe")); err != nil {
		t.Fatal(err)
	}
	notice, _ := os.ReadFile(filepath.Join(dest, "NOTICE.txt"))
	if !strings.Contains(string(notice), "\r\n") || !strings.Contains(string(notice), "x64 emulation") {
		t.Error("Windows NOTICE.txt needs CRLF and the note")
	}
}

func TestInstallFailures(t *testing.T) {
	native := tgz(t, entry{name: "signal-cli", body: "bin", mode: 0o755})
	traversal := tgz(t, entry{name: "../../evil", body: "x"})
	noBinary := tgz(t, entry{name: "README", body: "x"})
	s := newServer(t, map[string][]byte{"native.tar.gz": native, "traversal.tar.gz": traversal, "nobin.tar.gz": noBinary})
	good := s.download(KindNative, "native.tar.gz", native)

	tests := []struct {
		name string
		d    Download
		want string
		in   Installer
	}{
		{name: "checksum mismatch", d: Download{Kind: KindNative, Name: "native.tar.gz", URL: good.URL, SHA256: strings.Repeat("0", 64)}, want: "checksum mismatch"},
		{name: "not pinned", d: Download{Kind: KindNative, Name: "native.tar.gz", URL: good.URL}, want: "not pinned"},
		{name: "bad checksum format", d: Download{Kind: KindNative, Name: "native.tar.gz", URL: good.URL, SHA256: "abc"}, want: "bad SHA-256"},
		{name: "plain HTTP refused", d: Download{Kind: KindNative, Name: "native.tar.gz", URL: strings.Replace(good.URL, "https:", "http:", 1), SHA256: good.SHA256}, want: "HTTPS only"},
		{name: "redirect to HTTP refused", d: Download{Kind: KindNative, Name: "x.tar.gz", URL: s.URL + "/redirect-http", SHA256: good.SHA256}, want: "not HTTPS"},
		{name: "404", d: Download{Kind: KindNative, Name: "missing.tar.gz", URL: s.URL + "/missing.tar.gz", SHA256: good.SHA256}, want: "404"},
		{name: "path traversal", d: s.download(KindJVM, "traversal.tar.gz", traversal), want: "unsafe entry"},
		{name: "archive without signal-cli", d: s.download(KindNative, "nobin.tar.gz", noBinary), want: "no signal-cli executable"},
		{name: "file name with a slash", d: Download{Kind: KindNative, Name: "../x", URL: good.URL, SHA256: good.SHA256}, want: "bad file name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), DirName)
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dest, "keep")
			if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			in := tt.in
			if in.Client == nil && !strings.Contains(tt.name, "redirect") {
				in.Client = s.Client()
			}
			if strings.Contains(tt.name, "redirect") {
				// The default client (HTTPS-only redirects) with the test CA.
				c := in.client()
				c.Transport = s.Client().Transport
				in.Client = c
			}
			err := in.Install(context.Background(), Plan{OS: "linux", Arch: "amd64", Version: "1.0", Downloads: []Download{tt.d}}, dest)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if tt.name == "checksum mismatch" && !errors.Is(err, ErrChecksum) {
				t.Error("not ErrChecksum")
			}
			// A failed install leaves the existing directory untouched and
			// nothing behind.
			if b, err := os.ReadFile(marker); err != nil || string(b) != "old" {
				t.Fatalf("existing install damaged: %v", err)
			}
			entries, _ := os.ReadDir(filepath.Dir(dest))
			if len(entries) != 1 {
				t.Errorf("leftovers: %v", entries)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dest)), "evil")); err == nil {
				t.Error("traversal wrote outside")
			}
		})
	}
}

func TestSafeRel(t *testing.T) {
	for _, tt := range []struct {
		in   string
		n    int
		want string
		ok   bool
		err  bool
	}{
		{"a/b/c", 1, "b/c", true, false},
		{"a/", 1, "", false, false},
		{"a/../../x", 1, "", false, true},
		{"/etc/passwd", 0, "", false, true},
		{`C:\x`, 0, "", false, true},
		{`a\..\..\x`, 0, "", false, true},
		{"./a/b", 1, "b", true, false},
	} {
		got, ok, err := safeRel(tt.in, tt.n)
		if got != tt.want || ok != tt.ok || (err != nil) != tt.err {
			t.Errorf("safeRel(%q,%d) = %q %v %v", tt.in, tt.n, got, ok, err)
		}
	}
}
