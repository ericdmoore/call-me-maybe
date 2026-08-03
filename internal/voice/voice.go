// Package voice renders prompt text to audio through a choice of backends.
//
// The interface is deliberately the narrowest thing that works: text in, PCM
// out. Everything downstream — loudness, resampling, the 8 kHz/16 kHz pair,
// the file naming, the six-name prompt contract — is shared and lives outside
// the backend, so a new backend is one method and *cannot* get the output
// format wrong. The failure this prevents is four backends each reimplementing
// resampling slightly differently, so a pack sounds different depending on who
// rendered it.
//
// # Always PCM
//
// Every backend here can emit signed 16-bit little-endian mono PCM, so nothing
// in this program decodes MP3 and there is no audio codec dependency. Polly
// offers 8000 and 16000 natively — exactly the two rates a pack needs.
//
// # Nothing here runs on a call path
//
// Prompts are rendered once on a workstation and committed as WAVs; the Pi
// does no synthesis (CLAUDE.md invariant 7). This package is build-time tooling
// that happens to live in the same binary, and the daemon never calls it.
//
// # Transport is not the pattern
//
// A backend need not be HTTP. Piper is a subprocess, and wrapping it in a
// local web service to look like the others would buy a symmetry the interface
// never asked for at the cost of a daemon to run and a port to manage. Exec
// covers everything from `say` to a curl script pointing at a TTS server on
// another machine.
package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Renderer turns one prompt's text into audio.
type Renderer interface {
	Render(ctx context.Context, text string, v Voice) (Audio, error)
}

// A Voice names what to render with. ID is backend-specific — a piper model
// name, an ElevenLabs voice id, "Joanna", "nova".
//
// Settings carries per-backend knobs (stability, speed, engine) as strings so
// a new backend's options need no change to the pack format, and so the whole
// thing hashes cleanly for the render cache.
type Voice struct {
	ID       string
	Settings map[string]string
}

func (v Voice) setting(key, fallback string) string {
	if s, ok := v.Settings[key]; ok && s != "" {
		return s
	}
	return fallback
}

// Audio is always signed 16-bit little-endian mono.
type Audio struct {
	PCM        []byte
	SampleRate int
}

// Samples is the number of frames, useful for duration and for asserting a
// backend returned something plausible.
func (a Audio) Samples() int { return len(a.PCM) / 2 }

// Duration of the clip.
func (a Audio) Duration() time.Duration {
	if a.SampleRate == 0 {
		return 0
	}
	return time.Duration(a.Samples()) * time.Second / time.Duration(a.SampleRate)
}

// A Lister can enumerate the voices available to it. Optional: a backend that
// cannot introspect simply does not implement it.
type Lister interface {
	Voices(ctx context.Context) ([]Info, error)
}

// Info describes one available voice.
type Info struct {
	ID       string
	Name     string
	Language string
	Note     string
}

// An Estimator reports what rendering would cost before it is spent.
type Estimator interface {
	Estimate(texts []string) Cost
}

// Cost is characters and requests, never money.
//
// Vendor pricing changes and a hardcoded rate becomes a lie sitting in the
// repository. Characters are what all three paid backends bill on, so this is
// the number to multiply against whatever plan you are actually on.
type Cost struct {
	Characters int
	Requests   int
	Free       bool
	Note       string
}

// Config carries everything any backend might need. Fields not relevant to the
// chosen backend are ignored.
type Config struct {
	// APIKey for the paid backends. Empty means read the backend's usual
	// environment variable via Env.
	APIKey string
	// Region for Polly, e.g. "us-east-1".
	Region string
	// Model overrides the backend's default model.
	Model string
	// Command overrides the executable for piper and exec backends.
	Command string
	// Args are extra arguments for the exec backend.
	Args []string
	// SampleRate requests a rate from backends that offer a choice. 0 lets
	// the backend pick its best.
	SampleRate int
	// HTTP is the client for network backends. Nil means a sensible default.
	HTTP *http.Client
	// Env reads credentials. Nil means os.Getenv. Injected so tests never
	// depend on the developer's environment.
	Env func(string) string
	// Endpoint overrides the service URL, for testing and for
	// compatible third-party gateways.
	Endpoint string
}

func (c Config) env(key string) string {
	if c.Env != nil {
		return c.Env(key)
	}
	return osGetenv(key)
}

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// Generous: a long prompt through a slow model is not an error.
	return &http.Client{Timeout: 120 * time.Second}
}

// factory builds one backend.
type factory func(Config) (Renderer, error)

// backends is an explicit map rather than init()-time self-registration.
// Backends live in this tree regardless — Go plugins do not survive a Pi
// cross-compile — so a map you can grep beats indirection that hides the list.
var backends = map[string]factory{
	"piper":      newPiper,
	"exec":       newExec,
	"elevenlabs": newElevenLabs,
	"openai":     newOpenAI,
	"polly":      newPolly,
}

// Backends lists what New accepts, sorted.
func Backends() []string {
	names := make([]string, 0, len(backends))
	for n := range backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// New builds a backend by name.
func New(name string, cfg Config) (Renderer, error) {
	f, ok := backends[name]
	if !ok {
		return nil, fmt.Errorf("voice: unknown backend %q (have %s)",
			name, strings.Join(Backends(), ", "))
	}
	return f(cfg)
}

// ErrNoCredentials is returned when a paid backend has no key. Checked by the
// pack builder so it can say which environment variable to set rather than
// letting the vendor answer 401.
var ErrNoCredentials = errors.New("voice: no credentials")

// ── WAV ─────────────────────────────────────────────────────────────────

// decodeWAV extracts PCM and sample rate from a RIFF/WAVE file.
//
// Needed because piper writes a WAV rather than raw samples, and because the
// sample rate has to come from the file rather than being assumed: piper's
// medium models are 22.05 kHz and its low ones 16 kHz, and guessing wrong
// makes every prompt play at the wrong pitch.
func decodeWAV(b []byte) (Audio, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return Audio{}, errors.New("voice: not a RIFF/WAVE file")
	}

	var (
		rate, channels, bits int
		data                 []byte
		haveFmt              bool
	)

	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		if size < 0 || body+size > len(b) {
			// A truncated final chunk: take what is there rather than
			// failing, since a short clip is more useful than an error.
			size = len(b) - body
		}

		switch id {
		case "fmt ":
			if size < 16 {
				return Audio{}, errors.New("voice: short fmt chunk")
			}
			channels = int(binary.LittleEndian.Uint16(b[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(b[body+14 : body+16]))
			haveFmt = true
		case "data":
			data = b[body : body+size]
		}

		off = body + size
		if size%2 == 1 {
			off++ // chunks are word-aligned
		}
	}

	if !haveFmt {
		return Audio{}, errors.New("voice: no fmt chunk")
	}
	if data == nil {
		return Audio{}, errors.New("voice: no data chunk")
	}
	if bits != 16 {
		return Audio{}, fmt.Errorf("voice: %d-bit audio, want 16", bits)
	}
	if channels != 1 {
		return Audio{}, fmt.Errorf("voice: %d channels, want mono", channels)
	}
	return Audio{PCM: data, SampleRate: rate}, nil
}

// intSetting reads a numeric knob, falling back when absent or unparseable.
func intSetting(v Voice, key string, fallback int) int {
	if n, err := strconv.Atoi(v.setting(key, "")); err == nil {
		return n
	}
	return fallback
}

// countChars is the billing unit for every paid backend here.
func countChars(texts []string) (chars int) {
	for _, t := range texts {
		chars += len([]rune(t))
	}
	return chars
}
