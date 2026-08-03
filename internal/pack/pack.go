// Package pack builds prompt packs: it reads a pack manifest, renders every
// clip through a voice backend, runs the shared audio stage, and writes the
// files Asterisk plays.
//
// Rendering is content-addressed on (text, backend, voice, settings). Editing
// one line of a story re-renders one clip rather than the whole thing, which
// on a metered backend is the difference between a tool you use and one you
// are afraid of.
package pack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"callmemaybe/internal/story"
	"callmemaybe/internal/voice"
)

// Kinds this builder can produce.
const (
	KindVoice = "voice"
	KindStory = "story"
)

// A Manifest is pack.json.
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author,omitempty"`
	License     string `json:"license"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`

	// Voice is either a bare string (a piper model, the legacy form) or an
	// object naming a backend. Both load, so existing packs keep working.
	Voice json.RawMessage `json:"voice,omitempty"`

	// Prompts is the text for kind "voice": name → what is said.
	Prompts map[string]string `json:"prompts,omitempty"`

	// Story names the source file for kind "story". Defaults to story.md.
	Story string `json:"story,omitempty"`
}

// VoiceSpec is the object form of Manifest.Voice.
type VoiceSpec struct {
	Backend  string            `json:"backend"`
	ID       string            `json:"id"`
	Settings map[string]string `json:"settings,omitempty"`
}

// Resolve reads Voice in either form.
func (m Manifest) Resolve() (VoiceSpec, error) {
	if len(m.Voice) == 0 {
		return VoiceSpec{Backend: "piper", ID: "en_GB-alba-medium"}, nil
	}
	var s string
	if err := json.Unmarshal(m.Voice, &s); err == nil {
		// The legacy bare string has always meant a piper model.
		return VoiceSpec{Backend: "piper", ID: s}, nil
	}
	var v VoiceSpec
	if err := json.Unmarshal(m.Voice, &v); err != nil {
		return VoiceSpec{}, fmt.Errorf("pack: voice must be a model name or an object: %w", err)
	}
	if v.Backend == "" {
		v.Backend = "piper"
	}
	return v, nil
}

// An Item is one clip to render.
type Item struct {
	Name  string
	Text  string
	Voice VoiceSpec
}

