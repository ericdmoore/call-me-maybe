package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncmachine"
)

// lockedBuffer is a log sink the store's poller and the test can share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The reproduction from call-me-maybe#131: a disabled, unreferenced schedule
// starting at 25:99. `doorman check`, the daemon's reload and the sync
// observer must all refuse it, and all accept the same file at 03:05.
func oneValidatorPolicy(start string) string {
	return goodPolicy + `
[[people]]
name = "Grandma"
numbers = ["+15550100777"]

[[schedules]]
id = "holiday"
enabled = false
start = "` + start + `"
end = "07:00"
`
}

func TestCheckDaemonAndSyncRefuseTheSameInvalidTime(t *testing.T) {
	dir := policyDir(t, map[string]string{"policy.toml": oneValidatorPolicy("03:05")})
	policyPath, handsetsPath := filepath.Join(dir, "policy.toml"), filepath.Join(dir, "handsets.toml")
	trunksPath := filepath.Join(dir, "trunks.toml")
	check := func() int {
		var rc int
		captureStderr(t, func() {
			capture(t, func() {
				rc = runCheck([]string{"--policy-only", "--handsets", handsetsPath, "--trunks", trunksPath,
					"--contacts", filepath.Join(dir, "contacts.toml"), "--messages", filepath.Join(dir, "messages.toml"),
					"--env", filepath.Join(dir, ".env"), policyPath})
			})
		})
		return rc
	}
	write := func(start string) {
		if err := os.WriteFile(policyPath, []byte(oneValidatorPolicy(start)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// All three accept the valid file.
	if rc := check(); rc != 0 {
		t.Fatalf("check refused a valid file: %d", rc)
	}
	var logs lockedBuffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	files, _ := policy.DiscoverLines(policyPath)
	opened, err := openLines(files, handsetsPath, trunksPath, true, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, o := range opened {
			o.store.Close()
		}
	}()
	m, err := syncmachine.Open(filepath.Join(dir, "sync"), "ac409cd3-8105-4200-9a32-42a064a98bb0",
		syncmachine.Paths{Policy: policyPath, Handsets: handsetsPath, Trunks: trunksPath})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.Warn = func(error) {}
	good, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	before := opened[0].store.Current()

	write("25:99")

	// doorman check
	if rc := check(); rc != 1 {
		t.Fatalf("check accepted 25:99 (rc %d)", rc)
	}
	// the daemon: refuses the reload, keeps the last good policy, says why
	deadline := time.Now().Add(4 * time.Second)
	for !strings.Contains(logs.String(), "policy reload failed") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	out := logs.String()
	if !strings.Contains(out, "policy reload failed, keeping previous version") || !strings.Contains(out, `25:99`) {
		t.Fatalf("daemon did not report the refused reload: %s", out)
	}
	if strings.Contains(out, "policy reloaded") || opened[0].store.Current() != before {
		t.Fatal("the daemon loaded a file check refuses")
	}
	for _, secret := range []string{"428917", "5550100777", "0100777"} {
		if strings.Contains(out, secret) {
			t.Fatalf("daemon log carries %q: %s", secret, out)
		}
	}
	// the sync observer
	if _, err := m.Observe(); !errors.Is(err, syncmachine.ErrFiles) {
		t.Fatalf("observer err = %v", err)
	}
	if r := m.Rejection(); r == nil || r.File != "policy.toml" || !strings.Contains(r.Message, "25:99") {
		t.Fatalf("observer rejection = %+v", r)
	}
	// A restart now: the current files are refused, so the daemon runs the
	// last good copy it saved when 03:05 was accepted, and says so loudly.
	var restartLogs lockedBuffer
	restarted, err := openLines(files, handsetsPath, trunksPath, false, slog.New(slog.NewTextHandler(&restartLogs, nil)), nil)
	if err != nil {
		t.Fatalf("a box with a last good copy did not start: %v", err)
	}
	if restarted[0].store.Fallback() == nil || restarted[0].store.Current().SourceDigest() != before.SourceDigest() {
		t.Fatal("startup is not running the last good configuration")
	}
	out = restartLogs.String()
	if !strings.Contains(out, "POLICY REFUSED AT STARTUP") || !strings.Contains(out, "25:99") || strings.Contains(out, "428917") {
		t.Fatalf("startup fallback not reported (or not redacted): %s", out)
	}
	restarted[0].store.Close()
	// ... and a connected account is told, through the observer.
	if _, err := m.Observe(); err == nil || m.Rejection() == nil || m.Rejection().File != "policy.toml" {
		t.Fatal("observer does not report the file the daemon refused")
	}
	// Without a usable copy it refuses, as it always has.
	if err := os.RemoveAll(filepath.Join(dir, "policy.last-good")); err != nil {
		t.Fatal(err)
	}
	if _, err := openLines(files, handsetsPath, trunksPath, false, slog.New(slog.DiscardHandler), nil); err == nil {
		t.Fatal("started on a refused file with no last good copy")
	}

	write("03:05")
	if rc := check(); rc != 0 {
		t.Fatalf("check refused the fix: %d", rc)
	}
	if s, err := m.Observe(); err != nil || s.Revision != good.Revision || m.Rejection() != nil {
		t.Fatalf("observer did not accept the fix: %v %+v", err, m.Rejection())
	}
}

// "7:00" has always loaded and means 07:00: check, the daemon and sync all
// accept it, and the wire carries 07:00.
func TestOneDigitHourIsAcceptedEverywhere(t *testing.T) {
	dir := policyDir(t, map[string]string{"policy.toml": oneValidatorPolicy("7:00")})
	policyPath, handsetsPath := filepath.Join(dir, "policy.toml"), filepath.Join(dir, "handsets.toml")
	trunksPath := filepath.Join(dir, "trunks.toml")
	var rc int
	captureStderr(t, func() {
		capture(t, func() {
			rc = runCheck([]string{"--policy-only", "--handsets", handsetsPath, "--trunks", trunksPath,
				"--contacts", filepath.Join(dir, "contacts.toml"), "--messages", filepath.Join(dir, "messages.toml"),
				"--env", filepath.Join(dir, ".env"), policyPath})
		})
	})
	if rc != 0 {
		t.Fatalf("check refused 7:00 (rc %d)", rc)
	}
	files, _ := policy.DiscoverLines(policyPath)
	opened, err := openLines(files, handsetsPath, trunksPath, false, slog.New(slog.DiscardHandler), nil)
	if err != nil || opened[0].store.Fallback() != nil {
		t.Fatalf("daemon refused 7:00: %v", err)
	}
	opened[0].store.Close()
	m, err := syncmachine.Open(filepath.Join(dir, "sync"), "ac409cd3-8105-4200-9a32-42a064a98bb0",
		syncmachine.Paths{Policy: policyPath, Handsets: handsetsPath, Trunks: trunksPath})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err := m.Observe()
	if err != nil {
		t.Fatalf("sync refused 7:00: %v", err)
	}
	if got := s.Configuration.Policies[0].Schedules[0].Start; got != "07:00" {
		t.Fatalf("wire start = %q, want 07:00", got)
	}
}
