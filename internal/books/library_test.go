package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func largeCatalog(chapters int, prepared bool) Catalog {
	c := Catalog{Schema: 1}
	for i := 0; i < 128; i++ {
		b := Book{ID: fmt.Sprintf("book-%d", i), Title: fmt.Sprintf("Example book %d", i), Language: "en", Origin: "cli"}
		for j := 0; j < chapters; j++ {
			ch := Chapter{Title: fmt.Sprintf("Chapter %d: A sample title", j+1), Audio: fmt.Sprintf("%d/%d.wav", i, j)}
			if prepared {
				ch.Audio, ch.Seconds = chapterName(j)+".sln16", 600.25
			}
			b.Chapters = append(b.Chapters, ch)
		}
		if prepared {
			b.Revision = strings.Repeat("a", 64)
		}
		c.Books = append(c.Books, b)
	}
	return c
}

func TestPrepareRejectsOversizedCatalogBeforeConversion(t *testing.T) {
	root := t.TempDir()
	previous := fixtureBook(t, root, 0.1)
	path := filepath.Join(root, "catalog.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := largeCatalog(200, false)
	if err := validate(c, false); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil || len(data) >= maxJSONBytes {
		t.Fatalf("source must fit the input limit: bytes=%d err=%v", len(data), err)
	}
	source := filepath.Join(t.TempDir(), "source.json")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// No tools or source audio are available: the size failure must precede
	// tool discovery and any filesystem changes to the library.
	t.Setenv("PATH", "")
	if err := Prepare(context.Background(), source, root); !errors.Is(err, errJSONTooLarge) {
		t.Fatalf("wanted catalog size rejection before conversion, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) || !ready(root, previous) {
		t.Fatalf("rejected import changed the working library: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("rejected import created library artifacts: entries=%d err=%v", len(entries), err)
	}
}

func TestCatalogPublicationKeepsLastReadableLibrary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "catalog.json")
	small := largeCatalog(100, true)
	if err := validate(small, true); err != nil {
		t.Fatal(err)
	}
	if err := publishJSON(path, small, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(root); err != nil || len(got.Books) != 128 || len(got.Books[0].Chapters) != 100 {
		t.Fatalf("accepted catalog is unreadable: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Durations are not known during preflight. This catalog fits with the
	// shortest duration encoding but must still be rejected after conversion.
	large := largeCatalog(118, true)
	if err := validate(large, true); err != nil {
		t.Fatal(err)
	}
	if err := preflightCatalog(large); err != nil {
		t.Fatalf("fixture must fit the preflight estimate: %v", err)
	}
	if err := publishJSON(path, large, 0o644); !errors.Is(err, errJSONTooLarge) {
		t.Fatalf("oversized publication returned %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed publication replaced the live catalog")
	}
	if _, err := Load(root); err != nil {
		t.Fatalf("previous catalog no longer loads: %v", err)
	}
}

func TestJSONPublicationSizeBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.json")
	value := strings.Repeat("x", maxJSONBytes-3) // quotes and final newline
	if err := writeJSON(path, value); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := readJSON(path, &got); err != nil || got != value {
		t.Fatalf("exact-limit publication did not round-trip: %v", err)
	}
	if err := writeJSON(path, value+"x"); !errors.Is(err, errJSONTooLarge) {
		t.Fatalf("over-limit publication returned %v", err)
	}
	if err := readJSON(path, &got); err != nil || got != value {
		t.Fatal("over-limit publication replaced the previous value")
	}
}
