package tmpl

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"callmemaybe/internal/setup"
)

// Answers maps question id to the operator's answer. The concrete type depends
// on the question type: string for handset/mailbox/text, []string for
// handset-group, bool for yes-no, and Window for time-window.
type Answers map[string]any

// Window is the answer to a time-window question.
type Window struct {
	Start string   `toml:"start" json:"start"`
	End   string   `toml:"end" json:"end"`
	Days  []string `toml:"days" json:"days"`
}

// Options carry the context a template cannot know: what handsets exist, and
// which schedule ids are already taken.
type Options struct {
	// HandsetLabel resolves a handset id to its human label, for $x.label.
	HandsetLabel func(id string) string
	// TakenScheduleIDs prevents a template colliding with a schedule the
	// operator already has. Rather than interpolating ids — which would put
	// string building back into the design — a colliding id is suffixed and
	// every reference to it is rewritten.
	TakenScheduleIDs map[string]bool
	// TakenPINs stops a generated PIN colliding with an existing extension.
	TakenPINs map[string]bool
}

// ── output shapes ────────────────────────────────────────────────────────
// Mirrors the subset of policy.File a template may write. Encoding these
// rather than printing text is what makes the output valid by construction:
// quoting, escaping, and array syntax are the encoder's problem.

type outFile struct {
	Schedules  []outSchedule  `toml:"schedules,omitempty"`
	Extensions []outExtension `toml:"extensions,omitempty"`
}

type outSchedule struct {
	ID    string   `toml:"id"`
	Start string   `toml:"start"`
	End   string   `toml:"end"`
	Days  []string `toml:"days,omitempty"`
}

type outExtension struct {
	PIN            string    `toml:"pin"`
	Label          string    `toml:"label"`
	Handsets       []string  `toml:"handsets,omitempty"`
	Voicemail      string    `toml:"voicemail,omitempty"`
	Afterhours     string    `toml:"afterhours,omitempty"`
	AfterhoursRing []string  `toml:"afterhours_ring,omitempty"`
	Steps          []outStep `toml:"steps,omitempty"`
}

type outStep struct {
	Handsets []string `toml:"handsets"`
	// Pointers because exactly one of these is meaningful and omitempty does
	// not drop a zero int here — `seconds = 0` beside `rings = 3` is confusing
	// in a file people hand-edit.
	Rings   *int `toml:"rings,omitempty"`
	Seconds *int `toml:"seconds,omitempty"`
}

// Render resolves answers into policy TOML. The returned string is a fragment
// meant to be appended to policy.toml.
func (t *Template) Render(a Answers, o Options) (string, error) {
	if o.HandsetLabel == nil {
		o.HandsetLabel = func(id string) string { return id }
	}
	if o.TakenScheduleIDs == nil {
		o.TakenScheduleIDs = map[string]bool{}
	}
	if o.TakenPINs == nil {
		o.TakenPINs = map[string]bool{}
	}

	if err := t.checkAnswers(a); err != nil {
		return "", err
	}

	r := &resolver{tpl: t, answers: a, opts: o, scheduleRenames: map[string]string{}}
	var out outFile

	for _, s := range t.Emit.Schedules {
		on, err := r.truthy(s.When)
		if err != nil {
			return "", err
		}
		if !on {
			continue
		}
		id := r.uniqueScheduleID(s.ID)
		start, err := r.str(s.Start)
		if err != nil {
			return "", err
		}
		end, err := r.str(s.End)
		if err != nil {
			return "", err
		}
		days, err := r.strs(s.Days)
		if err != nil {
			return "", err
		}
		out.Schedules = append(out.Schedules, outSchedule{ID: id, Start: start, End: end, Days: days})
	}

	for _, e := range t.Emit.Extensions {
		on, err := r.truthy(e.When)
		if err != nil {
			return "", err
		}
		if !on {
			continue
		}

		pin, err := setup.PIN(6, r.opts.TakenPINs)
		if err != nil {
			return "", err
		}
		label, err := r.str(e.Label)
		if err != nil {
			return "", err
		}
		handsets, err := r.strs(e.Handsets)
		if err != nil {
			return "", err
		}
		vm, err := r.str(e.Voicemail)
		if err != nil {
			return "", err
		}
		ring, err := r.strs(e.AfterhoursRing)
		if err != nil {
			return "", err
		}

		ext := outExtension{
			PIN:            pin,
			Label:          label,
			Handsets:       handsets,
			Voicemail:      vm,
			Afterhours:     r.scheduleRef(e.Afterhours),
			AfterhoursRing: ring,
		}
		for _, st := range e.Steps {
			on, err := r.truthy(st.When)
			if err != nil {
				return "", err
			}
			if !on {
				continue
			}
			hs, err := r.strs(st.Handsets)
			if err != nil {
				return "", err
			}
			step := outStep{Handsets: hs}
			switch {
			case st.Seconds > 0:
				step.Seconds = &st.Seconds
			case st.Rings > 0:
				step.Rings = &st.Rings
			}
			ext.Steps = append(ext.Steps, step)
		}
		out.Extensions = append(out.Extensions, ext)
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\n# ── %s ", t.Meta.Name)
	buf.WriteString(strings.Repeat("─", max(0, 46-len(t.Meta.Name))))
	fmt.Fprintf(&buf, "\n# Generated from template %s@%s. Ordinary TOML from here on:\n",
		t.Meta.ID, t.Meta.Version)
	buf.WriteString("# edit it, move it, or delete it like anything else in this file.\n")
	if t.Meta.Description != "" {
		fmt.Fprintf(&buf, "#\n# %s\n", t.Meta.Description)
	}
	buf.WriteString("\n")

	if err := toml.NewEncoder(&buf).Encode(out); err != nil {
		return "", fmt.Errorf("encoding the generated policy: %w", err)
	}
	return buf.String(), nil
}

// PINs is every PIN this render generated, so the caller can show them once.
func (t *Template) PINs(rendered string) map[string]string {
	out := map[string]string{}
	var f outFile
	if _, err := toml.Decode(rendered, &f); err != nil {
		return out
	}
	for _, e := range f.Extensions {
		out[e.Label] = e.PIN
	}
	return out
}

// ── resolution ───────────────────────────────────────────────────────────

type resolver struct {
	tpl             *Template
	answers         Answers
	opts            Options
	scheduleRenames map[string]string
}

func (r *resolver) uniqueScheduleID(id string) string {
	final := id
	for i := 2; r.opts.TakenScheduleIDs[final]; i++ {
		final = fmt.Sprintf("%s-%d", id, i)
	}
	r.opts.TakenScheduleIDs[final] = true
	if final != id {
		r.scheduleRenames[id] = final
	}
	return final
}

func (r *resolver) scheduleRef(id string) string {
	if renamed, ok := r.scheduleRenames[id]; ok {
		return renamed
	}
	return id
}

// str resolves a single value to a string.
func (r *resolver) str(v string) (string, error) {
	if !strings.HasPrefix(v, "$") {
		return v, nil
	}
	got, err := r.lookup(strings.TrimPrefix(v, "$"))
	if err != nil {
		return "", err
	}
	switch x := got.(type) {
	case string:
		return x, nil
	case bool:
		return fmt.Sprint(x), nil
	default:
		return "", fmt.Errorf("%s resolved to %T, which cannot be used as a string", v, got)
	}
}

// strs resolves a value that may be a single reference, a literal list, or a
// list containing references.
func (r *resolver) strs(v any) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		// A conditional value: { when = "$answer", value = ... }. This is what
		// lets one field depend on an answer without duplicating the whole
		// block, and it stays data — there is still no expression language,
		// just a guard and a value.
		guard, _ := x["when"].(string)
		on, err := r.truthy(guard)
		if err != nil {
			return nil, err
		}
		if !on {
			return nil, nil
		}
		return r.strs(x["value"])
	case string:
		if !strings.HasPrefix(x, "$") {
			return []string{x}, nil
		}
		got, err := r.lookup(strings.TrimPrefix(x, "$"))
		if err != nil {
			return nil, err
		}
		return toStrings(got)
	case []string:
		return r.expandList(anySlice(x))
	case []any:
		return r.expandList(x)
	}
	return nil, fmt.Errorf("cannot use %T as a list of handsets", v)
}

