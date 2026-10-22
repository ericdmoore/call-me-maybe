package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const otherBox = "00000000-0000-7000-8000-000000000009"

// boxRemoved answers every request the way the service answers a box whose
// owner removed it from the account, and counts how often it was asked.
func boxRemoved(calls *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"box_removed"}`)
	}
}

// assertRemoved checks the durable outcome: the sign-out is recorded, no
// credential or reservation survives, and the identifiers stay for display.
func assertRemoved(t *testing.T, c *Client) {
	t.Helper()
	v := loadTest(t, c)
	if v.RemovedAt.IsZero() || !v.NeedsLogin || len(v.Tokens) != 0 || v.Upload != nil || v.Recovery != nil {
		t.Fatalf("removal not recorded: removedAt=%v needsLogin=%t tokens=%d", v.RemovedAt, v.NeedsLogin, len(v.Tokens))
	}
	if v.Box != testBox || v.House != testHouse {
		t.Fatal("removal forgot which box it was")
	}
	raw, err := os.ReadFile(filepath.Join(c.StateDir, "account", "tokens.bin"))
	if err != nil || strings.Contains(string(raw), "dd_") {
		t.Fatal("removed credentials still stored")
	}
	status, err := c.Status()
	if err != nil || !status.Removed || status.RemovedAt.IsZero() || !status.NeedsLogin || len(status.Capabilities) != 0 {
		t.Fatalf("status hides removal: %+v %v", status, err)
	}
}

func TestBoxRemovedIsATypedRefusal(t *testing.T) {
	err := failure(401, http.Header{}, []byte(`{"error":"box_removed"}`))
	var refused *ServiceError
	if !errors.As(err, &refused) || refused.Code != "box_removed" || !errors.Is(err, ErrRemoved) {
		t.Fatalf("box_removed not distinct: %v", err)
	}
	for _, other := range []error{
		failure(401, http.Header{}, []byte(`{"error":"unauthorized"}`)),
		failure(401, http.Header{}, []byte(`{"error":"revoked"}`)),
		failure(403, http.Header{}, []byte(`{"error":"box_removed"}`)),
		ErrLogin,
	} {
		if errors.Is(other, ErrRemoved) {
			t.Fatalf("%v read as a removal", other)
		}
	}
	if !removed(401, []byte(`{"error":"box_removed"}`)) || removed(401, []byte(`{"error":"unauthorized"}`)) ||
		removed(200, []byte(`{"error":"box_removed"}`)) || removed(401, []byte(`not json`)) {
		t.Fatal("removed() misclassified a response")
	}
}

func TestSyncStopsForGoodWhenTheBoxIsRemoved(t *testing.T) {
	calls := 0
	c := clientServer(t, boxRemoved(&calls))
	seed(t, c)
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); !errors.Is(err, ErrRemoved) {
		t.Fatalf("removal not reported: %v", err)
	}
	assertRemoved(t, c)
	for _, endpoint := range []SyncEndpoint{SyncQueue, SyncSnapshot} {
		body := []byte(`{}`)
		if endpoint == SyncQueue {
			body = nil
		}
		if _, err := c.Sync(context.Background(), endpoint, body); err != ErrRemoved {
			t.Fatalf("removed box kept syncing: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("removed box asked the service again: %d requests", calls)
	}
}

func TestSyncRemovalDuringAFreshLoginLeavesTheNewEnrolment(t *testing.T) {
	var c *Client
	c = clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		// A doorman login finishes while the old box's request is in flight.
		mutate(t, c, func(v *state) {
			v.Box = otherBox
			v.Tokens = map[string]credential{"sync": {Current: testToken("sync", "D")}, "backup": {Current: testToken("backup", "E")}}
		})
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"box_removed"}`)
	})
	seed(t, c)
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	if v := loadTest(t, c); !v.RemovedAt.IsZero() || v.NeedsLogin || v.Tokens["sync"].Current != testToken("sync", "D") {
		t.Fatal("old box's removal signed out the new enrolment")
	}
}

func TestSyncRemovalDuringRotationStillSignsOut(t *testing.T) {
	var c *Client
	c = clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		mutate(t, c, func(v *state) {
			v.Tokens["sync"] = credential{Current: testToken("sync", "A"), Pending: testToken("sync", "F")}
		})
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"box_removed"}`)
	})
	seed(t, c)
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	assertRemoved(t, c)
}

func TestGenericUnauthorizedStillOnlyAsksForLogin(t *testing.T) {
	calls := 0
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	})
	seed(t, c)
	_, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`))
	if errors.Is(err, ErrRemoved) {
		t.Fatal("generic 401 read as a removal")
	}
	v := loadTest(t, c)
	if !v.NeedsLogin || !v.RemovedAt.IsZero() || v.Tokens["sync"].Current != testToken("sync", "A") {
		t.Fatal("generic 401 changed more than before")
	}
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); err != ErrLogin {
		t.Fatal(err)
	}
	if status, _ := c.Status(); status.Removed {
		t.Fatal("status reported a removal")
	}
}

