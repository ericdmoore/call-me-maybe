package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testBox = "00000000-0000-7000-8000-000000000001"
const testHouse = "00000000-0000-7000-8000-000000000002"
const testBundle = "00000000-0000-7000-8000-000000000003"

func testToken(cap, letter string) string {
	return "dd_" + cap + "_" + strings.Repeat(letter, 16) + "_" + strings.Repeat(letter, 32)
}

func clientServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	c := New(t.TempDir())
	c.origin = server.URL
	c.http.Transport = server.Client().Transport
	c.wait = func(context.Context, time.Duration) error { return nil }
	return c
}

func seed(t *testing.T, c *Client) {
	t.Helper()
	s, err := openStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.save(&state{Version: 1, Box: testBox, House: testHouse, EnrolledAt: time.Now(),
		TransportKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		Tokens:       map[string]credential{"sync": {Current: testToken("sync", "A")}, "backup": {Current: testToken("backup", "B")}},
	}); err != nil {
		t.Fatal(err)
	}
}

func loadTest(t *testing.T, c *Client) *state {
	t.Helper()
	s, err := openStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUserCodeAndEnrollment(t *testing.T) {
	key, salt := make([]byte, 32), make([]byte, 32)
	for i := range key {
		key[i], salt[i] = byte(i), byte(i+32)
	}
	if got := userCode(key, salt); got != "9ZXMVMHVQEBY" {
		t.Fatal(got)
	}
	var expected string
	polls := 0
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("enrollment sent credentials")
		}
		switch r.URL.Path {
		case "/v1/login/box/device":
			var in struct {
				TransportPublicKey string `json:"transportPublicKey"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			public, _ := base64.RawURLEncoding.DecodeString(in.TransportPublicKey)
			if len(public) != 32 {
				t.Error("missing generated key")
			}
			expected = userCode(public, salt)
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": strings.Repeat("Z", 43), "userCode": expected,
				"userCodeSalt": base64.RawURLEncoding.EncodeToString(salt), "expiresIn": 900, "interval": 5})
		case "/v1/login/box/device/poll":
			polls++
			if polls == 1 {
				w.Header().Set("Retry-After", "10")
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":"slow_down"}`)
				return
			}
			if polls == 2 {
				w.WriteHeader(202)
				fmt.Fprint(w, `{"status":"pending"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"box": testBox, "house": testHouse,
				"tokens": map[string]string{"sync": testToken("sync", "A"), "backup": testToken("backup", "B")}})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	shown := ""
	status, err := c.Login(context.Background(), func(code string) { shown = code })
	if err != nil {
		t.Fatal(err)
	}
	if status.Box != testBox || strings.ReplaceAll(shown, "-", "") != expected {
		t.Fatal("wrong public status/code")
	}
	private := loadTest(t, c)
	keyBytes, _ := base64.RawURLEncoding.DecodeString(private.TransportKey)
	if len(keyBytes) != 32 || private.Device != nil {
		t.Fatal("transport state lost")
	}
	p := filepath.Join(c.StateDir, "account", "tokens.bin")
	info, _ := os.Stat(p)
	raw, _ := os.ReadFile(p)
	if info.Mode().Perm() != 0o600 || strings.Contains(string(raw), "dd_backup_") {
		t.Fatal("unsafe persistence")
	}
	display, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(display)
	if strings.Contains(string(b), "dd_") || strings.Contains(string(b), "transport") {
		t.Fatal("status disclosed credential")
	}
	if _, err := c.Login(context.Background(), func(string) {}); err == nil {
		t.Fatal("silently replaced enrollment")
	}
}

func TestEnrollmentRejectsSubstitutedKey(t *testing.T) {
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "poll") {
			t.Fatal("polled after key mismatch")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": strings.Repeat("Z", 43), "userCode": "AAAAAAAAAAAA",
			"userCodeSalt": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "expiresIn": 900, "interval": 5})
	})
	_, err := c.Login(context.Background(), func(string) { t.Fatal("displayed mismatched code") })
	if !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
}

func TestRotationPersistsBeforeFirstUseAndNeverFallsBack(t *testing.T) {
	old, successor := testToken("backup", "B"), testToken("backup", "C")
	calls := 0
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		auth := r.Header.Get("Authorization")
		if calls == 1 {
			if auth != "Bearer "+old {
				t.Error("wrong initial capability")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": successor})
			return
		}
		if auth != "Bearer "+successor {
			t.Error("fell back to predecessor")
		}
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"reuse"}`)
	})
	seed(t, c)
	if err := c.Rotate(context.Background(), "backup"); err != nil {
		t.Fatal(err)
	}
	v := loadTest(t, c)
	if v.Tokens["backup"].Current != old || v.Tokens["backup"].Pending != successor {
		t.Fatal("missing durable successor")
	}
	if err := c.Rotate(context.Background(), "backup"); err == nil {
		t.Fatal("accepted reuse")
	}
	if err := c.Rotate(context.Background(), "backup"); !errors.Is(err, ErrLogin) {
		t.Fatal(err)
	}
	if calls != 2 || !loadTest(t, c).NeedsLogin {
		t.Fatal("retried revoked state")
	}
}

