// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"slices"
	"testing"
)

func TestParseArgs(t *testing.T) {
	c, err := ParseArgs([]string{"/S", "/ALLUSERS", "/D=C:\\Program", "Files\\TuxMail"})
	if err != nil || !c.Silent || c.Scope != ScopeMachine || c.Dir != `C:\Program Files\TuxMail` || c.Mode != ModeInstall {
		t.Fatalf("NSIS style: %+v, %v", c, err)
	}
	c, err = ParseArgs([]string{"--silent", "--scope", "user", "--dir=D:\\Apps\\TuxMail", "--desktop-shortcut", "--no-start-menu", "--default-mail", "--lang=de-AT", "--log", `C:\tmp\setup.log`})
	if err != nil || c.Scope != ScopeUser || c.Dir != `D:\Apps\TuxMail` || c.Lang != "de" || c.LogFile != `C:\tmp\setup.log` {
		t.Fatalf("GNU style: %+v, %v", c, err)
	}
	if o := c.MergeOptions(Options{StartMenu: true, Autostart: true}); o != (Options{Desktop: true, DefaultMail: true, Autostart: true}) {
		t.Errorf("MergeOptions = %+v", o)
	}
	c, err = ParseArgs([]string{"--options=desktop,autostart"})
	if err != nil {
		t.Fatal(err)
	}
	if o := c.MergeOptions(DefaultOptions()); o != (Options{Desktop: true, Autostart: true}) {
		t.Errorf("--options = %+v", o)
	}
	c, err = ParseArgs([]string{"--uninstall", "--silent", "--remove-data", "--scope=machine"})
	if err != nil || c.Mode != ModeUninstall || c.KeepData || c.Scope != ScopeMachine {
		t.Fatalf("uninstall: %+v, %v", c, err)
	}
	c, _ = ParseArgs(nil)
	if !c.KeepData || !c.CloseApps || c.Silent || c.Scope != "" {
		t.Errorf("defaults: %+v", c)
	}
	for _, bad := range [][]string{{"--scope=everyone"}, {"--frobnicate"}, {"stray"}, {"--dir"}, {"--options=toolbar"}} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("ParseArgs(%q) accepted", bad)
		}
	}
	if c, _ := ParseArgs([]string{"/?"}); !c.Help {
		t.Error("/? is not help")
	}
}

func TestChildArgsRoundTrip(t *testing.T) {
	req := Request{Scope: ScopeMachine, Dir: `C:\Program Files\TuxMail`, Options: Options{StartMenu: true, DefaultMail: true}}
	args := ChildArgs(req, "de", `C:\Temp\p.json`, "")
	c, err := ParseArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Silent || !c.Elevated || c.Scope != req.Scope || c.Dir != req.Dir || c.Lang != "de" || c.ProgressFile != `C:\Temp\p.json` {
		t.Errorf("child config = %+v", c)
	}
	if o := c.MergeOptions(Options{Desktop: true, Autostart: true}); o != req.Options {
		t.Errorf("child options = %+v", o)
	}
	u, err := ParseArgs(UninstallChildArgs(UninstallRequest{Scope: ScopeUser, Dir: `C:\x\TuxMail`, KeepData: false}, "en", "", true))
	if err != nil || u.Mode != ModeUninstall || u.KeepData || !u.Silent || u.Dir != `C:\x\TuxMail` {
		t.Errorf("uninstall child = %+v, %v", u, err)
	}
}

func TestQuoteArgs(t *testing.T) {
	got := QuoteArgs([]string{"--silent", `--dir=C:\Program Files\TuxMail`, `a"b`, `C:\end\`, ""})
	want := `--silent "--dir=C:\Program Files\TuxMail" "a\"b" C:\end\ ""`
	if got != want {
		t.Errorf("QuoteArgs = %s\nwant       %s", got, want)
	}
	if q := quoteArg(`C:\with space\`); q != `"C:\with space\\"` {
		t.Errorf("trailing backslash: %s", q)
	}
	if !slices.Equal([]string{NormalizeLang("de-DE"), NormalizeLang("en-US"), NormalizeLang("fr")}, []string{"de", "en", "en"}) {
		t.Error("NormalizeLang")
	}
}