func TestUploadStopsAndDropsItsReservationWhenTheBoxIsRemoved(t *testing.T) {
	calls := 0
	c := clientServer(t, boxRemoved(&calls))
	seed(t, c)
	if _, err := c.Upload(context.Background(), []byte("synthetic encrypted bundle"), time.Now()); !errors.Is(err, ErrRemoved) {
		t.Fatalf("removal not reported: %v", err)
	}
	assertRemoved(t, c)
	if _, err := os.Stat(filepath.Join(c.StateDir, "account", "upload.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ciphertext for a dead reservation kept")
	}
	if _, err := c.Upload(context.Background(), []byte("next night"), time.Now()); err != ErrRemoved {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("removed box uploaded again: %d requests", calls)
	}
}

func TestUploadCompletionRemovalIsRecorded(t *testing.T) {
	calls := 0
	c := clientServer(t, boxRemoved(&calls))
	seed(t, c)
	content := []byte("synthetic encrypted bundle")
	saveTestRecovery(t, c, func(v *state) {
		v.Upload = &pendingUpload{ID: testBundle, URL: "https://storage.invalid/object", Hash: hashBytes(content), Bytes: int64(len(content)), CreatedAt: 1}
	})
	if err := os.WriteFile(filepath.Join(c.StateDir, "account", "upload.age"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upload(context.Background(), content, time.UnixMilli(1)); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	assertRemoved(t, c)
}

func TestRotationOfARemovedBoxMintsNothing(t *testing.T) {
	calls := 0
	c := clientServer(t, boxRemoved(&calls))
	seed(t, c)
	if err := c.Rotate(context.Background(), "sync"); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	assertRemoved(t, c)
	if err := c.Rotate(context.Background(), "backup"); err != ErrRemoved {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("rotated a removed box's token")
	}
}

func TestRecoveryOfARemovedBoxStops(t *testing.T) {
	calls := 0
	c := clientServer(t, boxRemoved(&calls))
	seed(t, c)
	if _, err := c.AuthorizeRecovery(context.Background(), func(string) { t.Fatal("displayed a code") }); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	assertRemoved(t, c)
	if _, err := c.AuthorizeRecovery(context.Background(), func(string) {}); err != ErrRemoved {
		t.Fatal(err)
	}
	if _, err := c.RecoveryStatus(context.Background()); err != ErrRemoved {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("asked again after removal")
	}

	c = clientServer(t, boxRemoved(&calls))
	approvedTestRecovery(t, c)
	if _, err := c.RecoveryStatus(context.Background()); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	assertRemoved(t, c)
}

func TestLoginAfterRemovalEnrolsAFreshBox(t *testing.T) {
	salt := make([]byte, 32)
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/login/") && r.Header.Get("Authorization") != "" {
			t.Error("re-enrolment sent a dead credential")
		}
		switch r.URL.Path {
		case "/v1/login/box/device":
			var in struct {
				TransportPublicKey string `json:"transportPublicKey"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			public, _ := base64.RawURLEncoding.DecodeString(in.TransportPublicKey)
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": strings.Repeat("Z", 43), "userCode": userCode(public, salt),
				"userCodeSalt": base64.RawURLEncoding.EncodeToString(salt), "expiresIn": 900, "interval": 5})
		case "/v1/login/box/device/poll":
			_ = json.NewEncoder(w).Encode(map[string]any{"box": otherBox, "house": testHouse,
				"tokens": map[string]string{"sync": testToken("sync", "G"), "backup": testToken("backup", "H")}})
		default:
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"box_removed"}`)
		}
	})
	seed(t, c)
	if _, err := c.Sync(context.Background(), SyncStatus, []byte(`{}`)); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	status, err := c.Login(context.Background(), func(string) {})
	if err != nil || status.Box != otherBox {
		t.Fatalf("removed box could not enrol again: %v", err)
	}
	after, err := c.Status()
	if err != nil || after.Removed || !after.RemovedAt.IsZero() || after.NeedsLogin || len(after.Capabilities) != 2 {
		t.Fatalf("login left the removal in place: %+v", after)
	}
}
