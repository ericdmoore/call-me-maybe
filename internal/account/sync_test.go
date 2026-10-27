package account

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSyncTransportKeepsCredentialsAndScopeInsideAccount(t *testing.T) {
	var method, path, token, body string
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, token = r.Method, r.URL.Path, r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	})
	seed(t, c)
	for _, test := range []struct {
		endpoint SyncEndpoint
		method   string
		body     string
	}{
		{SyncSnapshot, "PUT", `{"schema":1}`},
		{SyncQueue, "GET", ""},
		{SyncReceipts, "POST", `{"receipts":[]}`},
		{SyncStatus, "POST", `{"schema":1}`},
	} {
		if _, err := c.Sync(context.Background(), test.endpoint, []byte(test.body)); err != nil {
			t.Fatal(err)
		}
		if method != test.method || path != "/v1/sync/"+string(test.endpoint) || body != test.body || !strings.HasPrefix(token, "Bearer dd_sync_") {
			t.Fatal("incorrect sync transport scope")
		}
	}
	for _, endpoint := range []SyncEndpoint{"/v1/login", "https://outside.invalid", ""} {
		if _, err := c.Sync(context.Background(), endpoint, nil); err != ErrProtocol {
			t.Fatal("accepted an unknown endpoint")
		}
	}
	if _, err := c.Sync(context.Background(), SyncQueue, []byte("{}")); err != ErrProtocol {
		t.Fatal("accepted a queue GET body")
	}
	if _, err := c.Sync(context.Background(), SyncSnapshot, make([]byte, 1<<20+1)); err != ErrProtocol {
		t.Fatal("accepted an oversized snapshot")
	}
}

// mutate stands in for another account command (an Upload, a Rotate, a login)
// writing the store while a sync request is on the network.
func mutate(t *testing.T, c *Client, change func(*state)) {
	t.Helper()
	s, err := openStore(c.StateDir)
	if err != nil {
		t.Errorf("sync held the account lock across the network: %v", err)
		return
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		t.Error(err)
		return
	}
	change(v)
	if err := s.save(v); err != nil {
		t.Error(err)
	}
}

func TestSyncReleasesTheLockAndKeepsConcurrentWrites(t *testing.T) {
	var during func()
	status := http.StatusOK
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if during != nil {
			during()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	})
	seed(t, c)
	successor := testToken("sync", "C")
	mutate(t, c, func(v *state) { v.Tokens["sync"] = credential{Current: testToken("sync", "A"), Pending: successor} })

	// A backup reservation written mid-request survives the promotion.
	during = func() {
		mutate(t, c, func(v *state) { v.Upload = &pendingUpload{Hash: "reserved", Bytes: 1, CreatedAt: 1} })
	}
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	v := loadTest(t, c)
	if v.Tokens["sync"] != (credential{Current: successor}) || v.Upload == nil || v.Upload.Hash != "reserved" {
		t.Fatal("promotion lost a concurrent write or the successor")
	}

	// A rotation that lands mid-request is newer than the request's outcome.
	next := testToken("sync", "D")
	during = func() {
		mutate(t, c, func(v *state) { v.Tokens["sync"] = credential{Current: successor, Pending: next} })
	}
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if loadTest(t, c).Tokens["sync"] != (credential{Current: successor, Pending: next}) {
		t.Fatal("a stale response overwrote a newer rotation")
	}

	// A 401 for a credential that has since been replaced does not sign out.
	status = http.StatusUnauthorized
	during = func() { mutate(t, c, func(v *state) { v.Tokens["sync"] = credential{Current: next} }) }
	var refused *ServiceError
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); !errors.As(err, &refused) {
		t.Fatal(err)
	}
	if loadTest(t, c).NeedsLogin {
		t.Fatal("a stale 401 revoked a newer credential")
	}
	during = nil
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); !errors.As(err, &refused) {
		t.Fatal(err)
	}
	if !loadTest(t, c).NeedsLogin {
		t.Fatal("401 for the current credential did not require login")
	}
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); !errors.Is(err, ErrLogin) {
		t.Fatal("sent a revoked credential")
	}
}

func TestSyncWaitsBrieflyForTheLockThenReportsBusy(t *testing.T) {
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	})
	seed(t, c)
	s, err := openStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(60*time.Millisecond, s.close)
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); err != nil {
		t.Fatalf("a brief holder failed the request: %v", err)
	}
	s, err = openStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Sync(ctx, SyncStatus, []byte(`{}`)); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected busy, got %v", err)
	}
}
