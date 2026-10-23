package account

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDialBellSendsTheSyncTokenAndOpensAQuietSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sync/bell" || r.Method != http.MethodGet || r.URL.RawQuery != "" {
			t.Errorf("bell reached %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken("sync", "A") {
			t.Error("the bell did not carry the sync credential the way sync does")
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, data, _ := conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, append([]byte(nil), data...))
	})
	seed(t, c)
	conn, err := c.DialBell(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
	if _, data, err := conn.ReadMessage(); err != nil || string(data) != "ping" {
		t.Fatalf("socket not usable: %q %v", data, err)
	}
}

func TestDialBellOverPlainHTTPForLocalServices(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, err := upgrader.Upgrade(w, r, nil); err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	c := New(t.TempDir())
	c.origin = server.URL
	seed(t, c)
	conn, err := c.DialBell(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestDialBellRefusalsAreTypedAndCarryNothingFromTheServer(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		check  func(error) bool
	}{
		{401, `{"error":"unauthorized","detail":"dd_sync_secret"}`, func(err error) bool {
			var r *BellRefused
			return errors.As(err, &r) && r.Status == 401 && r.Code == "unauthorized"
		}},
		{403, `{"error":"sync_required"}`, func(err error) bool {
			var r *BellRefused
			return errors.As(err, &r) && r.Status == 403 && r.Code == "sync_required"
		}},
		{404, `{"error":"not_found"}`, func(err error) bool {
			var s *ServiceError
			return errors.As(err, &s) && s.Status == 404 && s.Code == "service_error"
		}},
		{426, `{"error":"upgrade_required"}`, func(err error) bool {
			var s *ServiceError
			return errors.As(err, &s) && s.Status == 426 && s.Code == "upgrade_required"
		}},
		{503, `<html>busy</html>`, func(err error) bool {
			var s *ServiceError
			return errors.As(err, &s) && s.Status == 503 && s.RetryAfter == 30*time.Second
		}},
	} {
		c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(test.status)
			fmt.Fprint(w, test.body)
		})
		seed(t, c)
		_, err := c.DialBell(context.Background())
		if !test.check(err) {
			t.Fatalf("%d: %#v", test.status, err)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "http") {
			t.Fatalf("%d: refusal leaked server text or a URL: %v", test.status, err)
		}
		if loadTest(t, c).NeedsLogin {
			t.Fatalf("%d: a bell refusal changed the stored credential; sync settles that", test.status)
		}
	}
}

func TestDialBellRecordsARemovalLikeSync(t *testing.T) {
	calls := 0
	c := clientServer(t, boxRemoved(&calls))
	seed(t, c)
	if _, err := c.DialBell(context.Background()); !errors.Is(err, ErrRemoved) {
		t.Fatalf("removal not reported: %v", err)
	}
	assertRemoved(t, c)
	if _, err := c.DialBell(context.Background()); !errors.Is(err, ErrRemoved) || calls != 1 {
		t.Fatalf("a removed box dialled again: %v after %d calls", err, calls)
	}
}

func TestDialBellRemovalForAnEnrolmentThatIsGoneIsStale(t *testing.T) {
	var c *Client
	c = clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		// A fresh login finished while the bell was being opened.
		s, err := openStore(c.StateDir)
		if err != nil {
			t.Error(err)
			return
		}
		v, _ := s.load()
		v.Box = otherBox
		_ = s.save(v)
		s.close()
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"box_removed"}`)
	})
	seed(t, c)
	if _, err := c.DialBell(context.Background()); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("stale removal: %v", err)
	}
	if !loadTest(t, c).RemovedAt.IsZero() {
		t.Fatal("a stale removal signed out the new enrolment")
	}
}

func TestDialBellWithoutAServiceOrCredential(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	c := New(t.TempDir())
	c.origin = server.URL
	server.Close()
	if _, err := c.DialBell(context.Background()); !errors.Is(err, ErrLogin) {
		t.Fatalf("no enrolment: %v", err)
	}
	if _, err := c.SyncCredentialTag(context.Background()); !errors.Is(err, ErrLogin) {
		t.Fatalf("no enrolment: %v", err)
	}
	seed(t, c)
	if _, err := c.DialBell(context.Background()); !errors.Is(err, ErrNetwork) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("unreachable service: %v", err)
	}
}

func TestSyncCredentialTagChangesWithTheCredentialAndHidesIt(t *testing.T) {
	c := New(t.TempDir())
	seed(t, c)
	first, err := c.SyncCredentialTag(context.Background())
	if err != nil || len(first) != 16 || strings.Contains(testToken("sync", "A"), first) {
		t.Fatalf("tag %q %v", first, err)
	}
	again, _ := c.SyncCredentialTag(context.Background())
	if again != first {
		t.Fatal("tag is not stable")
	}
	s, err := openStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.load()
	v.Tokens["sync"] = credential{Current: testToken("sync", "A"), Pending: testToken("sync", "C")}
	_ = s.save(v)
	s.close()
	if rotated, _ := c.SyncCredentialTag(context.Background()); rotated == first {
		t.Fatal("a rotation did not change the tag")
	}
}
