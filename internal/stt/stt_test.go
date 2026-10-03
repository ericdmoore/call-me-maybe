package stt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhisperPostsMultipartAndReadsText(t *testing.T) {
	var gotModel, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("no file part: %v", err)
		}
		b, _ := io.ReadAll(f)
		gotFile = hdr.Filename + ":" + string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text": "  Maddie from school \n"}`)
	}))
	defer srv.Close()
	w := &Whisper{Endpoint: srv.URL + "/v1/audio/transcriptions", Model: "small"}
	text, err := w.Transcribe(context.Background(), "norah-1.wav", []byte("RIFF..."))
	if err != nil || text != "Maddie from school" {
		t.Fatalf("Transcribe = %q, %v", text, err)
	}
	if gotModel != "small" || gotFile != "norah-1.wav:RIFF..." {
		t.Errorf("sent model=%q file=%q", gotModel, gotFile)
	}
}

func TestWhisperErrorsNameTheHostNeverTheText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model loading", 503)
	}))
	defer srv.Close()
	w := &Whisper{Endpoint: srv.URL + "/v1/audio/transcriptions"}
	_, err := w.Transcribe(context.Background(), "x.wav", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "/v1/") {
		t.Errorf("err = %v", err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"text":"   "}`) }))
	defer empty.Close()
	if _, err := (&Whisper{Endpoint: empty.URL}).Transcribe(context.Background(), "x.wav", []byte("x")); err == nil {
		t.Error("an empty transcript must be an error, so the caller retries rather than naming the entry nothing")
	}
	if _, err := (&Whisper{}).Transcribe(context.Background(), "x.wav", nil); err == nil {
		t.Error("no endpoint must be an error")
	}
}
