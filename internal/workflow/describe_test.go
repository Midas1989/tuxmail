// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/events"
)

func sampleValue(p ParamInfo) any {
	switch p.Type {
	case "bool":
		return true
	case "int":
		return 1
	case "duration":
		return "1h"
	case "size":
		return "1MB"
	case "list":
		if len(p.Enum) > 0 {
			return []any{p.Enum[0]}
		}
		return []any{"a@example.org"}
	case "map":
		return map[string]any{"X-A": "b"}
	case "enum":
		return p.Enum[0]
	case "query":
		return "from:x"
	}
	if p.Name == "url" {
		return "https://example.org/hook"
	}
	if p.Name == "channel" {
		return "signal:me"
	}
	return "x"
}

func TestDescribe(t *testing.T) {
	reg := DefaultRegistry()
	d := Describe(reg)
	if _, err := json.Marshal(d); err != nil {
		t.Fatal(err)
	}
	if len(d.Triggers) != len(events.AllTypes) {
		t.Fatalf("triggers %d", len(d.Triggers))
	}
	for _, tr := range d.Triggers {
		if tr.Description == "" {
			t.Errorf("trigger %s has no description", tr.Event)
		}
	}
	if len(d.Actions) != len(reg.Names()) {
		t.Fatalf("actions %d of %d", len(d.Actions), len(reg.Names()))
	}
	for _, at := range reg.Types() {
		params := actionParams(at)
		if params == nil {
			t.Errorf("action %s has no parameter description", at.Name)
			continue
		}
		// Every documented parameter is accepted by the constructor.
		vals := map[string]any{}
		for _, p := range params {
			vals[p.Name] = sampleValue(p)
		}
		if p, ok := paramInfo(params, "tag"); ok {
			delete(vals, p.Name)
		}
		if _, err := at.New(vals); err != nil && strings.Contains(err.Error(), "not found") {
			t.Errorf("action %s: documented parameter rejected: %v", at.Name, err)
		}
		// Required parameters are required.
		for _, p := range params {
			if !p.Required {
				continue
			}
			v := map[string]any{}
			for k, x := range vals {
				if k != p.Name {
					v[k] = x
				}
			}
			if _, err := at.New(v); err == nil {
				t.Errorf("action %s: missing required %s accepted", at.Name, p.Name)
			}
		}
	}
	names := map[string]bool{}
	for _, f := range d.Fields {
		names[f.Name] = true
	}
	for _, f := range []string{"mail.subject", "tag.path", "project.daysLeft", "searchfolder.name", "item.kind"} {
		if !names[f] {
			t.Errorf("field %s missing", f)
		}
	}
	var fn bool
	for _, f := range d.Functions {
		fn = fn || f.Name == "query"
	}
	if !fn || len(d.QueryFields) == 0 || len(d.Placeholders) == 0 || len(d.Options) == 0 {
		t.Fatal("functions, query fields, placeholders or options missing")
	}
	// Every condition field compiles in an expression.
	for _, f := range conditionFields() {
		if _, err := CompileExpr(f.expr + ` != nil || true`); err != nil {
			t.Errorf("field %s: %v", f.Name, err)
		}
	}
}

func TestJSONSchema(t *testing.T) {
	reg := DefaultRegistry()
	s := JSONSchema(reg)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema %v", back["$schema"])
	}
	defs := back["$defs"].(map[string]any)
	actions := defs["action"].(map[string]any)["oneOf"].([]any)
	if len(actions) != len(reg.Names()) {
		t.Fatalf("schema has %d actions", len(actions))
	}
	for _, a := range actions {
		am := a.(map[string]any)
		props := am["properties"].(map[string]any)
		typ := props["type"].(map[string]any)["const"].(string)
		at, _ := reg.Lookup(typ)
		for _, p := range actionParams(at) {
			if _, ok := props[p.Name]; !ok {
				t.Errorf("schema of %s lacks %s", typ, p.Name)
			}
		}
	}
	trig := back["properties"].(map[string]any)["trigger"].(map[string]any)
	evs := trig["properties"].(map[string]any)["event"].(map[string]any)["enum"].([]any)
	if len(evs) != len(events.AllTypes) {
		t.Fatalf("event enum %d", len(evs))
	}
	if !strings.Contains(string(b), `"$ref": "#/$defs/rule"`) {
		t.Fatal("rule definition not referenced")
	}
}
