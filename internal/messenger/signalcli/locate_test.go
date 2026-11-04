// SPDX-License-Identifier: AGPL-3.0-or-later

package signalcli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeFS maps paths (with the separator of the simulated OS) to modes; a
// path ending in sep is a directory. Parent directories are implied.
type fakeFS struct {
	sep   string
	files map[string]fs.FileMode
}

type fakeInfo struct {
	name string
	mode fs.FileMode
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeInfo) Sys() any           { return nil }

func newFakeFS(sep string, entries map[string]fs.FileMode) *fakeFS {
	f := &fakeFS{sep: sep, files: map[string]fs.FileMode{}}
	for p, m := range entries {
		f.files[p] = m
		for d := f.parent(p); d != "" && d != p; d = f.parent(d) {
			if _, ok := f.files[d]; !ok {
				f.files[d] = fs.ModeDir | 0o755
			}
		}
	}
	return f
}

func (f *fakeFS) parent(p string) string {
	i := strings.LastIndex(p, f.sep)
	if i <= 0 {
		return ""
	}
	return p[:i]
}

func (f *fakeFS) Stat(name string) (fs.FileInfo, error) {
	m, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return fakeInfo{name: name[strings.LastIndex(name, f.sep)+1:], mode: m}, nil
}

func (f *fakeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if m, ok := f.files[name]; !ok || !m.IsDir() {
		return nil, fs.ErrNotExist
	}
	var out []fs.DirEntry
	for p, m := range f.files {
		if f.parent(p) == name {
			out = append(out, fs.FileInfoToDirEntry(fakeInfo{name: p[len(name)+1:], mode: m}))
		}
	}
	return out, nil
}

const (
	exe = 0o755
	reg = 0o644
)

