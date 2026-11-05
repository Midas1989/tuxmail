// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
)

// BinaryPrefix is the file name prefix of plugin executables
// (tuxmail-plugin-whatsapp, tuxmail-plugin-telegram.exe ...).
const BinaryPrefix = "tuxmail-plugin-"

// Manifest is a discovered plugin.
type Manifest struct {
	Name string // e.g. "whatsapp"
	Path string // executable; empty for in-process plugins
	// New creates an in-process plugin (tests, development); Path is ignored.
	New func() sdk.Plugin `json:"-"`
}

// Discover finds plugin executables in dirs. The first directory that
// contains a plugin wins, so a development directory listed first
// overrides the installed plugins.
func Discover(dirs ...string) []Manifest {
	seen := map[string]bool{}
	var out []Manifest
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name, ok := pluginName(e.Name())
			if !ok || seen[name] || e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if !isExecutable(path) {
				continue
			}
			if err := checkTrusted(path); err != nil {
				// Another user could replace the program (AUDIT-D D-06).
				slog.Warn("messenger: ignoring plugin", "path", path, "err", err)
				continue
			}
			seen[name] = true
			out = append(out, Manifest{Name: name, Path: path})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func pluginName(file string) (string, bool) {
	if !strings.HasPrefix(file, BinaryPrefix) {
		return "", false
	}
	name := strings.TrimPrefix(file, BinaryPrefix)
	if runtime.GOOS == "windows" {
		var ok bool
		if name, ok = strings.CutSuffix(strings.ToLower(name), ".exe"); !ok {
			return "", false
		}
	} else if strings.Contains(name, ".") {
		return "", false // e.g. checksums or packages next to the binary
	}
	if name == "" || !sdk.ValidAccountID(name) {
		return "", false
	}
	return name, true
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || fi.Mode().Perm()&0o111 != 0
}

// DefaultDirs returns the plugin directories of an installation: an
// explicit development directory (TUXMAIL_PLUGIN_DIR), plugins/ next to the
// executable (Windows, portable and server archives) and ../lib/tuxmail/plugins
// relative to it (FHS packages: /usr/bin/tuxmail -> /usr/lib/tuxmail/plugins,
// AppImage, Docker /usr/local/lib/tuxmail/plugins). The executable's own
// directory is not searched: no package puts plugins there, and a TuxMail.exe
// in a shared download or tools folder would otherwise run any
// tuxmail-plugin-*.exe lying next to it (audit ND-13). Every candidate must
// also pass checkTrusted. bin/plugins of the working directory (build/plugins.sh output)
// is only searched by "go run" binaries: an installed TuxMail must never run
// programs from wherever it was started (AUDIT-D D-06).
func DefaultDirs() []string {
	var dirs []string
	if d := os.Getenv("TUXMAIL_PLUGIN_DIR"); d != "" {
		dirs = append(dirs, filepath.SplitList(d)...)
	}
	exe, err := os.Executable()
	if err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, installDirs(filepath.Dir(exe))...)
	}
	if wd, werr := os.Getwd(); werr == nil && err == nil && isGoRunBinary(exe) {
		dirs = append(dirs, filepath.Join(wd, "bin", "plugins"))
	}
	return dirs
}

// isGoRunBinary reports whether exe was built by "go run" / "go test"
// (a go-build* directory below the temporary directory).
func isGoRunBinary(exe string) bool {
	tmp := os.TempDir()
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	rel, err := filepath.Rel(tmp, exe)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return strings.HasPrefix(first, "go-build")
}

// installDirs are the plugin directories of an installation whose
// executable lives in exeDir.
func installDirs(exeDir string) []string {
	return []string{
		filepath.Join(exeDir, "plugins"),
		filepath.Join(filepath.Dir(exeDir), "lib", "tuxmail", "plugins"),
	}
}

// Proc is a running plugin.
type Proc interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// Wait blocks until the plugin exited.
	Wait() error
	// Kill terminates the plugin immediately.
	Kill() error
	PID() int
}

// Launcher starts plugins.
type Launcher interface {
	Launch(ctx context.Context, m Manifest) (Proc, error)
}

// ExecLauncher starts plugin executables; in-process manifests (New set)
// run in a goroutine.
type ExecLauncher struct {
	Logger *slog.Logger
	// Env is appended to the environment of the plugin.
	Env []string
	// FullEnv hands the complete environment of the host to the plugins
	// (tests, development); by default they get PluginEnv.
	FullEnv bool
}

// Launch implements Launcher.
func (l ExecLauncher) Launch(ctx context.Context, m Manifest) (Proc, error) {
	if m.New != nil {
		return InProcess(m.New(), l.Logger), nil
	}
	cmd := exec.Command(m.Path)
	if l.FullEnv {
		cmd.Env = append(os.Environ(), l.Env...)
	} else {
		cmd.Env = append(PluginEnv(os.Environ(), m.Name), l.Env...)
	}
	cmd.Dir = filepath.Dir(m.Path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	log := l.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("plugin", m.Name)
	p := &execProc{cmd: cmd, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			log.Info(sc.Text())
		}
	}()
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

type execProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	done   chan struct{}
	err    error
}

func (p *execProc) Stdin() io.WriteCloser { return p.stdin }
func (p *execProc) Stdout() io.Reader     { return p.stdout }
func (p *execProc) PID() int              { return p.cmd.Process.Pid }
func (p *execProc) Wait() error           { <-p.done; return p.err }
func (p *execProc) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	return p.cmd.Process.Kill()
}

// InProcess runs p in a goroutine connected through pipes (tests,
// development builds). Kill closes the pipes, which makes Serve return.
func InProcess(p sdk.Plugin, log *slog.Logger) Proc {
	hr, pw := io.Pipe() // plugin -> host
	pr, hw := io.Pipe() // host -> plugin
	ip := &inProc{stdin: hw, stdout: hr, done: make(chan struct{}), closers: []io.Closer{hr, pw, pr, hw}}
	go func() {
		ip.err = sdk.Serve(context.Background(), p, pr, pw, log)
		pw.Close()
		close(ip.done)
	}()
	return ip
}

type inProc struct {
	stdin   io.WriteCloser
	stdout  io.Reader
	done    chan struct{}
	err     error
	once    sync.Once
	closers []io.Closer
}

func (p *inProc) Stdin() io.WriteCloser { return p.stdin }
func (p *inProc) Stdout() io.Reader     { return p.stdout }
func (p *inProc) PID() int              { return 0 }
func (p *inProc) Wait() error           { <-p.done; return p.err }
func (p *inProc) Kill() error {
	p.once.Do(func() {
		for _, c := range p.closers {
			_ = c.Close()
		}
	})
	return nil
}
