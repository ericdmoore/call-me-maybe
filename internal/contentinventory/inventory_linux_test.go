//go:build linux

package contentinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"callmemaybe/internal/syncwire"
)

// Linux filenames are arbitrary bytes. These two differ on disk but both
// marshal to "a\uFFFD", which the server refuses as a duplicate id.
func TestNonUTF8NamesAreSkippedNotCollided(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a\xff", "a\xfe"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(t, root, "kept.mp3", "x")
	report := Scan(root)
	if report.State != "partial" || len(report.Items) != 1 || report.Items[0].Id != "kept.mp3" {
		t.Fatalf("invalid names reported: %+v", report)
	}
	data, err := json.Marshal(report)
	if err != nil || syncwire.Decode("ContentInventory", data, &syncwire.ContentInventory{}) != nil {
		t.Fatalf("report refused: %v", err)
	}
}
