package policy

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// File is the raw shape of policy.toml. Nothing outside this package should
// touch it — Compile turns it into a Policy with O(1) lookups, and that is
// what doorman consults.
type File struct {
	House      House       `toml:"house"`
	Handsets   []Handset   `toml:"handsets"`
	Groups     []Group     `toml:"groups"`
	Schedules  []Schedule  `toml:"schedules"`
	People     []Person    `toml:"people"`
	Extensions []Extension `toml:"extensions"`
}

// Schedule is a named time window, defined once and referenced by id from
// extensions (afterhours = "school-night"). Turning a schedule off for the
// holidays is one edit here, not a hunt through every extension using it.
type Schedule struct {
	ID string `toml:"id"`
	// Enabled defaults to true; false makes every reference inert without
	// deleting the definition.
	Enabled *bool  `toml:"enabled"`
	Start   string `toml:"start"` // "HH:MM" local time
	End     string `toml:"end"`   // may be earlier than Start (crosses midnight)
	// Days the window STARTS on: SU MO TU WE TH FR SA. A window that crosses
	// midnight belongs to the evening's day. Empty = every day.
	Days []string `toml:"days"`
}

type House struct {
	Handsets       []string `toml:"handsets"`
	CallerIDFormat string   `toml:"caller_id_format"`
	// Voicemail is the mailbox for callers nobody answers. Empty = the old
	// behaviour: "Sorry, nobody's available" and a hangup.
	Voicemail string `toml:"voicemail"`
}

type Handset struct {
	ID       string `toml:"id"`
	Label    string `toml:"label"`
	Endpoint string `toml:"endpoint"`

	// Inventory fields, used by `doorman render` to generate the per-handset
	// Asterisk config so id/endpoint/dialplan can never drift apart.

	// Number is the internal extension (101-199) for handset-to-handset
	// dialling and BLF hints. Zero = not directly dialable.
	Number int `toml:"number"`
	// Page marks the handset a member of the page-all group (dial 500).
	Page bool `toml:"page"`
	// Mailbox lights this phone's MWI lamp from voicemail.conf's
	// [household] section.
	Mailbox string `toml:"mailbox"`
	// PasswordEnv names the .env variable holding this handset's SIP
	// password. The secret itself never appears in this file.
	PasswordEnv string `toml:"password_env"`
}

// Group names a set of handsets. A group id can appear anywhere a handset id
// can — house, extensions, ladder steps — and expands at load time. Groups
// may only contain handsets, not other groups: no cycles, no surprises.
type Group struct {
	ID       string   `toml:"id"`
	Label    string   `toml:"label"`
	Handsets []string `toml:"handsets"`
}

type Person struct {
	Name    string   `toml:"name"`
	Numbers []string `toml:"numbers"`
	Notes   string   `toml:"notes"`
}

type Extension struct {
	PIN   string `toml:"pin"`
	Label string `toml:"label"`
	// Handsets is the simple form: one ring stage using the default timeout.
	// Mutually exclusive with Steps.
	Handsets []string `toml:"handsets"`
	// Steps is the ringer ladder: ring each stage in order, escalating on
	// no-answer. Mutually exclusive with Handsets.
	Steps []Step `toml:"steps"`
	// Voicemail is the mailbox when every stage goes unanswered — and the
	// afterhours destination. Empty = dismiss with "nobody's available".
	Voicemail string `toml:"voicemail"`
	// Afterhours names a [[schedules]] id. (Decoded as `any` so the retired
	// inline-table form can produce a helpful error instead of a decode
	// failure.)
	Afterhours any `toml:"afterhours"`
	// Enabled defaults to true when absent, hence the pointer.
	Enabled *bool `toml:"enabled"`
}

type Step struct {
	Handsets []string `toml:"handsets"`
	// Rings is the stage duration in standard ring cycles (~6s each).
	Rings int `toml:"rings"`
	// Seconds overrides Rings with an exact duration when set.
	Seconds int `toml:"seconds"`
}

