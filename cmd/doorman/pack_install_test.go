package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/pack"
	"callmemaybe/internal/voice"
)

type packTestRenderer struct {
	calls int
	fail  bool
}

func (r *packTestRenderer) Render(context.Context, string, voice.Voice) (voice.Audio, error) {
	r.calls++
	if r.fail {
		return voice.Audio{}, errors.New("private-provider-token")
	}
	pcm := make([]byte, 1600)
	for i := 0; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], 4096)
	}
	return voice.Audio{PCM: pcm, SampleRate: 8000}, nil
}

type packTestSystem struct {
	env   string
	calls []string
	fail  bool
	wrong bool
}

func (s *packTestSystem) Run(_ context.Context, args ...string) (string, error) {
	s.calls = append(s.calls, strings.Join(args, " "))
	if args[1] == "show" {
		if s.wrong {
			return "/different/.env (ignore_errors=no)", nil
		}
		return s.env + " (ignore_errors=no)", nil
	}
	if s.fail {
		s.fail = false
		return "private service output", errors.New("failed")
	}
	return "", nil
}
func packFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	m := pack.Manifest{ID: "cli-test", Name: "CLI test", Version: "1", Kind: pack.KindVoice, License: "CC0", Prompts: map[string]string{}}
	for _, n := range pack.PromptNames {
		m.Prompts[n] = "Text for " + n
	}
	b, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "pack.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	p, e := pack.Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "test.cmmpack")
	if e = p.WriteArchive(file, ""); e != nil {
		t.Fatal(e)
	}
	return dir, file
}
func TestPackCLIInstallPlanActivateAndList(t *testing.T) {
	t.Setenv("PROMPT_MEDIA_PREFIX", "")
	_, file := packFixture(t)
	sounds := filepath.Join(t.TempDir(), "sounds")
	env := filepath.Join(t.TempDir(), ".env")
	if e := os.WriteFile(env, []byte("PROMPT_MEDIA_PREFIX=call-me-maybe\nOTHER=private\n"), 0640); e != nil {
		t.Fatal(e)
	}
	var out, errs bytes.Buffer
	r := &packTestRenderer{}
	sys := &packTestSystem{env: env}
	prepared := 0
	cli := packCLI{out: &out, err: &errs, exec: sys, renderer: func(string) (voice.Renderer, error) { return r, nil }, prepare: func() { prepared++ }}
	run := func(args ...string) string {
		t.Helper()
		out.Reset()
		errs.Reset()
		args = append(args, "--sounds-dir", sounds, "--env", env)
		if n := cli.run(context.Background(), args); n != 0 {
			t.Fatalf("%v: %d %s", args, n, errs.String())
		}
		return out.String()
	}
	if got := run("list"); !strings.Contains(got, "No managed packs") {
		t.Fatal(got)
	}
	run("lint", file)
	if got := run("install", file, "--dry-run"); !strings.Contains(got, "6 to render") {
		t.Fatal(got)
	}
	if r.calls != 0 || prepared != 0 {
		t.Fatal("dry run rendered")
	}
	if _, e := os.Stat(sounds); !os.IsNotExist(e) {
		t.Fatal("dry run wrote storage", e)
	}
	if got := run("install", file); !strings.Contains(got, "Activate separately") || !strings.Contains(got, "installed 6/6") {
		t.Fatal(got)
	}
	if r.calls != 6 || prepared != 1 {
		t.Fatalf("render %d prepare %d", r.calls, prepared)
	}
	b, _ := os.ReadFile(env)
	if !strings.Contains(string(b), "call-me-maybe") {
		t.Fatal("install activated")
	}
	if got := run("install", file); !strings.Contains(got, "6 cached") {
		t.Fatal(got)
	}
	if r.calls != 6 || prepared != 1 {
		t.Fatal("reinstall rendered")
	}
	run("use", "cli-test")
	if len(sys.calls) != 2 || !strings.Contains(sys.calls[1], "try-restart doorman.service") {
		t.Fatal(sys.calls)
	}
	if got := run("list"); !strings.Contains(got, "active selection") {
		t.Fatal(got)
	}
	var listing struct {
		Active string       `json:"configured_active_prefix"`
		Packs  []pack.Entry `json:"packs"`
	}
	if e := json.Unmarshal([]byte(run("list", "--json")), &listing); e != nil || len(listing.Packs) != 1 || listing.Active != listing.Packs[0].Installed.Prefix {
		t.Fatalf("%+v %v", listing, e)
	}
	run("use", "cli-test", "--no-restart")
	if len(sys.calls) != 2 {
		t.Fatal("offline activation restarted service")
	}
	// An upgrade can fail while the old version and selection stay available.
	r.fail = true
	out.Reset()
	errs.Reset()
	code := cli.run(context.Background(), []string{"install", file, "--sounds-dir", sounds, "--renderer-revision", "new"})
	if code != 1 || strings.Contains(errs.String(), "private-provider-token") {
		t.Fatal(code, errs.String())
	}
	if got := run("list"); !strings.Contains(got, "active selection") || !strings.Contains(got, "last attempt: failed") {
		t.Fatal(got)
	}
}
func TestPackCLIActivationFailureRollsBackAndRejectsWrongService(t *testing.T) {
	t.Setenv("PROMPT_MEDIA_PREFIX", "")
	_, file := packFixture(t)
	sounds := t.TempDir()
	env := filepath.Join(t.TempDir(), ".env")
	original := "PROMPT_MEDIA_PREFIX=call-me-maybe\n"
	if e := os.WriteFile(env, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	var out, errs bytes.Buffer
	sys := &packTestSystem{env: env}
	cli := packCLI{out: &out, err: &errs, exec: sys, renderer: func(string) (voice.Renderer, error) { return &packTestRenderer{}, nil }}
	if code := cli.run(context.Background(), []string{"install", file, "--sounds-dir", sounds}); code != 0 {
		t.Fatal(errs.String())
	}
	args := []string{"use", "cli-test", "--sounds-dir", sounds, "--env", env}
	sys.wrong = true
	if code := cli.run(context.Background(), args); code != 1 {
		t.Fatal("accepted wrong service")
	}
	if len(sys.calls) != 1 {
		t.Fatal(sys.calls)
	}
	sys.wrong = false
	sys.fail = true
	errs.Reset()
	if code := cli.run(context.Background(), args); code != 1 {
		t.Fatal("restart failed but reported success")
	}
	b, _ := os.ReadFile(env)
	if string(b) != original || !strings.Contains(errs.String(), "restored the previous configuration and service") || strings.Contains(errs.String(), "private service output") {
		t.Fatal(string(b), errs.String())
	}
	if len(sys.calls) != 4 {
		t.Fatal("did not restart old service", sys.calls)
	}
}
func TestPackCLIInvalidArgumentsAndMissingArtifact(t *testing.T) {
	var out, errs bytes.Buffer
	cli := packCLI{out: &out, err: &errs}
	for _, args := range [][]string{{"install"}, {"use"}, {"list", "extra"}, {"lint", "one", "two"}, {"lint", "one", "--dry-run"}, {"install", "one", "--json"}, {"list", "--no-restart"}, {"list", "--unknown"}} {
		if code := cli.run(context.Background(), args); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
	if code := cli.run(context.Background(), []string{"lint", filepath.Join(t.TempDir(), "missing")}); code != 1 {
		t.Fatal(code)
	}
	// Flags before the final target are accepted too.
	_, file := packFixture(t)
	if code := cli.run(context.Background(), []string{"lint", "--env", filepath.Join(t.TempDir(), "none"), file}); code != 0 {
		t.Fatal(errs.String())
	}
}
func TestPackAuthorSourceOnlyBuildAndHashes(t *testing.T) {
	dir, _ := packFixture(t)
	file := filepath.Join(t.TempDir(), "source.cmmpack")
	for _, args := range [][]string{{dir, "--source-only"}, {dir, "--archive", file, "--backend", "exec"}} {
		capture(t, func() {
			if code := runPackBuild(args); code != 2 {
				t.Fatal(code)
			}
		})
	}
	capture(t, func() {
		if code := runPackBuild([]string{dir, "--source-only", "--archive", file, "--dry-run"}); code != 0 {
			t.Fatal(code)
		}
	})
	if _, e := os.Stat(file); !os.IsNotExist(e) {
		t.Fatal("dry-run created archive")
	}
	capture(t, func() {
		if code := runPackBuild([]string{dir, "--source-only", "--archive", file}); code != 0 {
			t.Fatal(code)
		}
	})
	a, e := pack.OpenArchive(file)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	if len(a.Pack.Manifest.Audio) != 0 {
		t.Fatal("source-only rendered")
	}
	out := capture(t, func() {
		if code := runPackCheck([]string{dir, "--hashes"}); code != 0 {
			t.Fatal(code)
		}
	})
	if !strings.Contains(out, a.Pack.Items[0].SourceHash()) {
		t.Fatal(out)
	}
	for _, args := range [][]string{nil, {"help"}, {"unknown"}} {
		capture(t, func() { runPack(args) })
	}
}
func TestPackOperatorConfigurationPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if e := os.WriteFile(path, []byte("VOICE_EXEC_CMD=operator-command\nVOICE_EXEC_ARGS=--voice $VOICE\nAWS_REGION=test-region\nPIPER_CMD=operator-piper\nPROMPT_MEDIA_PREFIX=from-file\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("VOICE_EXEC_CMD", "")
	t.Setenv("VOICE_EXEC_ARGS", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("PIPER_CMD", "")
	t.Setenv("PROMPT_MEDIA_PREFIX", "")
	cfg := packVoiceConfig(path, "exec")
	if cfg.Command != "operator-command" || len(cfg.Args) != 2 || cfg.Region != "test-region" {
		t.Fatalf("%+v", cfg)
	}
	if cfg := packVoiceConfig(path, "piper"); cfg.Command != "operator-piper" {
		t.Fatal(cfg.Command)
	}
	if configuredPack(path) != "from-file" {
		t.Fatal("file selection ignored")
	}
	t.Setenv("VOICE_EXEC_CMD", "environment-command")
	if cfg := packVoiceConfig(path, "exec"); cfg.Command != "environment-command" {
		t.Fatal(cfg.Command)
	}
	if configuredPack(filepath.Join(t.TempDir(), "missing")) != "call-me-maybe" {
		t.Fatal("wrong bundled default")
	}
}
