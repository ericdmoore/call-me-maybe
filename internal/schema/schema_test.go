package schema_test

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/schema"
	"callmemaybe/internal/tmpl"
)

// The schema is hand-authored, because the rules worth publishing — mutual
// exclusion, cross-file references, "a window crossing midnight belongs to the
// day it starts" — are not recoverable from struct tags. Hand-authored means
// it can drift, so these tests are the guard: they fail when the Go
// configuration surface grows something the schema does not mention.

func TestEveryTOMLKeyIsDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, s := range []*schema.Schema{schema.Policy(), schema.Handsets()} {
		collectProperties(s, documented)
	}

	for _, key := range tomlKeys(reflect.TypeOf(policy.File{})) {
		if !documented[key] {
			t.Errorf("policy.File exposes toml key %q but no schema documents it — "+
				"add it to internal/schema so `doorman schema` stays truthful", key)
		}
	}
}

func TestEveryEnvVarIsDocumented(t *testing.T) {
	// config.Load reads the environment through small helpers, so the set of
	// variable names is a set of string literals in that one file. Reading it
	// here is cheaper and more direct than any indirection we could add to
	// production code purely to be introspectable.
	src, err := os.ReadFile("../config/config.go")
	if err != nil {
		t.Fatalf("reading config source: %v", err)
	}
	// need("X"), str("X", ...), boolean("X", ...), integer("X", ...), ms("X", ...)
	re := regexp.MustCompile(`\b(?:need|str|boolean|integer|ms)\("([A-Z][A-Z0-9_]*)"`)
	matches := re.FindAllSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatal("found no env var literals in config.go — has the parsing style changed? " +
			"this test needs updating, do not delete it")
	}

	env := schema.Env()
	for _, m := range matches {
		name := string(m[1])
		if _, ok := env.Properties[name]; !ok {
			t.Errorf("config.Load reads %s but `doorman schema env` does not document it", name)
		}
	}

	// And the reverse: nothing documented that is not actually read, which
	// would send a reader chasing a variable with no effect.
	read := map[string]bool{}
	for _, m := range matches {
		read[string(m[1])] = true
	}
	for name := range env.Properties {
		if !read[name] {
			t.Errorf("schema documents %s but config.Load never reads it", name)
		}
	}
}

func TestRequiredEnvMatchesLoader(t *testing.T) {
	// need() is what makes a variable mandatory; the schema's required list
	// must agree or the document lies about what it takes to boot.
	src, err := os.ReadFile("../config/config.go")
	if err != nil {
		t.Fatalf("reading config source: %v", err)
	}
	re := regexp.MustCompile(`\bneed\("([A-Z][A-Z0-9_]*)"`)
	var want []string
	for _, m := range re.FindAllSubmatch(src, -1) {
		want = append(want, string(m[1]))
	}

	got := schema.Env().Required
	if len(want) != len(got) {
		t.Fatalf("config.Load requires %v, schema says %v", want, got)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is required by config.Load but not listed in the schema", w)
		}
	}
}

func TestSchemasAreValidJSON(t *testing.T) {
	for _, name := range schema.Names {
		s, err := schema.Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		b, err := schema.Marshal(s)
		if err != nil {
			t.Fatalf("marshal %q: %v", name, err)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Errorf("schema %q does not round-trip: %v", name, err)
		}
		if round["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("schema %q is missing its $schema declaration", name)
		}
		if !strings.HasSuffix(string(b), "\n") {
			t.Errorf("schema %q should end in a newline for shell friendliness", name)
		}
	}
}

func TestGetRejectsUnknownName(t *testing.T) {
	if _, err := schema.Get("nope"); err == nil {
		t.Fatal("expected an error for an unknown schema name")
	}
}

func TestBundleCarriesTheAuthorityNote(t *testing.T) {
	// The bundle must keep saying that `doorman check` is authoritative. A
	// schema that implies it is complete is worse than no schema, because a
	// reader will trust it for the cross-file rules it cannot express.
	b := schema.All("test")
	if !strings.Contains(b.Authority, "doorman check") {
		t.Error("bundle no longer points at `doorman check` as the authority")
	}
	for _, want := range []string{"policy.toml", "handsets.toml", "env"} {
		if _, ok := b.Schemas[want]; !ok {
			t.Errorf("bundle is missing %q", want)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func collectProperties(s *schema.Schema, into map[string]bool) {
	if s == nil {
		return
	}
	for name, sub := range s.Properties {
		into[name] = true
		collectProperties(sub, into)
	}
	collectProperties(s.Items, into)
}

// tomlKeys walks a struct type and returns every toml tag name it finds,
// following slices and nested structs.
func tomlKeys(t reflect.Type) []string {
	seen := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("toml")
			if name := strings.Split(tag, ",")[0]; name != "" && name != "-" {
				seen[name] = true
			}
			walk(f.Type)
		}
	}
	walk(t)

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// The template format is a published interface with outside authors, so its
// schema falling behind the Go structs is worse than for the config files:
// someone validates against it, it passes, and then the CLI rejects the file.
func TestTemplateFormatDocumentsEveryKey(t *testing.T) {
	documented := map[string]bool{}
	collectProperties(schema.TemplateFormat(), documented)

	for _, key := range tomlKeys(reflect.TypeOf(tmpl.Template{})) {
		// Emitted policy structures are documented by reference to the policy
		// schema rather than repeated field by field.
		switch key {
		case "handsets", "steps", "rings", "seconds", "voicemail", "afterhours",
			"afterhours_ring", "label", "pin", "when", "start", "end", "days",
			"schedules", "extensions":
			continue
		}
		if !documented[key] {
			t.Errorf("tmpl.Template exposes %q but `doorman schema template` does not document it", key)
		}
	}
}

func TestTemplateSchemaIsSelectable(t *testing.T) {
	s, err := schema.Get("template")
	if err != nil {
		t.Fatalf("template schema not selectable: %v", err)
	}
	if s.Title == "" {
		t.Error("template schema has no title")
	}
	// The emit allow-list is the security property; it has to be stated where
	// an author will read it.
	joined := ""
	for _, r := range s.Rules {
		joined += r
	}
	if !strings.Contains(joined, "people") {
		t.Error("the schema should say templates may not emit [[people]]")
	}
}
