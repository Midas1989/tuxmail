// SPDX-License-Identifier: AGPL-3.0-or-later

package signalcli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// DirName is the name of a signal-cli directory below a plugins directory.
const DirName = "signal-cli"

// MainClass is the entry point of the signal-cli JVM build.
const MainClass = "org.asamk.signal.Main"

// Sources of a located signal-cli.
const (
	SourceSetting   = "setting"   // signal_cli_path / TUXMAIL_SIGNAL_SIGNAL_CLI_PATH
	SourceBundled   = "bundled"   // one of Options.Dirs
	SourceInstalled = "installed" // Options.InstallDir (on-demand install)
	SourcePath      = "path"      // signal-cli in PATH
)

// Modes of a located signal-cli.
const (
	ModeNative = "native" // native executable (GraalVM build)
	ModeJVM    = "jvm"    // java -cp lib/* org.asamk.signal.Main
	ModeExec   = "exec"   // any other executable (wrapper script ...)
)

// ErrNotFound: no signal-cli anywhere. The Signal plugin reports it as
// wire.CodeComponentMissing, which lets the UI offer the on-demand install.
var ErrNotFound = errors.New("signal-cli not found")

// ErrNoJava: a JVM build of signal-cli was found but no Java runtime.
var ErrNoJava = errors.New("signal-cli needs Java " + LoadPins()["SIGNAL_CLI_JAVA"] + " or newer, none found")

// FS is the part of the file system Locate reads (tests use a fake).
type FS interface {
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
}

type osFS struct{}