// hash identifies the rendered output of an item. Changing the text, the
// backend, the voice or any setting produces a different one, which is what
// makes the cache correct rather than merely fast.
func (i Item) hash() string {
	h := sha256.New()
	keys := make([]string, 0, len(i.Voice.Settings))
	for k := range i.Voice.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintf(h, "v1\n%s\n%s\n%s\n", i.Text, i.Voice.Backend, i.Voice.ID)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, i.Voice.Settings[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// A Pack is a manifest plus everything it needs rendering.
type Pack struct {
	Dir      string
	Manifest Manifest
	Story    *story.Story // nil unless kind is story
	Items    []Item
}

// Load reads a pack directory and works out what has to be rendered.
func Load(dir string) (*Pack, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "pack.json"))
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("pack: pack.json is not valid JSON: %w", err)
	}
	if m.Kind == "" {
		m.Kind = KindVoice
	}

	p := &Pack{Dir: dir, Manifest: m}
	spec, err := m.Resolve()
	if err != nil {
		return nil, err
	}

	switch m.Kind {
	case KindVoice:
		p.Items, err = voiceItems(m, spec)
	case KindStory:
		p.Story, p.Items, err = storyItems(dir, m)
	default:
		err = fmt.Errorf("pack: %q packs cannot be built yet (see docs/PACKS.md)", m.Kind)
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// PromptNames is the six-name contract, mirrored from internal/lobby. Held
// here rather than imported so the pack builder does not drag the call state
// machine in; the test in this package fails if the two ever disagree.
var PromptNames = []string{
	"welcome-known", "lobby-greeting", "invalid-extension",
	"good-day", "no-answer", "connecting",
}

func voiceItems(m Manifest, spec VoiceSpec) ([]Item, error) {
	var missing []string
	for _, n := range PromptNames {
		if strings.TrimSpace(m.Prompts[n]) == "" {
			missing = append(missing, n)
		}
	}
	// A pack must supply all six. The contract is the thing that guarantees a
	// pack cannot half-work, and a missing prompt degrades to silence on a
	// call, which is the worst way for this to fail.
	if len(missing) > 0 {
		return nil, fmt.Errorf("pack: missing prompt text for %s — a voice pack must supply all six",
			strings.Join(missing, ", "))
	}
	for n := range m.Prompts {
		if !slicesContains(PromptNames, n) {
			return nil, fmt.Errorf("pack: %q is not one of the six prompt names; "+
				"adding one needs a code change (see docs/PACKS.md)", n)
		}
	}

	items := make([]Item, 0, len(PromptNames))
	for _, n := range PromptNames {
		items = append(items, Item{Name: n, Text: m.Prompts[n], Voice: spec})
	}
	return items, nil
}

func storyItems(dir string, m Manifest) (*story.Story, []Item, error) {
	name := m.Story
	if name == "" {
		name = "story.md"
	}
	src, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, nil, fmt.Errorf("pack: %w", err)
	}

	st, err := story.Parse(src)
	if err != nil {
		return nil, nil, fmt.Errorf("pack: %s:\n%w", name, err)
	}
	problems, _ := st.Validate()
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("pack: %s:\n%w", name, problems)
	}

	var items []Item
	for _, c := range st.Clips() {
		v, ok := st.Voices[c.Speaker]
		if !ok {
			return nil, nil, fmt.Errorf("pack: no voice declared for %s", c.Speaker)
		}
		items = append(items, Item{
			Name:  c.Name,
			Text:  c.Text,
			Voice: VoiceSpec{Backend: v.Backend, ID: v.ID, Settings: v.Settings},
		})
	}
	return st, items, nil
}

// Backends lists the distinct backends this pack needs, so the builder can
// check credentials for all of them before rendering anything.
func (p *Pack) Backends() []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range p.Items {
		if !seen[i.Voice.Backend] {
			seen[i.Voice.Backend] = true
			out = append(out, i.Voice.Backend)
		}
	}
	sort.Strings(out)
	return out
}

// Estimate reports what a full build would cost, per backend.
func (p *Pack) Estimate(cfg func(string) voice.Config) map[string]voice.Cost {
	byBackend := map[string][]string{}
	for _, i := range p.Items {
		byBackend[i.Voice.Backend] = append(byBackend[i.Voice.Backend], i.Text)
	}

	out := map[string]voice.Cost{}
	for name, texts := range byBackend {
		r, err := voice.New(name, cfg(name))
		if err != nil {
			out[name] = voice.Cost{Characters: countChars(texts), Requests: len(texts),
				Note: "cannot check: " + err.Error()}
			continue
		}
		if e, ok := r.(voice.Estimator); ok {
			out[name] = e.Estimate(texts)
		} else {
			out[name] = voice.Cost{Characters: countChars(texts), Requests: len(texts)}
		}
	}
	return out
}

func countChars(texts []string) (n int) {
	for _, t := range texts {
		n += len([]rune(t))
	}
	return n
}

// Report is what a build did.
type Report struct {
	Rendered []string
	Cached   []string
	OutDir   string
}

