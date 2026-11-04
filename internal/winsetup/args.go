// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"fmt"
	"strings"
)

// Mode is what the setup program does.
type Mode string

const (
	ModeInstall   Mode = "install"
	ModeUninstall Mode = "uninstall"
)

// Config is the parsed command line.
type Config struct {
	Mode   Mode
	Silent bool
	// Scope is empty when not given (UI: ask; silent: existing installation
	// or "user").
	Scope Scope
	Dir   string
	// Options set explicitly on the command line (merged over the defaults
	// or the options of the existing installation).
	Opt      map[string]bool
	KeepData bool
	Lang     string
	LogFile  string
	// CloseApps: close a running TuxMail without asking (silent mode).
	CloseApps bool
	Help      bool

	// Internal flags.
	ProgressFile string // elevated child: write Progress JSON here
	Elevated     bool   // started elevated by the setup itself
	TempCopy     bool   // uninstaller copy running from %TEMP%
	Payload      string // development: payload zip from a file
}

// Usage is shown for --help.
const Usage = `TuxMail Setup

  TuxMail-<version>-windows-<arch>-setup.exe [options]

  /S, --silent              install without user interface
  --scope=user|machine|portable
                            user: %LOCALAPPDATA%\Programs\TuxMail (default)
                            machine: %ProgramFiles%\TuxMail (administrator)
                            portable: extract with "portable" marker and data\
  /ALLUSERS, /CURRENTUSER   same as --scope=machine / --scope=user
  --dir=PATH, /D=PATH       destination folder (/D= must be the last option)
  --[no-]start-menu         start menu shortcut (default: on)
  --[no-]desktop-shortcut   desktop shortcut (default: off)
  --[no-]default-mail       register as e-mail program / mailto: (default: off)
  --[no-]autostart          start TuxMail at sign-in (default: off)
  --options=LIST            startmenu,desktop,mail,autostart (replaces the defaults)
  --no-close-apps           fail instead of closing a running TuxMail
  --lang=de|en              language of the user interface
  --log=FILE                write a log file
  --uninstall               remove TuxMail (with --silent: without questions)
  --keep-data, --remove-data
                            keep (default) or delete %APPDATA%\TuxMail on uninstall

Exit codes: 0 success, 1 error, 2 invalid arguments, 1223 cancelled,
740 administrator rights refused.
`