func (r *resolver) expandList(items []any) ([]string, error) {
	var out []string
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("list entries must be strings, got %T", it)
		}
		got, err := r.strs(s)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// truthy evaluates a `when` guard. Empty means always.
func (r *resolver) truthy(when string) (bool, error) {
	if when == "" {
		return true, nil
	}
	got, err := r.lookup(strings.TrimPrefix(when, "$"))
	if err != nil {
		return false, err
	}
	switch x := got.(type) {
	case bool:
		return x, nil
	case string:
		return x != "", nil
	case []string:
		return len(x) > 0, nil
	}
	return got != nil, nil
}

// lookup resolves "room", "room.label", "bedtime.start", "generate.pin".
func (r *resolver) lookup(ref string) (any, error) {
	head, tail, _ := strings.Cut(ref, ".")

	if head == "generate" {
		return nil, fmt.Errorf("$generate.%s is resolved by the renderer, not by lookup", tail)
	}

	v, ok := r.answers[head]
	if !ok {
		return nil, fmt.Errorf("no answer for $%s", head)
	}
	if tail == "" {
		return v, nil
	}

	switch x := v.(type) {
	case Window:
		switch tail {
		case "start":
			return x.Start, nil
		case "end":
			return x.End, nil
		case "days":
			return x.Days, nil
		}
		return nil, fmt.Errorf("$%s.%s: a time-window has start, end, and days", head, tail)
	case string:
		if tail == "label" {
			return r.opts.HandsetLabel(x), nil
		}
		return nil, fmt.Errorf("$%s.%s: only .label is available on a handset", head, tail)
	}
	return nil, fmt.Errorf("$%s.%s is not available", head, tail)
}

// checkAnswers verifies every question was answered with the right shape
// before anything is generated.
func (t *Template) checkAnswers(a Answers) error {
	var missing, wrong []string
	for _, q := range t.Questions {
		v, ok := a[q.ID]
		if !ok || v == nil {
			missing = append(missing, q.ID)
			continue
		}
		okType := false
		switch q.Type {
		case TypeHandset, TypeMailbox, TypeText:
			_, okType = v.(string)
		case TypeHandsetGroup:
			switch v.(type) {
			case []string, string:
				okType = true
			}
		case TypeTimeWindow:
			_, okType = v.(Window)
		case TypeYesNo:
			_, okType = v.(bool)
		}
		if !okType {
			wrong = append(wrong, fmt.Sprintf("%s (wanted %s, got %T)", q.ID, q.Type, v))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("unanswered: %s", strings.Join(missing, ", "))
	}
	if len(wrong) > 0 {
		return fmt.Errorf("wrong answer type for: %s", strings.Join(wrong, "; "))
	}
	return nil
}

func toStrings(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []string:
		return x, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			s, ok := it.(string)
			if !ok {
				return nil, fmt.Errorf("expected strings, got %T", it)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected a string or list, got %T", v)
}

func anySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
