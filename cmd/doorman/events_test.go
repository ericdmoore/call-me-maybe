package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/events"
)

func TestEventsDumpIsSelfContainedAndNeedsNoARISecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal", "events.db")
	w := events.Start(path, events.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w.Post(events.System(events.DaemonStarted, "", 0))
	w.Post(events.System(events.CallObserved, "", 0))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	if code := dumpEvents([]string{"--json", "--path", path, "--after", "1", "--limit", "1", "--eventType", "call.observed"}, &out, &diag); code != 0 {
		t.Fatalf("code %d: %s", code, diag.String())
	}
	var page events.Page
	if err := json.Unmarshal(out.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.NextCursor != 2 || diag.Len() != 0 {
		t.Fatalf("page %+v; diagnostics %s", page, diag.String())
	}
	if !strings.Contains(out.String(), `"next_cursor":"2"`) {
		t.Fatal("cursor must be a JSON string")
	}
	for _, args := range [][]string{{"--json", "--limit", "0"}, {"--json", "--after", "-1"}, {"--json", "--eventType", "typo"}, {"--json", "trailing"}, {"--path", path}} {
		out.Reset()
		diag.Reset()
		if code := dumpEvents(args, &out, &diag); code != 2 || out.Len() != 0 {
			t.Fatalf("invalid arguments: %v code=%d stdout=%s", args, code, out.String())
		}
	}
	out.Reset()
	diag.Reset()
	if code := dumpEvents([]string{"--json", "--path", path, "--after", "99"}, &out, &diag); code != 1 || out.Len() != 0 {
		t.Fatal("read failure must not produce JSON success")
	}
}