var (
	handsetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	endpointPattern  = regexp.MustCompile(`^[A-Za-z0-9]+/\S+$`)
	pinPattern       = regexp.MustCompile(`^\d+$`)
	mailboxPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	clockPattern     = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

var dayIndex = map[string]int{"SU": 0, "MO": 1, "TU": 2, "WE": 3, "TH": 4, "FR": 5, "SA": 6}

// ── Compiled types ───────────────────────────────────────────────────────

// KnownCaller is an allow-list entry keyed by E.164 number.
type KnownCaller struct {
	Name  string
	E164  string
	Notes string
}

// RingStep is one stage of a ring plan with handset ids resolved to
// endpoints. Exactly one of Timeout or Rings is set: Timeout for steps
// written in seconds, Rings for steps written in ring cycles (the session
// multiplies by its configured cycle length, keeping tests fast). Both zero
// means "use the configured default" — flat extensions and the house group.
type RingStep struct {
	Endpoints []string
	Timeout   time.Duration
	Rings     int
}

// RingPlan is what actually happens when a destination is rung: stages in
// order, then the mailbox — or a polite dismissal when Mailbox is empty.
type RingPlan struct {
	Steps   []RingStep
	Mailbox string
}

// Afterhours is a compiled quiet window.
type Afterhours struct {
	StartMin int // minutes since local midnight
	EndMin   int
	Days     [7]bool // window-start days, Sunday = 0
}

// Active reports whether t falls inside the window. Windows that cross
// midnight belong to the day they start: a Sunday 20:30–07:00 window is
// active at 03:00 Monday.
func (a *Afterhours) Active(t time.Time) bool {
	if a == nil {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	day := int(t.Weekday())
	if a.StartMin <= a.EndMin {
		return a.Days[day] && m >= a.StartMin && m < a.EndMin
	}
	// Crosses midnight.
	if a.Days[day] && m >= a.StartMin {
		return true
	}
	prev := (day + 6) % 7
	return a.Days[prev] && m < a.EndMin
}

// ResolvedExtension is an extension compiled into a runnable plan.
type ResolvedExtension struct {
	PIN        string
	Label      string
	Plan       RingPlan
	Afterhours *Afterhours
}

// Policy is a validated policy file compiled into lookups.
type Policy struct {
	allow          map[string]KnownCaller
	exts           map[string]ResolvedExtension
	house          RingPlan
	callerIDFormat string

	// PinLength is the uniform PIN length, or 0 if extensions have mixed
	// lengths. When uniform, the collector can fire the moment the last digit
	// lands instead of waiting out the inter-digit timer.
	PinLength int
}

// FromTOML parses, validates, and compiles a policy. Any structural problem —
// an unknown handset or group reference, a duplicate PIN, a number that
// cannot be normalised, an afterhours window with nowhere to send callers —
// is a hard error here, not a silent misbehaviour at call time.
func FromTOML(data []byte) (*Policy, error) {
	var f File
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	return compile(f)
}

// Load reads and compiles a single-file policy (the legacy layout, with
// handsets and groups inline).
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return FromTOML(data)
}

// LoadSplit compiles the two-file layout: handsets.toml holds the hardware
// inventory ([[handsets]], [[groups]]); policy.toml holds the rules
// ([house], [[schedules]], [[people]], [[extensions]]). The files change at
// different cadences by different people, so a bedtime typo can never
// invalidate the handset inventory.
//
// When handsetsPath is empty or the file does not exist, this degrades to
// the legacy single-file layout, so existing installs keep working.
func LoadSplit(policyPath, handsetsPath string) (*Policy, error) {
	pdata, err := os.ReadFile(policyPath)
	if err != nil {
		return nil, err
	}

	var hdata []byte
	if handsetsPath != "" {
		hdata, err = os.ReadFile(handsetsPath)
		if os.IsNotExist(err) {
			hdata = nil // legacy layout
		} else if err != nil {
			return nil, err
		}
	}
	return fromSplitTOML(pdata, hdata)
}

// SplitLayout reports whether a handsets file is actually in play — used by
// `doorman check` to nudge legacy installs.
func SplitLayout(handsetsPath string) bool {
	if handsetsPath == "" {
		return false
	}
	_, err := os.Stat(handsetsPath)
	return err == nil
}

func fromSplitTOML(policyData, handsetsData []byte) (*Policy, error) {
	var pf File
	if _, err := toml.Decode(string(policyData), &pf); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	if handsetsData == nil {
		return compile(pf)
	}

	var hf File
	if _, err := toml.Decode(string(handsetsData), &hf); err != nil {
		return nil, fmt.Errorf("handsets: %w", err)
	}

	// Each file owns its sections exclusively; anything in the wrong file is
	// an error, not a merge — silent precedence is how two sources of truth
	// drift apart.
	if len(pf.Handsets) > 0 || len(pf.Groups) > 0 {
		return nil, fmt.Errorf("policy: handsets/groups belong in handsets.toml now — move them there")
	}
	if len(hf.Extensions) > 0 || len(hf.People) > 0 || len(hf.Schedules) > 0 || len(hf.House.Handsets) > 0 {
		return nil, fmt.Errorf("handsets: extensions/people/schedules/house belong in policy.toml — move them there")
	}

	pf.Handsets, pf.Groups = hf.Handsets, hf.Groups
	return compile(pf)
}

// LoadHandsets reads just the inventory — what `doorman render` consumes.
func LoadHandsets(path string) ([]Handset, []Group, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var hf File
	if _, err := toml.Decode(string(data), &hf); err != nil {
		return nil, nil, fmt.Errorf("handsets: %w", err)
	}
	return hf.Handsets, hf.Groups, nil
}

func compile(f File) (*Policy, error) {
	p, problems := compileChecked(f)
	if len(problems) > 0 {
		return nil, fmt.Errorf("policy: %s", strings.Join(problems, "; "))
	}
	return p, nil
}

// compileChecked is compile with the problems individually addressable —
// what the LSP consumes to place one diagnostic per issue.
func compileChecked(f File) (*Policy, []string) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	handsets := make(map[string]Handset, len(f.Handsets))
	if len(f.Handsets) == 0 {
		fail("at least one [[handsets]] entry is required")
	}
	for _, h := range f.Handsets {
		if !handsetIDPattern.MatchString(h.ID) {
			fail("handset id %q must be lowercase alphanumeric/dash/underscore", h.ID)
			continue
		}
		if _, dup := handsets[h.ID]; dup {
			fail("duplicate handset id %q", h.ID)
			continue
		}
		if !endpointPattern.MatchString(h.Endpoint) {
			fail("handset %q endpoint %q must look like PJSIP/kitchen", h.ID, h.Endpoint)
		}
		handsets[h.ID] = h
	}

	numbers := make(map[int]string)
	for _, h := range f.Handsets {
		if h.Number == 0 {
			continue
		}
		if h.Number < 100 || h.Number > 199 {
			fail("handset %q number %d must be 100-199 (or 0 for not dialable)", h.ID, h.Number)
		}
		if prev, dup := numbers[h.Number]; dup {
			fail("handsets %q and %q share number %d", prev, h.ID, h.Number)
		}
		numbers[h.Number] = h.ID
		if h.Mailbox != "" && !mailboxPattern.MatchString(h.Mailbox) {
			fail("handset %q mailbox %q must be lowercase alphanumeric/dash/underscore", h.ID, h.Mailbox)
		}
	}

	schedules := make(map[string]*Afterhours, len(f.Schedules))
	for _, sc := range f.Schedules {
		if !handsetIDPattern.MatchString(sc.ID) {
			fail("schedule id %q must be lowercase alphanumeric/dash/underscore", sc.ID)
			continue
		}
		if _, dup := schedules[sc.ID]; dup {
			fail("duplicate schedule id %q", sc.ID)
			continue
		}
		if sc.Enabled != nil && !*sc.Enabled {
			// Present but off: references resolve to nil, i.e. inert. This
			// is the holiday switch.
			schedules[sc.ID] = nil
			continue
		}
		schedules[sc.ID] = compileWindow("schedule "+sc.ID, sc.Start, sc.End, sc.Days, fail)
	}

	groups := make(map[string][]string, len(f.Groups))
	for _, g := range f.Groups {
		if !handsetIDPattern.MatchString(g.ID) {
			fail("group id %q must be lowercase alphanumeric/dash/underscore", g.ID)
			continue
		}
		if _, clash := handsets[g.ID]; clash {
			fail("group id %q collides with a handset id", g.ID)
			continue
		}
		if _, dup := groups[g.ID]; dup {
			fail("duplicate group id %q", g.ID)
			continue
		}
		if len(g.Handsets) == 0 {
			fail("group %q has no handsets", g.ID)
		}
		var endpoints []string
		for _, id := range g.Handsets {
			h, ok := handsets[id]
			if !ok {
				fail("group %q references unknown handset %q (groups may only contain handsets)", g.ID, id)
				continue
			}
			endpoints = append(endpoints, h.Endpoint)
		}
		groups[g.ID] = endpoints
	}

	// expand resolves a mixed list of handset and group ids to endpoints.
	expand := func(where string, ids []string) []string {
		var endpoints []string
		for _, id := range ids {
			if h, ok := handsets[id]; ok {
				endpoints = append(endpoints, h.Endpoint)
				continue
			}
			if eps, ok := groups[id]; ok {
				endpoints = append(endpoints, eps...)
				continue
			}
			fail("%s references unknown handset or group %q", where, id)
		}
		return dedupe(endpoints)
	}

	if len(f.House.Handsets) == 0 {
		fail("[house] must list at least one handset or group")
	}
	house := RingPlan{
		Steps:   []RingStep{{Endpoints: expand("house", f.House.Handsets)}},
		Mailbox: f.House.Voicemail,
	}
	if f.House.Voicemail != "" && !mailboxPattern.MatchString(f.House.Voicemail) {
		fail("house voicemail %q must be lowercase alphanumeric/dash/underscore", f.House.Voicemail)
	}

	format := f.House.CallerIDFormat
	if format == "" {
		format = "{name} <{number}>"
	}

	allow := make(map[string]KnownCaller)
	for _, p := range f.People {
		if p.Name == "" {
			fail("a [[people]] entry is missing a name")
			continue
		}
		if len(p.Numbers) == 0 {
			fail("person %q has no numbers", p.Name)
		}
		for _, raw := range p.Numbers {
			n := NormaliseCallerID(raw, "1")
			if n.Kind != KindE164 {
				fail("number %q for %q is not a valid phone number", raw, p.Name)
				continue
			}
			allow[n.Value] = KnownCaller{Name: p.Name, E164: n.Value, Notes: p.Notes}
		}
	}

	exts := make(map[string]ResolvedExtension)
	lengths := make(map[int]bool)
	for _, e := range f.Extensions {
		if !pinPattern.MatchString(e.PIN) {
			fail("extension %q pin must be digits only", e.Label)
			continue
		}
		if _, dup := exts[e.PIN]; dup {
			fail("duplicate pin %q", e.PIN)
			continue
		}
		if e.Label == "" {
			fail("extension with pin ending %q is missing a label", tail(e.PIN))
		}
		where := fmt.Sprintf("extension %q", e.Label)

		if e.Voicemail != "" && !mailboxPattern.MatchString(e.Voicemail) {
			fail("%s voicemail %q must be lowercase alphanumeric/dash/underscore", where, e.Voicemail)
		}

		var ah *Afterhours
		switch ref := e.Afterhours.(type) {
		case nil:
		case string:
			window, known := schedules[ref]
			if !known {
				fail("%s references unknown schedule %q", where, ref)
			}
			ah = window // nil when the schedule is disabled: inert
			if known && window != nil && e.Voicemail == "" {
				fail("%s has afterhours but no voicemail — an afterhours caller needs somewhere to go", where)
			}
		default:
			fail("%s: afterhours is now a named schedule — define [[schedules]] with id/start/end/days and set afterhours = \"<id>\"", where)
		}

		var plan RingPlan
		switch {
		case len(e.Steps) > 0 && len(e.Handsets) > 0:
			fail("%s has both handsets and steps — use one or the other", where)
		case len(e.Steps) > 0:
			for i, st := range e.Steps {
				stepWhere := fmt.Sprintf("%s step %d", where, i+1)
				if len(st.Handsets) == 0 {
					fail("%s has no handsets", stepWhere)
					continue
				}
				step := RingStep{Endpoints: expand(stepWhere, st.Handsets)}
				switch {
				case st.Seconds > 0:
					step.Timeout = time.Duration(st.Seconds) * time.Second
				case st.Rings > 0:
					step.Rings = st.Rings
				default:
					fail("%s needs rings or seconds", stepWhere)
				}
				plan.Steps = append(plan.Steps, step)
			}
		case len(e.Handsets) > 0:
			plan.Steps = []RingStep{{Endpoints: expand(where, e.Handsets)}}
		default:
			fail("%s has no handsets and no steps", where)
		}
		plan.Mailbox = e.Voicemail

		if e.Enabled != nil && !*e.Enabled {
			continue
		}
		lengths[len(e.PIN)] = true
		exts[e.PIN] = ResolvedExtension{PIN: e.PIN, Label: e.Label, Plan: plan, Afterhours: ah}
	}

	if len(problems) > 0 {
		return nil, problems
	}

	pinLength := 0
	if len(lengths) == 1 {
		for l := range lengths {
			pinLength = l
		}
	}

	return &Policy{
		allow:          allow,
		exts:           exts,
		house:          house,
		callerIDFormat: format,
		PinLength:      pinLength,
	}, nil
}

