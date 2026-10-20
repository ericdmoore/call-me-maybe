package account

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
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
