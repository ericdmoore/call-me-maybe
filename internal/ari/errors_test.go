package ari

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Teardown may race a channel disappearing. Only a real ARI 404, including a
// wrapped response, is safe to ignore; transport/server failures must survive.
func TestHangupDistinguishesGoneChannelFromServerFailure(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/ari/channels/gone" {
					t.Error("unexpected teardown request")
				}
				http.Error(w, "synthetic teardown response", status)
			}))
			defer server.Close()
			client := New(Options{BaseURL: server.URL})
			err := client.Hangup(context.Background(), "gone")
			if err == nil {
				t.Fatal("failure disappeared")
			}
			if IsNotFound(err) != (status == http.StatusNotFound) || IsNotFound(fmt.Errorf("teardown: %w", err)) != (status == http.StatusNotFound) {
				t.Fatal("wrong teardown classification")
			}
			var response *Error
			if !errors.As(err, &response) || response.Status != status {
				t.Fatal("lost ARI response status")
			}
			if got, want := response.Error(), fmt.Sprintf("ari: %d on /channels/gone: synthetic teardown response", status); got != want {
				t.Fatalf("response context: %q", got)
			}
		})
	}
	for _, err := range []error{nil, errors.New("transport unavailable"), fmt.Errorf("wrapped: %w", errors.New("transport unavailable"))} {
		if IsNotFound(err) {
			t.Fatal("non-ARI failure ignored")
		}
	}
}
