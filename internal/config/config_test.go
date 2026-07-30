package config

import (
	"strings"
	"testing"
)

// withEnv sets the minimum required variables plus whatever the case needs,
// and clears them afterwards. t.Setenv restores automatically.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	t.Setenv("ARI_USERNAME", "doorman")
	t.Setenv("ARI_PASSWORD", "x")
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// CLAUDE.md invariant 2. The docs asserted this and the binary did not enforce
// it, so a mis-bound Asterisk or a copied .env would put full call control on
// the wire behind a plaintext password.
func TestNonLoopbackARIIsRefused(t *testing.T) {
	for _, url := range []string{
		"http://192.168.7.21:8088",
		"http://asterisk.local:8088",
		"https://ari.example.com",
		"http://100.96.149.53:8088", // even a tailnet address, without the opt-in
	} {
		withEnv(t, map[string]string{"ARI_BASE_URL": url})
		if _, err := Load(); err == nil {
			t.Errorf("%s was accepted; expected a refusal", url)
		} else if !strings.Contains(err.Error(), "not loopback") {
			t.Errorf("%s: expected a loopback complaint, got %v", url, err)
		}
	}
}

func TestLoopbackARIIsAccepted(t *testing.T) {
	for _, url := range []string{
		"http://127.0.0.1:8088",
		"http://localhost:8088",
		"http://[::1]:8088",
		"http://127.0.0.2:8088", // all of 127/8 is loopback
	} {
		withEnv(t, map[string]string{"ARI_BASE_URL": url})
		if _, err := Load(); err != nil {
			t.Errorf("%s was refused: %v", url, err)
		}
	}
}

// The invariant permits exactly one off-box case: over a tailnet, never the
// LAN. That has to be deliberate, so it needs an explicit opt-in.
func TestRemoteARIRequiresExplicitOptIn(t *testing.T) {
	withEnv(t, map[string]string{
		"ARI_BASE_URL":     "http://100.96.149.53:8088",
		"ARI_ALLOW_REMOTE": "true",
	})
	c, err := Load()
	if err != nil {
		t.Fatalf("opt-in was not honoured: %v", err)
	}
	if !c.AllowRemoteARI {
		t.Error("AllowRemoteARI should be true so startup can warn about it")
	}
}

func TestMalformedARIURLIsRefused(t *testing.T) {
	for _, url := range []string{"ws://127.0.0.1:8088", "127.0.0.1:8088", "://nope"} {
		withEnv(t, map[string]string{"ARI_BASE_URL": url})
		if _, err := Load(); err == nil {
			t.Errorf("%q was accepted as an ARI base URL", url)
		}
	}
}

// Issue #8: redaction is the default, and disabling it takes a deliberate act.
func TestCallerIDRedactionDefaultsOn(t *testing.T) {
	withEnv(t, nil)
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !c.RedactCallerID {
		t.Error("LOG_REDACT_CALLER_ID must default to redacting — see CLAUDE.md invariant 1")
	}

	withEnv(t, map[string]string{"LOG_REDACT_CALLER_ID": "false"})
	c, err = Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.RedactCallerID {
		t.Error("an explicit false should still turn redaction off")
	}
}
