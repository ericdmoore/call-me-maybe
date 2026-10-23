package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lastGoodFixture(t *testing.T, policyBody string) (dir, policyPath, handsetsPath string, o Options) {
	t.Helper()
	dir = t.TempDir()
	policyPath, handsetsPath = filepath.Join(dir, "policy.toml"), filepath.Join(dir, "handsets.toml")
	os.WriteFile(policyPath, []byte(policyBody), 0o600)
	os.WriteFile(handsetsPath, []byte(validatorHandsets), 0o600)
	o = Options{StrictUnknownKeys: true, TrunksPath: filepath.Join(dir, "trunks.toml"), LastGood: LastGoodPath(policyPath, DefaultLine)}
	return
}

func TestLastGoodIsWrittenOnAcceptanceOnly(t *testing.T) {
	good := withSchedule("start = \"03:05\"\nend = \"07:00\"")
	dir, policyPath, handsetsPath, o := lastGoodFixture(t, good)
	if got := LastGoodPath(policyPath, DefaultLine); got != filepath.Join(dir, "policy.last-good", DefaultLine) {
		t.Fatalf("LastGoodPath = %s", got)
	}
	failed := make(chan error, 4)
	reloaded := make(chan *Policy, 4)
	store, err := OpenStoreWith(policyPath, handsetsPath, true, o, func(p *Policy) { reloaded <- p }, func(err error) { failed <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	link := o.LastGood
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(link, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	target, err := os.Readlink(link)
	if err != nil || read("policy.toml") != good || read("handsets.toml") != validatorHandsets {
		t.Fatalf("no last good copy after an accepted load: %v", err)
	}
	if _, err := os.Stat(filepath.Join(link, "trunks.toml")); !os.IsNotExist(err) {
		t.Fatal("an absent trunks.toml was copied")
	}
	info, _ := os.Stat(filepath.Join(link, "policy.toml"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("copy mode %v, want 0600", info.Mode().Perm())
	}

	// Refused: the copy is untouched.
	os.WriteFile(policyPath, []byte(withSchedule("enabled = false\nstart = \"25:99\"\nend = \"07:00\"")), 0o600)
	select {
	case <-failed:
	case <-time.After(4 * time.Second):
		t.Fatal("refusal not reported")
	}
	if now, _ := os.Readlink(link); now != target || read("policy.toml") != good {
		t.Fatal("a refused file replaced the last good copy")
	}

	// Accepted again: the link moves, in one rename, to a complete copy, and
	// the superseded copy and any temporaries are gone.
	next := withSchedule("start = \"04:00\"\nend = \"07:00\"")
	os.WriteFile(policyPath, []byte(next), 0o600)
	select {
	case <-reloaded:
	case <-time.After(4 * time.Second):
		t.Fatal("valid edit not reloaded")
	}
	if now, _ := os.Readlink(link); now == target || read("policy.toml") != next {
		t.Fatal("the last good copy did not follow an accepted reload")
	}
	entries, _ := os.ReadDir(filepath.Dir(link))
	if len(entries) != 2 { // the link and the one copy it names
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("last good directory holds %v", names)
	}
}

func TestStartupFallsBackToLastGood(t *testing.T) {
	good := withSchedule("start = \"03:05\"\nend = \"07:00\"")
	dir, policyPath, handsetsPath, o := lastGoodFixture(t, good)
	os.WriteFile(o.TrunksPath, []byte("[[trunks]]\nid = \"t\"\nhost = \"h\"\nusername = \"u\"\npassword_env = \"P\"\n"), 0o600)
	first, err := OpenStoreWith(policyPath, handsetsPath, false, o, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := first.Current().SourceDigest()
	first.Close()
	if _, err := os.Stat(filepath.Join(o.LastGood, "trunks.toml")); err != nil {
		t.Fatal("trunks.toml missing from the copy")
	}

	// An upgrade or a bad edit while stopped: the current file is refused.
	os.WriteFile(policyPath, []byte(withSchedule("enabled = false\nstart = \"25:99\"\nend = \"07:00\"")), 0o600)
	reloaded := make(chan *Policy, 2)
	failed := make(chan error, 2)
	store, err := OpenStoreWith(policyPath, handsetsPath, true, o, func(p *Policy) { reloaded <- p }, func(err error) { failed <- err })
	if err != nil {
		t.Fatalf("did not fall back: %v", err)
	}
	defer store.Close()
	if store.Fallback() == nil || !strings.Contains(store.Fallback().Error(), "25:99") || store.Current().SourceDigest() != want {
		t.Fatalf("fallback = %v", store.Fallback())
	}
	// The refusal was reported once, at startup; the poller does not repeat it.
	select {
	case err := <-failed:
		t.Fatalf("refusal reported again: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	// A fix is picked up by the ordinary reload, and becomes the new copy.
	fixed := withSchedule("start = \"05:00\"\nend = \"07:00\"")
	os.WriteFile(policyPath, []byte(fixed), 0o600)
	select {
	case <-reloaded:
	case <-time.After(4 * time.Second):
		t.Fatal("fix not reloaded")
	}
	if store.Fallback() != nil {
		t.Fatal("still reports a fallback after an accepted reload")
	}
	if data, _ := os.ReadFile(filepath.Join(o.LastGood, "policy.toml")); string(data) != fixed {
		t.Fatal("the fix did not become the last good copy")
	}

	// No usable copy: refused exactly as before.
	os.WriteFile(policyPath, []byte(withSchedule("start = \"25:99\"\nend = \"07:00\"")), 0o600)
	os.RemoveAll(filepath.Join(dir, "policy.last-good"))
	if _, err := OpenStoreWith(policyPath, handsetsPath, false, o, nil, nil); err == nil || !strings.Contains(err.Error(), "25:99") {
		t.Fatalf("err = %v, want the refusal", err)
	}
	// Without LastGood nothing is written at all.
	os.WriteFile(policyPath, []byte(good), 0o600)
	o.LastGood = ""
	s, err := OpenStoreWith(policyPath, handsetsPath, false, o, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(filepath.Join(dir, "policy.last-good")); !os.IsNotExist(err) {
		t.Fatal("wrote a last good copy without being asked")
	}
}

func TestLastGoodWriteFailureIsReportedNotFatal(t *testing.T) {
	_, policyPath, handsetsPath, o := lastGoodFixture(t, withSchedule("start = \"03:05\"\nend = \"07:00\""))
	// A file where the copy's directory should be.
	os.WriteFile(filepath.Dir(o.LastGood), []byte("in the way"), 0o600)
	var reported error
	o.OnLastGoodError = func(err error) { reported = err }
	s, err := OpenStoreWith(policyPath, handsetsPath, false, o, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reported == nil {
		t.Fatal("a failed copy was not reported")
	}
}
