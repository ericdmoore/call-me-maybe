// Package stt turns a recording into text, off the call path. It is the
// narrow package s04 asked for: one method, and a backend addressed by URL.
// Nothing under internal/lobby may import it (asserted by test in
// cmd/doorman): a speech service never stands between a call and anything.
//
// The one backend speaks the OpenAI-compatible shape every self-hosted
// Whisper server offers (whisper.cpp's server, faster-whisper-server,
// speaches): multipart POST with `file` and `model`, JSON `{"text": …}`
// back. No key: the service is on the tailnet, and a key on the box is the
// thing the rules keep off it.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Transcriber is audio in, text out.
type Transcriber interface {
	Transcribe(ctx context.Context, filename string, audio []byte) (string, error)
}

// Whisper is the HTTP backend.
type Whisper struct {
	Endpoint string // e.g. http://alpaca:9000/v1/audio/transcriptions
	Model    string // sent as `model`; empty lets the server choose
	Timeout  time.Duration
	Client   *http.Client
}

// Transcribe posts the audio and returns the trimmed text. An empty
// transcript is an error: the caller treats "heard nothing" as a retry,
// not a name.
func (w *Whisper) Transcribe(ctx context.Context, filename string, audio []byte) (string, error) {
	if w.Endpoint == "" {
		return "", errors.New("stt: no endpoint")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio); err != nil {
		return "", err
	}
	if w.Model != "" {
		_ = mw.WriteField("model", w.Model)
	}
	_ = mw.WriteField("response_format", "json")
	if err := mw.Close(); err != nil {
		return "", err
	}

	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("stt: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("stt: %s answered %d", hostOf(w.Endpoint), resp.StatusCode)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("stt: %s answered something other than JSON text", hostOf(w.Endpoint))
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return "", errors.New("stt: empty transcript")
	}
	return text, nil
}

// hostOf is the endpoint without its path, for messages: the URL is
// configuration, and errors are logged.
func hostOf(endpoint string) string {
	rest := endpoint
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
