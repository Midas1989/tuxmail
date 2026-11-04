// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(files[n]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func payloadV(t *testing.T, version string, plugins ...string) *Payload {
	files := map[string]string{
		"TuxMail.exe": "app " + version,
		"LICENSE.txt": "AGPL",
		"NOTICE.txt":  "notice",
	}
	for _, p := range plugins {
		files["plugins/tuxmail-plugin-"+p+".exe"] = p + " " + version
	}
	p, err := OpenPayload(makeZip(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenPayloadRejectsUnsafe(t *testing.T) {
	if _, err := OpenPayload(nil); err != ErrNoPayload {
		t.Errorf("empty payload: %v", err)
	}
	for _, name := range []string{"../evil.exe", "/abs.exe", `plugins\x.exe`, "C:/x.exe", "a/../../b", "x:stream"} {
		if _, err := OpenPayload(makeZip(t, map[string]string{"TuxMail.exe": "x", name: "evil"})); err == nil {
			t.Errorf("payload with %q accepted", name)
		}
	}
	if _, err := OpenPayload(makeZip(t, map[string]string{"README.txt": "x"})); err == nil {
		t.Error("payload without TuxMail.exe accepted")
	}
	if _, err := OpenPayload(makeZip(t, map[string]string{"TuxMail.exe": "x", RecordFile: "{}"})); err == nil {
		t.Error("payload with reserved name accepted")
	}
}

type fakeLinks struct{ made map[string]Shortcut }

func (f *fakeLinks) CreateShortcut(link string, s Shortcut) error {
	if f.made == nil {
		f.made = map[string]Shortcut{}
	}
	f.made[link] = s
	return os.WriteFile(link, []byte("lnk"), 0o644)
}

type rig struct {
	in          *Installer
	reg         *MemRegistry
	links       *fakeLinks
	root        string
	start, desk string
	setup       string
	progress    []Progress
}

func newRig(t *testing.T, p *Payload, version string) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{reg: NewMemRegistry(), links: &fakeLinks{}, root: root,
		start: filepath.Join(root, "StartMenu"), desk: filepath.Join(root, "Desktop"),
		setup: filepath.Join(root, "Downloads", "TuxMail-setup.exe")}
	_ = os.MkdirAll(filepath.Dir(r.setup), 0o755)
	if err := os.WriteFile(r.setup, []byte("setup "+version), 0o755); err != nil {
		t.Fatal(err)
	}
	r.in = &Installer{
		Reg: r.reg, Links: r.links, Payload: p, Version: version, SetupPath: r.setup,
		Folders: func(Scope) (Folders, error) { return Folders{StartMenu: r.start, Desktop: r.desk}, nil },
		Now:     func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) },
	}
	return r
}

