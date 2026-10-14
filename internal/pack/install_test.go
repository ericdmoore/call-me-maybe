package pack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/voice"
)

type testRenderer struct {
	calls  int
	failAt int
	cancel context.CancelFunc
}

func (r *testRenderer) Render(ctx context.Context, text string, v voice.Voice) (voice.Audio, error) {
	r.calls++
	if r.calls == r.failAt {
		return voice.Audio{}, errors.New("provider returned private-test-credential")
	}
	if r.cancel != nil {
		r.cancel()
	}
	return testAudio(8000), nil
}
func testAudio(rate int) voice.Audio {
	pcm := make([]byte, rate/10*2)
	for i := 0; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], 4096)
	}
	return voice.Audio{PCM: pcm, SampleRate: rate}
}
func put(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func testSource(t *testing.T) *Pack {
	t.Helper()
	dir := t.TempDir()
	m := Manifest{ID: "test-pack", Name: "Test", Version: "1.0.0", License: "CC0", Kind: KindVoice, Prompts: map[string]string{}}
	for _, n := range PromptNames {
		m.Prompts[n] = "Text for " + n
	}
	saveManifest(t, dir, m)
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func saveManifest(t *testing.T, dir string, m Manifest) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "pack.json"), b)
}
func artifact(t *testing.T, p *Pack, audio string) *Artifact {
	t.Helper()
	f := filepath.Join(t.TempDir(), "test.cmmpack")
	if err := p.WriteArchive(f, audio); err != nil {
		t.Fatal(err)
	}
	a, err := OpenArchive(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}
func options(r *testRenderer) InstallOptions {
	return InstallOptions{Renderer: func(string) (voice.Renderer, error) { return r, nil }, RendererRevision: "test-v1"}
}
func stored(t *testing.T, s Store, id string) *Installation {
	t.Helper()
	v, e := s.Installed(id)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func bundle(t *testing.T, p *Pack) *Artifact {
	t.Helper()
	out := t.TempDir()
	r := &testRenderer{}
	if _, err := p.Build(context.Background(), out, options(r).Renderer); err != nil {
		t.Fatal(err)
	}
	return artifact(t, p, out)
}

func TestInstallTextCacheChangesAndRendererRevision(t *testing.T) {
	p := testSource(t)
	a := artifact(t, p, "")
	s := Store{SoundsDir: t.TempDir()}
	r := &testRenderer{}
	if plan := s.Plan(a, "test-v1"); len(plan.Render) != 6 || plan.Total != 6 {
		t.Fatalf("plan: %+v", plan)
	}
	first, err := s.Install(context.Background(), a, options(r))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 6 {
		t.Fatalf("render calls %d", r.calls)
	}
	again, err := s.Install(context.Background(), a, options(r))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 6 || again.Prefix != first.Prefix {
		t.Fatalf("repeat: calls %d, prefix %s vs %s", r.calls, again.Prefix, first.Prefix)
	}
	if plan := s.Plan(a, "test-v1"); plan.Cached != 6 || len(plan.Render) != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	p.Manifest.Version = "1.1.0"
	p.Manifest.Prompts["good-day"] = "A new goodbye."
	saveManifest(t, p.Dir, p.Manifest)
	p, err = Load(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	changed := artifact(t, p, "")
	next, err := s.Install(context.Background(), changed, options(r))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 7 || next.Prefix == first.Prefix {
		t.Fatalf("changed source re-rendered %d total calls", r.calls)
	}
	if _, err := os.Stat(filepath.Join(s.SoundsDir, first.Prefix, "good-day.wav")); err != nil {
		t.Fatal("old playback was removed", err)
	}
	opts := options(r)
	opts.RendererRevision = "test-v2"
	if _, err := s.Install(context.Background(), changed, opts); err != nil {
		t.Fatal(err)
	}
	if r.calls != 13 {
		t.Fatalf("model/pipeline revision did not invalidate cache: %d", r.calls)
	}
	entries, err := s.List()
	if err != nil || len(entries) != 1 || entries[0].Attempt.Status != "installed" {
		t.Fatalf("list %+v %v", entries, err)
	}
}

func TestFailureResumeCancellationAndSecretBoundary(t *testing.T) {
	p := testSource(t)
	a := artifact(t, p, "")
	s := Store{SoundsDir: t.TempDir()}
	r := &testRenderer{failAt: 3}
	_, err := s.Install(context.Background(), a, options(r))
	if err == nil || strings.Contains(err.Error(), "private-test-credential") {
		t.Fatalf("failure: %v", err)
	}
	entries, err := s.List()
	if err != nil || len(entries) != 1 || entries[0].Installed != nil || entries[0].Attempt.Status != "failed" || entries[0].Attempt.Completed != 2 {
		t.Fatalf("state %+v %v", entries, err)
	}
	raw, _ := os.ReadFile(s.progressPath(p.Manifest.ID))
	if bytes.Contains(raw, []byte("private-test-credential")) {
		t.Fatal("secret in progress")
	}
	r.failAt = 0
	first, err := s.Install(context.Background(), a, options(r))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 7 {
		t.Fatalf("resume re-rendered completed work: %d", r.calls)
	}
	p.Manifest.Prompts["good-day"] = "changed"
	saveManifest(t, p.Dir, p.Manifest)
	p, _ = Load(p.Dir)
	replacement := artifact(t, p, "")
	r.failAt = r.calls + 1
	if _, err = s.Install(context.Background(), replacement, options(r)); err == nil {
		t.Fatal("wanted failure")
	}
	if stored(t, s, p.Manifest.ID).Prefix != first.Prefix {
		t.Fatal("failed upgrade replaced installed version")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Install(ctx, a, options(r)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s2 := Store{SoundsDir: t.TempDir()}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	r2 := &testRenderer{cancel: cancel}
	if _, err = s2.Install(ctx, a, options(r2)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if entries, err = s2.List(); err != nil || entries[0].Attempt.Status != "failed" {
		t.Fatalf("cancel state %+v %v", entries, err)
	}
}

func TestBundledAndMixedAudioRequireOnlyMissingRenderers(t *testing.T) {
	p := testSource(t)
	a := bundle(t, p)
	s := Store{SoundsDir: t.TempDir()}
	never := InstallOptions{Renderer: func(string) (voice.Renderer, error) { t.Fatal("bundled audio called renderer"); return nil, nil }}
	first, err := s.Install(context.Background(), a, never)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.Install(context.Background(), a, never)
	if err != nil || first.Prefix != repeat.Prefix {
		t.Fatalf("bundled repeat %v", err)
	}
	if plan := s.Plan(a, ""); plan.Bundled != 6 || len(plan.Render) != 0 {
		t.Fatalf("%+v", plan)
	}
	// Keep one declared recording and synthesize the other five.
	m := a.Pack.Manifest
	for _, name := range PromptNames[1:] {
		delete(m.Audio, name)
		for _, ext := range []string{".wav", ".wav16"} {
			_ = os.Remove(filepath.Join(a.dir, name+ext))
		}
	}
	saveManifest(t, a.dir, m)
	mixed, err := Load(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	ma := artifact(t, mixed, "")
	r := &testRenderer{}
	if _, err = s.Install(context.Background(), ma, options(r)); err != nil {
		t.Fatal(err)
	}
	if r.calls != 5 {
		t.Fatalf("mixed rendered %d", r.calls)
	}
	// A voice may consist only of recordings, with no text or TTS credentials.
	p = testSource(t)
	p.Manifest.Audio = map[string]AudioSource{}
	p.Manifest.Prompts = nil
	for _, name := range PromptNames {
		p.Manifest.Audio[name] = AudioSource{Origin: "human"}
		for _, rate := range []int{8000, 16000} {
			put(t, filepath.Join(p.Dir, name+ext(rate)), voice.EncodeWAV(testAudio(rate)))
		}
	}
	saveManifest(t, p.Dir, p.Manifest)
	p, err = Load(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Install(context.Background(), artifact(t, p, ""), never); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptCacheIsRegeneratedAndBadInstalledAudioRefusesActivation(t *testing.T) {
	p := testSource(t)
	a := artifact(t, p, "")
	s := Store{SoundsDir: t.TempDir()}
	r := &testRenderer{}
	first, err := s.Install(context.Background(), a, options(r))
	if err != nil {
		t.Fatal(err)
	}
	item := p.Items[0]
	cache := s.cachePath(item, "test-v1")
	put(t, filepath.Join(cache, item.Name+".wav"), voice.EncodeWAV(voice.Normalise(testAudio(16000))))
	if plan := s.Plan(a, "test-v1"); len(plan.Render) != 1 {
		t.Fatalf("corrupt cache %+v", plan)
	}
	if _, err = s.Install(context.Background(), a, options(r)); err != nil {
		t.Fatal(err)
	}
	if r.calls != 7 {
		t.Fatalf("calls %d", r.calls)
	}
	env := filepath.Join(t.TempDir(), ".env")
	put(t, env, []byte("PROMPT_MEDIA_PREFIX=call-me-maybe\n"))
	put(t, filepath.Join(s.SoundsDir, first.Prefix, PromptNames[0]+".wav"), []byte("corrupt"))
	if _, err = s.Use(p.Manifest.ID, env, func() error { t.Fatal("reloaded corrupt audio"); return nil }); err == nil {
		t.Fatal("corrupt audio activated")
	}
	raw, _ := os.ReadFile(env)
	if string(raw) != "PROMPT_MEDIA_PREFIX=call-me-maybe\n" {
		t.Fatal("changed env")
	}
}

func TestActivationPreservesOtherConfigurationAndRollsBack(t *testing.T) {
	p := testSource(t)
	s := Store{SoundsDir: t.TempDir()}
	info, err := s.Install(context.Background(), bundle(t, p), InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"# house\nSECRET=private-test-value\nPROMPT_MEDIA_PREFIX=call-me-maybe\nFAMILY_OVERLAY=family\n", "# empty\n"} {
		env := filepath.Join(t.TempDir(), ".env")
		put(t, env, []byte(raw))
		calls := 0
		_, err = s.Use(p.Manifest.ID, env, func() error {
			calls++
			b, _ := os.ReadFile(env)
			if !strings.Contains(string(b), info.Prefix) {
				t.Fatal("reload before config write")
			}
			return nil
		})
		if err != nil || calls != 1 {
			t.Fatalf("use %v %d", err, calls)
		}
		current, _ := os.ReadFile(env)
		if strings.Contains(raw, "SECRET") && !strings.Contains(string(current), "SECRET=private-test-value\n") {
			t.Fatal("other values lost")
		}
		put(t, env, []byte(raw))
		calls = 0
		_, err = s.Use(p.Manifest.ID, env, func() error {
			calls++
			if calls == 1 {
				return errors.New("restart failed")
			}
			return nil
		})
		b, _ := os.ReadFile(env)
		if err == nil || string(b) != raw || calls != 2 {
			t.Fatalf("rollback %s %v %d", b, err, calls)
		}
		_, err = s.Use(p.Manifest.ID, env, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	env := filepath.Join(t.TempDir(), ".env")
	put(t, env, []byte("PROMPT_MEDIA_PREFIX=call-me-maybe\n"))
	if _, err = s.Use(p.Manifest.ID, env, func() error { return errors.New("down") }); err == nil || !strings.Contains(err.Error(), "still needs attention") {
		t.Fatal(err)
	}
	if _, err = s.Use(p.Manifest.ID, env, func() error { put(t, env, []byte("manual edit\n")); return errors.New("down") }); err == nil || !strings.Contains(err.Error(), "rollback refused") {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), ".env")
	if err = os.Symlink(env, target); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Use(p.Manifest.ID, target, nil); err == nil {
		t.Fatal("accepted env symlink")
	}
}

func TestStoreLocksAndPreflight(t *testing.T) {
	s := Store{SoundsDir: t.TempDir()}
	p := testSource(t)
	a := artifact(t, p, "")
	if entries, err := s.List(); err != nil || len(entries) != 0 {
		t.Fatalf("empty list %v %v", entries, err)
	}
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Lock(); err == nil {
		t.Fatal("concurrent lock accepted")
	}
	if _, err = s.Install(context.Background(), a, InstallOptions{}); err == nil {
		t.Fatal("concurrent install accepted")
	}
	unlock()
	if _, err = s.Install(context.Background(), a, InstallOptions{}); err == nil {
		t.Fatal("nil renderer")
	}
	opts := InstallOptions{Renderer: func(string) (voice.Renderer, error) { return nil, errors.New("private credential") }}
	if _, err = s.Install(context.Background(), a, opts); err == nil || strings.Contains(err.Error(), "private credential") {
		t.Fatal(err)
	}
	if _, err = s.Installed("../bad"); err == nil {
		t.Fatal("unsafe id")
	}
	if _, err = s.Installed("missing"); err == nil {
		t.Fatal("missing install")
	}
	put(t, s.installedPath(p.Manifest.ID), []byte("bad json"))
	if _, err = s.List(); err == nil {
		t.Fatal("corrupt registry")
	}
}

const simpleStory = `---
story:
  title: Test story
  start: Door
voices:
  NARRATOR:
    backend: piper
    id: en_GB-alba-medium
---
## Door
NARRATOR: Hello there.
<sfx name="knock"/>
<end/>
`

func TestStoryArchiveIncludesDeclaredEffectsAndCompilesGraph(t *testing.T) {
	p := testSource(t)
	p.Manifest.Kind = KindStory
	p.Manifest.Prompts = nil
	saveManifest(t, p.Dir, p.Manifest)
	put(t, filepath.Join(p.Dir, "story.md"), []byte(simpleStory))
	for _, rate := range []int{8000, 16000} {
		put(t, filepath.Join(p.Dir, "sfx/knock"+ext(rate)), voice.EncodeWAV(testAudio(rate)))
	}
	var err error
	p, err = Load(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	a := artifact(t, p, "")
	s := Store{SoundsDir: t.TempDir()}
	r := &testRenderer{}
	info, err := s.Install(context.Background(), a, options(r))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 {
		t.Fatalf("effect synthesized: %d", r.calls)
	}
	graph, err := os.ReadFile(filepath.Join(s.SoundsDir, info.Prefix, "story.json"))
	if err != nil || !bytes.Contains(graph, []byte("sfx/knock")) {
		t.Fatalf("graph %s %v", graph, err)
	}
	if _, err = s.Use(p.Manifest.ID, "unused", nil); err == nil {
		t.Fatal("story selected as voice")
	}
	if err = p.WriteArchive(filepath.Join(t.TempDir(), "story.cmmpack"), ""); err != nil {
		t.Fatal(err)
	}
	// This reference must not escape the story's audio namespace.
	put(t, filepath.Join(p.Dir, "story.md"), []byte(strings.Replace(simpleStory, "knock", "../../outside", 1)))
	if _, err = Load(p.Dir); err == nil {
		t.Fatal("unsafe effect name")
	}
}

func zipFiles(t *testing.T, files map[string][]byte, mutate func(*zip.FileHeader)) string {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for n, b := range files {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate}
		if mutate != nil {
			mutate(h)
		}
		w, e := z.CreateHeader(h)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	f := filepath.Join(t.TempDir(), "test.cmmpack")
	put(t, f, buf.Bytes())
	return f
}
func resign(files map[string][]byte) {
	var sums strings.Builder
	for n, b := range files {
		if n != "SHA256SUMS" {
			h := sha256.Sum256(b)
			fmt.Fprintf(&sums, "%x  %s\n", h, n)
		}
	}
	files["SHA256SUMS"] = []byte(sums.String())
}
func archiveFiles(t *testing.T, p *Pack) map[string][]byte {
	t.Helper()
	f := filepath.Join(t.TempDir(), "p.cmmpack")
	if e := p.WriteArchive(f, ""); e != nil {
		t.Fatal(e)
	}
	r, e := zip.OpenReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	out := map[string][]byte{}
	for _, entry := range r.File {
		f, e := entry.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		out[entry.Name] = b
	}
	return out
}

func TestArchiveRejectsTamperingTraversalAndUnsupportedContent(t *testing.T) {
	base := archiveFiles(t, testSource(t))
	cases := map[string]func(map[string][]byte){
		"missing sums":   func(f map[string][]byte) { delete(f, "SHA256SUMS") },
		"tamper":         func(f map[string][]byte) { f["pack.json"] = append(f["pack.json"], ' ') },
		"unlisted":       func(f map[string][]byte) { f["extra"] = []byte("not covered") },
		"undeclared":     func(f map[string][]byte) { f[".env"] = []byte("secret"); resign(f) },
		"extra payload":  func(f map[string][]byte) { f["extra.txt"] = []byte("extra"); resign(f) },
		"traversal":      func(f map[string][]byte) { f["../outside"] = nil; resign(f) },
		"absolute":       func(f map[string][]byte) { f["/tmp/outside"] = nil; resign(f) },
		"backslash":      func(f map[string][]byte) { f[`sub\outside`] = nil; resign(f) },
		"case collision": func(f map[string][]byte) { f["PACK.json"] = f["pack.json"]; resign(f) },
		"invalid json":   func(f map[string][]byte) { f["pack.json"] = []byte("{"); resign(f) },
		"unknown version": func(f map[string][]byte) {
			f["pack.json"] = bytes.Replace(f["pack.json"], []byte(`"archive_version": 1`), []byte(`"archive_version": 99`), 1)
			resign(f)
		},
		"missing version": func(f map[string][]byte) {
			f["pack.json"] = bytes.Replace(f["pack.json"], []byte(`"archive_version": 1`), []byte(`"archive_version": 0`), 1)
			resign(f)
		},
		"unknown kind": func(f map[string][]byte) {
			f["pack.json"] = bytes.Replace(f["pack.json"], []byte(`"kind": "voice"`), []byte(`"kind": "rotation"`), 1)
			resign(f)
		},
		"unsafe id": func(f map[string][]byte) {
			f["pack.json"] = bytes.Replace(f["pack.json"], []byte(`"id": "test-pack"`), []byte(`"id": "../escape"`), 1)
			resign(f)
		},
		"no license": func(f map[string][]byte) {
			f["pack.json"] = bytes.Replace(f["pack.json"], []byte(`"license": "CC0"`), []byte(`"license": ""`), 1)
			resign(f)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			files := map[string][]byte{}
			for n, b := range base {
				files[n] = bytes.Clone(b)
			}
			change(files)
			if a, e := OpenArchive(zipFiles(t, files, nil)); e == nil {
				a.Close()
				t.Fatal("accepted invalid archive")
			}
		})
	}
	for _, mode := range []os.FileMode{os.ModeSymlink | 0o777, os.ModeDir | 0o755} {
		if a, e := OpenArchive(zipFiles(t, base, func(h *zip.FileHeader) { h.SetMode(mode) })); e == nil {
			a.Close()
			t.Fatal("accepted special entry")
		}
	}
	f := filepath.Join(t.TempDir(), "bad.cmmpack")
	put(t, f, []byte("not zip"))
	if _, e := OpenArchive(f); e == nil {
		t.Fatal("accepted nonzip")
	}
}

func TestStrictAudioValidationAndLoudness(t *testing.T) {
	good := voice.EncodeWAV(testAudio(8000))
	cases := map[string]func([]byte) []byte{
		"format":          func(b []byte) []byte { b[20] = 3; return b },
		"stereo":          func(b []byte) []byte { b[22] = 2; return b },
		"bits":            func(b []byte) []byte { b[34] = 8; return b },
		"wrong rate":      func(b []byte) []byte { binary.LittleEndian.PutUint32(b[24:], 16000); return b },
		"truncated":       func(b []byte) []byte { return b[:len(b)-1] },
		"oversized chunk": func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:], ^uint32(0)); return b },
		"silence":         func(b []byte) []byte { clear(b[44:]); return b },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := validateWAV(change(bytes.Clone(good)), 8000); err == nil {
				t.Fatal("accepted invalid WAV")
			}
		})
	}
	if warning, err := validateWAV(good, 8000); err != nil || warning != "" {
		t.Fatalf("valid: %q %v", warning, err)
	}
	quiet := testAudio(8000)
	for i := range quiet.PCM {
		quiet.PCM[i] = 0
	}
	quiet.PCM[0] = 1
	if warning, err := validateWAV(voice.EncodeWAV(quiet), 8000); err != nil || warning == "" {
		t.Fatalf("quiet: %q %v", warning, err)
	}
}

func TestSameTextAndVoiceSettingsCacheIndependently(t *testing.T) {
	p := testSource(t)
	for n := range p.Manifest.Prompts {
		p.Manifest.Prompts[n] = "Hello."
	}
	saveManifest(t, p.Dir, p.Manifest)
	p, _ = Load(p.Dir)
	s := Store{SoundsDir: t.TempDir()}
	r := &testRenderer{}
	a := artifact(t, p, "")
	for range 2 {
		if _, e := s.Install(context.Background(), a, options(r)); e != nil {
			t.Fatal(e)
		}
	}
	if r.calls != 6 {
		t.Fatalf("identical prompts missed cache: %d", r.calls)
	}
	item := p.Items[0]
	original := item.SourceHash()
	item.Voice.Settings = map[string]string{"speed": "1.1", "stability": "0.5"}
	if item.SourceHash() == original {
		t.Fatal("voice settings not bound")
	}
	item2 := item
	item2.Voice.Settings = map[string]string{"stability": "0.5", "speed": "1.1"}
	if item2.SourceHash() != item.SourceHash() {
		t.Fatal("map order changes hash")
	}
}

func TestBundledBuildAlwaysCopiesLatestRecording(t *testing.T) {
	p := testSource(t)
	item := p.Items[0]
	p.Manifest.Audio = map[string]AudioSource{item.Name: {Origin: "human", SourceHash: item.SourceHash()}}
	for _, rate := range []int{8000, 16000} {
		put(t, filepath.Join(p.Dir, item.Name+ext(rate)), voice.EncodeWAV(testAudio(rate)))
	}
	out := t.TempDir()
	r := &testRenderer{}
	if _, e := p.Build(context.Background(), out, options(r).Renderer); e != nil {
		t.Fatal(e)
	}
	changed := testAudio(8000)
	changed.PCM[0] = 123
	put(t, filepath.Join(p.Dir, item.Name+".wav"), voice.EncodeWAV(changed))
	if _, e := p.Build(context.Background(), out, options(r).Renderer); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(filepath.Join(out, item.Name+".wav"))
	if !bytes.Equal(got, voice.EncodeWAV(changed)) || r.calls != 5 {
		t.Fatal("recording stale or synthesized")
	}
	a := artifact(t, p, out)
	if a.Pack.Manifest.Audio[item.Name].Origin != "human" {
		t.Fatal("lost recording provenance")
	}
	put(t, filepath.Join(p.Dir, item.Name+".wav"), []byte("bad WAV"))
	if _, e := p.Build(context.Background(), out, options(r).Renderer); e == nil {
		t.Fatal("built invalid recording")
	}
	if e := p.WriteArchive(filepath.Join(t.TempDir(), "p.cmmpack"), ""); e == nil {
		t.Fatal("packaged invalid recording")
	}
	if e := os.Remove(filepath.Join(p.Dir, item.Name+".wav")); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Build(context.Background(), out, options(r).Renderer); e == nil {
		t.Fatal("built missing recording")
	}
}

func TestManifestAudioBindingsAndRendererValidation(t *testing.T) {
	cases := map[string]func(*Pack){
		"unknown audio": func(p *Pack) { p.Manifest.Audio = map[string]AudioSource{"extra": {Origin: "human"}} },
		"bad origin":    func(p *Pack) { p.Manifest.Audio = map[string]AudioSource{p.Items[0].Name: {Origin: "unknown"}} },
		"stale recording": func(p *Pack) {
			p.Manifest.Audio = map[string]AudioSource{p.Items[0].Name: {Origin: "human", SourceHash: "stale"}}
		},
		"hash without script": func(p *Pack) {
			n := p.Items[0].Name
			p.Manifest.Prompts[n] = ""
			p.Manifest.Audio = map[string]AudioSource{n: {Origin: "human", SourceHash: "stale"}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := testSource(t)
			change(p)
			saveManifest(t, p.Dir, p.Manifest)
			if _, e := Load(p.Dir); e == nil {
				t.Fatal("accepted invalid binding")
			}
		})
	}
	for _, spec := range []VoiceSpec{{Backend: "unknown"}, {Backend: "piper", ID: "/tmp/model.onnx"}, {Backend: "exec", Settings: map[string]string{"sample_rate": "1"}}, {Backend: "exec", Settings: map[string]string{"sample_rate": "bad"}}} {
		p := testSource(t)
		p.Manifest.Voice, _ = json.Marshal(spec)
		saveManifest(t, p.Dir, p.Manifest)
		p, e := Load(p.Dir)
		if e != nil {
			t.Fatal(e)
		}
		if e = p.WriteArchive(filepath.Join(t.TempDir(), "p.cmmpack"), ""); e == nil {
			t.Fatalf("accepted unsafe renderer %+v", spec)
		}
	}
	p := testSource(t)
	p.Manifest.Voice = json.RawMessage(`{"backend":"exec","settings":{"sample_rate":"16000"}}`)
	saveManifest(t, p.Dir, p.Manifest)
	p, _ = Load(p.Dir)
	artifact(t, p, "")
}

func TestArchiveIncompleteAudioAndInputBounds(t *testing.T) {
	a := bundle(t, testSource(t))
	base := archiveFiles(t, a.Pack)
	for _, name := range []string{"missing audio", "invalid audio", "warning"} {
		t.Run(name, func(t *testing.T) {
			f := map[string][]byte{}
			for n, b := range base {
				f[n] = bytes.Clone(b)
			}
			switch name {
			case "missing audio":
				delete(f, "good-day.wav16")
			case "invalid audio":
				f["good-day.wav"] = []byte("broken")
			case "warning":
				quiet := testAudio(8000)
				for i := range quiet.PCM {
					quiet.PCM[i] = 0
				}
				quiet.PCM[0] = 1
				f["good-day.wav"] = voice.EncodeWAV(quiet)
			}
			resign(f)
			art, e := OpenArchive(zipFiles(t, f, nil))
			if name == "warning" {
				if e != nil {
					t.Fatal(e)
				}
				defer art.Close()
				if len(art.Warnings) == 0 {
					t.Fatal("no level warning")
				}
			} else if e == nil {
				art.Close()
				t.Fatal("accepted missing/invalid audio")
			}
		})
	}
	for _, f := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing"), zipFiles(t, map[string][]byte{}, nil), zipFiles(t, map[string][]byte{"pack.json": bytes.Repeat([]byte("x"), maxSourceBytes+1)}, nil)} {
		if a, e := OpenArchive(f); e == nil {
			a.Close()
			t.Fatal("accepted oversized/empty/nonfile input")
		}
	}
	huge := filepath.Join(t.TempDir(), "huge.cmmpack")
	f, e := os.Create(huge)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(maxArchiveBytes + 1); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if _, e = OpenArchive(huge); e == nil {
		t.Fatal("accepted oversized archive")
	}
	files := archiveFiles(t, testSource(t))
	for _, sums := range []string{"bad", strings.Repeat("z", 64) + "  pack.json\n", string(files["SHA256SUMS"]) + string(files["SHA256SUMS"])} {
		files["SHA256SUMS"] = []byte(sums)
		if a, e := OpenArchive(zipFiles(t, files, nil)); e == nil {
			a.Close()
			t.Fatal("accepted malformed checksums")
		}
	}
}

func TestStorageFailuresNeverPublishIncompleteInstall(t *testing.T) {
	a := artifact(t, testSource(t), "")
	for _, obstacle := range []string{".doorman-packs", ".doorman-packs/lock", "doorman-packs", ".doorman-packs/test-pack.progress.json", ".doorman-packs/test-pack.installed.json"} {
		t.Run(obstacle, func(t *testing.T) {
			s := Store{SoundsDir: t.TempDir()}
			target := filepath.Join(s.SoundsDir, obstacle)
			if strings.HasSuffix(obstacle, ".json") || strings.HasSuffix(obstacle, "/lock") {
				if e := os.MkdirAll(target, 0700); e != nil {
					t.Fatal(e)
				}
			} else {
				put(t, target, []byte("obstacle"))
			}
			if _, e := s.Install(context.Background(), a, options(&testRenderer{})); e == nil {
				t.Fatal("accepted broken storage")
			}
			if _, e := s.Installed("test-pack"); e == nil {
				t.Fatal("published incomplete install")
			}
			if e := os.RemoveAll(target); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Install(context.Background(), a, options(&testRenderer{})); e != nil {
				t.Fatal("could not recover", e)
			}
		})
	}
	s := Store{SoundsDir: t.TempDir()}
	unlock, e := s.Lock()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Use("test-pack", "unused", nil); e == nil {
		t.Fatal("concurrent use")
	}
	unlock()
	ctx, cancel := context.WithCancel(context.Background())
	opts := options(&testRenderer{})
	opts.Progress = func(p Progress) {
		if p.Completed == p.Total {
			cancel()
		}
	}
	if _, e = s.Install(ctx, a, opts); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = s.Installed("test-pack"); e == nil {
		t.Fatal("published cancelled install")
	}
	opts.Progress = nil
	if _, e = s.Install(context.Background(), a, opts); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Use("test-pack", filepath.Join(t.TempDir(), "missing"), nil); e == nil {
		t.Fatal("accepted missing environment")
	}
	env := filepath.Join(t.TempDir(), ".env")
	put(t, env, []byte("EXISTING=private\n"))
	if e = os.Chmod(env, 0640); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Use("test-pack", env, nil); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(env)
	if st.Mode().Perm() != 0640 {
		t.Fatal("lost service group read access")
	}
}

func TestCorruptRegistryAndCacheMetadata(t *testing.T) {
	p := testSource(t)
	a := artifact(t, p, "")
	s := Store{SoundsDir: t.TempDir()}
	opts := options(&testRenderer{})
	info, e := s.Install(context.Background(), a, opts)
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Installation){func(i *Installation) { i.ID = "wrong" }, func(i *Installation) { i.Prefix = "../outside" }, func(i *Installation) { i.Clips = nil }, func(i *Installation) { i.Clips[0].Files = nil }} {
		clone := *info
		clone.Clips = append([]Provenance(nil), info.Clips...)
		change(&clone)
		if e := writeJSON(s.installedPath(info.ID), clone, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Installed(info.ID); e == nil {
			t.Fatal("accepted corrupt registry")
		}
	}
	cache := s.cachePath(p.Items[0], opts.RendererRevision)
	var record Provenance
	if e = readJSON(cache, "provenance.json", &record); e != nil {
		t.Fatal(e)
	}
	record.RendererRevision = "stale"
	if e = writeJSON(filepath.Join(cache, "provenance.json"), record, 0600); e != nil {
		t.Fatal(e)
	}
	if plan := s.Plan(a, opts.RendererRevision); len(plan.Render) != 1 {
		t.Fatalf("stale metadata reused: %+v", plan)
	}
	put(t, s.progressPath(info.ID), []byte("invalid json"))
	if _, e = s.List(); e == nil {
		t.Fatal("accepted corrupt progress")
	}
}

func TestSourceArchiveDoesNotExportLocalSecretsOrEscapingSymlinks(t *testing.T) {
	p := testSource(t)
	put(t, filepath.Join(p.Dir, ".env"), []byte("private"))
	files := archiveFiles(t, p)
	if _, ok := files[".env"]; ok {
		t.Fatal("exported secret")
	}
	p.Manifest.Description = strings.Repeat("x", maxSourceBytes)
	if e := p.WriteArchive(filepath.Join(t.TempDir(), "p.cmmpack"), ""); e == nil {
		t.Fatal("oversized manifest")
	}
	p.Manifest.Description = ""
	if e := p.WriteArchive(filepath.Join(t.TempDir(), "p.cmmpack"), t.TempDir()); e == nil {
		t.Fatal("packaged stale build")
	}
	item := p.Items[0]
	p.Manifest.Audio = map[string]AudioSource{item.Name: {Origin: "human", SourceHash: item.SourceHash()}}
	outside := filepath.Join(t.TempDir(), "private.wav")
	put(t, outside, voice.EncodeWAV(testAudio(8000)))
	if e := os.Symlink(outside, filepath.Join(p.Dir, item.Name+".wav")); e != nil {
		t.Fatal(e)
	}
	if e := p.WriteArchive(filepath.Join(t.TempDir(), "p.cmmpack"), ""); e == nil {
		t.Fatal("exported external file")
	}
}

func TestWAVRejectsAmbiguousChunks(t *testing.T) {
	base := voice.EncodeWAV(testAudio(8000))
	cases := map[string][]byte{
		"duplicate format": append(bytes.Clone(base), base[12:36]...),
		"duplicate data":   append(bytes.Clone(base), base[36:]...),
		"trailing chunk":   append(bytes.Clone(base), 1),
		"missing padding":  append(bytes.Clone(base), []byte{'J', 'U', 'N', 'K', 1, 0, 0, 0, 1}...),
		"missing format":   append(bytes.Clone(base[:12]), base[36:]...),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
			if _, e := validateWAV(b, 8000); e == nil {
				t.Fatal("accepted malformed WAV")
			}
		})
	}
}
