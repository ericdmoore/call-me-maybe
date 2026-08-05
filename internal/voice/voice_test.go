package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A key that would be unmistakable if it ever leaked into an error.
const testKey = "sk-DO-NOT-LEAK-THIS-abc123"

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// wav builds a minimal RIFF/WAVE file around some samples.
func wav(rate int, samples []int16) []byte {
	var pcm bytes.Buffer
	for _, s := range samples {
		_ = binary.Write(&pcm, binary.LittleEndian, s)
	}
	data := pcm.Bytes()

	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// ── the registry ────────────────────────────────────────────────────────

func TestAllFourBackendsAreRegistered(t *testing.T) {
	got := strings.Join(Backends(), ",")
	for _, want := range []string{"piper", "elevenlabs", "openai", "polly", "exec"} {
		if !strings.Contains(got, want) {
			t.Errorf("backend %q is not registered, have %s", want, got)
		}
	}
}

func TestUnknownBackendNamesTheRealOnes(t *testing.T) {
	_, err := New("festival", Config{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "polly") {
		t.Errorf("the error should list what is available, got: %v", err)
	}
}

// A paid backend with no key must say which variable to set rather than
// letting the vendor answer 401 an hour later.
func TestMissingCredentialsAreNamed(t *testing.T) {
	for _, c := range []struct{ backend, wantVar string }{
		{"elevenlabs", "ELEVENLABS_API_KEY"},
		{"openai", "OPENAI_API_KEY"},
		{"polly", "AWS_ACCESS_KEY_ID"},
	} {
		_, err := New(c.backend, Config{Env: env(nil)})
		if !errors.Is(err, ErrNoCredentials) {
			t.Errorf("%s: want ErrNoCredentials, got %v", c.backend, err)
		}
		if !strings.Contains(err.Error(), c.wantVar) {
			t.Errorf("%s: error should name %s, got %v", c.backend, c.wantVar, err)
		}
	}
}

// ── WAV decoding ────────────────────────────────────────────────────────

func TestDecodeWAVReadsTheRateFromTheFile(t *testing.T) {
	for _, rate := range []int{8000, 16000, 22050, 24000} {
		a, err := decodeWAV(wav(rate, []int16{0, 1000, -1000, 0}))
		if err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
		if a.SampleRate != rate {
			t.Errorf("rate = %d, want %d — an assumed rate plays every prompt at the wrong pitch", a.SampleRate, rate)
		}
		if a.Samples() != 4 {
			t.Errorf("%d Hz: %d samples, want 4", rate, a.Samples())
		}
	}
}

func TestDecodeWAVRejectsWhatItCannotPlay(t *testing.T) {
	for _, c := range []struct {
		name string
		b    []byte
	}{
		{"not a wav", []byte("this is an mp3, honest")},
		{"truncated", wav(16000, []int16{1})[:8]},
		{"empty", nil},
	} {
		if _, err := decodeWAV(c.b); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestAudioDuration(t *testing.T) {
	a := Audio{PCM: make([]byte, 16000*2), SampleRate: 16000}
	if got := a.Duration().Seconds(); got != 1 {
		t.Errorf("duration = %v, want 1s", got)
	}
	if (Audio{}).Duration() != 0 {
		t.Error("a zero Audio must not divide by zero")
	}
}

// ── exec and piper ──────────────────────────────────────────────────────

// stub writes a shell script standing in for a TTS binary.
func stub(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	path := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// bytesFile writes exact bytes to a temp file and returns its path, so a
// shell stub can emit them with cat rather than through printf escapes that
// differ between bash and dash.
func bytesFile(t *testing.T, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pcm.raw")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecBackendTakesTextOnStdinAndPCMOnStdout(t *testing.T) {
	// Four samples of raw PCM, written from Go and cat'd by the stub.
	//
	// Not printf with \x escapes: that is a bash extension, and on a runner
	// where /bin/sh is dash it emits the literal characters instead of bytes.
	// The test then passes on macOS and fails on Linux.
	sh := stub(t, "cat > /dev/null; cat "+bytesFile(t, []byte{0, 0, 0x10, 0, 0x20, 0, 0x30, 0}))

	r, err := New("exec", Config{Command: sh, SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Render(context.Background(), "Good day.", Voice{})
	if err != nil {
		t.Fatal(err)
	}
	if a.SampleRate != 16000 || a.Samples() != 4 {
		t.Errorf("got %d samples at %d Hz", a.Samples(), a.SampleRate)
	}
}

// The whole point of exec: whatever the command writes, if it is a WAV we
// read its header rather than needing to be told the rate.
func TestExecBackendDetectsAWAVFromTheCommand(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(out, wav(22050, []int16{1, 2, 3}), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New("exec", Config{Command: stub(t, "cat > /dev/null; cat "+out), SampleRate: 8000})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Render(context.Background(), "x", Voice{})
	if err != nil {
		t.Fatal(err)
	}
	if a.SampleRate != 22050 {
		t.Errorf("rate = %d; a WAV's own header must win over the configured rate", a.SampleRate)
	}
}

func TestExecBackendReportsFailure(t *testing.T) {
	r, err := New("exec", Config{Command: stub(t, "echo 'model missing' >&2; exit 3")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Render(context.Background(), "x", Voice{})
	if err == nil {
		t.Fatal("a failing command must be an error")
	}
	if !strings.Contains(err.Error(), "model missing") {
		t.Errorf("stderr should reach the error, got: %v", err)
	}
}

func TestExecBackendNeedsACommand(t *testing.T) {
	if _, err := New("exec", Config{}); err == nil {
		t.Error("expected an error with no command")
	}
}

func TestPiperRendersThroughItsOutputFile(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "en_GB-alba-medium.onnx")
	if err := os.WriteFile(model, []byte("not a real model"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src.wav")
	if err := os.WriteFile(src, wav(22050, []int16{5, 6, 7, 8}), 0o600); err != nil {
		t.Fatal(err)
	}

	// piper --model X --output_file Y  → $4 is the output path.
	sh := stub(t, `cat > /dev/null; cp `+src+` "$4"`)

	r, err := New("piper", Config{Command: sh, Env: env(map[string]string{"PIPER_VOICE_DIR": dir})})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Render(context.Background(), "Good day.", Voice{ID: "en_GB-alba-medium"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SampleRate != 22050 || a.Samples() != 4 {
		t.Errorf("got %d samples at %d Hz", a.Samples(), a.SampleRate)
	}
}

func TestPiperSaysWhichModelIsMissing(t *testing.T) {
	r, err := New("piper", Config{Command: "true", Env: env(map[string]string{"PIPER_VOICE_DIR": t.TempDir()})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Render(context.Background(), "x", Voice{ID: "en_GB-nonexistent"})
	if err == nil || !strings.Contains(err.Error(), "en_GB-nonexistent") {
		t.Errorf("the error must name the model, got: %v", err)
	}
}

func TestPiperIsFree(t *testing.T) {
	r, _ := New("piper", Config{Command: "true", Env: env(nil)})
	e, ok := r.(Estimator)
	if !ok {
		t.Fatal("piper should estimate")
	}
	if c := e.Estimate([]string{"Good day."}); !c.Free {
		t.Error("piper runs locally and must report free — it is why the bundled pack needs no account")
	}
}

// ── the network backends ────────────────────────────────────────────────

type capture struct {
	path, method, auth, xiKey, body string
	query                           string
}

// apiServer answers with pcm and records what it was asked.
func apiServer(t *testing.T, pcm []byte, status int) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.path, c.method, c.body = r.URL.Path, r.Method, string(b)
		c.query = r.URL.RawQuery
		c.auth, c.xiKey = r.Header.Get("Authorization"), r.Header.Get("xi-api-key")
		w.WriteHeader(status)
		_, _ = w.Write(pcm)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func pcmBytes(n int) []byte { return make([]byte, n*2) }

func TestElevenLabsRequestShape(t *testing.T) {
	srv, c := apiServer(t, pcmBytes(8), 200)
	r, err := New("elevenlabs", Config{APIKey: testKey, Endpoint: srv.URL, HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	a, err := r.Render(context.Background(), "Good day.",
		Voice{ID: "voice-abc", Settings: map[string]string{"stability": "0.4"}})
	if err != nil {
		t.Fatal(err)
	}

	if c.path != "/v1/text-to-speech/voice-abc" {
		t.Errorf("path = %s", c.path)
	}
	if !strings.Contains(c.query, "output_format=pcm_16000") {
		t.Errorf("must request PCM so nothing here decodes MP3, got query %q", c.query)
	}
	if c.xiKey != testKey {
		t.Errorf("ElevenLabs uses xi-api-key, not Bearer; got header %q", c.xiKey)
	}
	if !strings.Contains(c.body, `"stability":0.4`) {
		t.Errorf("per-voice settings did not reach the body: %s", c.body)
	}
	if a.SampleRate != 16000 || a.Samples() != 8 {
		t.Errorf("got %d samples at %d Hz", a.Samples(), a.SampleRate)
	}
}

func TestOpenAIRequestShape(t *testing.T) {
	srv, c := apiServer(t, pcmBytes(12), 200)
	r, err := New("openai", Config{APIKey: testKey, Endpoint: srv.URL, HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	a, err := r.Render(context.Background(), "Good day.",
		Voice{ID: "nova", Settings: map[string]string{"instructions": "weary night porter"}})
	if err != nil {
		t.Fatal(err)
	}

	// The speech endpoint, emphatically not transcriptions.
	if c.path != "/v1/audio/speech" {
		t.Errorf("path = %s, want /v1/audio/speech (whisper is the other direction)", c.path)
	}
	if c.auth != "Bearer "+testKey {
		t.Errorf("auth = %q", c.auth)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["response_format"] != "pcm" {
		t.Errorf("response_format = %v, want pcm", body["response_format"])
	}
	if body["voice"] != "nova" {
		t.Errorf("voice = %v", body["voice"])
	}
	if body["instructions"] != "weary night porter" {
		t.Errorf("instructions did not reach the request: %v", body["instructions"])
	}
	if a.SampleRate != openAIRate {
		t.Errorf("rate = %d, want %d — the API fixes PCM at 24 kHz", a.SampleRate, openAIRate)
	}
}

func TestPollyRequestShapeAndSignature(t *testing.T) {
	srv, c := apiServer(t, pcmBytes(16), 200)
	r, err := New("polly", Config{
		Endpoint: srv.URL, HTTP: srv.Client(), Region: "us-east-1",
		Env: env(map[string]string{
			"AWS_ACCESS_KEY_ID":     "AKIDEXAMPLE",
			"AWS_SECRET_ACCESS_KEY": testKey,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	a, err := r.Render(context.Background(), "Good day.", Voice{ID: "Amy"})
	if err != nil {
		t.Fatal(err)
	}

	if c.path != "/v1/speech" {
		t.Errorf("path = %s", c.path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["OutputFormat"] != "pcm" {
		t.Errorf("OutputFormat = %v, want pcm", body["OutputFormat"])
	}
	// Polly offers exactly the two rates a pack needs.
	if body["SampleRate"] != "16000" {
		t.Errorf("SampleRate = %v", body["SampleRate"])
	}
	if body["VoiceId"] != "Amy" {
		t.Errorf("VoiceId = %v", body["VoiceId"])
	}

	// The request must actually be signed, and the secret must not be the
	// thing that is sent.
	if !strings.HasPrefix(c.auth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("polly request was not SigV4 signed: %q", c.auth)
	}
	if !strings.Contains(c.auth, "Credential=AKIDEXAMPLE/") {
		t.Errorf("credential scope missing from %q", c.auth)
	}
	if strings.Contains(c.auth, testKey) {
		t.Fatalf("the secret key is in the Authorization header: %q", c.auth)
	}
	if a.SampleRate != 16000 || a.Samples() != 16 {
		t.Errorf("got %d samples at %d Hz", a.Samples(), a.SampleRate)
	}
}

func TestPollyRefusesARateItCannotProduce(t *testing.T) {
	srv, _ := apiServer(t, nil, 200)
	r, _ := New("polly", Config{
		Endpoint: srv.URL, HTTP: srv.Client(),
		Env: env(map[string]string{"AWS_ACCESS_KEY_ID": "AKIDEXAMPLE", "AWS_SECRET_ACCESS_KEY": "x"}),
	})
	_, err := r.Render(context.Background(), "x",
		Voice{ID: "Amy", Settings: map[string]string{"sample_rate": "22050"}})
	if err == nil || !strings.Contains(err.Error(), "8000 or 16000") {
		t.Errorf("want a clear rate error, got %v", err)
	}
}

// The one that matters. An error travels into logs, bug reports and pasted
// terminal output; a key in one is a leaked key.
func TestAnAPIKeyNeverReachesAnError(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A vendor that unhelpfully echoes the request back.
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"bad key","sent":%q}`, r.Header.Get("Authorization"))
	}))
	defer fail.Close()

	for _, backend := range []string{"elevenlabs", "openai", "polly"} {
		cfg := Config{
			APIKey: testKey, Endpoint: fail.URL, HTTP: fail.Client(), Region: "us-east-1",
			Env: env(map[string]string{
				"AWS_ACCESS_KEY_ID": "AKIDEXAMPLE", "AWS_SECRET_ACCESS_KEY": testKey,
				"ELEVENLABS_API_KEY": testKey, "OPENAI_API_KEY": testKey,
			}),
		}
		r, err := New(backend, cfg)
		if err != nil {
			t.Fatalf("%s: %v", backend, err)
		}

		_, err = r.Render(context.Background(), "Good day.", Voice{ID: "Amy"})
		if err == nil {
			t.Fatalf("%s: a 401 must be an error", backend)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: the API key is in the error message: %v", backend, err)
		}
		// It still has to be diagnosable.
		if !strings.Contains(err.Error(), "401") {
			t.Errorf("%s: the error should carry the status: %v", backend, err)
		}
	}
}

func TestEmptyBodyIsAnErrorNotSilentSilence(t *testing.T) {
	srv, _ := apiServer(t, nil, 200)
	r, _ := New("openai", Config{APIKey: testKey, Endpoint: srv.URL, HTTP: srv.Client()})
	if _, err := r.Render(context.Background(), "x", Voice{ID: "nova"}); err == nil {
		t.Error("200 with no audio must be an error — a missing prompt degrades to silence on a call")
	}
}

func TestPaidBackendsEstimateInCharactersNotMoney(t *testing.T) {
	for _, backend := range []string{"elevenlabs", "openai", "polly"} {
		r, err := New(backend, Config{APIKey: testKey, Env: env(map[string]string{
			"AWS_ACCESS_KEY_ID": "a", "AWS_SECRET_ACCESS_KEY": "b",
		})})
		if err != nil {
			t.Fatal(err)
		}
		e, ok := r.(Estimator)
		if !ok {
			t.Fatalf("%s should estimate before spending", backend)
		}
		c := e.Estimate([]string{"Good day.", "Welcome."})
		if c.Characters != 17 { // "Good day." is 9, "Welcome." is 8
			t.Errorf("%s: characters = %d, want 17", backend, c.Characters)
		}
		if c.Requests != 2 {
			t.Errorf("%s: requests = %d, want 2", backend, c.Requests)
		}
		if c.Free {
			t.Errorf("%s: a paid backend must not report free", backend)
		}
		// No invented dollar figure: prices go stale, characters do not.
		if strings.Contains(c.Note, "$") {
			t.Errorf("%s: note quotes a price that will rot: %q", backend, c.Note)
		}
	}
}

func TestContextCancellationStopsARender(t *testing.T) {
	srv, _ := apiServer(t, pcmBytes(4), 200)
	r, _ := New("openai", Config{APIKey: testKey, Endpoint: srv.URL, HTTP: srv.Client()})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Render(ctx, "x", Voice{ID: "nova"}); err == nil {
		t.Error("a cancelled context must abort the render")
	}
}

// ── voice discovery ─────────────────────────────────────────────────────

// Listing is optional, but a backend that can introspect should, because
// "which voice ids are valid" is otherwise a trip to a web console.

func TestElevenLabsListsVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/voices" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"voices":[
			{"voice_id":"abc","name":"Alba","labels":{"accent":"british"}},
			{"voice_id":"def","name":"Reed","labels":{"accent":"american"}}]}`)
	}))
	defer srv.Close()

	r, _ := New("elevenlabs", Config{APIKey: testKey, Endpoint: srv.URL, HTTP: srv.Client()})
	l, ok := r.(Lister)
	if !ok {
		t.Fatal("elevenlabs should list")
	}
	vs, err := l.Voices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].ID != "abc" || vs[0].Name != "Alba" || vs[0].Note != "british" {
		t.Errorf("voices = %+v", vs)
	}
}

func TestPollyListsVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); !strings.HasPrefix(a, "AWS4-HMAC-SHA256 ") {
			t.Errorf("the voice list must be signed too: %q", a)
		}
		_, _ = io.WriteString(w, `{"Voices":[
			{"Id":"Amy","Name":"Amy","LanguageCode":"en-GB","Gender":"Female",
			 "SupportedEngines":["neural","standard"]}]}`)
	}))
	defer srv.Close()

	r, _ := New("polly", Config{Endpoint: srv.URL, HTTP: srv.Client(), Region: "us-east-1",
		Env: env(map[string]string{"AWS_ACCESS_KEY_ID": "AKIDEXAMPLE", "AWS_SECRET_ACCESS_KEY": "x"})})
	vs, err := r.(Lister).Voices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].ID != "Amy" || vs[0].Language != "en-GB" {
		t.Errorf("voices = %+v", vs)
	}
	if !strings.Contains(vs[0].Note, "neural") {
		t.Errorf("the engines matter when picking a voice: %q", vs[0].Note)
	}
}

func TestPiperListsInstalledModels(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"en_GB-alba-medium.onnx", "en_US-reed-low.onnx", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := New("piper", Config{Command: "true", Env: env(map[string]string{"PIPER_VOICE_DIR": dir})})
	vs, err := r.(Lister).Voices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("listed %d models, want 2 (the .txt is not a voice): %+v", len(vs), vs)
	}
	if vs[0].Language != "en_GB" {
		t.Errorf("language = %q, want en_GB", vs[0].Language)
	}
}

func TestListingUnreadableModelsIsAnError(t *testing.T) {
	r, _ := New("piper", Config{Command: "true",
		Env: env(map[string]string{"PIPER_VOICE_DIR": filepath.Join(t.TempDir(), "nope")})})
	if _, err := r.(Lister).Voices(context.Background()); err == nil {
		t.Error("a missing model directory should be reported, not silently empty")
	}
}

// ── remaining error paths ───────────────────────────────────────────────

func TestBackendsRejectAMissingVoiceWhereOneIsRequired(t *testing.T) {
	el, _ := New("elevenlabs", Config{APIKey: testKey})
	if _, err := el.Render(context.Background(), "x", Voice{}); err == nil {
		t.Error("elevenlabs puts the voice in the URL and cannot default it")
	}
	po, _ := New("polly", Config{Env: env(map[string]string{
		"AWS_ACCESS_KEY_ID": "a", "AWS_SECRET_ACCESS_KEY": "b"})})
	if _, err := po.Render(context.Background(), "x", Voice{}); err == nil {
		t.Error("polly needs a VoiceId")
	}
}

func TestUnreachableEndpointIsReported(t *testing.T) {
	r, _ := New("openai", Config{APIKey: testKey, Endpoint: "http://127.0.0.1:1"})
	if _, err := r.Render(context.Background(), "x", Voice{ID: "nova"}); err == nil {
		t.Error("a dead endpoint must be an error")
	}
}

func TestExecEstimatesFreeAndNamesTheCommand(t *testing.T) {
	r, _ := New("exec", Config{Command: "/usr/bin/say"})
	c := r.(Estimator).Estimate([]string{"Good day."})
	if !c.Free || !strings.Contains(c.Note, "say") {
		t.Errorf("cost = %+v", c)
	}
}

func TestExecRejectsHalfASample(t *testing.T) {
	r, _ := New("exec", Config{Command: stub(t, "cat > /dev/null; cat "+bytesFile(t, []byte{0, 0, 0x10}))})
	if _, err := r.Render(context.Background(), "x", Voice{}); err == nil {
		t.Error("an odd byte count is not whole 16-bit samples and must not be accepted")
	}
}

// $VOICE lets one command serve many voices without a wrapper script each.
func TestExecSubstitutesTheVoiceIntoArgs(t *testing.T) {
	r, err := New("exec", Config{
		Command:    stub(t, `cat > /dev/null; printf "$1" | tr -d '\n' | head -c 2`),
		Args:       []string{"$VOICE"},
		SampleRate: 8000,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Render(context.Background(), "x", Voice{ID: "ab"})
	if err != nil {
		t.Fatal(err)
	}
	if string(a.PCM) != "ab" {
		t.Errorf("$VOICE was not substituted: %q", a.PCM)
	}
}
