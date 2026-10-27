package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncMediaRequiresExplicitTrustedOrigin(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"once"}, {"list", "--source", "http://outside.invalid"}, {"once", "--source", "https://example.invalid/path"}, {"list", "--unknown"}} {
		if runSyncMedia(args) != 2 {
			t.Fatalf("accepted invalid media arguments: %v", args)
		}
	}
	if runSync([]string{"media", "unknown"}) != 2 {
		t.Fatal("sync routing")
	}
	if runSyncMedia([]string{"list", "--source", "https://example.invalid", "--state", t.TempDir()}) != 1 {
		t.Fatal("media ran without enrolment")
	}
}
func TestBooksInstallPreviewRequiresValidLocalArchive(t *testing.T) {
	if runBooks([]string{"install-preview"}) != 2 {
		t.Fatal("missing archive accepted")
	}
	if runBooks([]string{"install-preview", "--archive", filepath.Join(t.TempDir(), "missing")}) != 1 {
		t.Fatal("missing file accepted")
	}
	path := filepath.Join(t.TempDir(), "broken.cmmpack")
	if err := os.WriteFile(path, []byte("not zip"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "library")
	if runBooks([]string{"install-preview", "--archive", path, "--library", root}) != 1 {
		t.Fatal("invalid archive accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid archive changed library")
	}
}