func TestMintWithoutPersistenceLeavesPredecessorUsable(t *testing.T) {
	var c *Client
	c = clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken("backup", "B") {
			t.Error("old token not preserved")
		}
		dir := filepath.Join(c.StateDir, "account")
		if err := os.Rename(filepath.Join(dir, "tokens.bin"), filepath.Join(dir, "previous.bin")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "tokens.bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": testToken("backup", "C")})
	})
	seed(t, c)
	if err := c.Rotate(context.Background(), "backup"); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	dir := filepath.Join(c.StateDir, "account")
	_ = os.Remove(filepath.Join(dir, "tokens.bin"))
	_ = os.Rename(filepath.Join(dir, "previous.bin"), filepath.Join(dir, "tokens.bin"))
	if v := loadTest(t, c); v.Tokens["backup"].Current != testToken("backup", "B") || v.Tokens["backup"].Pending != "" {
		t.Fatal("old durable token changed")
	}
}

func TestUploadResumesStoredCiphertextAfterInterruptedCompletion(t *testing.T) {
	content := []byte("synthetic encrypted bundle")
	at := time.Now().Truncate(time.Millisecond)
	put, declares, completes := 0, 0, 0
	stored := false
	var c *Client
	c = clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/backups/uploads":
			declares++
			if r.Header.Get("Authorization") != "Bearer "+testToken("backup", "B") {
				t.Error("wrong capability")
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"uploadId": testBundle, "uploadUrl": c.origin + "/object?signature=synthetic",
				"uploadHeaders": map[string]string{"Content-Length": fmt.Sprint(len(content)), "If-None-Match": "*"},
				"expiresAt":     time.Now().Add(15 * time.Minute).UnixMilli(), "maxBytes": 200000000})
		case "/object":
			put++
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("credentials forwarded to object store")
			}
			if r.Header.Get("If-None-Match") != "*" || r.ContentLength != int64(len(content)) {
				t.Error("missing signed constraints")
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != string(content) {
				t.Error("changed ciphertext")
			}
			stored = true
		case "/v1/backups/uploads/complete":
			completes++
			if !stored {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"error":"not_uploaded"}`)
				return
			}
			if completes == 2 {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"untrusted-secret-body"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"bundle": Bundle{testBundle, int64(len(content)), at.UnixMilli(), time.Now().UnixMilli()}})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	seed(t, c)
	if _, err := c.Upload(context.Background(), content, at); err == nil || strings.Contains(err.Error(), "untrusted-secret-body") {
		t.Fatal("bad error handling")
	}
	if loadTest(t, c).Upload == nil {
		t.Fatal("lost pending upload")
	}
	bundle, err := c.Upload(context.Background(), content, at)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ID != testBundle || declares != 1 || put != 1 || loadTest(t, c).Upload != nil {
		t.Fatal("retry duplicated upload")
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "account", "upload.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("spool not cleared")
	}
}

func TestCredentialRedirectsAndUnsafeStateAreRefused(t *testing.T) {
	reached := false
	destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer destination.Close()
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(307)
	})
	seed(t, c)
	if err := c.Rotate(context.Background(), "backup"); err == nil {
		t.Fatal("accepted redirect")
	}
	if reached {
		t.Fatal("followed credential redirect")
	}
	s, err := openStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(c.StateDir); !errors.Is(err, ErrBusy) {
		t.Fatal("missing process lock")
	}
	s.close()
	path := filepath.Join(c.StateDir, "account", "tokens.bin")
	if os.Chmod(path, 0o644) != nil {
		t.Fatal("chmod")
	}
	if _, err := c.Status(); !errors.Is(err, ErrState) {
		t.Fatal("accepted world-readable tokens")
	}
	_ = os.Remove(path)
	if os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), path) != nil {
		t.Fatal("symlink")
	}
	if _, err := c.Status(); !errors.Is(err, ErrState) {
		t.Fatal("followed token symlink")
	}
}

// The enrolment request carries the suggested box name when there is one and
// no "name" key at all when there is not.
func TestEnrollmentSuggestsTheBoxName(t *testing.T) {
	for _, name := range []string{"kitchen-pi", ""} {
		var sent map[string]any
		c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/login/box/device" {
				t.Errorf("unexpected route %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&sent)
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"bad_request"}`)
		})
		c.Name = name
		if _, err := c.Login(context.Background(), func(string) {}); err == nil {
			t.Fatal("refusal ignored")
		}
		got, present := sent["name"]
		if name == "" && present {
			t.Fatalf("empty name sent as %v", got)
		}
		if name != "" && got != name {
			t.Fatalf("name: got %v, want %q", got, name)
		}
		if _, ok := sent["transportPublicKey"]; !ok || len(sent) != map[bool]int{true: 2, false: 1}[name != ""] {
			t.Fatalf("unexpected enrolment body keys: %v", sent)
		}
	}
}
