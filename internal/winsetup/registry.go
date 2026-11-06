// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Root is a registry hive.
type Root int

const (
	CurrentUser  Root = iota // HKEY_CURRENT_USER
	LocalMachine             // HKEY_LOCAL_MACHINE (64-bit view)
)

func (r Root) String() string {
	if r == LocalMachine {
		return "HKLM"
	}
	return "HKCU"
}

// RootFor returns the hive of an installation scope.
func RootFor(s Scope) Root {
	if s == ScopeMachine {
		return LocalMachine
	}
	return CurrentUser
}

// ErrNotExist is returned for missing keys and values.
var ErrNotExist = errors.New("registry: key or value does not exist")

// Registry is the subset of the Windows registry the setup uses. Paths are
// relative to the hive, e.g. `Software\Microsoft\...`.
type Registry interface {
	GetString(root Root, path, name string) (string, error)
	GetDWORD(root Root, path, name string) (uint32, error)
	SetString(root Root, path, name, value string) error
	SetDWORD(root Root, path, name string, value uint32) error
	// DeleteValue and DeleteTree return ErrNotExist (or nil) for missing entries.
	DeleteValue(root Root, path, name string) error
	DeleteTree(root Root, path string) error
}

// Registry locations.
const (
	UninstallKey     = `Software\Microsoft\Windows\CurrentVersion\Uninstall\TuxMail`
	RunKey           = `Software\Microsoft\Windows\CurrentVersion\Run`
	ProgIDKey        = `Software\Classes\TuxMail.mailto`
	ClientKey        = `Software\Clients\Mail\TuxMail`
	CapabilitiesKey  = ClientKey + `\Capabilities`
	RegisteredAppKey = `Software\RegisteredApplications`
	ProgID           = "TuxMail.mailto"
	Publisher        = "TuxMail"
	Homepage         = "https://github.com/Midas1989/tuxmail"
)

// OpKind is a registry operation.
type OpKind int

const (
	OpSetString OpKind = iota
	OpSetDWORD
	OpDeleteValue
	OpDeleteTree
)

// Op is one step of a registry plan.
type Op struct {
	Kind  OpKind
	Root  Root
	Path  string
	Name  string
	Str   string
	DWORD uint32
}

