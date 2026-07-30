// Package tmpl turns a declarative template plus answers into policy TOML.
//
// The important design decision is that a template is **data, not text**. A
// text-templating engine — mustache, handlebars — emits a string, which means
// it can emit invalid TOML, cannot be linted before it runs, and interpolates
// user answers straight into a config format. That last one is injection: a
// room name containing a quote and a newline could add an extension nobody
// asked for, the same way string-built SQL fails.
//
// So a template declares its questions and the *structures* it emits, answers
// are referenced rather than interpolated, and this package builds real Go
// values which are then serialised. Quoting becomes the serialiser's problem,
// output is structurally valid by construction, and the result is run through
// the ordinary validator before anyone sees it.
//
// Templates may emit extensions and schedules and nothing else. A template that
// could write [[people]] would be able to add its own author to the allow-list
// — skipping the lobby, ringing the house — which makes "install this from a
// URL a stranger wrote" indefensible. With that restriction the worst case is a
// bad ladder.
package tmpl

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Template is the authored file.
type Template struct {
	Meta      Meta       `toml:"template" json:"template"`
	Questions []Question `toml:"questions" json:"questions"`
	Emit      Emit       `toml:"emit" json:"emit"`
}

type Meta struct {
	ID          string `toml:"id" json:"id"`
	Name        string `toml:"name" json:"name"`
	Version     string `toml:"version" json:"version"`
	Description string `toml:"description" json:"description"`
	Author      string `toml:"author" json:"author"`
}

// Question is one prompt. Type drives both the prompt style and validation;
// the set is deliberately small, because every type is a promise to template
// authors that has to keep working.
type Question struct {
	ID      string   `toml:"id" json:"id"`
	Prompt  string   `toml:"prompt" json:"prompt"`
	Type    string   `toml:"type" json:"type"`
	Default any      `toml:"default" json:"default"`
	Choices []string `toml:"choices" json:"choices"`
}

// Known question types.
const (
	TypeHandset      = "handset"       // one handset id
	TypeHandsetGroup = "handset-group" // one or more handset or group ids
	TypeTimeWindow   = "time-window"   // start, end, days
	TypeMailbox      = "mailbox"       // a voicemail box name
	TypeYesNo        = "yes-no"        // boolean
	TypeText         = "text"          // free text, used as a label
)

var knownTypes = map[string]bool{
	TypeHandset: true, TypeHandsetGroup: true, TypeTimeWindow: true,
	TypeMailbox: true, TypeYesNo: true, TypeText: true,
}

// Emit is the allow-list of what a template may produce. Adding a field here
// is a security decision, not a convenience one — see the package comment.
type Emit struct {
	Schedules  []EmitSchedule  `toml:"schedules" json:"schedules"`
	Extensions []EmitExtension `toml:"extensions" json:"extensions"`
}

type EmitSchedule struct {
	ID    string `toml:"id" json:"id"`
	Start string `toml:"start" json:"start"`
	End   string `toml:"end" json:"end"`
	Days  any    `toml:"days" json:"days"`
	When  string `toml:"when" json:"when"`
}

type EmitExtension struct {
	Label          string     `toml:"label" json:"label"`
	PIN            string     `toml:"pin" json:"pin"`
	Handsets       any        `toml:"handsets" json:"handsets"`
	Steps          []EmitStep `toml:"steps" json:"steps"`
	Voicemail      string     `toml:"voicemail" json:"voicemail"`
	Afterhours     string     `toml:"afterhours" json:"afterhours"`
	AfterhoursRing any        `toml:"afterhours_ring" json:"afterhours_ring"`
	When           string     `toml:"when" json:"when"`
}

type EmitStep struct {
	Handsets any    `toml:"handsets" json:"handsets"`
	Rings    int    `toml:"rings" json:"rings"`
	Seconds  int    `toml:"seconds" json:"seconds"`
	When     string `toml:"when" json:"when"`
}

