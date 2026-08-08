package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("# empty\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func discoveredNames(files []LineFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Name
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The compatibility gate at the file level: an install that has only ever had
// one policy file must discover exactly one line, and it must be the default.
func TestDiscoverLinesFindsOnlyTheDefaultWhenThereIsOne(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "policy.toml")
	touch(t, dir, "handsets.toml")
	touch(t, dir, ".env")

	path := filepath.Join(dir, "policy.toml")
	files, ignored := DiscoverLines(path)
	if !sameStrings(discoveredNames(files), []string{DefaultLine}) {
		t.Fatalf("lines = %v, want just the default", discoveredNames(files))
	}
	if files[0].Path != path {
		t.Errorf("default line path = %q, want the policy path as given", files[0].Path)
	}
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want nothing", ignored)
	}
}

func TestDiscoverLinesFindsSiblings(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"policy.toml", "policy.biz.toml", "policy.kids.toml", "handsets.toml"} {
		touch(t, dir, f)
	}

	files, _ := DiscoverLines(filepath.Join(dir, "policy.toml"))
	// Default first, then sorted: logs and `doorman check` must read the same
	// way twice running, and directory order is not a promise any OS makes.
	if !sameStrings(discoveredNames(files), []string{DefaultLine, "biz", "kids"}) {
		t.Fatalf("lines = %v, want [default biz kids]", discoveredNames(files))
	}
	if got, want := files[1].Path, filepath.Join(dir, "policy.biz.toml"); got != want {
		t.Errorf("biz path = %q, want %q", got, want)
	}
}

// Every one of these has sat beside a policy.toml at some point: rotate's temp
// file, init's backup, an editor's leavings. None of them is a line.
func TestDiscoverLinesIgnoresWhatIsNotALinePolicy(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{
		"policy.toml",
		"policy.toml.bak-20260101-120000",
		"policy.toml~",
		".policy-rotate-123456",
		"handsets.biz.toml",
		"policy.biz.toml.bak",
	} {
		touch(t, dir, f)
	}
	if err := os.Mkdir(filepath.Join(dir, "policy.archive.toml"), 0o750); err != nil {
		t.Fatal(err)
	}

	files, ignored := DiscoverLines(filepath.Join(dir, "policy.toml"))
	if !sameStrings(discoveredNames(files), []string{DefaultLine}) {
		t.Fatalf("lines = %v, want just the default", discoveredNames(files))
	}
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want nothing — none of these looks like a line", ignored)
	}
}

// policy.default.toml is the trap: somebody writes it expecting to configure
// the default line, and it silently does nothing. Say so.
func TestDiscoverLinesReportsFilesItCannotUse(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"policy.toml", "policy.default.toml", "policy..toml", "policy.-odd.toml"} {
		touch(t, dir, f)
	}

	files, ignored := DiscoverLines(filepath.Join(dir, "policy.toml"))
	if !sameStrings(discoveredNames(files), []string{DefaultLine}) {
		t.Fatalf("lines = %v, want just the default", discoveredNames(files))
	}
	if len(ignored) != 3 {
		t.Fatalf("ignored %d files, want 3: %v", len(ignored), ignored)
	}
	for _, ig := range ignored {
		if ig.Reason == "" {
			t.Errorf("%s was ignored with no reason given", ig.Path)
		}
	}
	if filepath.Base(ignored[0].Path) != "policy.-odd.toml" {
		t.Errorf("ignored files are not sorted: %v", ignored)
	}
}

// Paths get printed back at operators, so a discovered sibling has to be in
// the same shape as the path they configured — not silently reshaped by a
// filepath.Join that drops a leading "./".
func TestDiscoverLinesKeepsThePathShapeItWasGiven(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "policy.toml")
	touch(t, dir, "policy.biz.toml")
	t.Chdir(dir)

	files, _ := DiscoverLines("./policy.toml")
	if len(files) != 2 {
		t.Fatalf("lines = %v, want the default and biz", discoveredNames(files))
	}
	if files[1].Path != "./policy.biz.toml" {
		t.Errorf("biz path = %q, want ./policy.biz.toml", files[1].Path)
	}
}

func TestDiscoverLinesHandlesAPolicyFileNotNamedToml(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "rules.conf")
	touch(t, dir, "rules.biz.toml")

	files, ignored := DiscoverLines(filepath.Join(dir, "rules.conf"))
	if !sameStrings(discoveredNames(files), []string{DefaultLine}) {
		t.Fatalf("lines = %v, want just the default", discoveredNames(files))
	}
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want nothing", ignored)
	}
}

func TestDiscoverLinesSurvivesAMissingDirectory(t *testing.T) {
	files, ignored := DiscoverLines(filepath.Join(t.TempDir(), "nope", "policy.toml"))
	if !sameStrings(discoveredNames(files), []string{DefaultLine}) {
		t.Fatalf("lines = %v, want just the default", discoveredNames(files))
	}
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want nothing", ignored)
	}
}
