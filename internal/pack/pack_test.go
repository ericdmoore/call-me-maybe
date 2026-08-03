package pack_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/lobby"
	"callmemaybe/internal/pack"
	"callmemaybe/internal/voice"
)

// A renderer that produces a recognisable tone and counts calls, so the cache
// can be observed rather than assumed.
type counting struct {
	n     int
	texts []string
}

func (c *counting) Render(ctx context.Context, text string, v voice.Voice) (voice.Audio, error) {
	c.n++
	c.texts = append(c.texts, text)
	pcm := make([]byte, 2*8000)
	for i := 0; i < len(pcm); i += 2 {
		pcm[i], pcm[i+1] = 0x00, 0x10
	}
	return voice.Audio{PCM: pcm, SampleRate: 22050}, nil
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const voicePack = `{
  "id": "test", "name": "Test", "version": "1.0.0", "license": "CC BY-SA 4.0",
  "kind": "voice",
  "voice": "en_GB-alba-medium",
  "prompts": {
    "welcome-known": "Welcome.",
    "lobby-greeting": "Please dial an extension.",
    "invalid-extension": "That is not an extension.",
    "good-day": "Good day.",
    "no-answer": "Nobody is available.",
    "connecting": "Connecting you now."
  }
}`

// The six names in the pack builder must be the six the binary plays.
// A mismatch would let a pack build cleanly and then be silent on a call.
func TestPromptNamesMatchTheBinary(t *testing.T) {
	want := map[string]bool{}
	for _, p := range []lobby.Prompt{
		lobby.PromptWelcomeKnown, lobby.PromptLobbyGreeting, lobby.PromptInvalidExtension,
		lobby.PromptGoodDay, lobby.PromptNoAnswer, lobby.PromptConnecting,
	} {
		want[string(p)] = true
	}
	if len(pack.PromptNames) != len(want) {
		t.Fatalf("pack has %d prompt names, the binary has %d", len(pack.PromptNames), len(want))
	}
	for _, n := range pack.PromptNames {
		if !want[n] {
			t.Errorf("%q is not a prompt the binary plays", n)
		}
	}
}

func TestBuildsAVoicePack(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)

	p, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 6 {
		t.Fatalf("got %d items, want 6", len(p.Items))
	}

	r := &counting{}
	out := filepath.Join(dir, "build")
	rep, err := p.Build(context.Background(), out, func(string) (voice.Renderer, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rendered) != 6 {
		t.Errorf("rendered %d, want 6", len(rep.Rendered))
	}

	// Both rates, because Asterisk picks whichever transcodes less.
	for _, n := range pack.PromptNames {
		for _, ext := range []string{".wav", ".wav16"} {
			if _, err := os.Stat(filepath.Join(out, n+ext)); err != nil {
				t.Errorf("missing %s%s", n, ext)
			}
		}
	}

	// And the audio is real: correct rate, non-empty.
	b, err := os.ReadFile(filepath.Join(out, "good-day.wav"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := voice.DecodeWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	if a.SampleRate != 8000 || a.Samples() == 0 {
		t.Errorf("built audio is %d samples at %d Hz", a.Samples(), a.SampleRate)
	}
}

// A missing prompt degrades to silence on a call, so it has to fail the build.
func TestAVoicePackMustSupplyAllSix(t *testing.T) {
	dir := t.TempDir()
	var m map[string]any
	if err := json.Unmarshal([]byte(voicePack), &m); err != nil {
		t.Fatal(err)
	}
	delete(m["prompts"].(map[string]any), "good-day")
	b, _ := json.Marshal(m)
	write(t, dir, "pack.json", string(b))

	if _, err := pack.Load(dir); err == nil || !strings.Contains(err.Error(), "good-day") {
		t.Errorf("want a complaint about good-day, got %v", err)
	}
}

func TestAVoicePackCannotInventPromptNames(t *testing.T) {
	dir := t.TempDir()
	var m map[string]any
	if err := json.Unmarshal([]byte(voicePack), &m); err != nil {
		t.Fatal(err)
	}
	m["prompts"].(map[string]any)["hold-music"] = "la la"
	b, _ := json.Marshal(m)
	write(t, dir, "pack.json", string(b))

	if _, err := pack.Load(dir); err == nil || !strings.Contains(err.Error(), "hold-music") {
		t.Errorf("want a complaint about an invented name, got %v", err)
	}
}

// The legacy bare-string voice has always meant a piper model, and existing
// packs must keep loading.
func TestABareVoiceStringStillMeansPiper(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	p, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := p.Manifest.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Backend != "piper" || spec.ID != "en_GB-alba-medium" {
		t.Errorf("spec = %+v", spec)
	}
}

func TestAnObjectVoiceNamesABackend(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", strings.Replace(voicePack,
		`"voice": "en_GB-alba-medium"`,
		`"voice": {"backend":"polly","id":"Amy","settings":{"engine":"neural"}}`, 1))

	p, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := p.Manifest.Resolve()
	if spec.Backend != "polly" || spec.ID != "Amy" || spec.Settings["engine"] != "neural" {
		t.Errorf("spec = %+v", spec)
	}
}

// ── caching ─────────────────────────────────────────────────────────────

// The property that makes this usable on a metered backend: editing one line
// re-renders one clip.
func TestRebuildRendersOnlyWhatChanged(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	out := filepath.Join(dir, "build")

	p, _ := pack.Load(dir)
	r := &counting{}
	mk := func(string) (voice.Renderer, error) { return r, nil }

	if _, err := p.Build(context.Background(), out, mk); err != nil {
		t.Fatal(err)
	}
	if r.n != 6 {
		t.Fatalf("first build rendered %d, want 6", r.n)
	}

	// Nothing changed: nothing re-renders.
	p2, _ := pack.Load(dir)
	rep, err := p2.Build(context.Background(), out, mk)
	if err != nil {
		t.Fatal(err)
	}
	if r.n != 6 {
		t.Errorf("an unchanged rebuild rendered %d more clips", r.n-6)
	}
	if len(rep.Cached) != 6 {
		t.Errorf("cached %d, want 6", len(rep.Cached))
	}

	// One line changes: exactly one clip re-renders.
	write(t, dir, "pack.json", strings.Replace(voicePack, "Good day.", "Good evening.", 1))
	p3, _ := pack.Load(dir)
	rep, err = p3.Build(context.Background(), out, mk)
	if err != nil {
		t.Fatal(err)
	}
	if r.n != 7 {
		t.Errorf("total renders = %d, want 7 — one edit should cost one clip", r.n)
	}
	if len(rep.Rendered) != 1 || rep.Rendered[0] != "good-day" {
		t.Errorf("rendered %v, want just good-day", rep.Rendered)
	}
}

// Changing the voice must invalidate the cache even though the text is the
// same, or a pack rebuilt with a new voice would keep the old audio.
func TestChangingTheVoiceInvalidatesTheCache(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	out := filepath.Join(dir, "build")

	r := &counting{}
	mk := func(string) (voice.Renderer, error) { return r, nil }
	p, _ := pack.Load(dir)
	if _, err := p.Build(context.Background(), out, mk); err != nil {
		t.Fatal(err)
	}

	write(t, dir, "pack.json", strings.Replace(voicePack,
		`"voice": "en_GB-alba-medium"`, `"voice": "en_US-reed-low"`, 1))
	p2, _ := pack.Load(dir)
	rep, err := p2.Build(context.Background(), out, mk)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rendered) != 6 {
		t.Errorf("a new voice re-rendered %d clips, want all 6", len(rep.Rendered))
	}
}

