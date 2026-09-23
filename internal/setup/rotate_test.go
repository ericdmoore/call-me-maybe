package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotateSecretsReplacesAddsAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("# comment\nHANDSET_KITCHEN_PASSWORD=old-k\nOTHER=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := 0
	gen := func() (string, error) { n++; return "new-" + string(rune('a'+n-1)), nil }
	if err := RotateSecrets(env, []string{"HANDSET_KITCHEN_PASSWORD", "HANDSET_THEATER_PASSWORD"}, gen); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(env)
	s := string(got)
	if strings.Contains(s, "old-k") {
		t.Fatalf("old value survived:\n%s", s)
	}
	for _, want := range []string{"# comment\n", "HANDSET_KITCHEN_PASSWORD=new-a\n", "OTHER=keep\n", "HANDSET_THEATER_PASSWORD=new-b\n"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	info, _ := os.Stat(env)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	backups, _ := filepath.Glob(env + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("want one backup, got %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); !strings.Contains(string(b), "old-k") {
		t.Fatal("the backup should hold the old value")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temp file left behind: %v", leftovers)
	}
}

func TestRotateSecretsRefusesABadKeyBeforeTouchingTheFile(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(env, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RotateSecrets(env, []string{"not a key"}, func() (string, error) { return "x", nil }); err == nil {
		t.Fatal("a malformed key must be refused")
	}
	if got, _ := os.ReadFile(env); string(got) != "A=1\n" {
		t.Fatal("the file must be untouched after a refusal")
	}
}