func (osFS) Stat(name string) (fs.FileInfo, error)      { return os.Stat(name) }
func (osFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

// Options control Locate.
type Options struct {
	// Explicit is the configured path (setting or environment): a
	// signal-cli executable, a signal-cli directory (layout above, or an
	// unpacked upstream release) or bin/signal-cli(.bat) of a JVM build.
	Explicit string
	// Dirs are signal-cli directories bundled with the application
	// (<plugins dir>/signal-cli), in priority order.
	Dirs []string
	// InstallDir is the on-demand install (<data dir>/plugins/signal-cli),
	// searched after Dirs.
	InstallDir string
	// GOOS defaults to runtime.GOOS.
	GOOS string
	// LookPath defaults to exec.LookPath; Getenv to os.Getenv; FS to the OS.
	LookPath func(string) (string, error)
	Getenv   func(string) string
	FS       FS

	// hostPaths: use the separators of the running OS with the rules
	// (.exe, exec bits) of GOOS (installer check of a cross-built bundle).
	hostPaths bool
}

// Command is a located signal-cli.
type Command struct {
	Path   string   // executable (signal-cli or java)
	Args   []string // arguments before the signal-cli arguments
	Env    []string // extra environment (JAVA_HOME)
	Mode   string
	Source string
	// Dir is the signal-cli directory (empty for ModeExec from PATH).
	Dir string
}

// Argv returns the full argument list (without the executable).
func (c Command) Argv(args ...string) []string {
	return append(append([]string{}, c.Args...), args...)
}

// Exec returns an exec.Cmd running signal-cli with args. The process gets
// no console window on Windows; arguments are passed without a shell.
func (c Command) Exec(args ...string) *exec.Cmd {
	cmd := exec.Command(c.Path, c.Argv(args...)...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	setProcAttr(cmd)
	return cmd
}

// Version reads the VERSION file of the signal-cli directory.
func (c Command) Version() string {
	if c.Dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(c.Dir, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (o *Options) defaults() {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.FS == nil {
		o.FS = osFS{}
	}
}

// Locate finds signal-cli: the explicit path, then the directories in
// Options.Dirs, then Options.InstallDir, then PATH. An explicit path that does not work is an error
// (no silent fallback to another signal-cli).
func Locate(o Options) (Command, error) {
	o.defaults()
	if o.Explicit != "" {
		c, err := o.fromPath(o.Explicit)
		if err != nil {
			return Command{}, fmt.Errorf("signal_cli_path %q: %w", o.Explicit, err)
		}
		c.Source = SourceSetting
		return c, nil
	}
	for i, d := range append(append([]string{}, o.Dirs...), o.InstallDir) {
		if d == "" {
			continue
		}
		c, ok, err := o.fromDir(d)
		if err != nil {
			return Command{}, err
		}
		if ok {
			c.Source = SourceBundled
			if i == len(o.Dirs) {
				c.Source = SourceInstalled
			}
			return c, nil
		}
	}
	if p, err := o.LookPath("signal-cli"); err == nil {
		c, err := o.fromPath(p)
		if err != nil {
			return Command{}, fmt.Errorf("signal-cli in PATH (%s): %w", p, err)
		}
		c.Source = SourcePath
		return c, nil
	}
	return Command{}, ErrNotFound
}

func (o *Options) isWindows() bool { return o.GOOS == "windows" }

// join, dir and base use the separator of the target OS, independent of
// the OS the code runs on (the tests cover Windows paths on Linux).
func (o *Options) join(elem ...string) string {
	if o.hostPaths {
		return filepath.Join(elem...)
	}
	if o.isWindows() {
		for i, e := range elem {
			elem[i] = strings.ReplaceAll(e, `\`, "/")
		}
		return strings.ReplaceAll(path.Join(elem...), "/", `\`)
	}
	return path.Join(elem...)
}

func (o *Options) dir(p string) string {
	if o.hostPaths {
		return filepath.Dir(p)
	}
	if o.isWindows() {
		return strings.ReplaceAll(path.Dir(strings.ReplaceAll(p, `\`, "/")), "/", `\`)
	}
	return path.Dir(p)
}

func (o *Options) base(p string) string {
	if o.hostPaths {
		return filepath.Base(p)
	}
	if o.isWindows() {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	return path.Base(p)
}

func (o *Options) exe(name string) string {
	if o.isWindows() {
		return name + ".exe"
	}
	return name
}

// isExec: a regular file that is executable (any regular file on Windows).
func (o *Options) isExec(p string) bool {
	fi, err := o.FS.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return o.isWindows() || fi.Mode().Perm()&0o111 != 0
}

func (o *Options) isDir(p string) bool {
	fi, err := o.FS.Stat(p)
	return err == nil && fi.IsDir()
}

// hasJars: dir/lib holds the signal-cli jar.
func (o *Options) hasJars(dir string) bool {
	entries, err := o.FS.ReadDir(o.join(dir, "lib"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "signal-cli") && strings.HasSuffix(n, ".jar") && !e.IsDir() {
			return true
		}
	}
	return false
}

// fromDir: native executable in dir, else a JVM build.
func (o *Options) fromDir(dir string) (Command, bool, error) {
	if !o.isDir(dir) {
		return Command{}, false, nil
	}
	if p := o.join(dir, o.exe("signal-cli")); o.isExec(p) {
		return Command{Path: p, Mode: ModeNative, Dir: dir}, true, nil
	}
	if o.hasJars(dir) {
		c, err := o.jvm(dir)
		return c, err == nil, err
	}
	return Command{}, false, nil
}

func (o *Options) fromPath(p string) (Command, error) {
	fi, err := o.FS.Stat(p)
	if err != nil {
		return Command{}, ErrNotFound
	}
	if fi.IsDir() {
		c, ok, err := o.fromDir(p)
		if err != nil {
			return Command{}, err
		}
		if !ok {
			return Command{}, fmt.Errorf("%w in directory", ErrNotFound)
		}
		return c, nil
	}
	base := strings.ToLower(o.base(p))
	root := o.dir(o.dir(p))
	jvmScript := strings.EqualFold(o.base(o.dir(p)), "bin") && o.hasJars(root)
	switch {
	case strings.HasSuffix(base, ".bat") || strings.HasSuffix(base, ".cmd"):
		// Batch files would run through cmd.exe with its own quoting rules:
		// start the JVM build directly instead.
		if !jvmScript {
			return Command{}, errors.New("batch files are not started directly; set the signal-cli directory instead")
		}
		return o.jvm(root)
	case jvmScript && (base == "signal-cli" || base == "signal-cli.sh"):
		return o.jvm(root)
	case !o.isExec(p):
		return Command{}, errors.New("not an executable file")
	case o.isWindows() && !strings.HasSuffix(base, ".exe"):
		return Command{}, errors.New("not an .exe file")
	}
	return Command{Path: p, Mode: ModeExec}, nil
}

// jvm: java from dir/jre, JAVA_HOME or PATH.
func (o *Options) jvm(dir string) (Command, error) {
	c := Command{Mode: ModeJVM, Dir: dir,
		Args: []string{"--enable-native-access=ALL-UNNAMED", "-cp", o.join(dir, "lib", "*"), MainClass}}
	javaIn := func(home string) string { return o.join(home, "bin", o.exe("java")) }
	if home := o.join(dir, "jre"); o.isExec(javaIn(home)) {
		c.Path, c.Env = javaIn(home), []string{"JAVA_HOME=" + home}
		return c, nil
	}
	if home := o.Getenv("JAVA_HOME"); home != "" && o.isExec(javaIn(home)) {
		c.Path = javaIn(home)
		return c, nil
	}
	if p, err := o.LookPath("java"); err == nil {
		c.Path = p
		return c, nil
	}
	return Command{}, ErrNoJava
}
