// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/signalcli"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// External programs (components) a plugin needs, and their on-demand
// install: only signal-cli for the Signal plugin so far. Release packages
// bundle it next to the plugins; development builds (or packages built
// without it) can download the pinned, checksum-verified release into
// <data dir>/plugins/signal-cli.

// Error reasons of a pairing (Pairing.Reason).
const (
	ReasonComponentMissing = "component_missing"
	ReasonUnavailable      = "unavailable"
	ReasonOutdated         = "outdated"
)

// ReasonFor maps a protocol error code to a pairing reason.
func ReasonFor(code int) string {
	switch code {
	case wire.CodeComponentMissing:
		return ReasonComponentMissing
	case wire.CodeUnavailable:
		return ReasonUnavailable
	case wire.CodeOutdated:
		return ReasonOutdated
	}
	return ""
}

// Component states.
const (
	ComponentReady       = "ready"
	ComponentMissing     = "missing"
	ComponentInstalling  = "installing"
	ComponentFailed      = "failed"
	ComponentUnsupported = "unsupported" // no pinned build for this platform
)

// ComponentOptions configure the on-demand install.
type ComponentOptions struct {
	// Pins default to the embedded pins (signalcli.LoadPins).
	Pins signalcli.Pins
	// Client is the HTTP client of the installer (default: HTTPS only).
	Client *http.Client
	// AllowHTTP permits http:// downloads (tests).
	AllowHTTP bool
	// GOOS/GOARCH default to the running platform.
	GOOS, GOARCH string
	// Locate replaces signalcli.Locate (tests).
	Locate func(signalcli.Options) (signalcli.Command, error)
}

// ComponentProgress is the progress of an install.
type ComponentProgress struct {
	Stage string `json:"stage"` // download | verify | unpack | done
	File  string `json:"file,omitempty"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Step  int    `json:"step"`
	Steps int    `json:"steps"`
}

// ComponentStatus describes the external program of a plugin.
type ComponentStatus struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Source  string `json:"source,omitempty"` // setting | bundled | installed | path (signalcli.Source*)
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	// Installable: the pinned download exists for this platform.
	Installable bool `json:"installable"`
	// InstallVersion is the version the install would fetch.
	InstallVersion string             `json:"installVersion,omitempty"`
	Error          string             `json:"error,omitempty"`
	Note           string             `json:"note,omitempty"`
	Progress       *ComponentProgress `json:"progress,omitempty"`
}

type install struct {
	progress ComponentProgress
	err      error
	running  bool
}

// ErrNoComponent: the plugin needs no external program.
var ErrNoComponent = errors.New("messenger: plugin has no installable component")

// ErrInstallRunning: an install is already running.
var ErrInstallRunning = errors.New("messenger: component install already running")

func (s *Service) componentPlatform() (string, string) {
	o := s.opts.Components
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (s *Service) pins() signalcli.Pins {
	if s.opts.Components.Pins != nil {
		return s.opts.Components.Pins
	}
	return signalcli.LoadPins()
}

// signalCLIInstallDir is <data dir>/plugins/signal-cli (the plugin looks
// for it next to its own data directory).
func (s *Service) signalCLIInstallDir() string { return s.host.PluginDataDir(signalcli.DirName) }

func (s *Service) signalCLIOptions() signalcli.Options {
	o := signalcli.Options{Explicit: s.pluginConfig("signal")["signal_cli_path"]}
	if m, ok := s.host.Manifest("signal"); ok && m.Path != "" {
		o.Dirs = append(o.Dirs, filepath.Join(filepath.Dir(m.Path), signalcli.DirName))
	}
	o.InstallDir = s.signalCLIInstallDir()
	return o
}

// Component returns the status of the external program a plugin needs
// (nil if it needs none).
func (s *Service) Component(plugin string) *ComponentStatus {
	if plugin != "signal" {
		return nil
	}
	st := &ComponentStatus{Name: "signal-cli"}
	goos, goarch := s.componentPlatform()
	plan, perr := s.pins().PlanFor(goos, goarch)
	if perr == nil && plan.Pinned() == nil {
		st.Installable, st.InstallVersion, st.Note = true, plan.Version, plan.Note
	}
	locate := s.opts.Components.Locate
	if locate == nil {
		locate = signalcli.Locate
	}
	c, err := locate(s.signalCLIOptions())
	switch {
	case err == nil:
		st.State, st.Source, st.Path, st.Version = ComponentReady, c.Source, c.Path, c.Version()
	case errors.Is(perr, signalcli.ErrUnsupported):
		st.State, st.Error = ComponentUnsupported, err.Error()
	default:
		st.State, st.Error = ComponentMissing, err.Error()
	}
	s.mu.Lock()
	if in := s.installs[plugin]; in != nil {
		p := in.progress
		switch {
		case in.running:
			st.State, st.Progress, st.Error = ComponentInstalling, &p, ""
		case in.err != nil && st.State != ComponentReady:
			st.State, st.Error = ComponentFailed, in.err.Error()
		}
	}
	s.mu.Unlock()
	return st
}

// InstallComponent downloads and installs the component of a plugin in
// the background; progress and the result arrive as plugin changes
// (Notifier.PluginChanged, Component).
func (s *Service) InstallComponent(ctx context.Context, plugin string) error {
	if plugin != "signal" {
		return ErrNoComponent
	}
	goos, goarch := s.componentPlatform()
	plan, err := s.pins().PlanFor(goos, goarch)
	if err != nil {
		return err
	}
	if err := plan.Pinned(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.installs == nil {
		s.installs = map[string]*install{}
	}
	if in := s.installs[plugin]; in != nil && in.running {
		s.mu.Unlock()
		return ErrInstallRunning
	}
	in := &install{running: true, progress: ComponentProgress{Stage: "download", Steps: len(plan.Downloads)}}
	s.installs[plugin] = in
	s.mu.Unlock()
	s.notify.PluginChanged(plugin)

	var lastNote time.Time
	inst := &signalcli.Installer{Client: s.opts.Components.Client, AllowHTTP: s.opts.Components.AllowHTTP,
		Progress: func(p signalcli.Progress) {
			s.mu.Lock()
			in.progress = ComponentProgress{Stage: p.Stage, File: p.File, Done: p.Done, Total: p.Total, Step: p.Step, Steps: p.Steps}
			s.mu.Unlock()
			if time.Since(lastNote) > 500*time.Millisecond || p.Stage != "download" {
				lastNote = time.Now()
				s.notify.PluginChanged(plugin)
			}
		}}
	dest := s.signalCLIInstallDir()
	s.goBackground(func(ctx context.Context) {
		err := inst.Install(ctx, plan, dest)
		s.mu.Lock()
		in.running, in.err = false, err
		s.mu.Unlock()
		if err != nil {
			s.log.Warn("messenger: install signal-cli", "err", err)
		} else {
			s.log.Info("messenger: installed signal-cli", "version", plan.Version, "dir", dest)
			// Accounts that failed for the missing signal-cli connect now.
			if s.host.Running(plugin) {
				s.onStarted(ctx, plugin)
			}
		}
		s.notify.PluginChanged(plugin)
	})
	return nil
}
