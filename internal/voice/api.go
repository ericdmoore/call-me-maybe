package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/awssig"
)

// maxBody caps what an error path reads back. A vendor answering with a
// gigabyte of HTML should produce a short error, not exhaust memory.
const maxBody = 1 << 20

// do sends a request and returns the body, converting a non-2xx into an error
// that quotes the status and a snippet.
//
// The snippet is why this is one function rather than three: an error must
// carry enough to diagnose a bad voice id or an exhausted quota, and must
// never carry the API key. Every backend goes through here so there is one
// place to be sure of that.
//
// secrets are scrubbed from that snippet. Not being careful with our own
// formatting is not enough: a vendor that echoes the request back — and one
// tested here does, in its 401 body — sends the key home inside the response,
// from where it would land in an error, a log, and a pasted terminal buffer.
func do(client *http.Client, req *http.Request, vendor string, secrets ...string) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voice: %s: %w", vendor, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("voice: %s: reading response: %w", vendor, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("voice: %s returned %s: %s",
			vendor, resp.Status, trimErr(scrub(string(body), secrets...)))
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("voice: %s returned %s with no audio", vendor, resp.Status)
	}
	return body, nil
}

// scrub removes credentials from anything about to be shown to a human.
func scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}

func postJSON(ctx context.Context, url string, payload any) (*http.Request, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("voice: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("voice: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, body, nil
}

// ── ElevenLabs ──────────────────────────────────────────────────────────

type elevenLabs struct {
	key      string
	model    string
	endpoint string
	rate     int
	client   *http.Client
}

func newElevenLabs(cfg Config) (Renderer, error) {
	key := cfg.APIKey
	if key == "" {
		key = cfg.env("ELEVENLABS_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("%w: set ELEVENLABS_API_KEY", ErrNoCredentials)
	}
	model := cfg.Model
	if model == "" {
		model = "eleven_multilingual_v2"
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://api.elevenlabs.io"
	}
	// 16 kHz is one of the two rates a pack needs and halves cleanly to the
	// other, so it is the rate that costs the least fidelity in conversion.
	rate := cfg.SampleRate
	if rate == 0 {
		rate = 16000
	}
	return &elevenLabs{key: key, model: model, endpoint: endpoint, rate: rate, client: cfg.client()}, nil
}

func (e *elevenLabs) Render(ctx context.Context, text string, v Voice) (Audio, error) {
	if v.ID == "" {
		return Audio{}, fmt.Errorf("voice: elevenlabs needs a voice id")
	}

	type settings struct {
		Stability       float64 `json:"stability,omitempty"`
		SimilarityBoost float64 `json:"similarity_boost,omitempty"`
		Style           float64 `json:"style,omitempty"`
	}
	payload := struct {
		Text     string   `json:"text"`
		ModelID  string   `json:"model_id"`
		Settings settings `json:"voice_settings,omitempty"`
	}{
		Text:    text,
		ModelID: v.setting("model", e.model),
		Settings: settings{
			Stability:       floatSetting(v, "stability", 0),
			SimilarityBoost: floatSetting(v, "similarity_boost", 0),
			Style:           floatSetting(v, "style", 0),
		},
	}

	rate := e.rate
	if n := intSetting(v, "sample_rate", 0); n > 0 {
		rate = n
	}
	url := fmt.Sprintf("%s/v1/text-to-speech/%s?output_format=pcm_%d", e.endpoint, v.ID, rate)

	req, _, err := postJSON(ctx, url, payload)
	if err != nil {
		return Audio{}, err
	}
	// Their auth header, not Bearer.
	req.Header.Set("xi-api-key", e.key)
	req.Header.Set("Accept", "audio/pcm")

	body, err := do(e.client, req, "elevenlabs", e.key)
	if err != nil {
		return Audio{}, err
	}
	return Audio{PCM: body, SampleRate: rate}, nil
}

func (e *elevenLabs) Estimate(texts []string) Cost {
	return Cost{Characters: countChars(texts), Requests: len(texts),
		Note: "ElevenLabs bills per character; check your plan's included quota"}
}

func (e *elevenLabs) Voices(ctx context.Context) ([]Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.endpoint+"/v1/voices", nil)
	if err != nil {
		return nil, fmt.Errorf("voice: %w", err)
	}
	req.Header.Set("xi-api-key", e.key)

	body, err := do(e.client, req, "elevenlabs", e.key)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Voices []struct {
			VoiceID string `json:"voice_id"`
			Name    string `json:"name"`
			Labels  struct {
				Accent string `json:"accent"`
			} `json:"labels"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("voice: elevenlabs voice list: %w", err)
	}
	out := make([]Info, 0, len(payload.Voices))
	for _, v := range payload.Voices {
		out = append(out, Info{ID: v.VoiceID, Name: v.Name, Note: v.Labels.Accent})
	}
	return out, nil
}

// ── OpenAI ──────────────────────────────────────────────────────────────

// openAI uses /v1/audio/speech. Whisper is the other direction — speech to
// text — and is not what renders a prompt.
type openAI struct {
	key      string
	model    string
	endpoint string
	client   *http.Client
}

// openAIRate is fixed by the API: response_format "pcm" is 24 kHz 16-bit mono.
// Not configurable, so it is a constant rather than a setting that would
// silently do nothing.
const openAIRate = 24000

func newOpenAI(cfg Config) (Renderer, error) {
	key := cfg.APIKey
	if key == "" {
		key = cfg.env("OPENAI_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("%w: set OPENAI_API_KEY", ErrNoCredentials)
	}
	model := cfg.Model
	if model == "" {
		model = "gpt-4o-mini-tts"
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com"
	}
	return &openAI{key: key, model: model, endpoint: endpoint, client: cfg.client()}, nil
}

func (o *openAI) Render(ctx context.Context, text string, v Voice) (Audio, error) {
	id := v.ID
	if id == "" {
		id = "alloy"
	}

	payload := struct {
		Model        string  `json:"model"`
		Input        string  `json:"input"`
		Voice        string  `json:"voice"`
		Format       string  `json:"response_format"`
		Speed        float64 `json:"speed,omitempty"`
		Instructions string  `json:"instructions,omitempty"`
	}{
		Model:  v.setting("model", o.model),
		Input:  text,
		Voice:  id,
		Format: "pcm",
		Speed:  floatSetting(v, "speed", 0),
		// Voice direction in words — "weary night porter, unhurried" — which
		// is the closest thing here to directing an actor.
		Instructions: v.setting("instructions", ""),
	}

	req, _, err := postJSON(ctx, o.endpoint+"/v1/audio/speech", payload)
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.key)

	body, err := do(o.client, req, "openai", o.key)
	if err != nil {
		return Audio{}, err
	}
	return Audio{PCM: body, SampleRate: openAIRate}, nil
}

func (o *openAI) Estimate(texts []string) Cost {
	return Cost{Characters: countChars(texts), Requests: len(texts),
		Note: "OpenAI bills per character of input"}
}

// ── AWS Polly ───────────────────────────────────────────────────────────

type polly struct {
	creds    awssig.Credentials
	signer   awssig.Signer
	endpoint string
	engine   string
	rate     int
	client   *http.Client
}

func newPolly(cfg Config) (Renderer, error) {
	creds := awssig.Credentials{
		AccessKeyID:     cfg.env("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: cfg.env("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    cfg.env("AWS_SESSION_TOKEN"),
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("%w: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY", ErrNoCredentials)
	}

	region := cfg.Region
	if region == "" {
		region = cfg.env("AWS_REGION")
	}
	if region == "" {
		region = cfg.env("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://polly." + region + ".amazonaws.com"
	}

	// Polly offers 8000 and 16000 for PCM — exactly the two rates a pack
	// needs, so nothing here is ever resampled from an awkward ratio.
	rate := cfg.SampleRate
	if rate == 0 {
		rate = 16000
	}

	return &polly{
		creds:    creds,
		signer:   awssig.Signer{Region: region, Service: "polly"},
		endpoint: endpoint,
		engine:   "neural",
		rate:     rate,
		client:   cfg.client(),
	}, nil
}

func (p *polly) Render(ctx context.Context, text string, v Voice) (Audio, error) {
	id := v.ID
	if id == "" {
		return Audio{}, fmt.Errorf("voice: polly needs a VoiceId, e.g. Amy or Joanna")
	}

	rate := p.rate
	if n := intSetting(v, "sample_rate", 0); n > 0 {
		rate = n
	}
	if rate != 8000 && rate != 16000 {
		return Audio{}, fmt.Errorf("voice: polly PCM is 8000 or 16000 Hz, not %d", rate)
	}

	payload := struct {
		Text         string `json:"Text"`
		TextType     string `json:"TextType,omitempty"`
		VoiceId      string `json:"VoiceId"` //nolint:revive // AWS field name
		OutputFormat string `json:"OutputFormat"`
		SampleRate   string `json:"SampleRate"`
		Engine       string `json:"Engine,omitempty"`
		LanguageCode string `json:"LanguageCode,omitempty"`
	}{
		Text:         text,
		TextType:     v.setting("text_type", ""), // "ssml" to hand-tune pacing
		VoiceId:      id,
		OutputFormat: "pcm",
		SampleRate:   strconv.Itoa(rate),
		Engine:       v.setting("engine", p.engine),
		LanguageCode: v.setting("language", ""),
	}

	req, body, err := postJSON(ctx, p.endpoint+"/v1/speech", payload)
	if err != nil {
		return Audio{}, err
	}
	// The body must be signed, so it is passed rather than re-read: reading
	// req.Body here would consume it before the transport sees it.
	if err := p.signer.Sign(req, body, p.creds, time.Now()); err != nil {
		return Audio{}, fmt.Errorf("voice: polly: %w", err)
	}

	pcm, err := do(p.client, req, "polly", p.creds.SecretAccessKey, p.creds.SessionToken)
	if err != nil {
		return Audio{}, err
	}
	return Audio{PCM: pcm, SampleRate: rate}, nil
}

func (p *polly) Estimate(texts []string) Cost {
	return Cost{Characters: countChars(texts), Requests: len(texts),
		Note: "Polly bills per character; neural costs more than standard"}
}

func (p *polly) Voices(ctx context.Context) ([]Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/v1/voices", nil)
	if err != nil {
		return nil, fmt.Errorf("voice: %w", err)
	}
	if err := p.signer.Sign(req, nil, p.creds, time.Now()); err != nil {
		return nil, fmt.Errorf("voice: polly: %w", err)
	}

	body, err := do(p.client, req, "polly", p.creds.SecretAccessKey, p.creds.SessionToken)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Voices []struct {
			ID           string   `json:"Id"`
			Name         string   `json:"Name"`
			LanguageCode string   `json:"LanguageCode"`
			Gender       string   `json:"Gender"`
			Engines      []string `json:"SupportedEngines"`
		} `json:"Voices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("voice: polly voice list: %w", err)
	}
	out := make([]Info, 0, len(payload.Voices))
	for _, v := range payload.Voices {
		out = append(out, Info{
			ID: v.ID, Name: v.Name, Language: v.LanguageCode,
			Note: v.Gender + " · " + strings.Join(v.Engines, "/"),
		})
	}
	return out, nil
}

func floatSetting(v Voice, key string, fallback float64) float64 {
	if f, err := strconv.ParseFloat(v.setting(key, ""), 64); err == nil {
		return f
	}
	return fallback
}
