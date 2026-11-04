// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"strings"
	"testing"
)

func entry(scope Scope, o Options) EntryInfo {
	return EntryInfo{Scope: scope, Dir: `C:\Program Files\TuxMail`, Version: "0.2.0", SizeKB: 123456, Date: "20261104", Options: o}
}

func TestInstallOpsMachine(t *testing.T) {
	reg := NewMemRegistry()
	if err := Apply(reg, InstallOps(entry(ScopeMachine, Options{StartMenu: true, DefaultMail: true, Autostart: true}))); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DisplayName":          "TuxMail",
		"DisplayVersion":       "0.2.0",
		"Publisher":            "TuxMail",
		"DisplayIcon":          `"C:\Program Files\TuxMail\TuxMail.exe",0`,
		"InstallLocation":      `C:\Program Files\TuxMail`,
		"UninstallString":      `"C:\Program Files\TuxMail\TuxMail-Setup.exe" --uninstall --scope=machine`,
		"QuietUninstallString": `"C:\Program Files\TuxMail\TuxMail-Setup.exe" --uninstall --silent --scope=machine`,
		"TuxMailScope":         "machine",
		"TuxMailOptions":       "startmenu,mail,autostart",
	}
	for name, v := range want {
		if got, err := reg.GetString(LocalMachine, UninstallKey, name); err != nil || got != v {
			t.Errorf("HKLM uninstall %s = %q (%v), want %q", name, got, err, v)
		}
	}
	if v, _ := reg.GetDWORD(LocalMachine, UninstallKey, "EstimatedSize"); v != 123456 {
		t.Errorf("EstimatedSize = %d", v)
	}
	if v, _ := reg.GetString(LocalMachine, CapabilitiesKey+`\URLAssociations`, "mailto"); v != ProgID {
		t.Errorf("URLAssociations mailto = %q", v)
	}
	if v, _ := reg.GetString(LocalMachine, RegisteredAppKey, "TuxMail"); v != CapabilitiesKey {
		t.Errorf("RegisteredApplications = %q", v)
	}
	if v, _ := reg.GetString(LocalMachine, ProgIDKey+`\shell\open\command`, ""); v != `"C:\Program Files\TuxMail\TuxMail.exe"` {
		t.Errorf("mailto command = %q", v)
	}
	if v, _ := reg.GetString(LocalMachine, RunKey, "TuxMail"); v != `"C:\Program Files\TuxMail\TuxMail.exe"` {
		t.Errorf("autostart = %q", v)
	}
	for _, k := range reg.Keys() {
		if strings.HasPrefix(k, "hkcu") {
			t.Errorf("machine installation wrote %s", k)
		}
	}

	// Detected as existing installation of the machine scope.
	ex := DetectExisting(reg)
	if len(ex) != 1 || ex[0].Scope != ScopeMachine || ex[0].Version != "0.2.0" || !ex[0].Options.DefaultMail || ex[0].Legacy {
		t.Fatalf("DetectExisting = %+v", ex)
	}

	// Update with mail and autostart turned off removes them.
	if err := Apply(reg, InstallOps(entry(ScopeMachine, Options{StartMenu: true}))); err != nil {
		t.Fatal(err)
	}
	for _, k := range reg.Keys() {
		if strings.Contains(k, `clients\mail`) || strings.Contains(k, `tuxmail.mailto`) || strings.Contains(k, `registeredapplications`) || strings.Contains(k, `\run [`) {
			t.Errorf("left over after disabling options: %s", k)
		}
	}

	// Uninstall removes everything.
	if err := Apply(reg, UninstallOps(ScopeMachine)); err != nil {
		t.Fatal(err)
	}
	if k := reg.Keys(); len(k) != 0 {
		t.Errorf("left after uninstall: %v", k)
	}
}

