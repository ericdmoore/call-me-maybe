package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// init → run → list → verify → restore --dry-run → restore --root: the
// whole life of a bundle on a temp house, without root and without a box.
func TestBackupLifecycle(t *testing.T) {
	root := t.TempDir()
	house := filepath.Join(root, "opt")
	os.MkdirAll(house, 0o755)
	env := filepath.Join(house, ".env")
	dest := filepath.Join(root, "backups")
	os.WriteFile(env, []byte("ARI_USERNAME=u\nARI_PASSWORD=p\nBACKUP_PATH="+dest+"\n"), 0o600)
	os.WriteFile(filepath.Join(house, "policy.toml"), []byte("[house]\nhandsets = []\n"), 0o644)
	os.WriteFile(filepath.Join(house, "handsets.toml"), []byte("[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n"), 0o644)
	state := filepath.Join(root, "state")
	os.MkdirAll(filepath.Join(state, "provision"), 0o700)
	os.WriteFile(filepath.Join(state, "provision", "cert.pem"), []byte("CERT"), 0o600)
	vm := filepath.Join(root, "vm")
	os.MkdirAll(filepath.Join(vm, "household", "family", "INBOX"), 0o755)
	os.WriteFile(filepath.Join(vm, "household", "family", "INBOX", "msg0000.wav"), []byte("RIFF"), 0o644)
	common := []string{"-env", env, "-handsets", filepath.Join(house, "handsets.toml"), "-policy", filepath.Join(house, "policy.toml"),
		"-trunks", filepath.Join(house, "trunks.toml"), "-contacts", filepath.Join(house, "contacts.toml"), "-messages", filepath.Join(house, "messages.toml"),
		"-state", state, "-voicemail", vm, "-asterisk", filepath.Join(root, "etc")}

	// Before init: run does nothing and says why.
	out := capture(t, func() {
		if rc := runBackup(append([]string{"run"}, common...)); rc != 0 {
			t.Errorf("run without a key: rc = %d", rc)
		}
	})
	if !strings.Contains(out, "BACKUP_RECIPIENT is not set") {
		t.Errorf("run without key: %q", out)
	}

	// init prints the identity once; .env gains the recipient only.
	out = capture(t, func() {
		if rc := runBackup(append([]string{"init"}, common...)); rc != 0 {
			t.Errorf("init rc = %d", rc)
		}
	})
	var identity string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "AGE-SECRET-KEY-") {
			identity = strings.TrimSpace(line)
		}
	}
	if identity == "" {
		t.Fatalf("no identity printed:\n%s", out)
	}
	envNow, _ := os.ReadFile(env)
	if !strings.Contains(string(envNow), "BACKUP_RECIPIENT=age1") || strings.Contains(string(envNow), "AGE-SECRET-KEY") {
		t.Fatalf(".env after init: %q", envNow)
	}
	out = capture(t, func() {
		if rc := runBackup(append([]string{"init"}, common...)); rc == 0 {
			t.Error("a second init must refuse to replace the recipient")
		}
	})

	// run delivers to the directory and records the last run.
	out = capture(t, func() {
		if rc := runBackup(append([]string{"run"}, common...)); rc != 0 {
			t.Errorf("run rc = %d", rc)
		}
	})
	if !strings.Contains(out, "✓ file: callmemaybe-") {
		t.Fatalf("run output: %q", out)
	}
	bundles, _ := filepath.Glob(filepath.Join(dest, "callmemaybe-*.age"))
	if len(bundles) != 1 {
		t.Fatalf("bundles = %v", bundles)
	}
	raw, _ := os.ReadFile(bundles[0])
	if strings.Contains(string(raw), "ARI_PASSWORD") || strings.Contains(string(raw), "CERT") {
		t.Fatal("plaintext in the bundle")
	}
	line, ok := describeBackup(state, time.Now(), secretsFrom(map[string]string{"BACKUP_RECIPIENT": "age1x"}))
	if !ok || !strings.Contains(line, "→ file") {
		t.Errorf("check line = %q, ok=%v", line, ok)
	}
	stale, ok := describeBackup(state, time.Now().Add(30*time.Hour), secretsFrom(map[string]string{"BACKUP_RECIPIENT": "age1x"}))
	if ok || !strings.Contains(stale, "STALE") {
		t.Errorf("stale line = %q, ok=%v", stale, ok)
	}

	// verify and restore read the identity from a file, never an argument.
	idFile := filepath.Join(root, "identity")
	os.WriteFile(idFile, []byte(identity+"\n"), 0o600)
	t.Setenv("BACKUP_IDENTITY_FILE", idFile)
	out = capture(t, func() {
		if rc := runBackup(append([]string{"verify"}, common...)); rc != 0 {
			t.Errorf("verify rc = %d", rc)
		}
	})
	if !strings.Contains(out, "opens:") || !strings.Contains(out, "house") {
		t.Errorf("verify: %q", out)
	}
	out = capture(t, func() {
		if rc := runRestore(append([]string{"--latest", "--dry-run"}, common...)); rc != 0 {
			t.Errorf("dry run rc = %d", rc)
		}
	})
	if !strings.Contains(out, "Would write:") || !strings.Contains(out, "cert.pem") {
		t.Errorf("dry run: %q", out)
	}
	// A real restore into another root is byte-identical.
	other := filepath.Join(root, "restored")
	out = capture(t, func() {
		if rc := runRestore(append([]string{"--latest", "--root", other}, common...)); rc != 0 {
			t.Errorf("restore rc = %d: %s", rc, out)
		}
	})
	got, err := os.ReadFile(filepath.Join(other, env))
	if err != nil || string(got) != string(envNow) {
		t.Errorf("restored .env differs: %v\n%s", err, got)
	}
	if c, _ := os.ReadFile(filepath.Join(other, state, "provision", "cert.pem")); string(c) != "CERT" {
		t.Error("the provisioning certificate did not come back")
	}
	// Onto a box that already has a .env: refused without --force.
	out = capture(t, func() {
		if rc := runRestore(append([]string{"--latest", "--root", other}, common...)); rc == 0 {
			t.Error("restore over an existing house must refuse without --force")
		}
	})
}

func TestTheBackupPackageIsReachableFromItsCommandsOnly(t *testing.T) {
	fset := token.NewFileSet()
	root := filepath.Join("..", "..", "internal")
	dirs, _ := os.ReadDir(root)
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "backup" {
			continue
		}
		pkgs, err := parser.ParseDir(fset, filepath.Join(root, d.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range pkgs {
			for path, f := range pkg.Files {
				for _, imp := range f.Imports {
					if strings.Contains(imp.Path.Value, "internal/backup") {
						t.Errorf("%s imports internal/backup", path)
					}
				}
			}
		}
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "backup.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "\"callmemaybe/internal/backup\"") {
			t.Errorf("%s imports internal/backup; only backup.go may", f)
		}
	}
}
