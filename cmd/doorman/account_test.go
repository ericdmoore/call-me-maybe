package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/backup"
	"callmemaybe/internal/config"
)

func TestCloudFailurePreservesLocalBackupAndFailsOverallVerification(t *testing.T) {
	root := t.TempDir()
	recipient, identity, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "backup"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityFile(state), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(root, ".env")
	dest := filepath.Join(root, "copies")
	if err := os.WriteFile(env, []byte("BACKUP_RECIPIENT="+recipient+"\nBACKUP_PATH="+dest+"\nBACKUP_CLOUD=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() {
		if rc := runBackup([]string{"run", "--env", env, "--state", state, "--no-voicemail",
			"--asterisk", filepath.Join(root, "missing")}); rc != 1 {
			t.Errorf("missing cloud login must fail overall, got %d", rc)
		}
	})
	if !strings.Contains(out, "✓ file:") || !strings.Contains(out, "read back and opened") ||
		!strings.Contains(out, "✗ cloud: account: sign in") || strings.Contains(out, "✓ verified:") {
		t.Fatalf("incorrect mixed-destination result: %s", out)
	}
	var last lastBackup
	raw, err := os.ReadFile(lastBackupPath(state))
	if err != nil || json.Unmarshal(raw, &last) != nil || last.Verified ||
		len(last.Dests) != 1 || last.Dests[0] != "file" || len(last.Failed) != 1 || last.Failed[0] != "cloud" {
		t.Fatal("last run hid the cloud failure")
	}
	bundles, _ := filepath.Glob(filepath.Join(dest, "*.age"))
	if len(bundles) != 1 {
		t.Fatal("local backup was not retained")
	}
	data, _ := os.ReadFile(bundles[0])
	if _, err := backup.ReadManifest(bytes.NewReader(data), identity); err != nil {
		t.Fatal("local copy is not recoverable")
	}
}

type recordingCloud struct {
	data []byte
	at   time.Time
}

func (c *recordingCloud) Upload(_ context.Context, b []byte, at time.Time) (account.Bundle, error) {
	c.data, c.at = b, at
	return account.Bundle{ID: "00000000-0000-7000-8000-000000000003"}, nil
}

func TestCloudDestinationPreservesManifestTimeAndIsWriteOnly(t *testing.T) {
	client := &recordingCloud{}
	at := time.Now().UTC()
	dest := &cloudDestination{client: client, createdAt: at}
	capture(t, func() {
		if err := dest.Put(context.Background(), "synthetic.age", strings.NewReader("encrypted")); err != nil {
			t.Fatal(err)
		}
	})
	if !client.at.Equal(at) || string(client.data) != "encrypted" || !dest.WriteOnly() {
		t.Fatal("cloud destination changed the payload or timestamp")
	}
	if _, err := dest.Get(context.Background(), "synthetic"); err == nil {
		t.Fatal("upload credentials allowed download")
	}
	if _, err := dest.List(context.Background()); err == nil {
		t.Fatal("upload credentials allowed list")
	}
	if err := dest.Delete(context.Background(), "synthetic"); err == nil {
		t.Fatal("upload credentials allowed deletion")
	}
}

func TestCloudConfigurationIsOptInAndEnablesTimerOnlyWhenTrue(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			env := secretsFrom(map[string]string{"BACKUP_RECIPIENT": "synthetic", "BACKUP_CLOUD": value})
			o := &backupOpts{env: env, stateDir: t.TempDir()}
			dests, errs := o.destinations()
			if (len(dests) == 1) != (value == "true") || (len(errs) > 0) != (value == "invalid") {
				t.Fatal("incorrect cloud destination configuration")
			}
			wants := decideWants(env, "", "", "")
			if wants.Backup != (value == "true") {
				t.Fatal("timer enabled without explicit cloud destination")
			}
		})
	}
}

func TestAccountCommandsPrintNoCredentials(t *testing.T) {
	state := t.TempDir()
	out := capture(t, func() {
		if rc := runAccount([]string{"status", "--state", state, "--json"}); rc != 1 {
			t.Error("empty state must require login")
		}
	})
	var status account.Status
	if json.Unmarshal([]byte(out), &status) != nil || !status.NeedsLogin {
		t.Fatal("missing public enrollment status")
	}
	if strings.Contains(out, "tokens") || strings.Contains(out, "transportKey") {
		t.Fatal("credential fields exposed")
	}
	if runLogin([]string{"--unknown"}) != 2 || runAccount(nil) != 2 ||
		runAccount([]string{"unknown"}) != 2 || runAccount([]string{"status", "extra"}) != 2 {
		t.Fatal("invalid arguments accepted")
	}
}

func TestAccountPackageRemainsOutsideTheDaemon(t *testing.T) {
	err := filepath.WalkDir(filepath.Join("..", "..", "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "account" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"callmemaybe/internal/account"` {
				t.Errorf("%s imports the account client", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "account.go" || file == "backup.go" || file == "recovery.go" || file == "sync.go" || file == "sync_pace.go" {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(`"callmemaybe/internal/account"`)) {
			t.Errorf("%s imports account outside the account, backup, recovery and sync commands", file)
		}
	}
}

// login reads BOX_NAME from its environment first, then from the .env the
// units read, and otherwise suggests the hostname.
func TestLoginSuggestsBOX_NAMEThenTheHostname(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("BOX_NAME=\"Cabin\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOX_NAME", "")
	if got := suggestedBoxName(envFile); got != "Cabin" {
		t.Fatalf(".env: got %q", got)
	}
	t.Setenv("BOX_NAME", "Barn")
	if got := suggestedBoxName(envFile); got != "Barn" {
		t.Fatalf("environment: got %q", got)
	}
	t.Setenv("BOX_NAME", "")
	host, _ := os.Hostname()
	want := config.BoxName(func(string) string { return "" }, os.Hostname)
	if got := suggestedBoxName(filepath.Join(t.TempDir(), "missing")); got != want {
		t.Fatalf("hostname %q: got %q, want %q", host, got, want)
	}
}
