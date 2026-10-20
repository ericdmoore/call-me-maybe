package account

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testAccount = "00000000-0000-7000-8000-000000000004"
const testGrant = "00000000-0000-7000-8000-000000000005"

func saveTestRecovery(t *testing.T, c *Client, edit func(*state)) {
	t.Helper()
	s, e := openStore(c.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	v, e := s.load()
	if e != nil {
		t.Fatal(e)
	}
	edit(v)
	if e = s.save(v); e != nil {
		t.Fatal(e)
	}
}
func approvedTestRecovery(t *testing.T, c *Client) RecoveryScope {
	t.Helper()
	seed(t, c)
	scope := RecoveryScope{testGrant, testAccount, testHouse, testBox, c.now().Add(30 * time.Minute).UnixMilli()}
	saveTestRecovery(t, c, func(v *state) {
		v.Recovery = &recoveryGrant{Secret: "dd_recovery_" + strings.Repeat("A", 43), Scope: scope}
	})
	return scope
}
func TestRecoveryApprovalResumeAndScope(t *testing.T) {
	var secret string
	starts, polls := 0, 0
	now := time.Now()
	key, _ := ecdh.X25519().NewPrivateKey(make([]byte, 32))
	salt := make([]byte, 32)
	scope := RecoveryScope{testGrant, testAccount, testHouse, testBox, now.Add(30 * time.Minute).UnixMilli()}
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/login/backup-recovery/start":
			starts++
			if r.Header.Get("Authorization") != "Bearer "+testToken("backup", "B") {
				t.Error("missing upload authentication")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if secret != "" && secret != body["secret"] {
				t.Error("lost start replaced secret")
			}
			secret = body["secret"]
			if starts == 1 {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"temporarily_unavailable"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"userCode": userCode(key.PublicKey().Bytes(), salt), "userCodeSalt": base64.RawURLEncoding.EncodeToString(salt), "requestExpiresAt": now.Add(15 * time.Minute).UnixMilli()})
		case "/v1/login/backup-recovery/poll":
			polls++
			if r.Header.Get("Authorization") != "Bearer "+secret {
				t.Error("poll reused upload credential")
			}
			if polls == 1 {
				w.WriteHeader(202)
				fmt.Fprint(w, `{"status":"pending"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(scope)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	c.now = func() time.Time { return now }
	seed(t, c)
	if _, e := c.AuthorizeRecovery(context.Background(), func(string) {}); e == nil {
		t.Fatal("expected lost start")
	}
	if g := loadTest(t, c).Recovery; g == nil || g.Secret != secret {
		t.Fatal("secret was not persisted before start")
	}
	shown := ""
	got, e := c.AuthorizeRecovery(context.Background(), func(s string) { shown = s })
	if e != nil || got != scope || len(shown) != 14 || polls != 2 {
		t.Fatalf("approval failed: %v", e)
	}
	if _, e = c.AuthorizeRecovery(context.Background(), func(string) { t.Error("redisplayed approved code") }); e != nil || starts != 2 {
		t.Fatal("did not reuse approved window")
	}
}
func TestRecoveryApprovalRejectsSubstitutionAndExpiredLostStart(t *testing.T) {
	for _, kind := range []string{"key", "expiry", "scope", "json", "denied", "network"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now()
			key, _ := ecdh.X25519().NewPrivateKey(make([]byte, 32))
			salt := make([]byte, 32)
			c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "start") {
					if kind == "json" {
						fmt.Fprint(w, "{")
						return
					}
					code := userCode(key.PublicKey().Bytes(), salt)
					expiry := now.Add(15 * time.Minute).UnixMilli()
					if kind == "key" {
						code = "AAAAAAAAAAAA"
					}
					if kind == "expiry" {
						expiry = now.Add(-time.Minute).UnixMilli()
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"userCode": code, "userCodeSalt": base64.RawURLEncoding.EncodeToString(salt), "requestExpiresAt": expiry})
					return
				}
				if kind == "denied" {
					w.WriteHeader(401)
					fmt.Fprint(w, `{"error":"revoked"}`)
					return
				}
				_ = json.NewEncoder(w).Encode(RecoveryScope{testGrant, testAccount, testHouse, "foreign", now.Add(30 * time.Minute).UnixMilli()})
			})
			seed(t, c)
			c.now = func() time.Time { return now }
			if kind == "network" {
				c.wait = func(context.Context, time.Duration) error { return context.Canceled }
			}
			_, err := c.AuthorizeRecovery(context.Background(), func(string) {})
			if err == nil {
				t.Fatal("accepted invalid approval")
			}
			if kind == "expiry" && loadTest(t, c).Recovery != nil {
				t.Fatal("expired ambiguous start trapped client")
			}
		})
	}
}

type recoveryRoundTrip func(*http.Request) (*http.Response, error)

func (f recoveryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRecoveryProtocolAndStorageCredentialBoundary(t *testing.T) {
	env := []byte("synthetic encrypted identity")
	g := RecoveryGeneration{ID: testGrant, Recipient: "synthetic-public-recipient", SHA256: hashBytes(env), Bytes: len(env), State: "pending", HealthyCopies: 2}
	b := Bundle{ID: testBundle, Bytes: 4}
	bad := ""
	scope := RecoveryScope{}
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer dd_recovery_") {
			t.Error("used ordinary device token")
		}
		if bad == "revoked" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"revoked"}`)
			return
		}
		if bad == "error" {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"recovery_unavailable"}`)
			return
		}
		if bad == "json" {
			fmt.Fprint(w, "{")
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/v1/backups/recovery/") {
		case "status":
			result := RecoveryListing{Account: scope.Account, House: scope.House, Box: scope.Box, Generations: []RecoveryGeneration{g}, Bundles: []Bundle{b}}
			if bad == "scope" {
				result.House = "foreign"
			}
			if bad == "generation" {
				result.Generations[0].Bytes = 0
			}
			if bad == "bundle" {
				result.Bundles[0].Bytes = 200000001
			}
			_ = json.NewEncoder(w).Encode(result)
		case "prepare":
			reply := g
			if bad == "generation" {
				reply.HealthyCopies = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"generation": reply})
		case "material":
			result := RecoveryMaterial{scope.Account, scope.House, g, env}
			if bad == "generation" {
				result.Envelope = []byte("corrupt")
			}
			_ = json.NewEncoder(w).Encode(result)
		case "confirm":
			reply := g
			reply.State = "current"
			if bad == "generation" {
				reply.SHA256 = "different"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"generation": reply})
		case "cancel":
			_ = json.NewEncoder(w).Encode(map[string]any{"cancelled": bad != "generation"})
		case "download":
			url := "https://synthetic.r2.cloudflarestorage.com/bucket/backup?synthetic-signature"
			if bad == "url" {
				url = "https://untrusted.example/download"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": b.ID, "bytes": b.Bytes, "downloadUrl": url})
		default:
			t.Error("unknown recovery route")
			w.WriteHeader(404)
		}
	})
	scope = approvedTestRecovery(t, c)
	transport := c.http.Transport
	c.http.Transport = recoveryRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "synthetic.r2.cloudflarestorage.com" {
			if r.Header.Get("Authorization") != "" {
				t.Error("credential forwarded to storage")
			}
			if bad == "network" {
				return nil, errors.New("synthetic")
			}
			code := 200
			content := "data"
			if bad == "status" {
				code = 403
			}
			if bad == "length" {
				content = "truncated"
			}
			return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(content))}, nil
		}
		return transport.RoundTrip(r)
	})
	if _, e := c.RecoveryStatus(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := c.RecoveryPrepare(context.Background(), g, env); e != nil {
		t.Fatal(e)
	}
	if _, e := c.RecoveryMaterial(context.Background(), g.ID); e != nil {
		t.Fatal(e)
	}
	if e := c.RecoveryConfirm(context.Background(), g); e != nil {
		t.Fatal(e)
	}
	if e := c.RecoveryCancel(context.Background(), g.ID); e != nil {
		t.Fatal(e)
	}
	if got, e := c.RecoveryDownload(context.Background(), b); e != nil || string(got) != "data" {
		t.Fatal(e)
	}
	for _, kind := range []string{"scope", "generation", "bundle", "json", "error"} {
		bad = kind
		if _, e := c.RecoveryStatus(context.Background()); e == nil {
			t.Fatal("accepted invalid listing", kind)
		}
	}
	bad = "generation"
	if e := c.RecoveryPrepare(context.Background(), g, env); e != ErrProtocol {
		t.Fatal(e)
	}
	if _, e := c.RecoveryMaterial(context.Background(), g.ID); e != ErrProtocol {
		t.Fatal(e)
	}
	if e := c.RecoveryConfirm(context.Background(), g); e != ErrProtocol {
		t.Fatal(e)
	}
	if e := c.RecoveryCancel(context.Background(), g.ID); e != ErrProtocol {
		t.Fatal(e)
	}
	for _, kind := range []string{"url", "length", "network", "status", "error"} {
		bad = kind
		if _, e := c.RecoveryDownload(context.Background(), b); e == nil {
			t.Fatal("accepted invalid download", kind)
		}
	}
	bad = "revoked"
	if _, e := c.RecoveryStatus(context.Background()); e != ErrRecoveryApproval {
		t.Fatal(e)
	}
	if loadTest(t, c).Recovery != nil {
		t.Fatal("revoked grant remained reusable")
	}
	if _, e := c.RecoveryStatus(context.Background()); e != ErrRecoveryApproval {
		t.Fatal(e)
	}
}
