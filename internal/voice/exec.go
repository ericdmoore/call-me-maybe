package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// osGetenv is a variable so tests can drive credential lookup without touching
// the developer's environment.
var osGetenv = os.Getenv

// ── piper ───────────────────────────────────────────────────────────────

// piperRenderer shells out to piper. It does not embed an ONNX runtime and
// never will: that would put a neural TTS engine into the binary that gets
// cross-compiled to a Pi, for a subcommand the daemon never invokes.
type piperRenderer struct {
	command string
	models  string // directory searched when a voice id is not a path
}

func newPiper(cfg Config) (Renderer, error) {
	cmd := cfg.Command
	if cmd == "" {
		cmd = cfg.env("PIPER_CMD")
	}
	if cmd == "" {
		cmd = "piper"
	}

	models := cfg.env("PIPER_VOICE_DIR")
	if models == "" {
		if home, err := os.UserHomeDir(); err == nil {
			models = filepath.Join(home, ".local", "share", "piper")
		}
	}
	return &piperRenderer{command: cmd, models: models}, nil
}

// model resolves a voice id to an .onnx path. A pack names a voice the way
// piper's own docs do — "en_GB-alba-medium" — and an absolute path also works
// so a model outside the usual directory is still usable.
func (p *piperRenderer) model(id string) string {
	if id == "" {
		id = "en_GB-alba-medium"
	}
	if strings.HasSuffix(id, ".onnx") {
		return id
	}
	return filepath.Join(p.models, id+".onnx")
}

func (p *piperRenderer) Render(ctx context.Context, text string, v Voice) (Audio, error) {
	model := p.model(v.ID)
	if _, err := os.Stat(model); err != nil {
		return Audio{}, fmt.Errorf("voice: piper model %q not found (set PIPER_VOICE_DIR): %w", v.ID, err)
	}

	// A temp file rather than raw stdout because the WAV header carries the
	// sample rate, and piper's rate depends on the model — medium voices are
	// 22.05 kHz, low ones 16 kHz. Assuming it makes every prompt play at the
	// wrong pitch, which is exactly the kind of failure that survives review.
	dir, err := os.MkdirTemp("", "doorman-piper-")
	if err != nil {
		return Audio{}, fmt.Errorf("voice: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "out.wav")

	cmd := exec.CommandContext(ctx, p.command, "--model", model, "--output_file", out)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return Audio{}, fmt.Errorf("voice: piper failed: %w: %s", err, trimErr(stderr.String()))
	}

	b, err := os.ReadFile(out)
	if err != nil {
		return Audio{}, fmt.Errorf("voice: piper wrote nothing: %w", err)
	}
	return decodeWAV(b)
}

// Estimate reports free, which is the argument for piper being the default and
// for the bundled pack never requiring an account.
func (p *piperRenderer) Estimate(texts []string) Cost {
	return Cost{Characters: countChars(texts), Requests: len(texts), Free: true,
		Note: "piper runs locally and costs nothing"}
}

// Voices lists the models actually installed, which is more useful than a
// catalogue of ones that could be downloaded.
func (p *piperRenderer) Voices(ctx context.Context) ([]Info, error) {
	entries, err := os.ReadDir(p.models)
	if err != nil {
		return nil, fmt.Errorf("voice: cannot list piper models in %s: %w", p.models, err)
	}
	var out []Info
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".onnx") {
			continue
		}
		id := strings.TrimSuffix(name, ".onnx")
		lang, _, _ := strings.Cut(id, "-")
		out = append(out, Info{ID: id, Name: id, Language: lang})
	}
	return out, nil
}

// ── exec ────────────────────────────────────────────────────────────────

// execRenderer runs any command that takes text on stdin and writes signed
// 16-bit little-endian mono PCM to stdout.
//
// This is the extensibility story. A fifth backend arrives without touching Go:
// `say` on a Mac, espeak, a curl script pointing at a TTS server on another
// machine — including a Wyoming or HTTP piper already running elsewhere, which
// is why this package does not ship a web service of its own.
type execRenderer struct {
	command string
	args    []string
	rate    int
}

func newExec(cfg Config) (Renderer, error) {
	if cfg.Command == "" {
		return nil, errors.New("voice: the exec backend needs a command")
	}
	rate := cfg.SampleRate
	if rate == 0 {
		rate = 22050
	}
	return &execRenderer{command: cfg.Command, args: cfg.Args, rate: rate}, nil
}

func (e *execRenderer) Render(ctx context.Context, text string, v Voice) (Audio, error) {
	// $VOICE lets one command serve many voices without a wrapper per voice.
	args := make([]string, len(e.args))
	for i, a := range e.args {
		args[i] = strings.ReplaceAll(a, "$VOICE", v.ID)
	}

	cmd := exec.CommandContext(ctx, e.command, args...)
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		return Audio{}, fmt.Errorf("voice: %s failed: %w: %s", e.command, err, trimErr(stderr.String()))
	}
	if stdout.Len() == 0 {
		return Audio{}, fmt.Errorf("voice: %s produced no audio: %s", e.command, trimErr(stderr.String()))
	}

	b := stdout.Bytes()
	rate := e.rate
	if n := intSetting(v, "sample_rate", 0); n > 0 {
		rate = n
	}

	// A command that emits a WAV rather than raw samples is common enough
	// (`say -o`, ffmpeg) that guessing from the header beats a config flag.
	if len(b) > 12 && string(b[0:4]) == "RIFF" {
		return decodeWAV(b)
	}
	if len(b)%2 != 0 {
		return Audio{}, fmt.Errorf("voice: %s wrote %d bytes, not a whole number of 16-bit samples", e.command, len(b))
	}
	return Audio{PCM: b, SampleRate: rate}, nil
}

func (e *execRenderer) Estimate(texts []string) Cost {
	return Cost{Characters: countChars(texts), Requests: len(texts), Free: true,
		Note: "whatever " + e.command + " costs"}
}

// trimErr keeps an error message useful without pasting a screenful of a
// subprocess's stderr into it.
func trimErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		return "(no output)"
	}
	return s
}
