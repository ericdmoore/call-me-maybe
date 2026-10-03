package ari

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The REST surface the daemon and the curfew keeper use, against a fake
// Asterisk: paths, methods, query strings and the JSON shapes we decode.
func TestRESTSurfaceAgainstAFakeAsterisk(t *testing.T) {
	var hungUp []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "doorman" || p != "pw" {
			http.Error(w, "unauthorized", 401)
			return
		}
		// The client speaks to Asterisk's /ari prefix; the fake strips it.
		path := strings.TrimPrefix(r.URL.Path, "/ari")
		switch {
		case r.Method == http.MethodGet && path == "/asterisk/info":
			json.NewEncoder(w).Encode(map[string]any{"system": map[string]string{"version": "20.5.0"}})
		case r.Method == http.MethodGet && path == "/asterisk/variable":
			if r.URL.Query().Get("variable") != "DND_norah" {
				http.Error(w, "wrong variable", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"value": "1790996506"})
		case r.Method == http.MethodGet && path == "/channels":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "c1", "name": "PJSIP/norah-00000012", "state": "Up"},
				{"id": "c2", "name": "PJSIP/kitchen-00000013", "state": "Ring"},
			})
		case r.Method == http.MethodDelete && path == "/channels/c1":
			hungUp = append(hungUp, "c1")
			w.WriteHeader(204)
		case r.Method == http.MethodGet && path == "/endpoints":
			json.NewEncoder(w).Encode([]map[string]any{{"technology": "PJSIP", "resource": "norah", "state": "online", "channel_ids": []string{"c1"}}})
		case r.Method == http.MethodGet && path == "/endpoints/PJSIP/norah":
			json.NewEncoder(w).Encode(map[string]any{"technology": "PJSIP", "resource": "norah", "state": "online"})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
		}
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, Username: "doorman", Password: "pw", App: "doorman"})
	ctx := context.Background()

	if v, err := c.Ping(ctx); err != nil || v != "20.5.0" {
		t.Errorf("Ping = %q, %v", v, err)
	}
	if v, err := c.Variable(ctx, "DND_norah"); err != nil || v != "1790996506" {
		t.Errorf("Variable = %q, %v", v, err)
	}
	chans, err := c.Channels(ctx)
	if err != nil || len(chans) != 2 || chans[0].Name != "PJSIP/norah-00000012" || chans[1].ID != "c2" {
		t.Fatalf("Channels = %+v, %v", chans, err)
	}
	if err := c.Hangup(ctx, "c1"); err != nil || len(hungUp) != 1 {
		t.Errorf("Hangup: %v, %v", err, hungUp)
	}
	eps, err := c.Endpoints(ctx)
	if err != nil || len(eps) != 1 || eps[0].Resource != "norah" || eps[0].State != "online" || len(eps[0].ChannelIDs) != 1 {
		t.Errorf("Endpoints = %+v, %v", eps, err)
	}
	if ep, err := c.Endpoint(ctx, "PJSIP", "norah"); err != nil || ep.State != "online" {
		t.Errorf("Endpoint = %+v, %v", ep, err)
	}
	// A wrong password is an error, not a silent empty answer.
	bad := New(Options{BaseURL: srv.URL, Username: "doorman", Password: "nope"})
	if _, err := bad.Channels(ctx); err == nil {
		t.Error("401 must be an error")
	}
}