// LintSplit returns every problem in a policy/handsets pair, one string per
// issue; empty means valid. handsetsData nil means the legacy single-file
// layout. Undecodable TOML comes back as a single problem (the LSP gets a
// precise position for those from the decoder directly).
func LintSplit(policyData, handsetsData []byte) []string {
	var pf File
	if _, err := toml.Decode(string(policyData), &pf); err != nil {
		return []string{err.Error()}
	}
	if handsetsData != nil {
		var hf File
		if _, err := toml.Decode(string(handsetsData), &hf); err != nil {
			return []string{err.Error()}
		}
		var problems []string
		if len(pf.Handsets) > 0 || len(pf.Groups) > 0 {
			problems = append(problems, "handsets/groups belong in handsets.toml now — move them there")
		}
		if len(hf.Extensions) > 0 || len(hf.People) > 0 || len(hf.Schedules) > 0 || len(hf.House.Handsets) > 0 {
			problems = append(problems, "extensions/people/schedules/house belong in policy.toml — move them there")
		}
		if len(problems) > 0 {
			return problems
		}
		pf.Handsets, pf.Groups = hf.Handsets, hf.Groups
	}
	_, problems := compileChecked(pf)
	return problems
}

func compileWindow(where, startStr, endStr string, dayList []string, fail func(string, ...any)) *Afterhours {
	parse := func(field, v string) (int, bool) {
		m := clockPattern.FindStringSubmatch(v)
		if m == nil {
			fail("%s %s %q must be HH:MM", where, field, v)
			return 0, false
		}
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if h > 23 || min > 59 {
			fail("%s %s %q is not a time of day", where, field, v)
			return 0, false
		}
		return h*60 + min, true
	}
	start, ok1 := parse("start", startStr)
	end, ok2 := parse("end", endStr)
	if !ok1 || !ok2 {
		return nil
	}
	var days [7]bool
	if len(dayList) == 0 {
		days = [7]bool{true, true, true, true, true, true, true}
	}
	for _, d := range dayList {
		i, ok := dayIndex[strings.ToUpper(d)]
		if !ok {
			fail("%s day %q must be one of SU MO TU WE TH FR SA", where, d)
			continue
		}
		days[i] = true
	}
	return &Afterhours{StartMin: start, EndMin: end, Days: days}
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// tail keeps error messages useful without printing a whole credential.
func tail(pin string) string {
	if len(pin) <= 2 {
		return pin
	}
	return "…" + pin[len(pin)-2:]
}

// LookupCaller returns the allow-list entry for an E.164 number. ok=false
// means "not on the list" — i.e. send them to the bouncer. An empty string
// (anonymous/unparseable caller ID) is never on the list.
func (p *Policy) LookupCaller(e164 string) (KnownCaller, bool) {
	if e164 == "" {
		return KnownCaller{}, false
	}
	c, ok := p.allow[e164]
	return c, ok
}

// LookupExtension is an exact match against enabled extensions. No prefix
// matching, no fuzz — extensions are credentials.
func (p *Policy) LookupExtension(pin string) (ResolvedExtension, bool) {
	e, ok := p.exts[pin]
	return e, ok
}

// HousePlan is what happens for a welcomed known caller.
func (p *Policy) HousePlan() RingPlan { return p.house }

// HouseEndpoints flattens the house plan's first stage — kept for display.
func (p *Policy) HouseEndpoints() []string {
	if len(p.house.Steps) == 0 {
		return nil
	}
	return p.house.Steps[0].Endpoints
}

// Extensions returns every enabled extension, sorted by label, for display.
func (p *Policy) Extensions() []ResolvedExtension {
	out := make([]ResolvedExtension, 0, len(p.exts))
	for _, e := range p.exts {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func (p *Policy) AllowListCount() int { return len(p.allow) }
func (p *Policy) ExtensionCount() int { return len(p.exts) }

// FormatCallerID renders the caller ID shown on ringing handsets.
func (p *Policy) FormatCallerID(name, number string) string {
	s := strings.ReplaceAll(p.callerIDFormat, "{name}", name)
	return strings.ReplaceAll(s, "{number}", number)
}
