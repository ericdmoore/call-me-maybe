package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSTTSetsAndClearsTheEndpointWithoutARestart(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(env, []byte("ARI_USERNAME=x\n"), 0o600)
	out := capture(t, func() {
		if rc := runSTT([]string{"-env", env, "http://alpaca:9001"}); rc != 0 {
			t.Errorf("rc = %d", rc)
		}
	})
	b, _ := os.ReadFile(env)
	if !strings.Contains(string(b), "STT_ENDPOINT=http://alpaca:9001/v1/audio/transcriptions\n") || !strings.Contains(string(b), "ARI_USERNAME=x") {
		t.Errorf(".env = %q", b)
	}
	if !strings.Contains(out, "remote alpaca:9001") {
		t.Errorf("output = %q", out)
	}
	capture(t, func() {
		if rc := runSTT([]string{"-env", env, "none"}); rc != 0 {
			t.Errorf("rc = %d", rc)
		}
	})
	b, _ = os.ReadFile(env)
	if !strings.Contains(string(b), "STT_ENDPOINT=\n") {
		t.Errorf("after none: %q", b)
	}
	capture(t, func() {
		if rc := runSTT([]string{"-env", env, "ftp://nope"}); rc != 2 {
			t.Errorf("a non-http URL must be refused, rc = %d", rc)
		}
		if rc := runSTT([]string{"-env", env, "here"}); rc != 2 {
			t.Errorf("here is not shipped yet, rc = %d", rc)
		}
	})
}

func TestDescribeSTTProbesTheHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	up := describeSTT(secretsFrom(map[string]string{"STT_ENDPOINT": srv.URL + "/v1/audio/transcriptions"}), 2*time.Second)
	if !strings.Contains(up, "remote 127.0.0.1") || !strings.Contains(up, "answering") || strings.Contains(up, "/v1/") {
		t.Errorf("up = %q", up)
	}
	down := describeSTT(secretsFrom(map[string]string{"STT_ENDPOINT": "http://127.0.0.1:1/v1/audio/transcriptions"}), 300*time.Millisecond)
	if !strings.Contains(down, "not answering") {
		t.Errorf("down = %q", down)
	}
	if none := describeSTT(secretsFrom(map[string]string{}), time.Second); !strings.Contains(none, "none") {
		t.Errorf("none = %q", none)
	}
}
