package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/backup"
	"callmemaybe/internal/host"
	"callmemaybe/internal/syncmachine"
)

// writeRemovedAccount stores the state a box is left in after the service
// answered box_removed: identifiers for display, no credential at all. It is
// written the way the account package writes it, so no network is involved.
func writeRemovedAccount(t *testing.T, state string) {
	t.Helper()
	dir := filepath.Join(state, "account")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(map[string]any{"version": 1, "box": "00000000-0000-7000-8000-000000000001",
		"house": "00000000-0000-7000-8000-000000000002", "transportKey": "", "tokens": map[string]any{},
		"needsLogin": true, "removedAt": time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)})
	var data bytes.Buffer
	z := gzip.NewWriter(&data)
	_, _ = z.Write(plain)
	_ = z.Close()
	if err := os.WriteFile(filepath.Join(dir, "tokens.bin"), data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureStderr is capture for the stream sync and the account commands
// report problems on.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()
	f()
	os.Stderr = saved
	_ = w.Close()
	return <-done
}

func TestSyncOfARemovedBoxStopsWithOneLineAndANoRestartStatus(t *testing.T) {
	for _, mode := range []string{"run", "once"} {
		state := t.TempDir()
		writeRemovedAccount(t, state)
		var rc int
		out := captureStderr(t, func() { rc = runSync([]string{mode, "--state", state}) })
		if rc != syncExitRemoved {
			t.Fatalf("%s: exit %d, want %d", mode, rc, syncExitRemoved)
		}
		if strings.TrimSpace(out) != syncRemovedMessage || strings.Count(out, "\n") != 1 {
			t.Fatalf("%s: want exactly the removal line, got %q", mode, out)
		}
		if _, err := os.Stat(filepath.Join(state, "sync")); !os.IsNotExist(err) {
			t.Fatalf("%s: removed box opened its sync ledger", mode)
		}
	}
	for _, word := range []string{"removed from its account", "calls keep working", "doorman login"} {
		if !strings.Contains(syncRemovedMessage, word) {
			t.Fatalf("removal line lacks %q", word)
		}
	}
}

func TestSyncUnitTreatsRemovalAsACleanStop(t *testing.T) {
	units, err := host.Units()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if u.Name != "doorman-sync.service" {
			continue
		}
		if !strings.Contains(string(u.Content), fmt.Sprintf("\nSuccessExitStatus=%d\n", syncExitRemoved)) ||
			!strings.Contains(string(u.Content), "\nRestart=on-failure\n") {
			t.Fatal("doorman-sync.service would restart a removed box")
		}
		return
	}
	t.Fatal("no doorman-sync.service")
}

func TestAccountStatusAndCheckSayTheBoxWasRemoved(t *testing.T) {
	state := t.TempDir()
	if line := describeAccount(state); line != "" {
		t.Fatalf("check spoke about an account that was never enrolled: %q", line)
	}
	if _, err := os.Stat(filepath.Join(state, "account")); !os.IsNotExist(err) {
		t.Fatal("check created account state")
	}
	writeRemovedAccount(t, state)
	var rc int
	out := capture(t, func() { rc = runAccount([]string{"status", "--state", state}) })
	if rc != 1 || !strings.Contains(out, "removed from its account on 2026-10-20T12:00:00Z") ||
		!strings.Contains(out, "calls still work") || !strings.Contains(out, "doorman login") {
		t.Fatalf("status hid the removal (%d): %s", rc, out)
	}
	out = capture(t, func() { rc = runAccount([]string{"status", "--state", state, "--json"}) })
	var status account.Status
	if json.Unmarshal([]byte(out), &status) != nil || !status.Removed || status.RemovedAt.IsZero() || !status.NeedsLogin {
		t.Fatalf("JSON status hid the removal: %s", out)
	}
	if line := describeAccount(state); !strings.Contains(line, "removed from its account") {
		t.Fatalf("check hid the removal: %q", line)
	}
}

