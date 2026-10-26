package ari

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGameMediaREST(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Query().Get("direction") != "" && r.URL.Query().Get("direction") != "in" {
			t.Error("wrong mute direction")
		}
		if r.URL.Query().Get("media") != "" && r.URL.Query().Get("media") != "sound:games/test/prompt" {
			t.Error("wrong bridge media")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c := New(Options{BaseURL: server.URL, Username: "local", Password: "test", App: "game"})
	defer c.Close()
	ctx := context.Background()
	for _, err := range []error{c.Answer(ctx, "a"), c.MuteInput(ctx, "a", true), c.MuteInput(ctx, "a", false), c.PlayBridge(ctx, "room", "sound:games/test/prompt", "clip")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"POST /ari/channels/a/answer", "POST /ari/channels/a/mute", "DELETE /ari/channels/a/mute", "POST /ari/bridges/room/play/clip"}
	for i, call := range calls {
		if call != want[i] {
			t.Fatalf("unexpected game media call %s", call)
		}
	}
}
