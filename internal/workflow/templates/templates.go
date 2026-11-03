// SPDX-License-Identifier: AGPL-3.0-or-later

// Package templates contains the embedded workflow template gallery
// (KONZEPT 8.5 "Vorlagen-Galerie"). Templates are shipped disabled; the UI
// lets the user adapt and enable them.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/Midas1989/Mailclient/internal/workflow"
)

//go:embed *.yaml
var files embed.FS

// Template is a gallery entry.
type Template struct {
	ID          string
	Name        string
	Description string
	YAML        string
}

// Workflow parses the template into a new workflow.
func (t Template) Workflow() (*workflow.Workflow, error) {
	return workflow.Parse([]byte(t.YAML))
}

// List returns all templates sorted by name.
func List() ([]Template, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		b, err := fs.ReadFile(files, e.Name())
		if err != nil {
			return nil, err
		}
		w, err := workflow.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", e.Name(), err)
		}
		out = append(out, Template{ID: w.ID, Name: w.Name, Description: w.Description, YAML: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the template with the given workflow id.
func Get(id string) (Template, error) {
	ts, err := List()
	if err != nil {
		return Template{}, err
	}
	for _, t := range ts {
		if t.ID == id {
			return t, nil
		}
	}
	return Template{}, fmt.Errorf("templates: %q: %w", id, workflow.ErrNotFound)
}
