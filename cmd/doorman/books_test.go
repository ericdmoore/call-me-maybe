package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBooksCLIValidationAndListing(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"prepare"}, {"prepare", "--unknown"}, {"list", "extra"}} {
		if got := runBooks(args); got != 2 {
			t.Fatalf("usage exit %d", got)
		}
	}
	root := t.TempDir()
	if got := runBooks([]string{"list", "--library", root}); got != 1 {
		t.Fatalf("missing catalog exit %d", got)
	}
	if got := runBooks([]string{"prepare", "--source", filepath.Join(root, "absent"), "--library", root}); got != 1 {
		t.Fatalf("missing input exit %d", got)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(`{"schema":1,"books":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runBooks([]string{"list", "--library", root}); got != 0 {
		t.Fatalf("list exit %d", got)
	}
}