func (r *rig) report(p Progress) { r.progress = append(r.progress, p) }

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallUpdateUninstall(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, payloadV(t, "0.1.0", "whatsapp", "signal"), "0.1.0")
	dir := filepath.Join(r.root, "Programs", "TuxMail")

	// Fresh per-user installation with start menu + desktop shortcuts.
	req := Request{Scope: ScopeUser, Dir: dir, Options: Options{StartMenu: true, Desktop: true, DefaultMail: true}}
	if err := r.in.Install(ctx, req, r.report); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "TuxMail.exe")); got != "app 0.1.0" {
		t.Errorf("TuxMail.exe = %q", got)
	}
	if got := read(t, filepath.Join(dir, "plugins", "tuxmail-plugin-signal.exe")); got != "signal 0.1.0" {
		t.Errorf("plugin = %q", got)
	}
	if got := read(t, filepath.Join(dir, SetupExe)); got != "setup 0.1.0" {
		t.Errorf("setup copy = %q", got)
	}
	if len(r.links.made) != 2 {
		t.Errorf("shortcuts = %v", r.links.made)
	}
	if s := r.links.made[filepath.Join(r.start, LinkName)]; s.Target != filepath.Join(dir, AppExe) || s.WorkDir != dir {
		t.Errorf("start menu shortcut = %+v", s)
	}
	last := r.progress[len(r.progress)-1]
	if last.Step != StepFinish || last.Percent != 100 {
		t.Errorf("last progress = %+v", last)
	}
	for i := 1; i < len(r.progress); i++ {
		if r.progress[i].Percent < r.progress[i-1].Percent {
			t.Errorf("progress goes backwards: %+v -> %+v", r.progress[i-1], r.progress[i])
		}
	}
	if v, _ := r.reg.GetString(CurrentUser, UninstallKey, "UninstallString"); v != `"`+filepath.Join(dir, SetupExe)+`" --uninstall --scope=user` {
		t.Errorf("UninstallString = %q", v)
	}
	if v, _ := r.reg.GetDWORD(CurrentUser, UninstallKey, "EstimatedSize"); v == 0 {
		t.Error("EstimatedSize missing")
	}
	rec, err := ReadRecord(dir)
	if err != nil || rec.Version != "0.1.0" || !slices.Contains(rec.Files, SetupExe) || !slices.Contains(rec.Files, "plugins/tuxmail-plugin-whatsapp.exe") {
		t.Fatalf("record = %+v, %v", rec, err)
	}

	// A file of the user in the installation folder survives everything.
	foreign := filepath.Join(dir, "my-notes.txt")
	_ = os.WriteFile(foreign, []byte("mine"), 0o644)

	// Update to 0.2.0: signal plugin dropped, desktop shortcut and mail off.
	ex, ok := FindExisting(DetectExisting(r.reg), ScopeUser)
	if !ok || Classify(ex.Version, "0.2.0") != Upgrade || !ex.Options.Desktop {
		t.Fatalf("existing = %+v", ex)
	}
	r.in.Payload, r.in.Version = payloadV(t, "0.2.0", "whatsapp"), "0.2.0"
	req.Options = Options{StartMenu: true}
	if err := r.in.Install(ctx, req, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "TuxMail.exe")); got != "app 0.2.0" {
		t.Errorf("updated TuxMail.exe = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "tuxmail-plugin-signal.exe")); !os.IsNotExist(err) {
		t.Error("stale plugin not removed")
	}
	if _, err := os.Stat(filepath.Join(r.desk, LinkName)); !os.IsNotExist(err) {
		t.Error("desktop shortcut not removed")
	}
	if _, err := r.reg.GetString(CurrentUser, RegisteredAppKey, "TuxMail"); err == nil {
		t.Error("mail registration not removed")
	}
	if v, _ := r.reg.GetString(CurrentUser, UninstallKey, "DisplayVersion"); v != "0.2.0" {
		t.Errorf("DisplayVersion = %q", v)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*"+newSuffix)); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}

	// Uninstall, keeping the user data.
	data := filepath.Join(r.root, "AppData", "TuxMail")
	_ = os.MkdirAll(data, 0o755)
	failed, err := r.in.Uninstall(ctx, UninstallRequest{Scope: ScopeUser, Dir: dir, KeepData: true, DataDir: data}, nil)
	if err != nil || len(failed) > 0 {
		t.Fatalf("uninstall: %v, failed %v", err, failed)
	}
	if _, err := os.Stat(filepath.Join(dir, "TuxMail.exe")); !os.IsNotExist(err) {
		t.Error("TuxMail.exe still there")
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins")); !os.IsNotExist(err) {
		t.Error("plugins folder still there")
	}
	if read(t, foreign) != "mine" {
		t.Error("foreign file touched")
	}
	if _, err := os.Stat(filepath.Join(r.start, LinkName)); !os.IsNotExist(err) {
		t.Error("start menu shortcut still there")
	}
	if k := r.reg.Keys(); len(k) != 0 {
		t.Errorf("registry left: %v", k)
	}
	if _, err := os.Stat(data); err != nil {
		t.Error("user data removed although KeepData")
	}
}

func TestUninstallRemovesDataAndEmptyDir(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, payloadV(t, "0.1.0", "matrix"), "0.1.0")
	dir := filepath.Join(r.root, "Program Files", "TuxMail")
	if err := r.in.Install(ctx, Request{Scope: ScopeMachine, Dir: dir, Options: DefaultOptions()}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reg.GetString(LocalMachine, UninstallKey, "InstallLocation"); err != nil {
		t.Fatal("machine entry missing")
	}
	data := filepath.Join(r.root, "Roaming", "TuxMail")
	_ = os.MkdirAll(filepath.Join(data, "locales"), 0o755)
	_ = os.WriteFile(filepath.Join(data, "tuxmail.db"), []byte("db"), 0o600)
	if _, err := r.in.Uninstall(ctx, UninstallRequest{Scope: ScopeMachine, Dir: dir, KeepData: false, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("installation folder not removed")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Error("data folder not removed")
	}
	// Never delete a data folder that is not called TuxMail.
	if err := removeDataDir(r.root); err == nil {
		t.Error("removeDataDir accepted a foreign folder")
	}
	if _, err := r.in.Uninstall(ctx, UninstallRequest{Scope: ScopeUser, Dir: "/"}, nil); err == nil {
		t.Error("uninstall from / accepted")
	}
}

func TestInstallPortable(t *testing.T) {
	r := newRig(t, payloadV(t, "0.1.0", "telegram"), "0.1.0")
	dir := filepath.Join(r.root, "USB", "TuxMail")
	if err := r.in.Install(context.Background(), Request{Scope: ScopePortable, Dir: dir, Options: Options{StartMenu: true, DefaultMail: true}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{PortableMark, "TuxMail.exe", "plugins/tuxmail-plugin-telegram.exe"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s missing", p)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, PortableData)); err != nil || !st.IsDir() {
		t.Error("data folder missing")
	}
	if _, err := os.Stat(filepath.Join(dir, SetupExe)); !os.IsNotExist(err) {
		t.Error("portable folder contains the setup program")
	}
	if len(r.reg.Keys()) != 0 || len(r.links.made) != 0 {
		t.Errorf("portable installation touched the system: %v %v", r.reg.Keys(), r.links.made)
	}
	if st := StatDir(dir); !st.HasApp || !st.Portable {
		t.Errorf("StatDir = %+v", st)
	}
}

func TestInstallOverLegacyNSIS(t *testing.T) {
	r := newRig(t, payloadV(t, "0.2.0"), "0.2.0")
	dir := filepath.Join(r.root, "Programs", "TuxMail")
	_ = os.MkdirAll(dir, 0o755)
	for _, f := range []string{"TuxMail.exe", "Uninstall.exe", "LICENSE.txt", "NOTICE.txt"} {
		_ = os.WriteFile(filepath.Join(dir, f), []byte("old"), 0o644)
	}
	_ = r.reg.SetString(CurrentUser, UninstallKey, "InstallLocation", dir)
	_ = r.reg.SetString(CurrentUser, UninstallKey, "UninstallString", `"`+filepath.Join(dir, "Uninstall.exe")+`"`)
	_ = r.reg.SetString(CurrentUser, UninstallKey, "DisplayVersion", "0.1.0")
	if err := r.in.Install(context.Background(), Request{Scope: ScopeUser, Dir: dir, Options: DefaultOptions()}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Uninstall.exe")); !os.IsNotExist(err) {
		t.Error("NSIS uninstaller not removed")
	}
	if v, _ := r.reg.GetString(CurrentUser, UninstallKey, "UninstallString"); !strings.Contains(v, SetupExe) {
		t.Errorf("UninstallString = %q", v)
	}
}

func TestFailedFreshInstallRollsBack(t *testing.T) {
	r := newRig(t, payloadV(t, "0.1.0", "whatsapp"), "0.1.0")
	r.in.SetupPath = filepath.Join(r.root, "missing.exe") // copy fails after extraction
	dir := filepath.Join(r.root, "Programs", "TuxMail")
	if err := r.in.Install(context.Background(), Request{Scope: ScopeUser, Dir: dir}, nil); err == nil {
		t.Fatal("install succeeded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(dir)
		t.Errorf("folder left after rollback: %v", entries)
	}
	if len(r.reg.Keys()) != 0 {
		t.Error("registry written")
	}
}

func TestExtractReplacesLockedTarget(t *testing.T) {
	dir := t.TempDir()
	p := payloadV(t, "0.2.0")
	// A directory where TuxMail.exe should go cannot be renamed over:
	// replaceFile moves it aside and puts the new file in place.
	if err := os.MkdirAll(filepath.Join(dir, "TuxMail.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.Extract(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "TuxMail.exe")) != "app 0.2.0" {
		t.Error("not replaced")
	}
	CleanupOld(dir)
	if _, err := os.Stat(filepath.Join(dir, "TuxMail.exe"+oldSuffix)); err == nil {
		// The moved-aside directory is not a regular leftover file; fine either way.
		_ = os.RemoveAll(filepath.Join(dir, "TuxMail.exe"+oldSuffix))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Extract(ctx, t.TempDir(), nil); err == nil {
		t.Error("cancelled extraction succeeded")
	}
}

func TestWritePayloadZstdRoundTrip(t *testing.T) {
	src := t.TempDir()
	big := bytes.Repeat([]byte("TuxMail payload "), 64<<10)
	for name, data := range map[string][]byte{"TuxMail.exe": big, "plugins/tuxmail-plugin-matrix.exe": []byte("matrix"), "LICENSE.txt": []byte("AGPL")} {
		p := filepath.Join(src, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	mtime := time.Unix(1700000000, 0)
	if err := WritePayload(&buf, src, mtime); err != nil {
		t.Fatal(err)
	}
	var again bytes.Buffer
	_ = WritePayload(&again, src, mtime)
	if !bytes.Equal(buf.Bytes(), again.Bytes()) {
		t.Error("payload is not reproducible")
	}
	if buf.Len() > len(big)/10 {
		t.Errorf("payload not compressed: %d bytes", buf.Len())
	}
	p, err := OpenPayload(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Files(), []string{"LICENSE.txt", "TuxMail.exe", "plugins/tuxmail-plugin-matrix.exe"}) {
		t.Errorf("files = %v", p.Files())
	}
	dst := t.TempDir()
	if err := p.Extract(context.Background(), dst, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "TuxMail.exe")); !bytes.Equal(got, big) {
		t.Error("TuxMail.exe differs after extraction")
	}
	if st, _ := os.Stat(filepath.Join(dst, "TuxMail.exe")); !st.ModTime().Equal(mtime) {
		t.Errorf("mtime = %v", st.ModTime())
	}
}

func TestStaleAndRemoveFiles(t *testing.T) {
	old := &Record{Files: []string{"TuxMail.exe", "plugins/tuxmail-plugin-signal.exe", "plugins/tuxmail-plugin-whatsapp.exe", "../escape"}}
	if got := Stale(old, []string{"tuxmail.exe", "plugins/tuxmail-plugin-whatsapp.exe"}); !slices.Equal(got, []string{"plugins/tuxmail-plugin-signal.exe", "../escape"}) {
		t.Errorf("Stale = %v", got)
	}
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "escape")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	defer os.Remove(outside)
	RemoveFiles(dir, []string{"../escape"})
	if _, err := os.Stat(outside); err != nil {
		t.Error("RemoveFiles followed ../")
	}
}
