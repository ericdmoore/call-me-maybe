package activation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryReportsLiveDaemonStateAndStopsOnShutdown(t *testing.T) {
	// Unix socket paths are short on macOS; t.TempDir can exceed its limit.
	dir, err := os.MkdirTemp("/tmp", "activation-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Serve(ctx, dir, func() map[string]string { return map[string]string{"default": "exact-compiled-revision"} })
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(time.Second)
	for {
		value, err := Read(dir)
		if err == nil {
			if value["default"] != "exact-compiled-revision" {
				t.Fatal("incorrect revision")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("activation query did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if _, err := Read(dir); err == nil {
		t.Fatal("dead daemon still reported an active revision")
	}
}

func TestActivationRefusesSharedOrMissingDirectories(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	for _, path := range []string{dir, filepath.Join(dir, "missing")} {
		serve(context.Background(), path, func() map[string]string { t.Fatal("opened unsafe observer"); return nil })
	}
}
