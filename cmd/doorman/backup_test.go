package main

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/backup"
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

	// With the on-box key copy in place, run verifies what it wrote and
	// the key copy never enters the bundle.
	os.MkdirAll(filepath.Join(state, "backup"), 0o700)
	os.WriteFile(identityFile(state), []byte(identity+"\n"), 0o600)
	out = capture(t, func() {
		if rc := runBackup(append([]string{"run"}, common...)); rc != 0 {
			t.Errorf("run rc = %d", rc)
		}
	})
	if !strings.Contains(out, "read back and opened") || !strings.Contains(out, "✓ verified") {
		t.Errorf("run should read each destination's copy back: %q", out)
	}
	line, _ = describeBackup(state, time.Now(), secretsFrom(map[string]string{"BACKUP_RECIPIENT": "age1x"}))
	if !strings.Contains(line, ", verified") {
		t.Errorf("check line = %q", line)
	}
	// A configured key that cannot be read fails the run and the check.
	t.Setenv("BACKUP_IDENTITY_FILE", filepath.Join(root, "missing.key"))
	out = capture(t, func() {
		if rc := runBackup(append([]string{"run"}, common...)); rc != 1 {
			t.Errorf("an unreadable configured key must fail the run, rc = %d", rc)
		}
	})
	if line, ok := describeBackup(state, time.Now(), secretsFrom(map[string]string{"BACKUP_RECIPIENT": "age1x"})); ok || !strings.Contains(line, "failed: verify") {
		t.Errorf("check must fail: %q", line)
	}
	t.Setenv("BACKUP_IDENTITY_FILE", "")
	// A wrong on-box key (someone rotated the recipient but not the copy)
	// must stop the bundle from leaving the box: the good bundles at the
	// destinations stay, and the failure is recorded.
	before, _ := filepath.Glob(filepath.Join(dest, "callmemaybe-*.age"))
	_, wrong, _ := backupNewKeyForTest()
	os.WriteFile(identityFile(state), []byte(wrong+"\n"), 0o600)
	out = capture(t, func() {
		if rc := runBackup(append([]string{"run"}, common...)); rc != 1 {
			t.Errorf("a bundle that does not open must fail the run, rc = %d", rc)
		}
	})
	if !strings.Contains(out, "not delivered") {
		t.Errorf("run with a wrong key: %q", out)
	}
	after, _ := filepath.Glob(filepath.Join(dest, "callmemaybe-*.age"))
	if len(after) != len(before) {
		t.Errorf("an unverifiable bundle was delivered: before %d, after %d", len(before), len(after))
	}
	if line, ok := describeBackup(state, time.Now(), secretsFrom(map[string]string{"BACKUP_RECIPIENT": "age1x"})); ok || !strings.Contains(line, "failed: verify") {
		t.Errorf("check must report the failed verification: %q", line)
	}
	os.Remove(identityFile(state))
	// verify and restore read the identity from a file, never an argument.
	idFile := filepath.Join(root, "identity")
	os.WriteFile(idFile, []byte(identity+"\n"), 0o600)
	t.Setenv("BACKUP_IDENTITY_FILE", idFile)
	out = capture(t, func() {
		if rc := runBackup(append([]string{"verify"}, common...)); rc != 0 {
			t.Errorf("verify rc = %d", rc)
		}
	})
	if !strings.Contains(out, "at file opens:") || !strings.Contains(out, "house") {
		t.Errorf("verify: %q", out)
	}
	out = capture(t, func() {
		if rc := runBackup(append([]string{"verify", "-from", "s3"}, common...)); rc != 2 {
			t.Errorf("-from a destination that is not configured must be refused, rc = %d", rc)
		}
	})
	out = capture(t, func() {
		if rc := runRestore(append([]string{"--latest", "--dry-run"}, common...)); rc != 0 {
			t.Errorf("dry run rc = %d", rc)
		}
	})
	if !strings.Contains(out, "Would write:") || !strings.Contains(out, "cert.pem") {
		t.Errorf("dry run: %q", out)
	}
	if strings.Contains(out, "identity.key") || strings.Contains(out, "last.json") {
		t.Error("the backup state directory must never be in a bundle")
	}
	// An identity file left somewhere a bundle collects is flagged.
	t.Setenv("BACKUP_IDENTITY_FILE", filepath.Join(state, "provision", "identity.key"))
	if line, ok := describeBackup(state, time.Now(), secretsFrom(map[string]string{"BACKUP_RECIPIENT": "age1x"})); ok || !strings.Contains(line, "lies where bundles are collected") {
		t.Errorf("a key inside the collected tree must fail check: %q", line)
	}
	t.Setenv("BACKUP_IDENTITY_FILE", idFile)
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

func backupNewKeyForTest() (string, string, error) { return backup.NewKey() }

// A destination that acknowledges the PUT but keeps a truncated object is
// a failed destination: "verified" means what was retained opens.
func TestADestinationThatKeepsATruncatedObjectIsNotVerified(t *testing.T) {
	rcpt, id, _ := backup.NewKey()
	bad := &truncatingDest{}
	ctx := context.Background()
	var buf bytesBuffer
	m := &backup.Manifest{Format: backup.FormatVersion, Host: "h", CreatedAt: time.Now()}
	if err := backup.Bundle(&buf, m, nil, rcpt); err != nil {
		t.Fatal(err)
	}
	res := backup.Deliver(ctx, []backup.Destination{bad}, backup.Name("h", time.Now()), buf.Bytes(), 7, 8, time.Now())
	if res[0].Err != nil {
		t.Fatal("the fake must accept the PUT")
	}
	if err := readBack(ctx, bad, backup.Name("h", time.Now()), id, 0); err == nil {
		t.Error("a truncated object must not read back as verified")
	}
}

type truncatingDest struct{ kept []byte }

func (d *truncatingDest) Name() string { return "trunc" }
func (d *truncatingDest) Put(_ context.Context, _ string, r io.Reader) error {
	b, _ := io.ReadAll(r)
	d.kept = b[:len(b)/2]
	return nil
}
func (d *truncatingDest) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(d.kept)), nil
}
func (d *truncatingDest) List(context.Context) ([]string, error) { return nil, nil }
func (d *truncatingDest) Delete(context.Context, string) error   { return nil }

type bytesBuffer = bytes.Buffer