// Deleting the audio but leaving the sidecar must not be reported as cached.
func TestASidecarWithoutAudioIsNotCurrent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	out := filepath.Join(dir, "build")

	r := &counting{}
	mk := func(string) (voice.Renderer, error) { return r, nil }
	p, _ := pack.Load(dir)
	if _, err := p.Build(context.Background(), out, mk); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(out, "good-day.wav")); err != nil {
		t.Fatal(err)
	}

	p2, _ := pack.Load(dir)
	rep, err := p2.Build(context.Background(), out, mk)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rendered) != 1 {
		t.Errorf("rendered %v, want good-day again", rep.Rendered)
	}
}

// ── story packs ─────────────────────────────────────────────────────────

const storyPack = `{
  "id": "threshold", "name": "The Threshold", "version": "1.0.0",
  "license": "CC BY-SA 4.0", "kind": "story"
}`

const storySrc = `---
story: { title: The Threshold, start: entrance }
voices:
  NARRATOR: { backend: piper, id: en_GB-alba-medium }
  DOORMAN:  { backend: polly, id: Amy }
---

## entrance

The lobby smells of rain.

DOORMAN: Good evening.

1. [Bluff](#out)
2. [Leave](#out)

## out

DOORMAN: Mind the step.

<end/>
`

func TestBuildsAStoryPack(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", storyPack)
	write(t, dir, "story.md", storySrc)

	p, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Three utterances plus two choice labels.
	if len(p.Items) != 5 {
		t.Fatalf("got %d items, want 5: %+v", len(p.Items), p.Items)
	}
	// Two backends, so the builder can check both sets of credentials up front.
	if got := p.Backends(); len(got) != 2 {
		t.Errorf("backends = %v, want piper and polly", got)
	}

	r := &counting{}
	out := filepath.Join(dir, "build")
	if _, err := p.Build(context.Background(), out, func(string) (voice.Renderer, error) { return r, nil }); err != nil {
		t.Fatal(err)
	}

	for _, n := range []string{"entrance-01.wav", "entrance-02.wav16", "out-01.wav"} {
		if _, err := os.Stat(filepath.Join(out, n)); err != nil {
			t.Errorf("missing %s", n)
		}
	}
	// Choice clips land in their own directory.
	if _, err := os.Stat(filepath.Join(out, "choice", "entrance-1.wav")); err != nil {
		t.Errorf("choice clip missing: %v", err)
	}

	// The compiled graph is what the interpreter loads, so Markdown is never
	// parsed on a call.
	b, err := os.ReadFile(filepath.Join(out, "story.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Start string `json:"start"`
		Nodes map[string]struct {
			Clips   []string `json:"clips"`
			Choices []struct {
				Digit, Target string
			} `json:"choices"`
			End bool `json:"end"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Start != "entrance" || len(doc.Nodes) != 2 {
		t.Errorf("graph = %+v", doc)
	}
	if !doc.Nodes["out"].End {
		t.Error("the ending was not compiled")
	}
	if len(doc.Nodes["entrance"].Clips) != 2 {
		t.Errorf("entrance clips = %v", doc.Nodes["entrance"].Clips)
	}
}

// A story that does not validate must not produce a half-built pack.
func TestABrokenStoryFailsTheBuild(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", storyPack)
	write(t, dir, "story.md", strings.Replace(storySrc, "(#out)", "(#nowhere)", 1))

	if _, err := pack.Load(dir); err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("want a dangling-anchor failure, got %v", err)
	}
}

func TestUnbuildableKindsSaySo(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", `{"id":"x","name":"X","version":"1.0.0","license":"CC0","kind":"rotation"}`)
	if _, err := pack.Load(dir); err == nil || !strings.Contains(err.Error(), "rotation") {
		t.Errorf("want a clear 'not yet' for kinds without a mechanism, got %v", err)
	}
}

func TestMissingManifestIsClear(t *testing.T) {
	if _, err := pack.Load(t.TempDir()); err == nil {
		t.Error("a directory with no pack.json is an error")
	}
}

// ── the remaining corners ───────────────────────────────────────────────

type failing struct{ err error }

func (f failing) Render(context.Context, string, voice.Voice) (voice.Audio, error) {
	return voice.Audio{}, f.err
}

type silent struct{}

func (silent) Render(context.Context, string, voice.Voice) (voice.Audio, error) {
	return voice.Audio{SampleRate: 16000}, nil
}

func TestARenderFailureNamesTheClip(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	p, _ := pack.Load(dir)

	_, err := p.Build(context.Background(), filepath.Join(dir, "build"),
		func(string) (voice.Renderer, error) { return failing{errors.New("quota exhausted")}, nil })
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("want the vendor's reason, got %v", err)
	}
	if !strings.Contains(err.Error(), "welcome-known") {
		t.Errorf("the error must name which clip failed: %v", err)
	}
}

// A prompt that renders to nothing degrades to silence on a call, which is the
// worst way for this to fail, so it must fail the build instead.
func TestSilenceFailsTheBuild(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	p, _ := pack.Load(dir)

	_, err := p.Build(context.Background(), filepath.Join(dir, "build"),
		func(string) (voice.Renderer, error) { return silent{}, nil })
	if err == nil || !strings.Contains(err.Error(), "silence") {
		t.Errorf("want a silence failure, got %v", err)
	}
}

func TestAnUnavailableBackendStopsTheBuild(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	p, _ := pack.Load(dir)

	_, err := p.Build(context.Background(), filepath.Join(dir, "build"),
		func(string) (voice.Renderer, error) { return nil, errors.New("no credentials") })
	if err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Errorf("got %v", err)
	}
}

func TestCancellationStopsABuild(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", voicePack)
	p, _ := pack.Load(dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Build(ctx, filepath.Join(dir, "build"),
		func(string) (voice.Renderer, error) { return &counting{}, nil }); err == nil {
		t.Error("a cancelled build must stop")
	}
}

func TestEstimateReportsPerBackend(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", storyPack)
	write(t, dir, "story.md", storySrc)
	p, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	est := p.Estimate(func(string) voice.Config {
		return voice.Config{Command: "true", Env: func(string) string { return "" }}
	})
	if len(est) != 2 {
		t.Fatalf("estimate covers %d backends, want 2: %+v", len(est), est)
	}
	if est["piper"].Characters == 0 {
		t.Error("piper's share was not counted")
	}
	// Polly has no credentials here; the estimate must still say something
	// useful rather than vanishing.
	if est["polly"].Characters == 0 {
		t.Error("a backend without credentials should still report its character count")
	}
}

func TestBadManifestIsReported(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", "{ not json")
	if _, err := pack.Load(dir); err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("got %v", err)
	}
}

func TestAMissingStoryFileIsReported(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pack.json", storyPack)
	if _, err := pack.Load(dir); err == nil {
		t.Error("a story pack with no story.md is an error")
	}
}

func TestVoiceCanBeNeitherStringNorObject(t *testing.T) {
	var m pack.Manifest
	if err := json.Unmarshal([]byte(`{"voice": 42}`), &m); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(); err == nil {
		t.Error("a numeric voice should be rejected")
	}
}

func TestNoVoiceDefaultsToPiper(t *testing.T) {
	var m pack.Manifest
	spec, err := m.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Backend != "piper" {
		t.Errorf("backend = %q; the free local one is the default", spec.Backend)
	}
}
