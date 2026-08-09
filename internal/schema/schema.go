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
	"strings"
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
var Names = []string{"policy", "handsets", "trunks", "env", "template"}

// Get returns one schema by name.
func Get(name string) (*Schema, error) {
	switch name {
	case "policy":
		return Policy(), nil
	case "handsets":
		return Handsets(), nil
	case "trunks":
		return Trunks(), nil
	case "env":
		return Env(), nil
	case "template":
		return TemplateFormat(), nil
	}
	return nil, fmt.Errorf("unknown schema %q (want one of %s)", name, strings.Join(Names, ", "))
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
			"trunks.toml":   Trunks(),
			"env":           Env(),
			"template":      TemplateFormat(),
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

// ── trunks.toml ──────────────────────────────────────────────────────────

func Trunks() *Schema {
	return &Schema{
		SchemaURI:   "https://json-schema.org/draft/2020-12/schema",
		ID:          "https://callmemaybe.cc/schema/trunks.json",
		Title:       "trunks.toml — the provider inventory",
		Type:        "object",
		Description: "The providers this box registers with. Optional, and its absence is the compatibility gate: with no trunks.toml `doorman render` generates only the handset config and a hand-written pjsip.conf keeps working exactly as it does today. Adding it makes a second provider a TOML block rather than a page of copied PJSIP. Changes when you buy a number somewhere new.",
		Rules: []string{
			"`doorman render` generates pjsip_trunks.conf and extensions_trunks.conf from this file. Both are OUTPUTS: never hand-edit them, and never commit the PJSIP one — it holds real registration passwords.",
			"Every generated registration carries line=yes and endpoint=<id>. That pair is what binds inbound calls arriving on a registration to its endpoint, which is why no identify block and no provider IP allow-list is needed. Get it wrong and inbound calls hit the anonymous endpoint and vanish with no error anywhere.",
			"One inbound dialplan context per trunk, because each registration binds to its own endpoint with its own context=. Inside it, one route per DID, generated from [line] number and [line] trunk across every policy file.",
			"A trunk id may not collide with a handset id: both become a PJSIP endpoint named after the id.",
			"Secrets are named here, never written here. password_env holds the NAME of a .env variable; the loader refuses anything that is not shaped like one, so a pasted password fails loudly instead of being committed.",
			"Deleting trunks.toml is the whole rollback. There is no migration and no stored state.",
		},
		Properties: map[string]*Schema{
			"emergency_trunk": {
				Type: "string",
				Description: "Which trunk carries 911. Optional: unset, emergency calls leave by the primary line's trunk — the [line] trunk in plain policy.toml, the same file that is already the default for everything unqualified. " +
					"`doorman check` and every startup print which trunk it is and whether that was chosen or inferred, so the default is never a surprise and there is no state where somebody forgot.",
				CrossRefs: []string{"trunks.toml [[trunks]].id", "policy.toml [line] trunk"},
				Rules: []string{
					"It lives here rather than in a policy file on purpose. Whoever grabs the nearest handset has no idea which line they are on, and E911 is registered per DID against a street address, so the trunk carrying 911 must be the one whose address is filed — not the one belonging to whichever number a caller happens to be on.",
					"Not \"whichever trunk sorts first\" and not \"the first declared\": a default derived from file order moves silently when a block is added at the top of a file, and this is the most safety-critical route in the system.",
					"Outbound routing by trunk is not implemented yet. This key settles where 911 will go and is what `doorman check` reports; today an emergency call still leaves by the hand-written dialplan.",
				},
			},
			"trunks": {
				Type:        "array",
				Description: "Every provider registration this box opens.",
				MinItems:    one,
				Items:       trunkItem(),
			},
		},
		Required:             []string{"trunks"},
		AdditionalProperties: falsy,
	}
}

func trunkItem() *Schema {
	return &Schema{
		Type:     "object",
		Required: []string{"id", "host", "username", "password_env"},
		Properties: map[string]*Schema{
			"id": {
				Type:    "string",
				Pattern: "^[a-z0-9][a-z0-9_-]*$",
				Description: "Names the generated PJSIP endpoint and the objects beside it — <id>_auth, <id>_reg, <id>_aor. Referenced from policy.toml as [line] trunk. " +
					"It is also what the outbound dial string in the dialplan already says (PJSIP/1${EXTEN}@voipms), so keeping the id you already use is what lets a hand-written trunk become a generated one with no dialplan edit.",
				Rules: []string{
					"Must be unique, and may not collide with a handset id.",
					"May not end in _auth, _reg or _aor — render derives those from the id and the blocks would collide.",
				},
				CrossRefs: []string{"policy.toml [line] trunk", "asterisk/extensions.conf outbound Dial(PJSIP/...@<id>)"},
			},
			"provider": {
				Type:        "string",
				Description: "The company, for display only — \"voip.ms\", \"telnyx\". Nothing routes on it; it is what makes a table of short ids readable in `doorman check`.",
			},
			"host": {
				Type:        "string",
				Pattern:     `^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:\d{1,5})?$`,
				Description: "The POP or SIP host this box registers with, optionally with a port. A hostname, not a URL and not a sip: URI.",
				Rules:       []string{"Use the same host everywhere. Mixing POPs between the registration and the AOR causes flapping that looks like a network problem."},
			},
			"username": {
				Type:        "string",
				Description: "The provider sub-account this box registers as.",
				Rules:       []string{"Never the main account login. A compromised Pi should cost a sub-account, not a balance and a DID."},
			},
			"password_env": {
				Type:        "string",
				Pattern:     "^[A-Z][A-Z0-9_]*$",
				Description: "Name of the .env variable holding this trunk's registration password. The secret itself never appears in this file. `doorman render` substitutes it and fails if unset.",
				Rules: []string{
					"Names a variable; never put the password here. The pattern is enforced so that a pasted password fails the load rather than reaching a commit.",
					"Convention: TRUNK_<ID>_PASSWORD, matching handsets.toml's HANDSET_<NAME>_PASSWORD.",
				},
				CrossRefs: []string{"the environment, and examples/.env.example"},
			},
			"context": {
				Type:        "string",
				Pattern:     "^[a-zA-Z0-9][a-zA-Z0-9_-]*$",
				Default:     "from-<id>",
				Description: "Inbound dialplan context for calls arriving on this trunk, generated into extensions_trunks.conf. One per trunk is what makes several providers work: each registration binds to its own endpoint with its own context, so one [inbound-trunk] becomes one context per provider.",
				Rules:       []string{"Two trunks may not share a context — inbound calls could not be told apart."},
			},
			"codecs": {
				Type:        "array",
				MinItems:    one,
				Items:       &Schema{Type: "string", Pattern: "^[a-z0-9]+$"},
				Default:     []any{"ulaw", "g722"},
				Description: "The endpoint's allow= list, in order. ulaw and g722 need no transcoding on a Pi; g729 costs money and would transcode.",
			},
			"from_user": {
				Type:        "string",
				Default:     "the username",
				Description: "What this box puts in From, and the user part of the registration's client URI and contact. Defaults to username, which is what a registration trunk almost always wants; providers that authenticate as one name and expect another in From are why this exists.",
			},
			"from_domain": {
				Type:        "string",
				Default:     "the host, without any port",
				Description: "Domain part of From. Defaults to host.",
			},
			"expiration": {
				Type:        "integer",
				Minimum:     ptr(30),
				Maximum:     ptr(86400),
				Default:     300,
				Description: "Registration lifetime in seconds. Some providers throttle short ones, which is the only reason it is a knob.",
			},
			"e911": {
				Type:        "boolean",
				Description: "Whether a street address is registered against this trunk's DIDs. Declared rather than inferred: it is the one fact the emergency-trunk decision turns on and the one doorman cannot discover. Absent means unknown, and `doorman check` says \"unknown\" rather than guessing either way.",
				Rules: []string{
					"Not every provider offers E911 in every area. A provider without it means a phone that cannot call for help, which is a reason to reject the provider rather than a line item to skip.",
					"This is a supplementary phone on a consumer internet connection. It stops working in a power cut, and nobody should make it a household's only route to emergency services.",
				},
			},
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
			"One file per phone number. A box answering several numbers puts each extra line in policy.<line>.toml beside policy.toml, and the dialplan names the line with Stasis(app,line,<name>). Bare policy.toml is the default line and serves every call that names none. The schema is the same for all of them; handsets.toml is shared.",
			"Extensions are credentials. Treat a PIN like a password and prefer `doorman rotate` over choosing one.",
			"Allow-listed numbers may be written in any format; they normalise to E.164 at load time. An unparseable number is a load error, not a silent never-match.",
		},
		Properties: map[string]*Schema{
			"line":       line(),
			"house":      house(),
			"people":     {Type: "array", Description: "The allow-list. These callers hear the welcome prompt and ring the house.", Items: person()},
			"schedules":  {Type: "array", Description: "Named time windows, defined once and referenced by id from extensions.", Items: schedule()},
			"extensions": {Type: "array", Description: "What an unknown caller may dial in the lobby.", Items: extension()},
		},
		AdditionalProperties: falsy,
	}
}

func line() *Schema {
	return &Schema{
		Type: "object",
		Description: "What this number is, and how it treats a caller who dials nothing. " +
			"Every key is optional and a file with no [line] section behaves exactly as it did before lines existed, " +
			"so this is what you add to make one number a curt doorman and another a courteous concierge on the same binary.",
		Rules: []string{
			"An allow-listed caller is never dismissed by on_no_input: they reach the house whatever it says.",
			"Nothing routes on number. The dialplan already named the line — this is identity, for `doorman check` and for the handset display.",
			"outbound_cid and outbound_handsets are the outbound half. A handset no line claims presents policy.toml's outbound_cid — the primary line, which is also 911's route, so there is one answer to \"which line am I on when I have not said\".",
		},
		Properties: map[string]*Schema{
			"label": {
				Type: "string",
				Description: "Human name for this number — \"Mertaugh Enterprises\". Shown by `doorman check`, " +
					"and displayed on the ringing handset when a caller reaches the house without dialling an extension, " +
					"so whoever picks up knows which line to answer as.",
			},
			"number": {
				Type:        "string",
				Description: "The DID this line answers. Any format; normalised to E.164 at load. Probe a value with `doorman e164 <number>`.",
				Rules:       []string{"An unparseable number fails the load rather than being kept as written."},
			},
			"prompts": {
				Type:    "string",
				Pattern: `^[A-Za-z0-9][A-Za-z0-9_-]*(/[A-Za-z0-9][A-Za-z0-9_-]*)*$`,
				Description: "Prompt pack for this line, overriding PROMPT_MEDIA_PREFIX. An Asterisk media prefix under " +
					"/var/lib/asterisk/sounds/, not a filesystem path. Two lines with different packs are the same engine with different voices.",
				CrossRefs: []string{"/var/lib/asterisk/sounds/<prefix>/", "PROMPT_MEDIA_PREFIX in the environment"},
			},
			"trunk": {
				Type: "string",
				Description: "The provider this line's number lives at — an id from trunks.toml. Optional, and absent is the whole of a single-provider install: one registration, one hand-written or generated trunk, and the dialplan decides. " +
					"Inbound it is what puts this line's DID route in the right provider's context when `doorman render` generates them. Outbound it is what will choose the path out of the building once routing by trunk lands.",
				CrossRefs: []string{"trunks.toml [[trunks]].id"},
				Rules: []string{
					"Validated as a cross-file reference by `doorman check` and `doorman render`, exactly as a handset id is. The daemon does not validate it, because nothing on the call path routes on a trunk yet and refusing to load a policy over a reference nothing reads would be the wrong trade.",
					"plain policy.toml's trunk is the one 911 inherits when trunks.toml sets no emergency_trunk.",
					"A route is generated only for a line that has BOTH trunk and number. A line with a trunk and no number gets a context but no DID route, and calls for it fall through to the default line.",
				},
			},
			"outbound_cid": {
				Type: "string",
				Description: "What a call placed as this line presents to the person being called. Any format; normalised to E.164 at load. " +
					"Absent means whatever the trunk sends, which is what every outbound call did before this key existed. " +
					"It reaches a call two ways: `doorman render` writes it into the generated PJSIP config for the handsets this line claims, " +
					"and the *4 console sets it per call for a line chosen at the keypad.",
				Rules: []string{
					"An unparseable number fails the load rather than being kept as written.",
					"policy.toml's value is the house default: any handset no line claims presents it.",
					"Changing it takes effect on the *4 console at the next reload, and on the plain dialplan path at the next `doorman render`.",
					"911 never presents it. Emergency calls leave with the trunk's own caller ID, because E911 is registered per DID against a street address.",
				},
				CrossRefs: []string{"asterisk/extensions.conf [internal] OUTBOUND_CID", "the DID this line answers, at your provider"},
			},
			"outbound_handsets": {
				Type:     "array",
				MinItems: one,
				Items:    &Schema{Type: "string"},
				Description: "Handsets that call as this line without being asked: pick one of these up, dial a number, and the callee sees this line's outbound_cid. " +
					"The claim is written here rather than in handsets.toml because that file is one shared inventory and cannot hold something true of only one line.",
				CrossRefs: []string{"handsets.toml [[handsets]].id", "handsets.toml [[groups]].id"},
				Rules: []string{
					"Requires outbound_cid — without one these handsets would present the trunk default, which is what they do already.",
					"A handset may be claimed by one line only. `doorman check` and `doorman render` refuse a handset claimed twice; the daemon warns and the primary line wins.",
					"A handset no line claims presents policy.toml's outbound_cid.",
					"Takes effect at the next `doorman render` — a phone that just picks up and dials never reaches doorman, which is why outbound calling survives doorman being down.",
				},
			},
			"on_no_input": {
				Type:    "string",
				Enum:    []any{"dismiss", "ring-house", "voicemail"},
				Default: "dismiss",
				Description: "What happens to a caller who reaches the end of the dial window having entered nothing. " +
					"dismiss is the historical behaviour: the parting prompt, then a hangup. " +
					"voicemail takes a message, which is what a business line wants — never drop a lead. " +
					"ring-house puts them through to the house ring group.",
				Rules: []string{
					"voicemail requires [house] voicemail — the caller needs a mailbox to land in.",
					"ring-house lets anyone patient enough to say nothing reach the house, which makes the allow-list a shortcut rather than a gate. Choose it deliberately.",
					"This governs an empty dial window only. A caller who exhausts their PIN attempts is still dismissed.",
				},
			},
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
				Description: "Digits only, minimum 4. Choose one you will remember — a number nobody can recite is a number nobody gives out. Guessable ones are refused at load: all-same digits, runs (including in twos), repeated blocks, palindromes, the handful everyone tries, and — at six digits and up — anything one digit away from all-same or from a run, since a guessing list is the patterns plus one typo. That is 0.31% of the six-digit space; everything else, dates included, is allowed. `doorman rotate` generates one when you would rather not choose.",
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
				Description: "Id of a [[schedules]] entry. During the window the line does not ring normally: it either redirects to afterhours_ring, or goes to voicemail.",
				CrossRefs:   []string{"policy.toml [[schedules]].id"},
				Rules:       []string{"The inline-table form is retired; use a named schedule id.", "Requires either voicemail or afterhours_ring, so the caller has somewhere to go."},
			},
			"afterhours_ring": {
				Type:        "array",
				MinItems:    one,
				Items:       &Schema{Type: "string"},
				Description: "Ring these instead of taking a message during the afterhours window. Without it, quiet hours can only mean \"voicemail\", which cannot express homework hours, a rotating night shift, or forwarding to a babysitter. If nobody answers the redirect, the caller still lands in voicemail.",
				CrossRefs:   []string{"handsets.toml [[handsets]].id", "handsets.toml [[groups]].id"},
				Rules:       []string{"Requires afterhours — there must be a window for it to apply to."},
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
		"ARI_ALLOW_REMOTE":     env("Permit a non-loopback ARI host. doorman refuses to start otherwise, because ARI is full call control behind a plaintext password. The one documented exception is running off-box over a tailnet — never the LAN.", "boolean", false),

		"POLICY_PATH":   env("Path to policy.toml.", "path", "./policy.toml"),
		"HANDSETS_PATH": env("Path to handsets.toml.", "path", "./handsets.toml"),
		"TRUNKS_PATH":   env("Path to trunks.toml, the provider inventory. The file is optional and absent is the default: with no trunks.toml `doorman render` generates only the handset config and a hand-written pjsip.conf keeps working. The daemon reads it for one thing only — to say at startup which trunk carries 911 and whether that was chosen or inferred. Nothing on the call path routes on a trunk yet.", "path", "./trunks.toml"),
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
		"MAX_CONCURRENT_CALLS":   env("Ceiling on simultaneous inbound calls. The rate limit counts failures per caller and cannot see a flood from many numbers, which would keep every handset ringing. Excess calls are hung up without ringing anything. 0 disables.", "positive integer", 5),

		"PROMPT_MEDIA_PREFIX": env("Asterisk media prefix, NOT a filesystem path. Files live in /var/lib/asterisk/sounds/<prefix>/. Also how a prompt pack is swapped in.", "string", "call-me-maybe"),

		"LOG_LEVEL":            env("Verbosity.", "enum", "info"),
		"LOG_FORMAT":           env("Structured lines or human-readable.", "enum", "json"),
		"LOG_REDACT_CALLER_ID": env("Redact caller IDs in logs, keeping only a fragment. On by default: the caller attribute rides on every line a call produces, and journals get shipped and backed up. Setting this false is a deliberate debugging opt-out — see CLAUDE.md invariant 1.", "boolean", true),
		"CALL_LOG_PATH":        env("Enables the per-call record log, one JSON line per completed call and per call placed through the *4 outbound console. Empty (the default) means no call log. Unlike the operational log this file holds full caller IDs — which is what makes \"who called while I was out\" answerable — so it is created 0600 and belongs on the box. Read it with `doorman calls`, which redacts by default. One file for every line, with `line` on the record, so a whole day still reads in order; `line` is absent on the default line and `direction` is absent on an inbound call, so a box answering one number writes exactly what it always did. Written off the call path: a full buffer drops records and counts them rather than delaying a call. Nothing on the call path ever reads it back; it is an output, not state.", "string", ""),
		"CALL_LOG_MAX_BYTES":   env("Rotate the call log at this size, keeping one previous generation. Twenty calls a day is roughly 2 MB a year, so this cap is for the case that is not twenty calls a day.", "integer", 33554432),

		"WEBHOOK_URL":              env("Enables the event webhook: doorman POSTs one JSON object when the house starts ringing and one when an inbound call ends, so Home Assistant can announce \"call from Grandma\" on a speaker or flash a light. Empty (the default) means no webhook. The payload is an event — what happened, who, on which line, and how it ended — and never names a speaker, a media_player entity, or any routing: Home Assistant already models the house's devices and decides which of them hear it. `line` is what an automation routes on when the box answers several numbers, and it is absent on the default line. Posted off the call path through a buffered channel, so a dead or slow endpoint can never delay a call; a full buffer drops events and counts them. Treat the value as a secret — a Home Assistant webhook id is the whole credential — which is why doorman only ever logs its host.", "url", ""),
		"WEBHOOK_TOKEN":            env("Sent as `Authorization: Bearer` on every webhook post. Home Assistant's /api/webhook/<id> endpoints need nothing here; its REST API and most other receivers do. Never logged.", "string", ""),
		"WEBHOOK_TIMEOUT_MS":       env("Bounds one delivery attempt. Short on purpose: a notification that arrives late has already missed the ring it was describing. It also bounds shutdown — past this grace period a dead endpoint's backlog is abandoned rather than holding the process open.", "duration (milliseconds)", 3000),
		"WEBHOOK_REDACT_CALLER_ID": env("Redact the caller ID in the webhook payload, keeping only a fragment. On by default — the opposite of the call log, deliberately: the call log is a 0600 file that stays on the box, while this payload is handed to another program over the network. Set it false when the receiver needs the full number, for example to announce an unknown caller. Entered digits and PINs are never sent at any setting; a payload says whether a PIN was valid, never what was typed.", "boolean", true),
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

// ── the template format ──────────────────────────────────────────────────

// Template describes the policy-template format itself, so a template author
// can validate against something published rather than reverse-engineering the
// Go structs. Same reasoning as the config schemas: the format is an interface,
// and an interface nobody can read is not one.
func TemplateFormat() *Schema {
	return &Schema{
		SchemaURI:   "https://json-schema.org/draft/2020-12/schema",
		ID:          "https://callmemaybe.cc/schema/template.json",
		Title:       "A policy template",
		Type:        "object",
		Description: "Declares questions and the policy structures to emit from the answers. A template is data, not text: answers are referenced rather than interpolated, so the output is serialised and cannot be malformed or injected into.",
		Rules: []string{
			"Templates may emit extensions and schedules only. One that could write [[people]] could add its author to the allow-list.",
			"PINs must be \"$generate.pin\" — a template may not hardcode a credential.",
			"Every $reference must name a declared question.",
			"A value may be conditional: { when = \"$answer\", value = ... }.",
			"Accepted as TOML or JSON, chosen by content rather than file extension.",
		},
		Required:             []string{"template"},
		AdditionalProperties: falsy,
		Properties: map[string]*Schema{
			"template": {
				Type:                 "object",
				Description:          "Identity and provenance.",
				Required:             []string{"id", "name", "version"},
				AdditionalProperties: falsy,
				Properties: map[string]*Schema{
					"id":          {Type: "string", Description: "Stable identifier, used as the argument to `doorman template apply`."},
					"name":        {Type: "string", Description: "Human name, shown by `doorman template list`."},
					"version":     {Type: "string", Description: "Author's version of this template."},
					"description": {Type: "string", Description: "One sentence on what it sets up."},
					"author":      {Type: "string"},
				},
			},
			"questions": {
				Type:        "array",
				Description: "What the operator is asked, in order.",
				Items: &Schema{
					Type:                 "object",
					Required:             []string{"id", "prompt", "type"},
					AdditionalProperties: falsy,
					Properties: map[string]*Schema{
						"id":     {Type: "string", Description: "Referenced from emit blocks as $id."},
						"prompt": {Type: "string", Description: "Asked verbatim, so write it as a question."},
						"type": {
							Type:        "string",
							Enum:        []any{"handset", "handset-group", "time-window", "mailbox", "yes-no", "text"},
							Description: "Drives both the prompt style and validation. handset and handset-group offer a picker from handsets.toml.",
						},
						"default": {Description: "Offered when the operator presses enter. For time-window, a table of start, end, and days."},
						"choices": {Type: "array", Items: &Schema{Type: "string"}},
					},
				},
			},
			"emit": {
				Type:                 "object",
				Description:          "The policy structures produced. This is an allow-list; adding to it is a security decision.",
				AdditionalProperties: falsy,
				Properties: map[string]*Schema{
					"schedules": {
						Type:  "array",
						Items: &Schema{Type: "object", Description: "Same shape as policy.toml [[schedules]]. A colliding id is suffixed and references are rewritten."},
					},
					"extensions": {
						Type:  "array",
						Items: &Schema{Type: "object", Description: "Same shape as policy.toml [[extensions]], except pin must be \"$generate.pin\"."},
					},
				},
			},
		},
	}
}