// Build renders everything not already current and writes the pack.
//
// renderers is consulted per backend so a story using three vendors needs one
// client each rather than one per clip.
func (p *Pack) Build(ctx context.Context, out string, renderers func(string) (voice.Renderer, error)) (Report, error) {
	rep := Report{OutDir: out}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return rep, fmt.Errorf("pack: %w", err)
	}

	cache := map[string]voice.Renderer{}
	for _, item := range p.Items {
		if err := ctx.Err(); err != nil {
			return rep, err
		}

		dest := filepath.Join(out, filepath.FromSlash(item.Name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return rep, fmt.Errorf("pack: %w", err)
		}

		// Content-addressed skip: the sidecar records what produced the audio
		// sitting beside it.
		want := item.hash()
		if current(dest, want) {
			rep.Cached = append(rep.Cached, item.Name)
			continue
		}

		r, ok := cache[item.Voice.Backend]
		if !ok {
			var err error
			if r, err = renderers(item.Voice.Backend); err != nil {
				return rep, err
			}
			cache[item.Voice.Backend] = r
		}

		audio, err := r.Render(ctx, item.Text, voice.Voice{ID: item.Voice.ID, Settings: item.Voice.Settings})
		if err != nil {
			return rep, fmt.Errorf("pack: rendering %q: %w", item.Name, err)
		}
		if audio.Samples() == 0 {
			return rep, fmt.Errorf("pack: %q rendered to silence", item.Name)
		}

		// The shared stage: one normalisation, then both pack rates.
		for rate, a := range voice.Prepare(audio) {
			if err := os.WriteFile(dest+ext(rate), voice.EncodeWAV(a), 0o644); err != nil {
				return rep, fmt.Errorf("pack: %w", err)
			}
		}
		if err := os.WriteFile(dest+".hash", []byte(want), 0o644); err != nil {
			return rep, fmt.Errorf("pack: %w", err)
		}
		rep.Rendered = append(rep.Rendered, item.Name)
	}

	if p.Story != nil {
		if err := p.writeGraph(out); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// ext maps a rate to the suffix Asterisk expects: .wav is narrowband, .wav16
// is the wideband companion it prefers on g722.
func ext(rate int) string {
	if rate == 16000 {
		return ".wav16"
	}
	return ".wav"
}

func current(dest, want string) bool {
	got, err := os.ReadFile(dest + ".hash")
	if err != nil || string(got) != want {
		return false
	}
	// The sidecar is only trustworthy if the audio is actually there.
	for _, rate := range voice.PackRates {
		if _, err := os.Stat(dest + ext(rate)); err != nil {
			return false
		}
	}
	return true
}

// writeGraph emits the compiled story, which is what the interpreter loads at
// call time so it never parses Markdown on a phone call.
func (p *Pack) writeGraph(out string) error {
	type jsonChoice struct {
		Digit  string `json:"digit"`
		Target string `json:"target"`
	}
	type jsonNode struct {
		Clips     []string     `json:"clips"`
		Choices   []jsonChoice `json:"choices,omitempty"`
		Goto      string       `json:"goto,omitempty"`
		End       bool         `json:"end,omitempty"`
		TimeoutMS int64        `json:"timeout_ms"`
		Retries   int          `json:"retries"`
		OnTimeout string       `json:"on_timeout"`
		OnInvalid string       `json:"on_invalid"`
	}

	doc := struct {
		Title string              `json:"title"`
		Start string              `json:"start"`
		Nodes map[string]jsonNode `json:"nodes"`
	}{
		Title: p.Story.Meta.Title,
		Start: story.Slug(p.Story.Meta.Start),
		Nodes: map[string]jsonNode{},
	}

	for _, id := range p.Story.Order {
		n := p.Story.Nodes[id]
		jn := jsonNode{
			Goto: n.Goto, End: n.End,
			TimeoutMS: n.Behaviour.Timeout.Milliseconds(),
			Retries:   n.Behaviour.Retries,
			OnTimeout: n.Behaviour.OnTimeout,
			OnInvalid: n.Behaviour.OnInvalid,
		}
		for _, l := range n.Lines {
			switch {
			case l.Spoken():
				jn.Clips = append(jn.Clips, l.Clip)
			case l.Verb == "sfx":
				jn.Clips = append(jn.Clips, "sfx/"+l.Attrs["name"])
			}
		}
		for _, c := range n.Choices {
			jn.Choices = append(jn.Choices, jsonChoice{Digit: c.Digit, Target: c.Target})
		}
		doc.Nodes[id] = jn
	}

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("pack: %w", err)
	}
	return os.WriteFile(filepath.Join(out, "story.json"), append(b, '\n'), 0o644)
}

// ErrNotAPack is returned when a directory has no manifest.
var ErrNotAPack = errors.New("pack: no pack.json here")

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