// Parse reads a template from TOML or JSON, choosing by content rather than by
// file extension so a template served from a URL without one still works.
//
// YAML is deliberately absent: it would be this project's third dependency, and
// CONTRIBUTING requires a strong reason for each. TOML matches the rest of the
// configuration and JSON needs nothing at all.
func Parse(data []byte) (*Template, error) {
	trimmed := strings.TrimSpace(string(data))
	var t Template
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("template is not valid JSON: %w", err)
		}
	} else {
		if _, err := toml.Decode(string(data), &t); err != nil {
			return nil, fmt.Errorf("template is not valid TOML: %w", err)
		}
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Validate checks a template before it is ever run, which is the whole reason
// for choosing data over text: a mustache template's validity depends on its
// answers, and this one's does not.
func (t *Template) Validate() error {
	var problems []string
	fail := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	if t.Meta.ID == "" {
		fail("template.id is required")
	}
	if t.Meta.Name == "" {
		fail("template.name is required")
	}
	if t.Meta.Version == "" {
		fail("template.version is required")
	}

	seen := map[string]bool{}
	for i, q := range t.Questions {
		where := fmt.Sprintf("question %d", i+1)
		if q.ID != "" {
			where = fmt.Sprintf("question %q", q.ID)
		}
		if q.ID == "" {
			fail("%s has no id", where)
		}
		if seen[q.ID] {
			fail("%s is declared twice", where)
		}
		seen[q.ID] = true
		if q.Prompt == "" {
			fail("%s has no prompt", where)
		}
		if !knownTypes[q.Type] {
			fail("%s has unknown type %q (want one of %s)", where, q.Type, strings.Join(TypeNames(), ", "))
		}
	}

	if len(t.Emit.Extensions) == 0 && len(t.Emit.Schedules) == 0 {
		fail("template emits nothing")
	}

	// Every reference has to name a question that exists, or the failure
	// surfaces at fill-in time as a confusing empty value.
	check := func(where string, v any) {
		for _, ref := range refsIn(v) {
			if !seen[ref] && ref != "generate.pin" {
				fail("%s references $%s, which is not a question", where, ref)
			}
		}
	}
	for i, e := range t.Emit.Extensions {
		where := fmt.Sprintf("emit.extensions[%d]", i)
		check(where, e.Label)
		check(where, e.Handsets)
		check(where, e.Voicemail)
		check(where, e.AfterhoursRing)
		check(where, e.When)
		if e.PIN != "" && e.PIN != "$generate.pin" {
			fail("%s sets an explicit pin; templates must use $generate.pin so PINs come from crypto/rand", where)
		}
		for j, st := range e.Steps {
			check(fmt.Sprintf("%s.steps[%d]", where, j), st.Handsets)
		}
	}
	for i, s := range t.Emit.Schedules {
		where := fmt.Sprintf("emit.schedules[%d]", i)
		if s.ID == "" {
			fail("%s has no id", where)
		}
		check(where, s.Start)
		check(where, s.End)
		check(where, s.Days)
	}

	if len(problems) > 0 {
		return fmt.Errorf("template %q is not valid:\n  %s", t.Meta.ID, strings.Join(problems, "\n  "))
	}
	return nil
}

func TypeNames() []string {
	out := make([]string, 0, len(knownTypes))
	for k := range knownTypes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// refsIn finds $references inside any emitted value.
func refsIn(v any) []string {
	var out []string
	switch x := v.(type) {
	case string:
		if r, ok := strings.CutPrefix(x, "$"); ok {
			out = append(out, strings.SplitN(r, ".", 2)[0]+suffixOf(r))
		}
	case []string:
		for _, s := range x {
			out = append(out, refsIn(s)...)
		}
	case []any:
		for _, s := range x {
			out = append(out, refsIn(s)...)
		}
	case map[string]any:
		// A conditional value: both the guard and the value can reference
		// questions, and both have to exist.
		out = append(out, refsIn(x["when"])...)
		out = append(out, refsIn(x["value"])...)
	}
	return out
}

// suffixOf keeps "generate.pin" whole while reducing "bedtime.start" to
// "bedtime", since the question is what must exist.
func suffixOf(ref string) string {
	if strings.HasPrefix(ref, "generate.") {
		return strings.TrimPrefix(ref, "generate")
	}
	return ""
}
