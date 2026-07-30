// Package schema describes doorman's configuration surface as JSON Schema.
//
// Why this exists: policy.toml and handsets.toml are the real interface to
// this system, and until now the only machine-readable knowledge of their
// shape lived inside policy.compileChecked — reachable by running the
// validator against a candidate file, but not by reading anything. A person
// or a model authoring a policy had to infer the rules from examples/ or from
// Go source. `doorman schema` publishes them.
//
// Honesty about the limits, which the emitted document repeats: JSON Schema
// can express types, enums, patterns, and required keys. It cannot express
// "every id in extensions.steps[].handsets must exist in handsets.toml", nor
// "handsets and steps are mutually exclusive per extension", nor the ~33
// semantic checks the validator applies. Those are recorded as
// x-cross-references and x-rules annotations, and the document points at
// `doorman check` as the authority. A schema that silently implied it was
// complete would be worse than none.
package schema

import (
	"encoding/json"
	"fmt"
)

// Schema is the subset of JSON Schema draft 2020-12 this package emits.
// Field order here is the order in the output, since encoding/json follows
// struct declaration order — worth keeping readable for whoever reads it raw.
type Schema struct {
	SchemaURI   string `json:"$schema,omitempty"`
	ID          string `json:"$id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`

	Type  string `json:"type,omitempty"`
	Enum  []any  `json:"enum,omitempty"`
	Const any    `json:"const,omitempty"`

	// Strings
	Pattern   string `json:"pattern,omitempty"`
	MinLength *int   `json:"minLength,omitempty"`
	MaxLength *int   `json:"maxLength,omitempty"`

	// Numbers
	Minimum *int `json:"minimum,omitempty"`
	Maximum *int `json:"maximum,omitempty"`

	// Objects
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`

	// Arrays
	Items    *Schema `json:"items,omitempty"`
	MinItems *int    `json:"minItems,omitempty"`

	Default any `json:"default,omitempty"`

	// Extensions. JSON Schema has no vocabulary for cross-file identifier
	// references or for mutually exclusive keys with a helpful explanation,
	// and both are load-bearing here.
	CrossRefs []string `json:"x-cross-references,omitempty"`
	Rules     []string `json:"x-rules,omitempty"`
	EnvVar    string   `json:"x-env-var,omitempty"`
	ParsedAs  string   `json:"x-parsed-as,omitempty"`
}

// Bundle is what `doorman schema` prints with no arguments: every schema at
// once, which is the form a model wants when it is about to author a config.
type Bundle struct {
	Doorman   string             `json:"doorman"`
	Generated string             `json:"x-generated-by"`
	Authority string             `json:"x-authority"`
	Schemas   map[string]*Schema `json:"schemas"`
}

func ptr[T any](v T) *T { return &v }

var (
	falsy = ptr(false)
	one   = ptr(1)
)

// Names are the selectable schema names for `doorman schema <name>`.
var Names = []string{"policy", "handsets", "env"}

// Get returns one schema by name.
func Get(name string) (*Schema, error) {
	switch name {
	case "policy":
		return Policy(), nil
	case "handsets":
		return Handsets(), nil
	case "env":
		return Env(), nil
	}
	return nil, fmt.Errorf("unknown schema %q (want one of policy, handsets, env)", name)
}

// All returns the bundle of every schema.
func All(version string) *Bundle {
	return &Bundle{
		Doorman:   version,
		Generated: "doorman schema",
		Authority: "This document describes shape. `doorman check` is the authority on " +
			"validity: it applies cross-file reference checks and semantic rules that " +
			"JSON Schema cannot express. Always run `doorman check` before relying on a config.",
		Schemas: map[string]*Schema{
			"policy.toml":   Policy(),
			"handsets.toml": Handsets(),
			"env":           Env(),
		},
	}
}

// Marshal renders v as indented JSON with a trailing newline.
func Marshal(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ── handsets.toml ────────────────────────────────────────────────────────

func Handsets() *Schema {
	return &Schema{
		SchemaURI:   "https://json-schema.org/draft/2020-12/schema",
		ID:          "https://callmemaybe.cc/schema/handsets.json",
		Title:       "handsets.toml — the hardware inventory",
		Type:        "object",
		Description: "The phone plant: what physically exists. `doorman render` generates the per-handset Asterisk config from this file, so ids, endpoints, and dialplan cannot drift apart. Changes when you buy hardware. Rules live in policy.toml.",
		Rules: []string{
			"At least one [[handsets]] entry is required.",
			"A group id may appear anywhere a handset id may, and expands at load time.",
			"Groups may contain handsets only, never other groups — no cycles.",
		},
		Properties: map[string]*Schema{
			"handsets": {
				Type:        "array",
				Description: "Every physical phone or dialplan destination.",
				MinItems:    one,
				Items:       handsetItem(),
			},
			"groups": {
				Type:        "array",
				Description: "Named sets of handsets, usable anywhere a handset id is.",
				Items:       groupItem(),
			},
		},
		Required:             []string{"handsets"},
		AdditionalProperties: falsy,
	}
}

func handsetItem() *Schema {
	return &Schema{
		Type:     "object",
		Required: []string{"id", "endpoint"},
		Properties: map[string]*Schema{
			"id": {
				Type:        "string",
				Description: "Stable identifier, referenced from policy.toml.",
				Pattern:     "^[a-z0-9][a-z0-9_-]*$",
				Rules:       []string{"Must be unique across handsets and groups."},
			},
			"label": {
				Type:        "string",
				Description: "Human name, shown in `doorman check` output.",
			},
			"endpoint": {
				Type:        "string",
				Description: "Asterisk endpoint. Must be PJSIP/<id> — `doorman render` enforces the match so the generated config cannot disagree with the inventory.",
				Pattern:     `^[A-Za-z0-9]+/\S+$`,
			},
			"number": {
				Type:        "integer",
				Description: "Internal extension for handset-to-handset dialling and BLF hints. 0 or absent means not directly dialable.",
				Minimum:     ptr(100),
				Maximum:     ptr(199),
				Rules:       []string{"Two handsets may not share a number.", "0 is allowed and means not dialable."},
			},
			"page": {
				Type:        "boolean",
				Description: "Include in the page-all group reached by dialling 500.",
				Default:     false,
			},
			"mailbox": {
				Type:        "string",
				Description: "Mailbox whose message-waiting lamp this phone follows.",
				Pattern:     "^[a-z0-9][a-z0-9_-]*$",
				CrossRefs:   []string{"asterisk/voicemail.conf [household] context"},
			},
			"password_env": {
				Type:        "string",
				Description: "Name of the .env variable holding this handset's SIP password. The secret itself never appears in this file. `doorman render` substitutes it and fails if unset.",
				Rules:       []string{"Required by `doorman render`, which reports it as an error when missing.", "Names a variable; never put the password here."},
			},
		},
		AdditionalProperties: falsy,
	}
}

func groupItem() *Schema {
	return &Schema{
		Type:     "object",
		Required: []string{"id", "handsets"},
		Properties: map[string]*Schema{
			"id": {
				Type:        "string",
				Pattern:     "^[a-z0-9][a-z0-9_-]*$",
				Description: "Group identifier.",
				Rules:       []string{"May not collide with any handset id.", "Must be unique."},
			},
			"label":    {Type: "string", Description: "Human name."},
			"handsets": {Type: "array", MinItems: one, Items: &Schema{Type: "string"}, Description: "Member handset ids.", CrossRefs: []string{"handsets.toml [[handsets]].id"}},
		},
		AdditionalProperties: falsy,
	}
}

// ── policy.toml ──────────────────────────────────────────────────────────

func Policy() *Schema {
	return &Schema{
		SchemaURI:   "https://json-schema.org/draft/2020-12/schema",
		ID:          "https://callmemaybe.cc/schema/policy.json",
		Title:       "policy.toml — who gets in, what rings, and when",
		Type:        "object",
		Description: "The rules. Safe to hand-edit: a file that fails validation is logged and discarded on reload, and the last good policy stays in service. Handset and group ids referenced here must exist in handsets.toml.",
		Rules: []string{
			"Reloaded live when POLICY_WATCH is true; an invalid file is rejected, never applied.",
			"Extensions are credentials. Treat a PIN like a password and prefer `doorman rotate` over choosing one.",
			"Allow-listed numbers may be written in any format; they normalise to E.164 at load time. An unparseable number is a load error, not a silent never-match.",
		},
		Properties: map[string]*Schema{
			"house":      house(),
			"people":     {Type: "array", Description: "The allow-list. These callers hear the welcome prompt and ring the house.", Items: person()},
			"schedules":  {Type: "array", Description: "Named time windows, defined once and referenced by id from extensions.", Items: schedule()},
			"extensions": {Type: "array", Description: "What an unknown caller may dial in the lobby.", Items: extension()},
		},
		AdditionalProperties: falsy,
	}
}

func house() *Schema {
	return &Schema{
		Type:        "object",
		Description: "What rings when a known caller is welcomed.",
		Properties: map[string]*Schema{
			"handsets": {
				Type:        "array",
				MinItems:    one,
				Items:       &Schema{Type: "string"},
				Description: "Handset or group ids that ring together.",
				CrossRefs:   []string{"handsets.toml [[handsets]].id", "handsets.toml [[groups]].id"},
			},
			"voicemail": {
				Type:        "string",
				Pattern:     "^[a-z0-9][a-z0-9_-]*$",
				Description: "Mailbox for callers nobody answers. Absent means 'nobody's available' and a hangup.",
				CrossRefs:   []string{"asterisk/voicemail.conf [household] context"},
			},
			"caller_id_format": {
				Type:        "string",
				Description: "Handset display format. {name} is the caller or extension label; {number} is the normalised caller.",
				Default:     "Lobby: {name} <{number}>",
			},
		},
		Required:             []string{"handsets"},
		AdditionalProperties: falsy,
	}
}

func person() *Schema {
	return &Schema{
		Type:     "object",
		Required: []string{"name", "numbers"},
		Properties: map[string]*Schema{
			"name": {Type: "string", Description: "Who this is. Appears on the handset display and in logs."},
			"numbers": {
				Type:        "array",
				MinItems:    one,
				Items:       &Schema{Type: "string"},
				Description: "Any format: 512-555-0100, (512) 555-0100, +15125550100. Normalised to E.164 at load. Probe a value with `doorman e164 <number>`.",
				Rules:       []string{"An unparseable number fails the load rather than never matching."},
			},
			"notes": {Type: "string", Description: "Free text for whoever edits this next."},
		},
		AdditionalProperties: falsy,
	}
}

func schedule() *Schema {
	return &Schema{
		Type:     "object",
		Required: []string{"id", "start", "end"},
		Properties: map[string]*Schema{
			"id": {Type: "string", Pattern: "^[a-z0-9][a-z0-9_-]*$", Description: "Referenced as afterhours = \"<id>\" from an extension."},
			"enabled": {
				Type:        "boolean",
				Default:     true,
				Description: "False makes every reference inert without deleting the definition — one edit turns bedtime off for a school holiday.",
			},
			"start": {Type: "string", Pattern: `^\d{1,2}:\d{2}$`, Description: "Local time, HH:MM."},
			"end":   {Type: "string", Pattern: `^\d{1,2}:\d{2}$`, Description: "Local time, HH:MM. May be earlier than start, which means the window crosses midnight."},
			"days": {
				Type:        "array",
				Items:       &Schema{Type: "string", Enum: []any{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}},
				Description: "Days the window STARTS on. Empty means every day.",
				Rules:       []string{"A window crossing midnight belongs to the day it starts: 20:30-07:00 on MO covers Monday night into Tuesday morning."},
			},
		},
		AdditionalProperties: falsy,
	}
}

func extension() *Schema {
	return &Schema{
		Type:        "object",
		Required:    []string{"pin", "label"},
		Description: "One dialable destination. The PIN is the credential; there is no separate username.",
		Rules: []string{
			"handsets and steps are mutually exclusive: use handsets for a single ring stage, steps for an escalating ladder.",
			"Exactly one of them must be present.",
			"afterhours requires voicemail — an afterhours caller needs somewhere to go.",
		},
		Properties: map[string]*Schema{
			"pin": {
				Type:        "string",
				Pattern:     `^\d+$`,
				Description: "Digits only. Exposed to the PSTN, so treat it as a password: generate with `doorman rotate`, never pick 123456.",
				Rules:       []string{"Must be unique across extensions.", "When every PIN shares a length, the lobby accepts on the final digit instead of waiting out the inter-digit timer."},
			},
			"label":   {Type: "string", Description: "Human name, used by `doorman rotate <label>` and shown in `doorman check`."},
			"enabled": {Type: "boolean", Default: true, Description: "False disables the extension without deleting it."},
			"handsets": {
				Type:        "array",
				MinItems:    one,
				Items:       &Schema{Type: "string"},
				Description: "Simple form: one ring stage at the default timeout. Mutually exclusive with steps.",
				CrossRefs:   []string{"handsets.toml [[handsets]].id", "handsets.toml [[groups]].id"},
			},
			"steps": {
				Type:        "array",
				MinItems:    one,
				Items:       step(),
				Description: "Ringer ladder: ring each stage in order, escalating on no-answer. Mutually exclusive with handsets.",
			},
			"voicemail": {
				Type:        "string",
				Pattern:     "^[a-z0-9][a-z0-9_-]*$",
				Description: "Mailbox when every stage goes unanswered, and the afterhours destination. Absent means a polite dismissal.",
				CrossRefs:   []string{"asterisk/voicemail.conf [household] context"},
			},
			"afterhours": {
				Type:        "string",
				Description: "Id of a [[schedules]] entry. During the window the line does not ring at all and goes straight to voicemail.",
				CrossRefs:   []string{"policy.toml [[schedules]].id"},
				Rules:       []string{"The inline-table form is retired; use a named schedule id.", "Requires voicemail to be set."},
			},
		},
		AdditionalProperties: falsy,
	}
}

func step() *Schema {
	return &Schema{
		Type:        "object",
		Required:    []string{"handsets"},
		Description: "One stage of a ladder.",
		Rules:       []string{"Needs rings or seconds; seconds overrides rings when both are set."},
		Properties: map[string]*Schema{
			"handsets": {
				Type:      "array",
				MinItems:  one,
				Items:     &Schema{Type: "string"},
				CrossRefs: []string{"handsets.toml [[handsets]].id", "handsets.toml [[groups]].id"},
			},
			"rings":   {Type: "integer", Minimum: one, Description: "Stage duration in ring cycles of roughly six seconds."},
			"seconds": {Type: "integer", Minimum: one, Description: "Exact stage duration, overriding rings."},
		},
		AdditionalProperties: falsy,
	}
}

// ── environment ──────────────────────────────────────────────────────────

// env describes one variable. Every value arrives as a string; x-parsed-as
// records what doorman turns it into, which is what a caller actually needs.
func env(desc, parsedAs string, def any, opts ...func(*Schema)) *Schema {
	s := &Schema{Type: "string", Description: desc, ParsedAs: parsedAs, Default: def}
	for _, o := range opts {
		o(s)
	}
	return s
}

func required(s *Schema) { s.Rules = append(s.Rules, "Required. doorman refuses to start without it.") }

func Env() *Schema {
	props := map[string]*Schema{
		"ARI_BASE_URL":         env("Asterisk ARI base URL. Keep ARI bound to loopback; doorman runs on the same host.", "string", "http://127.0.0.1:8088"),
		"ARI_USERNAME":         env("ARI user, matching asterisk/ari.conf.", "string", nil, required),
		"ARI_PASSWORD":         env("ARI password, matching asterisk/ari.conf.", "string", nil, required),
		"ARI_APP":              env("Stasis application name. Must match Stasis(...) in asterisk/extensions.conf.", "string", "doorman"),
		"ARI_RECONNECT_MIN_MS": env("Minimum event-WebSocket reconnect backoff.", "duration (milliseconds)", 500),
		"ARI_RECONNECT_MAX_MS": env("Maximum event-WebSocket reconnect backoff.", "duration (milliseconds)", 30000),

		"POLICY_PATH":   env("Path to policy.toml.", "path", "./policy.toml"),
		"HANDSETS_PATH": env("Path to handsets.toml.", "path", "./handsets.toml"),
		"POLICY_WATCH":  env("Re-read both config files on change so edits go live without a restart. An invalid file is rejected and the previous policy stays in service.", "boolean (1|true|yes|on)", true),

		"DEFAULT_COUNTRY_CODE": env("Country code assumed when a caller ID arrives without one.", "string", "1"),

		"EXTENSION_LENGTH":       env("Digits in an extension when PIN lengths are mixed.", "positive integer", 6),
		"FIRST_DIGIT_TIMEOUT_MS": env("Time to dial the FIRST digit, measured from the end of the greeting. Generous on purpose: a stranger reading a PIN off a card needs longer than a stopwatch allows.", "duration (milliseconds)", 10000),
		"INTER_DIGIT_TIMEOUT_MS": env("Time allowed between subsequent digits.", "duration (milliseconds)", 3000),
		"RING_TIMEOUT_S":         env("How long the house rings before giving up.", "duration (seconds)", 30),
		"MAX_PIN_ATTEMPTS":       env("Wrong-PIN retries allowed within one call before dismissal.", "positive integer", 2),

		"RATELIMIT_ENABLED":      env("After repeated failures a caller skips the greeting entirely and goes straight to dismissal, leaving nothing to brute-force against.", "boolean", true),
		"RATELIMIT_MAX_FAILURES": env("Failures from one number inside the window before that caller is dismissed without a greeting.", "positive integer", 3),
		"RATELIMIT_WINDOW_MS":    env("Rate-limit window.", "duration (milliseconds)", 3600000),

		"PROMPT_MEDIA_PREFIX": env("Asterisk media prefix, NOT a filesystem path. Files live in /var/lib/asterisk/sounds/<prefix>/. Also how a prompt pack is swapped in.", "string", "call-me-maybe"),

		"LOG_LEVEL":            env("Verbosity.", "enum", "info"),
		"LOG_FORMAT":           env("Structured lines or human-readable.", "enum", "json"),
		"LOG_REDACT_CALLER_ID": env("Log only the last four digits of unknown callers.", "boolean", false),
	}
	props["LOG_LEVEL"].Enum = []any{"trace", "debug", "info", "warn", "error"}
	props["LOG_FORMAT"].Enum = []any{"json", "pretty"}

	for name, s := range props {
		s.EnvVar = name
	}

	return &Schema{
		SchemaURI:   "https://json-schema.org/draft/2020-12/schema",
		ID:          "https://callmemaybe.cc/schema/env.json",
		Title:       "doorman environment variables",
		Type:        "object",
		Description: "Secrets and tuning. Loaded by systemd's EnvironmentFile in production and by `make run` in development. Names and defaults match examples/.env.example exactly.",
		Rules: []string{
			"Every problem is reported at once — doorman does not make you fix env vars one restart at a time.",
			"HANDSET_<NAME>_PASSWORD variables are named by password_env in handsets.toml and read by `doorman render`, not by the daemon.",
			"The VOICEMAIL_*, STT_*, and SMTP_* keys in examples/.env.example are reserved for the unshipped voicemail feature. They are deliberately not read yet, so .env will not churn when it lands.",
		},
		Properties: props,
		Required:   []string{"ARI_USERNAME", "ARI_PASSWORD"},
	}
}
