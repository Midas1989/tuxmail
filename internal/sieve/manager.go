// SPDX-License-Identifier: AGPL-3.0-or-later

package sieve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// DefaultScriptName is the script TuxMail creates when none is active.
const DefaultScriptName = "tuxmail"

// Manager applies TuxMail's managed section to the server's scripts.
//
// Activation strategy: if no script is active, TuxMail creates
// DefaultScriptName with only the managed section and activates it. If
// the user already has an active script, the managed section is inserted
// into it (after its require commands) and the rest of the script stays
// untouched. With Separate (and the "include" extension, RFC 6609) the
// managed content lives in DefaultScriptName and the active script only
// gets a managed `include :personal "tuxmail";`.
type Manager struct {
	Dial       func(ctx context.Context) (*Client, error)
	ScriptName string
	Separate   bool

	mu   sync.Mutex
	exts []string
	when time.Time
}

func (m *Manager) name() string {
	if m.ScriptName != "" {
		return m.ScriptName
	}
	return DefaultScriptName
}

func (m *Manager) with(ctx context.Context, fn func(c *Client) error) error {
	if m.Dial == nil {
		return errors.New("sieve: no ManageSieve server configured")
	}
	c, err := m.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	m.mu.Lock()
	m.exts, m.when = c.Extensions(), time.Now()
	m.mu.Unlock()
	return fn(c)
}

// Extensions returns the server's Sieve extensions (cached for 10 minutes).
func (m *Manager) Extensions(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	exts, when := m.exts, m.when
	m.mu.Unlock()
	if exts != nil && time.Since(when) < 10*time.Minute {
		return exts, nil
	}
	err := m.with(ctx, func(c *Client) error { exts = c.Extensions(); return nil })
	return exts, err
}

// ==== raw editor ===============================================================

// Scripts lists the scripts.
func (m *Manager) Scripts(ctx context.Context) ([]ScriptInfo, error) {
	var out []ScriptInfo
	err := m.with(ctx, func(c *Client) error {
		var err error
		out, err = c.ListScripts()
		return err
	})
	return out, err
}

// Script returns a script's source.
func (m *Manager) Script(ctx context.Context, name string) (string, error) {
	var out string
	err := m.with(ctx, func(c *Client) error {
		var err error
		out, err = c.GetScript(name)
		return err
	})
	return out, err
}

// Put stores a script (the server validates it) and optionally activates it.
func (m *Manager) Put(ctx context.Context, name, src string, activate bool) error {
	return m.with(ctx, func(c *Client) error {
		if err := c.PutScript(name, src); err != nil {
			return err
		}
		if activate {
			return c.SetActive(name)
		}
		return nil
	})
}

// Check validates a script on the server (warnings are returned as text).
// Without CHECKSCRIPT support only the local parser runs.
func (m *Manager) Check(ctx context.Context, src string) (string, error) {
	var warn string
	err := m.with(ctx, func(c *Client) error {
		var err error
		warn, err = c.CheckScript(src)
		if err != nil && strings.Contains(err.Error(), "not supported") {
			if r := Parse(src, false); r.Error != "" {
				return fmt.Errorf("line %d: %s", r.ErrorLine, r.Error)
			}
			return nil
		}
		return err
	})
	return warn, err
}

// Activate activates a script ("" deactivates all).
func (m *Manager) Activate(ctx context.Context, name string) error {
	return m.with(ctx, func(c *Client) error { return c.SetActive(name) })
}

// Delete deletes a script.
func (m *Manager) Delete(ctx context.Context, name string) error {
	return m.with(ctx, func(c *Client) error { return c.DeleteScript(name) })
}

// ==== managed section ==========================================================

// ApplyPart writes one part of the managed section (an empty body removes
// it) according to the activation strategy.
func (m *Manager) ApplyPart(ctx context.Context, name string, p Part) error {
	return m.with(ctx, func(c *Client) error { return m.applyPart(c, name, p) })
}

func (m *Manager) applyPart(c *Client, name string, p Part) error {
	list, err := c.ListScripts()
	if err != nil {
		return err
	}
	active := ""
	for _, s := range list {
		if s.Active {
			active = s.Name
		}
	}
	own := m.name()
	exists := func(n string) bool {
		for _, s := range list {
			if s.Name == n {
				return true
			}
		}
		return false
	}
	update := func(script string, fn func(*Section)) error {
		src := ""
		if exists(script) {
			if src, err = c.GetScript(script); err != nil {
				return err
			}
		}
		sec := ParseSection(src)
		fn(&sec)
		out := ReplaceSection(src, sec)
		if out == src && exists(script) {
			return nil
		}
		return c.PutScript(script, out)
	}
	separate := m.Separate && active != "" && active != own && c.HasExtension("include")
	if separate {
		if err := update(own, func(s *Section) { s.SetPart(name, p) }); err != nil {
			return err
		}
		inc := Part{Requires: []string{"include"}, Body: "include :personal " + sieveString(own) + ";\n"}
		return update(active, func(s *Section) { s.SetPart(PartInclude, inc) })
	}
	target := active
	if target == "" {
		target = own
	}
	if err := update(target, func(s *Section) { s.SetPart(name, p) }); err != nil {
		return err
	}
	if active == "" {
		return c.SetActive(own)
	}
	return nil
}

// SyncWorkflows generates the rules part from the workflows and writes it.
// The result lists the included and skipped workflows.
func (m *Manager) SyncWorkflows(ctx context.Context, ws []*workflow.Workflow, opts GenerateOptions) (GenerateResult, error) {
	var res GenerateResult
	err := m.with(ctx, func(c *Client) error {
		if len(opts.Extensions) == 0 {
			opts.Extensions = c.Extensions()
		}
		res = Generate(ws, opts)
		return m.applyPart(c, PartRules, res.Part)
	})
	return res, err
}

// RemoveWorkflows removes the rules part (toggle "Workflows als
// Serverregeln übertragen" switched off).
func (m *Manager) RemoveWorkflows(ctx context.Context) error {
	return m.ApplyPart(ctx, PartRules, Part{})
}

// ==== vacation =================================================================

// VacationSync implements vacation.ServerVacationSync on ManageSieve
// (Dovecot Pigeonhole, Cyrus timsieved, Stalwart ...).
type VacationSync struct {
	M   *Manager
	Now func() time.Time
	Loc *time.Location
}

var _ vacation.ServerVacationSync = (*VacationSync)(nil)

// Supported implements vacation.ServerVacationSync.
func (v *VacationSync) Supported() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exts, err := v.M.Extensions(ctx)
	if err != nil {
		return false
	}
	for _, e := range exts {
		if e == "vacation" {
			return true
		}
	}
	return false
}

// Push implements vacation.ServerVacationSync.
func (v *VacationSync) Push(ctx context.Context, s vacation.Settings) error {
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	return v.M.with(ctx, func(c *Client) error {
		p, err := VacationPart(s, GenerateOptions{Extensions: c.Extensions()}, now, v.Loc)
		if err != nil {
			return err
		}
		return v.M.applyPart(c, PartVacation, p)
	})
}