// ParseArgs parses the setup command line. It accepts GNU style
// ("--scope=machine", "--scope machine") and NSIS/Inno style ("/S",
// "/ALLUSERS", "/D=C:\Path" - the rest of the line, may contain spaces).
func ParseArgs(args []string) (Config, error) {
	c := Config{Mode: ModeInstall, KeepData: true, CloseApps: true, Opt: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		// NSIS: /D=path is always last and taken verbatim.
		if len(a) > 3 && strings.EqualFold(a[:3], "/D=") {
			c.Dir = strings.TrimSpace(strings.Join(append([]string{a[3:]}, args[i+1:]...), " "))
			break
		}
		name, val, hasVal := a, "", false
		if strings.HasPrefix(a, "--") || strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") {
			name = strings.TrimLeft(a, "-/")
			if k, v, ok := strings.Cut(name, "="); ok {
				name, val, hasVal = k, v, true
			}
		} else {
			return c, fmt.Errorf("unexpected argument %q", a)
		}
		name = strings.ToLower(name)
		value := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 < len(args) {
				i++
				return args[i], nil
			}
			return "", fmt.Errorf("--%s needs a value", name)
		}
		var err error
		switch name {
		case "s", "silent", "verysilent", "quiet", "q", "qn":
			c.Silent = true
		case "scope":
			var v string
			if v, err = value(); err == nil {
				c.Scope, err = ParseScope(v)
			}
		case "allusers":
			c.Scope = ScopeMachine
		case "currentuser":
			c.Scope = ScopeUser
		case "portable":
			c.Scope = ScopePortable
		case "dir", "d", "destination":
			c.Dir, err = value()
		case "options":
			var v string
			if v, err = value(); err == nil {
				var o Options
				if o, err = ParseOptions(v); err == nil {
					c.Opt = map[string]bool{"startmenu": o.StartMenu, "desktop": o.Desktop, "mail": o.DefaultMail, "autostart": o.Autostart}
				}
			}
		case "start-menu", "startmenu":
			c.Opt["startmenu"] = true
		case "no-start-menu", "no-startmenu":
			c.Opt["startmenu"] = false
		case "desktop-shortcut", "desktop":
			c.Opt["desktop"] = true
		case "no-desktop-shortcut", "no-desktop":
			c.Opt["desktop"] = false
		case "default-mail", "mailto":
			c.Opt["mail"] = true
		case "no-default-mail", "no-mailto":
			c.Opt["mail"] = false
		case "autostart":
			c.Opt["autostart"] = true
		case "no-autostart":
			c.Opt["autostart"] = false
		case "uninstall", "u", "remove":
			c.Mode = ModeUninstall
		case "keep-data":
			c.KeepData = true
		case "remove-data", "delete-data":
			c.KeepData = false
		case "lang", "language":
			var v string
			if v, err = value(); err == nil {
				c.Lang = NormalizeLang(v)
			}
		case "log":
			c.LogFile, err = value()
		case "no-close-apps":
			c.CloseApps = false
		case "close-apps":
			c.CloseApps = true
		case "progress":
			c.ProgressFile, err = value()
		case "elevated":
			c.Elevated = true
		case "temp-copy":
			c.TempCopy = true
		case "payload":
			c.Payload, err = value()
		case "h", "help", "?":
			c.Help = true
		default:
			err = fmt.Errorf("unknown option %q", a)
		}
		if err != nil {
			return c, err
		}
	}
	return c, nil
}

// MergeOptions applies the explicit command line options to base.
func (c Config) MergeOptions(base Options) Options {
	for k, v := range c.Opt {
		switch k {
		case "startmenu":
			base.StartMenu = v
		case "desktop":
			base.Desktop = v
		case "mail":
			base.DefaultMail = v
		case "autostart":
			base.Autostart = v
		}
	}
	return base
}

// ChildArgs builds the command line of the elevated child process that
// performs an installation chosen in the UI.
func ChildArgs(req Request, lang, progressFile, logFile string) []string {
	args := []string{"--silent", "--elevated", "--scope=" + string(req.Scope), "--dir=" + req.Dir,
		"--options=" + req.Options.String()}
	if lang != "" {
		args = append(args, "--lang="+lang)
	}
	if progressFile != "" {
		args = append(args, "--progress="+progressFile)
	}
	if logFile != "" {
		args = append(args, "--log="+logFile)
	}
	return args
}

// UninstallChildArgs is ChildArgs for an uninstallation.
func UninstallChildArgs(req UninstallRequest, lang, progressFile string, silent bool) []string {
	args := []string{"--uninstall", "--scope=" + string(req.Scope), "--dir=" + req.Dir}
	if silent {
		args = append(args, "--silent")
	}
	if req.KeepData {
		args = append(args, "--keep-data")
	} else {
		args = append(args, "--remove-data")
	}
	if lang != "" {
		args = append(args, "--lang="+lang)
	}
	if progressFile != "" {
		args = append(args, "--progress="+progressFile)
	}
	return args
}

// NormalizeLang maps a language tag to a UI language (de or en).
func NormalizeLang(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	if t == "de" || strings.HasPrefix(t, "de-") || strings.HasPrefix(t, "de_") {
		return "de"
	}
	return "en"
}

// QuoteArgs joins arguments for a Windows command line (CommandLineToArgvW rules).
func QuoteArgs(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteArg(a))
	}
	return b.String()
}

func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
			b.WriteRune(r)
			slashes = 0
			continue
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
			slashes = 0
			b.WriteRune(r)
			continue
		}
	}
	b.WriteString(strings.Repeat(`\`, slashes*2))
	b.WriteByte('"')
	return b.String()
}
