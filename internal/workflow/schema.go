// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"github.com/Midas1989/Mailclient/internal/events"
)

// JSONSchemaID is the $id of the workflow JSON Schema.
const JSONSchemaID = "https://tuxmail.example/schemas/workflow.schema.json"

// JSONSchema returns a JSON Schema (draft 2020-12) of the workflow YAML
// format for the actions in reg (DefaultRegistry if nil), so that editors
// (e.g. the YAML language server) can validate and complete workflows.
// Action parameters are checked per action type; condition expressions and
// search queries are only checked to be strings (use ValidateText for
// semantic diagnostics).
func JSONSchema(reg *Registry) map[string]any {
	if reg == nil {
		reg = DefaultRegistry()
	}
	var evs []any
	for _, t := range events.AllTypes {
		evs = append(evs, string(t))
	}
	strList := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	duration := map[string]any{"type": "string", "pattern": `^\s*([0-9]+(\.[0-9]+)?\s*(ns|us|µs|ms|s|m|h|d|w)\s*)+$`,
		"description": "Duration like 30m, 24h, 3d or 1w"}
	trigger := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"event"},
		"properties": map[string]any{
			"event":          map[string]any{"enum": evs, "description": "Trigger event"},
			"accounts":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"account_names":  strList,
			"folders":        strList,
			"channels":       strList,
			"schedule":       map[string]any{"type": "string"},
			"name":           map[string]any{"type": "string"},
			"tags":           strList,
			"projects":       strList,
			"statuses":       map[string]any{"type": "array", "items": map[string]any{"enum": toAny(ProjectStatuses)}},
			"search_folders": strList,
		},
	}
	var fields []any
	for _, f := range conditionFields() {
		fields = append(fields, f.Name)
	}
	var ops []any
	for op := range ruleOpDocs {
		ops = append(ops, op)
	}
	rule := map[string]any{
		"type": "object", "additionalProperties": false, "minProperties": 1,
		"description": "Structured condition: exactly one of all, any, not, field (with op/value) or query",
		"properties": map[string]any{
			"all":   map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/rule"}},
			"any":   map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/rule"}},
			"not":   map[string]any{"$ref": "#/$defs/rule"},
			"field": map[string]any{"type": "string", "anyOf": []any{map[string]any{"enum": fields}, map[string]any{"pattern": `^(mail\.headers|vars|data|webhook\.headers)\..+`}}},
			"op":    map[string]any{"enum": ops},
			"value": map[string]any{},
			"query": map[string]any{"type": "string", "description": "Expert search query the item must match"},
		},
	}
	var actions []any
	for _, at := range reg.Types() {
		props := map[string]any{
			"type":              map[string]any{"const": at.Name},
			"if":                map[string]any{"type": "string", "description": "Expression; skip the action when false"},
			"continue_on_error": map[string]any{"type": "boolean"},
		}
		required := []any{"type"}
		for _, p := range actionParams(at) {
			props[p.Name] = paramSchema(p, duration)
			if p.Required {
				required = append(required, p.Name)
			}
		}
		actions = append(actions, map[string]any{
			"type": "object", "title": at.Name, "description": at.Description, "additionalProperties": false,
			"required": required, "properties": props,
		})
	}
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  JSONSchemaID,
		"title":                "TuxMail workflow",
		"type":                 "object",
		"required":             []any{"id", "name", "trigger", "actions"},
		"additionalProperties": false,
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "pattern": idPattern.String()},
			"name":        map[string]any{"type": "string", "minLength": 1},
			"description": map[string]any{"type": "string"},
			"enabled":     map[string]any{"type": "boolean"},
			"trigger":     trigger,
			"condition": map[string]any{"oneOf": []any{
				map[string]any{"type": "string", "description": "Condition expression (expr language)"},
				map[string]any{"$ref": "#/$defs/rule"},
			}},
			"actions": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"$ref": "#/$defs/action"}},
			"options": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"loop_protection": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
						"max_per_message": map[string]any{"type": "integer", "minimum": 0},
						"window":          duration,
						"max_depth":       map[string]any{"type": "integer", "minimum": 0},
					}},
					"rate_limit": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
						"max": map[string]any{"type": "integer", "minimum": 0},
						"per": duration,
					}},
					"run_location":        map[string]any{"enum": []any{"", "local", "server"}},
					"allow_decrypted_pgp": map[string]any{"type": "boolean"},
				},
			},
		},
		"$defs": map[string]any{
			"rule":   rule,
			"action": map[string]any{"oneOf": actions},
		},
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func paramSchema(p ParamInfo, duration map[string]any) map[string]any {
	var s map[string]any
	switch p.Type {
	case "bool":
		s = map[string]any{"type": "boolean"}
	case "int":
		s = map[string]any{"type": "integer"}
	case "duration":
		s = map[string]any{}
		for k, v := range duration {
			s[k] = v
		}
	case "size":
		s = map[string]any{"type": []any{"string", "integer"}}
	case "list":
		items := map[string]any{"type": "string"}
		if len(p.Enum) > 0 && p.Name == "scopes" {
			items = map[string]any{"enum": toAny(p.Enum)}
		}
		s = map[string]any{"type": "array", "items": items}
	case "map":
		s = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	case "enum":
		s = map[string]any{"enum": toAny(p.Enum)}
	default:
		s = map[string]any{"type": "string"}
	}
	if p.Description != "" {
		s["description"] = p.Description
	}
	if p.Default != "" {
		s["default"] = p.Default
	}
	return s
}