func TestLocate(t *testing.T) {
	noPath := func(string) (string, error) { return "", errors.New("not in PATH") }
	jvmArgs := func(dir, sep string) []string {
		return []string{"--enable-native-access=ALL-UNNAMED", "-cp", dir + sep + "lib" + sep + "*", MainClass}
	}
	tests := []struct {
		name     string
		goos     string
		files    map[string]fs.FileMode
		explicit string
		dirs     []string
		inst     string
		path     map[string]string // LookPath results
		javaHome string
		want     Command
		wantErr  error
	}{
		{name: "bundled native wins over installed and PATH", goos: "linux",
			files: map[string]fs.FileMode{
				"/opt/tm/plugins/signal-cli/signal-cli":           exe,
				"/home/u/.local/tm/plugins/signal-cli/signal-cli": exe,
				"/usr/bin/signal-cli":                             exe,
			},
			dirs: []string{"/opt/tm/plugins/signal-cli"}, inst: "/home/u/.local/tm/plugins/signal-cli",
			path: map[string]string{"signal-cli": "/usr/bin/signal-cli"},
			want: Command{Path: "/opt/tm/plugins/signal-cli/signal-cli", Mode: ModeNative, Source: SourceBundled, Dir: "/opt/tm/plugins/signal-cli"}},
		{name: "installed on demand when nothing is bundled", goos: "linux",
			files: map[string]fs.FileMode{"/data/plugins/signal-cli/signal-cli": exe},
			dirs:  []string{"/opt/tm/plugins/signal-cli"}, inst: "/data/plugins/signal-cli",
			want: Command{Path: "/data/plugins/signal-cli/signal-cli", Mode: ModeNative, Source: SourceInstalled, Dir: "/data/plugins/signal-cli"}},
		{name: "non-executable native file is ignored", goos: "linux",
			files: map[string]fs.FileMode{"/b/signal-cli/signal-cli": reg, "/usr/bin/signal-cli": exe},
			dirs:  []string{"/b/signal-cli"}, path: map[string]string{"signal-cli": "/usr/bin/signal-cli"},
			want: Command{Path: "/usr/bin/signal-cli", Mode: ModeExec, Source: SourcePath}},
		{name: "explicit setting wins", goos: "linux",
			files:    map[string]fs.FileMode{"/b/signal-cli/signal-cli": exe, "/custom/signal-cli": exe},
			explicit: "/custom/signal-cli", dirs: []string{"/b/signal-cli"},
			want: Command{Path: "/custom/signal-cli", Mode: ModeExec, Source: SourceSetting}},
		{name: "explicit setting missing: no fallback", goos: "linux",
			files:    map[string]fs.FileMode{"/b/signal-cli/signal-cli": exe},
			explicit: "/nope/signal-cli", dirs: []string{"/b/signal-cli"},
			wantErr: ErrNotFound},
		{name: "bundled JVM build uses the bundled JRE", goos: "linux",
			files: map[string]fs.FileMode{
				"/b/signal-cli/lib/signal-cli-0.13.18.jar": reg,
				"/b/signal-cli/lib/libsignal-client.jar":   reg,
				"/b/signal-cli/jre/bin/java":               exe,
			},
			dirs: []string{"/b/signal-cli"}, path: map[string]string{"java": "/usr/bin/java"},
			want: Command{Path: "/b/signal-cli/jre/bin/java", Args: jvmArgs("/b/signal-cli", "/"), Env: []string{"JAVA_HOME=/b/signal-cli/jre"},
				Mode: ModeJVM, Source: SourceBundled, Dir: "/b/signal-cli"}},
		{name: "explicit upstream bin/signal-cli with JAVA_HOME", goos: "linux",
			files: map[string]fs.FileMode{
				"/opt/signal-cli-0.13.18/bin/signal-cli":               exe,
				"/opt/signal-cli-0.13.18/lib/signal-cli-0.13.18.jar":   reg,
				"/usr/lib/jvm/temurin-21/bin/java":                     exe,
				"/opt/signal-cli-0.13.18/lib/libsignal-client-0.1.jar": reg,
			},
			explicit: "/opt/signal-cli-0.13.18/bin/signal-cli", javaHome: "/usr/lib/jvm/temurin-21",
			want: Command{Path: "/usr/lib/jvm/temurin-21/bin/java", Args: jvmArgs("/opt/signal-cli-0.13.18", "/"), Mode: ModeJVM,
				Source: SourceSetting, Dir: "/opt/signal-cli-0.13.18"}},
		{name: "JVM build without any Java", goos: "linux",
			files:   map[string]fs.FileMode{"/b/signal-cli/lib/signal-cli-1.jar": reg},
			dirs:    []string{"/b/signal-cli"},
			wantErr: ErrNoJava},
		{name: "nothing anywhere", goos: "linux", dirs: []string{"/b/signal-cli"}, wantErr: ErrNotFound},

		// Windows: paths with spaces, java.exe of the bundled JRE, no .bat.
		{name: "windows bundled JVM build with spaces in the path", goos: "windows",
			files: map[string]fs.FileMode{
				`C:\Program Files\TuxMail\plugins\signal-cli\lib\signal-cli-0.13.18.jar`: reg,
				`C:\Program Files\TuxMail\plugins\signal-cli\bin\signal-cli.bat`:         reg,
				`C:\Program Files\TuxMail\plugins\signal-cli\jre\bin\java.exe`:           reg,
			},
			dirs: []string{`C:\Program Files\TuxMail\plugins\signal-cli`, `C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli`},
			want: Command{Path: `C:\Program Files\TuxMail\plugins\signal-cli\jre\bin\java.exe`,
				Args: jvmArgs(`C:\Program Files\TuxMail\plugins\signal-cli`, `\`),
				Env:  []string{`JAVA_HOME=C:\Program Files\TuxMail\plugins\signal-cli\jre`},
				Mode: ModeJVM, Source: SourceBundled, Dir: `C:\Program Files\TuxMail\plugins\signal-cli`}},
		{name: "windows installed on demand into the data folder", goos: "windows",
			files: map[string]fs.FileMode{
				`C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli\lib\signal-cli-0.13.18.jar`: reg,
				`C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli\jre\bin\java.exe`:           reg,
			},
			dirs: []string{`C:\Program Files\TuxMail\plugins\signal-cli`}, inst: `C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli`,
			want: Command{Path: `C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli\jre\bin\java.exe`,
				Args: jvmArgs(`C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli`, `\`),
				Env:  []string{`JAVA_HOME=C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli\jre`},
				Mode: ModeJVM, Source: SourceInstalled, Dir: `C:\Users\Ä Ö\AppData\Roaming\TuxMail\plugins\signal-cli`}},
		{name: "windows explicit .bat becomes a direct java start", goos: "windows",
			files: map[string]fs.FileMode{
				`D:\tools\signal-cli\bin\signal-cli.bat`:        reg,
				`D:\tools\signal-cli\lib\signal-cli-0.13.0.jar`: reg,
				`D:\jdk 21\bin\java.exe`:                        reg,
			},
			explicit: `D:\tools\signal-cli\bin\signal-cli.bat`, javaHome: `D:\jdk 21`,
			want: Command{Path: `D:\jdk 21\bin\java.exe`, Args: jvmArgs(`D:\tools\signal-cli`, `\`), Mode: ModeJVM, Source: SourceSetting,
				Dir: `D:\tools\signal-cli`}},
		{name: "windows .bat in PATH uses java from PATH", goos: "windows",
			files: map[string]fs.FileMode{
				`C:\sc\bin\signal-cli.bat`:   reg,
				`C:\sc\lib\signal-cli-1.jar`: reg,
			},
			path: map[string]string{"signal-cli": `C:\sc\bin\signal-cli.bat`, "java": `C:\java\bin\java.exe`},
			want: Command{Path: `C:\java\bin\java.exe`, Args: jvmArgs(`C:\sc`, `\`), Mode: ModeJVM, Source: SourcePath, Dir: `C:\sc`}},
		{name: "windows lone .bat is refused", goos: "windows",
			files:    map[string]fs.FileMode{`C:\x\run.bat`: reg},
			explicit: `C:\x\run.bat`, wantErr: errors.New("batch files")},
		{name: "windows explicit directory", goos: "windows",
			files: map[string]fs.FileMode{
				`E:\signal cli\lib\signal-cli-1.jar`: reg,
				`E:\signal cli\jre\bin\java.exe`:     reg,
			},
			explicit: `E:\signal cli`,
			want: Command{Path: `E:\signal cli\jre\bin\java.exe`, Args: jvmArgs(`E:\signal cli`, `\`), Env: []string{`JAVA_HOME=E:\signal cli\jre`},
				Mode: ModeJVM, Source: SourceSetting, Dir: `E:\signal cli`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sep := "/"
			if tt.goos == "windows" {
				sep = `\`
			}
			got, err := Locate(Options{Explicit: tt.explicit, Dirs: tt.dirs, InstallDir: tt.inst, GOOS: tt.goos, FS: newFakeFS(sep, tt.files),
				LookPath: func(name string) (string, error) {
					if p, ok := tt.path[name]; ok {
						return p, nil
					}
					return noPath(name)
				},
				Getenv: func(k string) string {
					if k == "JAVA_HOME" {
						return tt.javaHome
					}
					return ""
				}})
			if tt.wantErr != nil {
				if err == nil || (!errors.Is(err, tt.wantErr) && !strings.Contains(err.Error(), tt.wantErr.Error())) {
					t.Fatalf("err = %v, want %v (got %+v)", err, tt.wantErr, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

// The command is started without a shell: every argument (paths with
// spaces, the classpath wildcard) stays one argv element.
func TestCommandExecArgs(t *testing.T) {
	c := Command{Path: "/x/jre/bin/java", Args: []string{"-cp", "/a b/lib/*", MainClass}, Env: []string{"JAVA_HOME=/x/jre"}}
	cmd := c.Exec("--config", "/data dir/acc", "jsonRpc")
	want := []string{"/x/jre/bin/java", "-cp", "/a b/lib/*", MainClass, "--config", "/data dir/acc", "jsonRpc"}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("args = %q", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "JAVA_HOME=/x/jre") {
		t.Fatal("JAVA_HOME not set")
	}
	if cmd.Path != "/x/jre/bin/java" {
		t.Fatalf("path = %q", cmd.Path)
	}
}

// The real OS file system: a fake native signal-cli in a bundle directory.
func TestLocateRealFS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native layout is Linux only")
	}
	dir := filepath.Join(t.TempDir(), DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signal-cli"), []byte("#!/bin/sh\necho signal-cli 0.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("0.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Locate(Options{Dirs: []string{dir}, LookPath: func(string) (string, error) { return "", errors.New("no") }})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeNative || c.Version() != "0.0.0" {
		t.Fatalf("got %+v version %q", c, c.Version())
	}
	out, err := c.Exec("--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "signal-cli 0.0.0" {
		t.Fatalf("run: %q %v", out, err)
	}
}

func TestPlanFor(t *testing.T) {
	p := LoadPins()
	for _, tt := range []struct {
		os, arch string
		kinds    []string
		jre      string
	}{
		{"linux", "amd64", []string{KindNative}, ""},
		{"linux", "arm64", []string{KindJVM, KindJRE}, "aarch64_linux"},
		{"windows", "amd64", []string{KindJVM, KindJRE}, "x64_windows"},
		{"windows", "arm64", []string{KindJVM, KindJRE}, "x64_windows"},
	} {
		pl, err := p.PlanFor(tt.os, tt.arch)
		if err != nil {
			t.Fatalf("%s/%s: %v", tt.os, tt.arch, err)
		}
		var kinds []string
		for _, d := range pl.Downloads {
			kinds = append(kinds, d.Kind)
			if !strings.HasPrefix(d.URL, "https://github.com/") {
				t.Errorf("%s: URL %q not from github.com over HTTPS", d.Name, d.URL)
			}
			if d.Kind == KindJRE && !strings.Contains(d.Name, tt.jre) {
				t.Errorf("%s/%s: JRE %s", tt.os, tt.arch, d.Name)
			}
		}
		if !slices.Equal(kinds, tt.kinds) {
			t.Errorf("%s/%s: kinds %v", tt.os, tt.arch, kinds)
		}
		if pl.Version == "" || !strings.Contains(pl.Downloads[0].URL, pl.Version) {
			t.Errorf("%s/%s: version %q not in %s", tt.os, tt.arch, pl.Version, pl.Downloads[0].URL)
		}
	}
	if _, err := p.PlanFor("darwin", "arm64"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("darwin: %v", err)
	}
	if _, err := ParsePins("BROKEN LINE"); err == nil {
		t.Fatal("bad pins accepted")
	}
}