func TestNightlyBackupReportsARemovedBoxAndKeepsLocalCopies(t *testing.T) {
	root := t.TempDir()
	recipient, identity, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	writeRemovedAccount(t, state)
	env := filepath.Join(root, ".env")
	dest := filepath.Join(root, "copies")
	if err := os.WriteFile(env, []byte("BACKUP_RECIPIENT="+recipient+"\nBACKUP_PATH="+dest+"\nBACKUP_CLOUD=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for night := range 2 {
		out := capture(t, func() {
			if rc := runBackup([]string{"run", "--env", env, "--state", state, "--no-voicemail",
				"--asterisk", filepath.Join(root, "missing")}); rc != 0 {
				t.Errorf("night %d: a removed box failed the backup run: %d", night, rc)
			}
		})
		if !strings.Contains(out, "✓ file:") || !strings.Contains(out, "– cloud: stopped, this box was removed from its account") ||
			strings.Contains(out, "✗") {
			t.Fatalf("night %d: incorrect result: %s", night, out)
		}
	}
	var last lastBackup
	raw, err := os.ReadFile(lastBackupPath(state))
	if err != nil || json.Unmarshal(raw, &last) != nil || len(last.Failed) != 0 ||
		len(last.Removed) != 1 || last.Removed[0] != "cloud" || len(last.Dests) != 1 || last.Dests[0] != "file" {
		t.Fatalf("last run misrecorded the removal: %s", raw)
	}
	bundles, _ := filepath.Glob(filepath.Join(dest, "*.age"))
	// Two runs in one second share a bundle name; at least one must be there.
	if len(bundles) == 0 {
		t.Fatal("local backups were not kept")
	}
	data, _ := os.ReadFile(bundles[0])
	if _, err := backup.ReadManifest(bytes.NewReader(data), identity); err != nil {
		t.Fatal("local copy is not recoverable")
	}
	line, ok := describeBackup(state, last.At, secretsFrom(map[string]string{"BACKUP_RECIPIENT": recipient}))
	if !ok || !strings.Contains(line, "stopped: cloud (box removed from its account") {
		t.Fatalf("check line: %q (ok=%t)", line, ok)
	}
	last.Dests = nil
	b, _ := json.Marshal(last)
	_ = os.WriteFile(lastBackupPath(state), b, 0o600)
	if _, ok := describeBackup(state, last.At, secretsFrom(map[string]string{"BACKUP_RECIPIENT": recipient})); ok {
		t.Fatal("a run that delivered nowhere passed the check")
	}
}

func TestSyncTransportEndsOnRemovalOnly(t *testing.T) {
	removal := &account.ServiceError{Status: 401, Code: "box_removed"}
	for _, test := range []struct {
		err   error
		ended bool
	}{
		{removal, true},
		{account.ErrRemoved, true},
		{&account.ServiceError{Status: 401, Code: "unauthorized"}, false},
		{account.ErrLogin, false},
		{account.ErrSuperseded, false},
		{account.ErrNetwork, false},
		{nil, false},
	} {
		send := func(context.Context, account.SyncEndpoint, []byte) ([]byte, error) { return nil, test.err }
		_, err := syncTransport(send)(context.Background(), "status", nil)
		if errors.Is(err, syncmachine.ErrTransportEnded) != test.ended || !errors.Is(err, test.err) {
			t.Fatalf("%v: ended=%t", test.err, errors.Is(err, syncmachine.ErrTransportEnded))
		}
	}
}

// A removal that raced a fresh login is not a sign-out: runSync leaves its
// loop only for errors.Is(err, ErrRemoved) (exit 6, which the unit treats as
// a clean stop), so the stale error must not match, must not end the pass,
// and must back off like any other retryable failure.
func TestStaleRemovalNeitherEndsSyncNorExitsSix(t *testing.T) {
	send := func(context.Context, account.SyncEndpoint, []byte) ([]byte, error) {
		return nil, account.ErrSuperseded
	}
	_, err := syncTransport(send)(context.Background(), "status", nil)
	if errors.Is(err, account.ErrRemoved) || errors.Is(err, syncmachine.ErrTransportEnded) {
		t.Fatalf("a stale removal would stop sync for the new enrolment: %v", err)
	}
	pace := syncPace{jitter: func(d time.Duration) time.Duration { return d }}
	if d := pace.next(err, time.Second); d <= 0 || d > syncMaxDelay {
		t.Fatalf("no retry scheduled: %v", d)
	}
}

func TestNightlyBackupOfACloudOnlyRemovedBoxIsNotGreen(t *testing.T) {
	root := t.TempDir()
	recipient, _, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	writeRemovedAccount(t, state)
	env := filepath.Join(root, ".env")
	if err := os.WriteFile(env, []byte("BACKUP_RECIPIENT="+recipient+"\nBACKUP_CLOUD=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() {
		if rc := runBackup([]string{"run", "--env", env, "--state", state, "--no-voicemail",
			"--asterisk", filepath.Join(root, "missing")}); rc != 1 {
			t.Errorf("a run that delivered nowhere exited %d, want 1", rc)
		}
	})
	if !strings.Contains(out, "– cloud: stopped") || !strings.Contains(out, "✗ nothing was backed up") {
		t.Fatalf("incorrect result: %s", out)
	}
	var last lastBackup
	raw, err := os.ReadFile(lastBackupPath(state))
	if err != nil || json.Unmarshal(raw, &last) != nil || len(last.Dests) != 0 ||
		len(last.Removed) != 1 || last.Removed[0] != "cloud" {
		t.Fatalf("last run misrecorded: %s", raw)
	}
	if _, ok := describeBackup(state, last.At, secretsFrom(map[string]string{"BACKUP_RECIPIENT": recipient})); ok {
		t.Fatal("check passed a box with no copy anywhere")
	}
}

func TestLoginClearsTheRemovedNoteFromTheLastBackup(t *testing.T) {
	state := t.TempDir()
	forgetRemovedBackup(state) // no record yet: nothing to do, nothing created
	if _, err := os.Stat(lastBackupPath(state)); !os.IsNotExist(err) {
		t.Fatal("created a record out of nothing")
	}
	at := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	writeLast(state, lastBackup{At: at, Name: "n", Files: 3, Bytes: 10, Removed: []string{"cloud"}})
	forgetRemovedBackup(state)
	var last lastBackup
	raw, err := os.ReadFile(lastBackupPath(state))
	if err != nil || json.Unmarshal(raw, &last) != nil || len(last.Removed) != 0 || last.Name != "n" || last.Files != 3 {
		t.Fatalf("record not cleared cleanly: %s", raw)
	}
	line, _ := describeBackup(state, at, secretsFrom(map[string]string{}))
	if strings.Contains(line, "stopped") {
		t.Fatalf("a re-enrolled box still reads as stopped: %q", line)
	}
	forgetRemovedBackup(state) // a record with no note is left alone
}