func (o Op) String() string {
	k := o.Root.String() + `\` + o.Path
	name := o.Name
	if name == "" {
		name = "(Default)"
	}
	switch o.Kind {
	case OpSetString:
		return fmt.Sprintf("set %s [%s] = %q", k, name, o.Str)
	case OpSetDWORD:
		return fmt.Sprintf("set %s [%s] = dword:%d", k, name, o.DWORD)
	case OpDeleteValue:
		return fmt.Sprintf("delete %s [%s]", k, name)
	default:
		return "delete tree " + k
	}
}

// Apply runs a plan; deleting something that does not exist is no error.
func Apply(reg Registry, ops []Op) error {
	for _, o := range ops {
		var err error
		switch o.Kind {
		case OpSetString:
			err = reg.SetString(o.Root, o.Path, o.Name, o.Str)
		case OpSetDWORD:
			err = reg.SetDWORD(o.Root, o.Path, o.Name, o.DWORD)
		case OpDeleteValue:
			err = reg.DeleteValue(o.Root, o.Path, o.Name)
		case OpDeleteTree:
			err = reg.DeleteTree(o.Root, o.Path)
		}
		if err != nil && !errors.Is(err, ErrNotExist) {
			return fmt.Errorf("%s: %w", o, err)
		}
	}
	return nil
}

// Options are the optional integrations chosen on the options page.
type Options struct {
	StartMenu   bool `json:"startMenu"`
	Desktop     bool `json:"desktop"`
	DefaultMail bool `json:"defaultMail"`
	Autostart   bool `json:"autostart"`
}

// DefaultOptions: start menu entry only; system integration on request
// (KONZEPT 15.2).
func DefaultOptions() Options { return Options{StartMenu: true} }

// String encodes options as comma list ("startmenu,desktop,mail,autostart").
func (o Options) String() string {
	var s []string
	if o.StartMenu {
		s = append(s, "startmenu")
	}
	if o.Desktop {
		s = append(s, "desktop")
	}
	if o.DefaultMail {
		s = append(s, "mail")
	}
	if o.Autostart {
		s = append(s, "autostart")
	}
	return strings.Join(s, ",")
}

// ParseOptions decodes Options.String (unknown entries are an error).
func ParseOptions(s string) (Options, error) {
	var o Options
	for _, p := range strings.Split(s, ",") {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "":
		case "startmenu", "start-menu":
			o.StartMenu = true
		case "desktop", "desktop-shortcut":
			o.Desktop = true
		case "mail", "mailto", "default-mail", "defaultmail":
			o.DefaultMail = true
		case "autostart":
			o.Autostart = true
		default:
			return o, fmt.Errorf("unknown option %q", p)
		}
	}
	return o, nil
}

// EntryInfo describes an installation for the registry plan.
type EntryInfo struct {
	Scope   Scope
	Dir     string // installation folder
	Version string
	SizeKB  uint32
	Date    string // yyyymmdd
	Options Options
	// Description of the app (localized) for Default Programs.
	Description string
	// PathJoin joins paths (filepath.Join on the target system).
	PathJoin func(...string) string
}

func (e EntryInfo) join(elem ...string) string {
	if e.PathJoin != nil {
		return e.PathJoin(elem...)
	}
	return WinJoin(elem...)
}

func quote(s string) string { return `"` + s + `"` }

// InstallOps is the registry plan of an installation or update. Options
// that are off remove what an earlier installation registered.
// Portable installations do not touch the registry.
func InstallOps(e EntryInfo) []Op {
	if e.Scope == ScopePortable {
		return nil
	}
	r := RootFor(e.Scope)
	exe := e.join(e.Dir, AppExe)
	setup := e.join(e.Dir, SetupExe)
	s := func(path, name, v string) Op { return Op{Kind: OpSetString, Root: r, Path: path, Name: name, Str: v} }
	d := func(path, name string, v uint32) Op {
		return Op{Kind: OpSetDWORD, Root: r, Path: path, Name: name, DWORD: v}
	}
	scopeArg := " --scope=" + string(e.Scope)
	ops := []Op{
		s(UninstallKey, "DisplayName", AppName),
		s(UninstallKey, "DisplayVersion", e.Version),
		s(UninstallKey, "DisplayIcon", quote(exe)+",0"),
		s(UninstallKey, "Publisher", Publisher),
		s(UninstallKey, "URLInfoAbout", Homepage),
		s(UninstallKey, "HelpLink", Homepage+"/issues"),
		s(UninstallKey, "InstallLocation", e.Dir),
		s(UninstallKey, "InstallDate", e.Date),
		s(UninstallKey, "UninstallString", quote(setup)+" --uninstall"+scopeArg),
		s(UninstallKey, "QuietUninstallString", quote(setup)+" --uninstall --silent"+scopeArg),
		s(UninstallKey, "ModifyPath", quote(setup)+scopeArg),
		d(UninstallKey, "NoModify", 0),
		d(UninstallKey, "NoRepair", 1),
		d(UninstallKey, "EstimatedSize", e.SizeKB),
		s(UninstallKey, "TuxMailScope", string(e.Scope)),
		s(UninstallKey, "TuxMailOptions", e.Options.String()),
		// Written by the classic NSIS installer.
		{Kind: OpDeleteValue, Root: r, Path: UninstallKey, Name: "MailtoRegistered"},
	}
	if e.Options.DefaultMail {
		ops = append(ops, MailOps(r, exe, e.Description)...)
	} else {
		ops = append(ops, UnregisterMailOps(r)...)
	}
	if e.Options.Autostart {
		ops = append(ops, s(RunKey, AppName, quote(exe)))
	} else {
		ops = append(ops, Op{Kind: OpDeleteValue, Root: r, Path: RunKey, Name: AppName})
	}
	return ops
}

// MailOps registers TuxMail as mail program: a ProgID for mailto:, the
// legacy Clients\Mail entry and Default Programs capabilities
// (Settings > Apps > Default apps). Windows lets only the user pick the
// default; the setup opens that page afterwards.
//
// The command does not pass "%1" yet: the tuxmail command line has no
// "open mailto: URL" option (docs/RELEASE.md).
func MailOps(r Root, exe, description string) []Op {
	s := func(path, name, v string) Op { return Op{Kind: OpSetString, Root: r, Path: path, Name: name, Str: v} }
	icon := quote(exe) + ",0"
	cmd := quote(exe)
	if description == "" {
		description = "Email, calendar, contacts and messengers in one app"
	}
	return []Op{
		s(ProgIDKey, "", "TuxMail mailto link"),
		s(ProgIDKey, "FriendlyTypeName", "TuxMail mailto link"),
		s(ProgIDKey+`\DefaultIcon`, "", icon),
		s(ProgIDKey+`\shell\open\command`, "", cmd),
		s(ClientKey, "", AppName),
		s(ClientKey, "LocalizedString", AppName),
		s(ClientKey+`\DefaultIcon`, "", icon),
		s(ClientKey+`\shell\open\command`, "", cmd),
		s(CapabilitiesKey, "ApplicationName", AppName),
		s(CapabilitiesKey, "ApplicationDescription", description),
		s(CapabilitiesKey, "ApplicationIcon", icon),
		s(CapabilitiesKey+`\URLAssociations`, "mailto", ProgID),
		s(CapabilitiesKey+`\StartMenu`, "Mail", AppName),
		s(RegisteredAppKey, AppName, CapabilitiesKey),
	}
}

// UnregisterMailOps removes MailOps.
func UnregisterMailOps(r Root) []Op {
	return []Op{
		{Kind: OpDeleteValue, Root: r, Path: RegisteredAppKey, Name: AppName},
		{Kind: OpDeleteTree, Root: r, Path: ClientKey},
		{Kind: OpDeleteTree, Root: r, Path: ProgIDKey},
	}
}

// UninstallOps removes everything InstallOps wrote.
func UninstallOps(scope Scope) []Op {
	if scope == ScopePortable {
		return nil
	}
	r := RootFor(scope)
	ops := UnregisterMailOps(r)
	return append(ops,
		Op{Kind: OpDeleteValue, Root: r, Path: RunKey, Name: AppName},
		Op{Kind: OpDeleteTree, Root: r, Path: UninstallKey},
	)
}

// Existing is an installation found in the registry.
type Existing struct {
	Scope   Scope   `json:"scope"`
	Dir     string  `json:"dir"`
	Version string  `json:"version"`
	Options Options `json:"options"`
	// Legacy: installed by the classic NSIS setup (Uninstall.exe).
	Legacy bool `json:"legacy"`
}

// DetectExisting returns the installations registered for the current user
// and for all users (in this order).
func DetectExisting(reg Registry) []Existing {
	var out []Existing
	for _, sc := range []Scope{ScopeUser, ScopeMachine} {
		r := RootFor(sc)
		dir, err := reg.GetString(r, UninstallKey, "InstallLocation")
		if err != nil || strings.TrimSpace(dir) == "" {
			continue
		}
		e := Existing{Scope: sc, Dir: dir}
		e.Version, _ = reg.GetString(r, UninstallKey, "DisplayVersion")
		uninst, _ := reg.GetString(r, UninstallKey, "UninstallString")
		e.Legacy = strings.Contains(strings.ToLower(uninst), strings.ToLower(legacyUninst))
		if o, err := reg.GetString(r, UninstallKey, "TuxMailOptions"); err == nil {
			e.Options, _ = ParseOptions(o)
		} else {
			// Classic installer: start menu entry always, mailto flag as DWORD.
			e.Options = Options{StartMenu: true}
			if v, err := reg.GetDWORD(r, UninstallKey, "MailtoRegistered"); err == nil && v == 1 {
				e.Options.DefaultMail = true
			}
		}
		out = append(out, e)
	}
	return out
}

// FindExisting returns the installation of a scope, if any.
func FindExisting(all []Existing, scope Scope) (Existing, bool) {
	for _, e := range all {
		if e.Scope == scope {
			return e, true
		}
	}
	return Existing{}, false
}

// MemRegistry is an in-memory Registry (tests, dry runs).
type MemRegistry struct {
	Values map[string]any // "HKCU\path\to\key" + "\x00" + name -> string|uint32
}

// NewMemRegistry returns an empty MemRegistry.
func NewMemRegistry() *MemRegistry { return &MemRegistry{Values: map[string]any{}} }

func memKey(r Root, path, name string) string {
	return strings.ToLower(r.String()+`\`+path) + "\x00" + strings.ToLower(name)
}

func (m *MemRegistry) GetString(r Root, path, name string) (string, error) {
	v, ok := m.Values[memKey(r, path, name)].(string)
	if !ok {
		return "", ErrNotExist
	}
	return v, nil
}

func (m *MemRegistry) GetDWORD(r Root, path, name string) (uint32, error) {
	v, ok := m.Values[memKey(r, path, name)].(uint32)
	if !ok {
		return 0, ErrNotExist
	}
	return v, nil
}

func (m *MemRegistry) SetString(r Root, path, name, value string) error {
	m.Values[memKey(r, path, name)] = value
	return nil
}

func (m *MemRegistry) SetDWORD(r Root, path, name string, value uint32) error {
	m.Values[memKey(r, path, name)] = value
	return nil
}

func (m *MemRegistry) DeleteValue(r Root, path, name string) error {
	k := memKey(r, path, name)
	if _, ok := m.Values[k]; !ok {
		return ErrNotExist
	}
	delete(m.Values, k)
	return nil
}

func (m *MemRegistry) DeleteTree(r Root, path string) error {
	prefix := strings.ToLower(r.String() + `\` + path)
	found := false
	for k := range m.Values {
		key := k[:strings.IndexByte(k, 0)]
		if key == prefix || strings.HasPrefix(key, prefix+`\`) {
			delete(m.Values, k)
			found = true
		}
	}
	if !found {
		return ErrNotExist
	}
	return nil
}

// Keys lists the stored values as "HKCU\path [name]" (sorted, for tests).
func (m *MemRegistry) Keys() []string {
	var out []string
	for k := range m.Values {
		i := strings.IndexByte(k, 0)
		out = append(out, k[:i]+" ["+k[i+1:]+"]")
	}
	sort.Strings(out)
	return out
}