func TestInstallOpsUserAndPortable(t *testing.T) {
	reg := NewMemRegistry()
	e := entry(ScopeUser, Options{DefaultMail: true})
	e.Dir = `C:\Users\anna\AppData\Local\Programs\TuxMail`
	if err := Apply(reg, InstallOps(e)); err != nil {
		t.Fatal(err)
	}
	for _, k := range reg.Keys() {
		if !strings.HasPrefix(k, "hkcu") {
			t.Errorf("user installation wrote %s", k)
		}
	}
	if v, _ := reg.GetString(CurrentUser, `Software\Classes\TuxMail.mailto\DefaultIcon`, ""); !strings.HasSuffix(v, `TuxMail.exe",0`) {
		t.Errorf("ProgID icon = %q", v)
	}
	if ops := InstallOps(entry(ScopePortable, Options{DefaultMail: true})); ops != nil {
		t.Errorf("portable installation touches the registry: %v", ops)
	}
	if ops := UninstallOps(ScopePortable); ops != nil {
		t.Errorf("portable uninstall touches the registry: %v", ops)
	}
}

func TestDetectLegacyNSIS(t *testing.T) {
	reg := NewMemRegistry()
	_ = reg.SetString(CurrentUser, UninstallKey, "InstallLocation", `C:\Users\anna\AppData\Local\Programs\TuxMail`)
	_ = reg.SetString(CurrentUser, UninstallKey, "DisplayVersion", "0.1.0")
	_ = reg.SetString(CurrentUser, UninstallKey, "UninstallString", `"C:\Users\anna\AppData\Local\Programs\TuxMail\Uninstall.exe"`)
	_ = reg.SetDWORD(CurrentUser, UninstallKey, "MailtoRegistered", 1)
	ex := DetectExisting(reg)
	if len(ex) != 1 || !ex[0].Legacy || !ex[0].Options.DefaultMail || !ex[0].Options.StartMenu || ex[0].Scope != ScopeUser {
		t.Fatalf("DetectExisting = %+v", ex)
	}
	if _, ok := FindExisting(ex, ScopeMachine); ok {
		t.Error("FindExisting(machine) found the user installation")
	}
	// The new entry drops the NSIS flag.
	if err := Apply(reg, InstallOps(EntryInfo{Scope: ScopeUser, Dir: ex[0].Dir, Version: "0.2.0", Options: ex[0].Options})); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.GetDWORD(CurrentUser, UninstallKey, "MailtoRegistered"); err == nil {
		t.Error("MailtoRegistered still set")
	}
}

func TestOptionsRoundTrip(t *testing.T) {
	for _, o := range []Options{{}, {StartMenu: true}, {true, true, true, true}, {Desktop: true, Autostart: true}} {
		got, err := ParseOptions(o.String())
		if err != nil || got != o {
			t.Errorf("ParseOptions(%q) = %+v, %v", o.String(), got, err)
		}
	}
	if _, err := ParseOptions("startmenu,toolbar"); err == nil {
		t.Error("unknown option accepted")
	}
}

func TestOpString(t *testing.T) {
	o := Op{Kind: OpSetString, Root: CurrentUser, Path: RunKey, Name: "TuxMail", Str: "x"}
	if s := o.String(); !strings.Contains(s, `HKCU\Software\Microsoft\Windows\CurrentVersion\Run [TuxMail]`) {
		t.Errorf("Op.String = %s", s)
	}
}

func TestVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want UpgradeKind
	}{
		{"", "0.2.0", Fresh},
		{"0.1.0", "0.2.0", Upgrade},
		{"0.2.0", "0.1.9", Downgrade},
		{"0.2.0", "0.2.0", Reinstall},
		{"0.2.0-dev.abc1234", "0.2.0", Upgrade},
		{"0.2.0", "0.2.0-dev.abc1234", Downgrade},
		{"0.2.0-dev.abc1234", "0.2.0-dev.def5678", Upgrade},
		{"0.1.0", "0.10.0", Upgrade},
		{"1.0.0-rc.1", "1.0.0-rc.2", Upgrade},
		{"v1.2", "1.2.0", Upgrade},
	} {
		if got := Classify(tc.a, tc.b); got != tc.want {
			t.Errorf("Classify(%q, %q) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}
