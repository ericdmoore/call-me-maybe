// Package updatecheck provides an operator-only, best-effort release notice.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"callmemaybe/internal/config"
	"callmemaybe/internal/xdg"
)

const endpoint = "https://api.github.com/repos/ericdmoore/call-me-maybe/releases/latest"
const releaseURL = "https://github.com/ericdmoore/call-me-maybe/releases/tag/"

// Allowed is deliberately an allowlist: new services and protocol modes must
// never acquire network activity just because their streams happen to be TTYs.
func Allowed(command string) bool {
	switch command {
	case "check", "render", "rotate", "balance", "calls", "e164", "schema", "pack", "template", "init":
		return true
	}
	return false
}

type release struct {
	Tag         string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
}
type state struct {
	Checked  time.Time `json:"checked_at"`
	Notified time.Time `json:"notified_at,omitempty"`
	Release  release   `json:"release"`
}

type deps struct {
	now            func() time.Time
	getenv         func(string) string
	home           func() (string, error)
	tty            func(*os.File) bool
	stdout, stderr *os.File
	errOut         io.Writer
	client         *http.Client
	endpoint       string
	upgrade        string
}

// Run overlaps the independent release fetch with the command, preserving its
// exit status. started is captured at entry, not when the command finishes.
func Run(started time.Time, version string, args []string, command func() int) int {
	instruction := "To upgrade, rerun install.sh (https://raw.githubusercontent.com/ericdmoore/call-me-maybe/main/install.sh)."
	if runtime.GOOS == "linux" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "arm") {
		instruction = "To upgrade, scp the new binary from your workstation; see docs/RUNBOOK.md (Deploy)."
	}
	return run(started, version, args, command, deps{
		now: time.Now, getenv: os.Getenv, home: os.UserHomeDir, tty: isTerminal,
		stdout: os.Stdout, stderr: os.Stderr, errOut: os.Stderr, client: &http.Client{}, endpoint: endpoint, upgrade: instruction,
	})
}

func run(started time.Time, version string, args []string, command func() int, d deps) int {
	// Opt-out precedes even terminal probing and state-directory resolution.
	if !config.UpdateChecksEnabled(d.getenv) || d.getenv("CI") != "" || len(args) == 0 || !Allowed(args[0]) ||
		parseVersion(version) == nil || sourceStamp.MatchString(version) || strings.Contains(version, "-dirty") ||
		!d.tty(d.stdout) || !d.tty(d.stderr) {
		return command()
	}
	deadline := started.Add(time.Second)
	remaining := deadline.Sub(d.now())
	if remaining <= 0 {
		return command()
	}
	ctx, cancel := context.WithTimeout(context.Background(), remaining)
	defer cancel()
	var mu sync.Mutex
	var cached state
	var path string
	done := make(chan struct{})
	go func() {
		defer close(done)
		root := xdg.Dir("STATE", d.getenv, d.home)
		if root == "" {
			return
		}
		p := filepath.Join(root, "doorman", "update-check.json")
		s := readState(p)
		mu.Lock()
		cached = s
		path = p
		mu.Unlock()
		if !s.Checked.IsZero() && d.now().Sub(s.Checked) < 24*time.Hour {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint, nil)
		if err != nil {
			return
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := d.client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var r release
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r) != nil || parseVersion(r.Tag) == nil {
			return
		}
		s.Checked = d.now()
		s.Release = r
		// Persist even if the command failed or has not finished yet.
		mu.Lock()
		s.Notified = cached.Notified
		_ = writeState(p, s)
		cached = s
		mu.Unlock()
	}()
	code := command()
	if code != 0 {
		return code
	}
	if remaining = deadline.Sub(d.now()); remaining > 0 {
		timer := time.NewTimer(remaining)
		select {
		case <-done:
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
	// A cache write must not turn the exit deadline into an unbounded wait.
	if !mu.TryLock() {
		return code
	}
	defer mu.Unlock()
	s, p := cached, path
	if newer(s.Release.Tag, version) && (s.Notified.IsZero() || d.now().Sub(s.Notified) >= 24*time.Hour) {
		fmt.Fprintf(d.errOut, "\nA new release of doorman is available: %s → %s\n%s\n%s%s\n", version, s.Release.Tag, d.upgrade, releaseURL, url.PathEscape(s.Release.Tag))
		s.Notified = d.now()
		cached = s
		_ = writeState(p, s)
	}
	return code
}

func readState(path string) state {
	b, err := os.ReadFile(path)
	var s state
	if err != nil || json.Unmarshal(b, &s) != nil || parseVersion(s.Release.Tag) == nil {
		return state{}
	}
	return s
}
func writeState(path string, s state) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".update-check-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
