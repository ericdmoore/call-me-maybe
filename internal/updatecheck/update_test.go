package updatecheck

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, handler http.HandlerFunc) (deps, *bytes.Buffer, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	root := t.TempDir()
	out := new(bytes.Buffer)
	return deps{now: time.Now, getenv: func(k string) string {
		if k == "XDG_STATE_HOME" {
			return root
		}
		return ""
	}, home: func() (string, error) { return "", fmt.Errorf("no home") }, tty: func(*os.File) bool { return true }, stdout: os.Stdout, stderr: os.Stderr, errOut: out, client: srv.Client(), endpoint: srv.URL, upgrade: "To upgrade, rerun install.sh."}, out, filepath.Join(root, "doorman", "update-check.json")
}
func latest(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `{"tag_name":"v0.5.0","published_at":"2026-09-01T00:00:00Z","html_url":"https://evil.invalid"}`)
}

func TestGatesNeverRequest(t *testing.T) {
	for _, name := range []string{"stdout pipe", "stderr pipe", "CI", "disabled", "dev", "source", "dirty", "daemon", "lsp", "unknown", "version", "help"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			d, out, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); latest(w, r) })
			args := []string{"check"}
			version := "0.4.1"
			getenv := d.getenv
			switch name {
			case "stdout pipe":
				d.tty = func(f *os.File) bool { return f != os.Stdout }
			case "stderr pipe":
				d.tty = func(f *os.File) bool { return f != os.Stderr }
			case "CI":
				d.getenv = func(k string) string {
					if k == "CI" {
						return "1"
					}
					return getenv(k)
				}
			case "disabled":
				d.getenv = func(k string) string {
					if k == "UPDATE_CHECK_ENABLED" {
						return "false"
					}
					return getenv(k)
				}
				d.tty = func(*os.File) bool { t.Error("opt-out must precede TTY probe"); return true }
			case "dev":
				version = "dev"
			case "source":
				version = "0.4.1-2-gabcdef"
			case "dirty":
				version = "0.4.1-dirty"
			case "daemon":
				args = nil
			default:
				args = []string{name}
			}
			code := run(d.now(), version, args, func() int { return 0 }, d)
			if code != 0 || requests.Load() != 0 || out.Len() != 0 {
				t.Fatalf("code=%d requests=%d notice=%q", code, requests.Load(), out)
			}
		})
	}
}
func TestNoticeCacheAndCadence(t *testing.T) {
	var requests atomic.Int32
	d, out, path := fixture(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); latest(w, r) })
	now := time.Now()
	d.now = func() time.Time { return now }
	stdout := new(bytes.Buffer)
	code := run(now, "0.4.1", []string{"schema"}, func() int { fmt.Fprint(stdout, "protocol"); fmt.Fprint(out, "command\n"); return 0 }, d)
	if code != 0 || stdout.String() != "protocol" || !strings.HasPrefix(out.String(), "command\n\nA new release") || !strings.Contains(out.String(), "0.4.1 → v0.5.0") || !strings.Contains(out.String(), releaseURL+"v0.5.0") || strings.Contains(out.String(), "evil.invalid") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, out)
	}
	for p, want := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Fatalf("%s mode %o", p, st.Mode().Perm())
		}
	}
	out.Reset()
	now = now.Add(time.Hour)
	run(now, "0.4.1", []string{"schema"}, func() int { return 0 }, d)
	if requests.Load() != 1 || out.Len() != 0 {
		t.Fatalf("daily gate: requests=%d out=%q", requests.Load(), out)
	}
	now = now.Add(24 * time.Hour)
	run(now, "0.4.1", []string{"schema"}, func() int { return 0 }, d)
	if requests.Load() != 2 || out.Len() == 0 {
		t.Fatal("next day should check and notify")
	}
}
func TestCachedNewerAndFailedCommand(t *testing.T) {
	var requests atomic.Int32
	d, out, path := fixture(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); latest(w, r) })
	if err := writeState(path, state{Checked: d.now(), Release: release{Tag: "v0.5.0"}}); err != nil {
		t.Fatal(err)
	}
	if code := run(d.now(), "0.4.1", []string{"check"}, func() int { return 3 }, d); code != 3 || out.Len() != 0 {
		t.Fatalf("code=%d out=%q", code, out)
	}
	run(d.now(), "0.4.1", []string{"check"}, func() int { return 0 }, d)
	if requests.Load() != 0 || out.Len() == 0 {
		t.Fatalf("cached notice requests=%d out=%q", requests.Load(), out)
	}
}
func TestFetchOverlapsAndPersistsBeforeCommandEnds(t *testing.T) {
	fetched := make(chan struct{})
	d, out, path := fixture(t, func(w http.ResponseWriter, r *http.Request) { latest(w, r); close(fetched) })
	run(d.now(), "0.4.1", []string{"check"}, func() int {
		select {
		case <-fetched:
		case <-time.After(time.Second):
			t.Fatal("fetch did not overlap")
		}
		deadline := time.Now().Add(time.Second)
		for readState(path).Checked.IsZero() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if readState(path).Checked.IsZero() {
			t.Fatal("fetch result not cached while command runs")
		}
		return 1
	}, d)
	if out.Len() != 0 {
		t.Fatal("failed command printed notice")
	}
	run(d.now(), "0.4.1", []string{"check"}, func() int { return 0 }, d)
	if out.Len() == 0 {
		t.Fatal("later run did not use cached result")
	}
}
func TestSilentVersions(t *testing.T) {
	for _, version := range []string{"0.5.0", "0.6.0", "9.0.0"} {
		t.Run(version, func(t *testing.T) {
			d, out, _ := fixture(t, latest)
			run(d.now(), version, []string{"check"}, func() int { return 0 }, d)
			if out.Len() != 0 {
				t.Fatalf("unexpected notice: %s", out)
			}
		})
	}
}
func TestFailuresAndBudget(t *testing.T) {
	for _, kind := range []string{"server", "timeout", "long command", "bad json", "bad tag"} {
		t.Run(kind, func(t *testing.T) {
			requested := make(chan struct{})
			d, out, path := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				close(requested)
				switch kind {
				case "server":
					w.WriteHeader(500)
				case "bad json":
					fmt.Fprint(w, "{")
				case "bad tag":
					fmt.Fprint(w, `{"tag_name":"v9.0.0\nexecute me"}`)
				default:
					<-r.Context().Done()
				}
			})
			// Simulate 850ms of startup/command work already spent. The checker gets
			// only the remaining 150ms, not another second at exit.
			start := time.Now()
			code := run(start.Add(-850*time.Millisecond), "0.4.1", []string{"check"}, func() int {
				if kind == "long command" {
					<-requested
					time.Sleep(180 * time.Millisecond)
				}
				return 0
			}, d)
			elapsed := time.Since(start)
			if elapsed > 300*time.Millisecond || code != 0 || out.Len() != 0 {
				t.Fatalf("elapsed=%s code=%d out=%q", elapsed, code, out)
			}
			if !readState(path).Checked.IsZero() {
				t.Fatal("failed fetch advanced timestamp")
			}
		})
	}
}
func TestCorruptStateChecksNow(t *testing.T) {
	d, out, path := fixture(t, latest)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	run(d.now(), "0.4.1", []string{"check"}, func() int { return 0 }, d)
	if out.Len() == 0 || readState(path).Checked.IsZero() {
		t.Fatal("corrupt cache prevented fetch")
	}
}
func TestSemver(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v1.10.0", "1.9.9", true}, {"1.0.0", "1.0.0-rc.1", true}, {"1.0.0-rc.10", "1.0.0-rc.2", true},
		{"1.0.0-alpha.1", "1.0.0-alpha", true}, {"1.0.0-alpha", "1.0.0-1", true}, {"1.0.0+new", "1.0.0+old", false},
		{"1.0.0-rc.1", "1.0.0", false}, {"01.0.0", "0.1.0", false}, {"1.0.0-01", "0.1.0", false}, {"1.0", "0.1.0", false},
		{"999999999999999999999.0.0", "1.0.0", true},
	} {
		if got := newer(tc.a, tc.b); got != tc.want {
			t.Errorf("newer(%q,%q)=%v", tc.a, tc.b, got)
		}
	}
}
func TestNonTerminals(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("character device is not a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(w) {
		t.Fatal("pipe is not a terminal")
	}
}
