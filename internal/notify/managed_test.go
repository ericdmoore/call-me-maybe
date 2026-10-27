package notify_test

import (
	"callmemaybe/internal/notify"
	"callmemaybe/internal/webhookconfig"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedReloadPostsFixedJSONWithoutLegacyCredentials(t *testing.T) {
	type received struct {
		Path, Auth, Content string
		Payload             notify.Event
	}
	got := make(chan received, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload notify.Event
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if r.Method != http.MethodPost {
			t.Error("not POST")
		}
		got <- received{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), payload}
	}))
	defer receiver.Close()
	path := filepath.Join(t.TempDir(), "webhook.toml")
	store := webhookconfig.OpenStore(path)
	hook, err := notify.Open(notify.Options{Token: "test-legacy-token", SendFullCallerID: true, Resolve: func() notify.Target {
		current := store.Current()
		if current.Managed {
			return notify.Target{URL: current.Config.URL(), NoRedirect: true}
		}
		return notify.Target{}
	}})
	if err != nil {
		t.Fatal(err)
	}
	hook.Post(notify.Event{Type: "ringing"}) // absent managed file: disabled target
	for _, prefix := range []string{"/first", "/second"} {
		os.WriteFile(path, webhookconfig.Encode(webhookconfig.Config{Domain: receiver.URL, PathPrefix: prefix}), 0600)
		if store.Reload() != nil {
			t.Fatal("reload")
		}
		hook.Post(notify.Event{Type: "completed", Caller: "+15125550100"})
	}
	os.WriteFile(path, webhookconfig.Encode(webhookconfig.Config{}), 0600)
	_ = store.Reload()
	hook.Post(notify.Event{Type: "ringing"})
	_ = hook.Close()
	if len(got) != 2 {
		t.Fatalf("got %d events", len(got))
	}
	for _, prefix := range []string{"/first", "/second"} {
		r := <-got
		if r.Path != prefix+webhookconfig.Suffix || r.Auth != "" || r.Content != "application/json" || r.Payload.Type != "completed" || r.Payload.Caller == "+15125550100" {
			t.Fatalf("incorrect delivery: %#v", r)
		}
	}
}

func TestManagedRedirectIsRefusedWhileLegacyStillFollows(t *testing.T) {
	target := newReceiver(t)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	managed := open(t, notify.Options{Resolve: func() notify.Target { return notify.Target{URL: redirect.URL, NoRedirect: true} }})
	managed.Post(notify.Event{Type: "ringing"})
	_ = managed.Close()
	if len(target.got()) != 0 || managed.Failed() != 1 {
		t.Fatal("managed endpoint followed redirect")
	}
	legacy := open(t, notify.Options{URL: redirect.URL})
	legacy.Post(notify.Event{Type: "ringing"})
	_ = legacy.Close()
	if len(target.got()) != 1 || legacy.Failed() != 0 {
		t.Fatal("legacy redirect behavior changed")
	}
}
