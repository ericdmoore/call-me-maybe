package books

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/mediaformat"
)

func writeRecordedFixture(t *testing.T, files map[string][]byte) (string, string) {
	t.Helper()
	members := map[string][]byte{}
	for name, data := range files {
		if name != "SHA256SUMS" {
			members[name] = data
		}
	}
	data := previewArchive(t, members)
	file := filepath.Join(t.TempDir(), "book.cmmpack")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file, fmt.Sprintf("%x", sha256.Sum256(data))
}
func TestAmbiguousZIPTrailerIsRefusedBeforeParserAllocation(t *testing.T) {
	data := previewArchive(t, map[string][]byte{"pack.json": []byte("{}"), "audio/x.opus": []byte("audio")})
	end := len(data) - 22
	fake := append([]byte(nil), data[end:]...)
	comment := append(fake, []byte("trailing")...)
	binary.LittleEndian.PutUint16(data[end+20:end+22], uint16(len(comment)))
	data = append(data, comment...)
	if boundedZipDirectory(bytes.NewReader(data), int64(len(data))) {
		t.Fatal("accepted a second EOCD hidden in the real comment")
	}
}
func TestFullBookUsesDecodedTimelineAndPreservesPreviewIdentity(t *testing.T) {
	data, _ := recordedArchive(t)
	original, err := previewFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"novel-x", strings.Repeat("a", 70)} {
		t.Run(id, func(t *testing.T) {
			files := map[string][]byte{}
			for name, data := range original {
				files[name] = data
			}
			manifest := strings.ReplaceAll(string(files["pack.json"]), "recorded-example", id)
			manifest = strings.ReplaceAll(strings.ReplaceAll(manifest, "1000", "800"), "500", "400")
			files["pack.json"] = []byte(manifest)
			file, hash := writeRecordedFixture(t, files)
			root := t.TempDir()
			if err := ImportAudiobook(t.Context(), file, "", root, ""); err != nil {
				t.Fatal(err)
			}
			c, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			full := c.Books[0]
			if !strings.HasPrefix(full.ID, "recorded-") || math.Abs(full.Chapters[0].Seconds-1) > 0.005 {
				t.Fatal("used declared chapter length", full)
			}
			playlist, err := os.ReadFile(filepath.Join(bookDir(root, full), full.Chapters[0].Audio))
			if err != nil || !strings.Contains(string(playlist), "duration 0.500000") {
				t.Fatal("declared concat timing", string(playlist), err)
			}
			stream, err := startStream(t.Context(), t.TempDir(), filepath.Join(bookDir(root, full), full.Chapters[0].Audio), 0.75, 10)
			if err != nil {
				t.Fatal(err)
			}
			var samples int64
			for window := range stream.windows {
				if window.err != nil {
					t.Fatal(window.err)
				}
				samples += window.samples
				os.Remove(window.path)
			}
			stream.close()
			if math.Abs(float64(samples)/sampleRate-0.25) > 0.03 {
				t.Fatal("resumed tail drifted", samples)
			}
			archive, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := ImportPreview(t.Context(), archive, hash, root); err != nil {
				t.Fatal(err)
			}
			c, err = Load(root)
			if err != nil || len(c.Books) != 2 || c.Books[0].Preview || !c.Books[1].Preview {
				t.Fatal("preview displaced full recording", c, err)
			}
			if err := ImportAudiobook(t.Context(), file, hash, root, ""); err != nil {
				t.Fatal("full retry", err)
			}
			// Even a deliberately colliding legacy preview ID cannot downgrade a full book.
			files["pack.json"] = []byte(strings.ReplaceAll(manifest, id, full.ID))
			bad, badHash := writeRecordedFixture(t, files)
			badData, _ := os.ReadFile(bad)
			if err := ImportPreview(t.Context(), badData, badHash, root); !errors.Is(err, ErrPreviewDowngrade) {
				t.Fatal("allowed preview downgrade", err)
			}
		})
	}
}
func TestFullImportSweepsAbandonedStagesWithoutFollowingLinks(t *testing.T) {
	data, _ := recordedArchive(t)
	files, err := previewFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	file, hash := writeRecordedFixture(t, files)
	root := t.TempDir()
	for _, name := range []string{".audiobook-killed", ".preview-killed"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".audiobook-link")); err != nil {
		t.Fatal(err)
	}
	if err := ImportAudiobook(t.Context(), file, hash, root, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".audiobook-killed", ".preview-killed"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("retained abandoned stage", name)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("followed staging symlink", err)
	}
}
func TestFullImportPreflightsCatalogBeforeDecode(t *testing.T) {
	data, _ := recordedArchive(t)
	files, err := previewFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	file, hash := writeRecordedFixture(t, files)
	root := t.TempDir()
	base := fixtureBook(t, root, 0.1)
	c := Catalog{Schema: 1}
	for i := 0; i < 128; i++ {
		b := base
		b.ID = fmt.Sprintf("existing-%d", i)
		c.Books = append(c.Books, b)
	}
	if err := writeJSON(filepath.Join(root, "catalog.json"), c); err != nil {
		t.Fatal(err)
	}
	if err := ImportAudiobook(t.Context(), file, hash, root, ""); err == nil {
		t.Fatal("accepted 129th book")
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".audiobook-*"))
	if len(matches) != 0 {
		t.Fatal("staged before catalog preflight")
	}
	// Within the book-count limit, reserve the actual JSON byte budget as well.
	c.Books = nil
	for i := 0; i < 128; i++ {
		book := base
		book.ID = fmt.Sprintf("existing-%d", i)
		book.Chapters = nil
		for j := 0; j < 700; j++ {
			book.Chapters = append(book.Chapters, Chapter{Title: strings.Repeat("x", 150), Audio: chapterName(j) + ".sln16", Seconds: 1})
		}
		next := Catalog{Schema: 1, Books: append(append([]Book(nil), c.Books...), book)}
		if _, err := encodeJSON(next); errors.Is(err, errJSONTooLarge) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		c = next
	}
	if _, err := encodeJSON(c); err != nil {
		t.Fatal("existing catalog must fit", err)
	}
	extra := base
	extra.ID = "new"
	extra.Storage = "opus"
	extra.Chapters = nil
	for i := 0; i < 999; i++ {
		extra.Chapters = append(extra.Chapters, Chapter{Title: strings.Repeat("y", 240), Audio: chapterName(i) + ".ffconcat", Seconds: 1})
	}
	if err := preflightRecordedCatalog(c, extra); !errors.Is(err, errJSONTooLarge) {
		t.Fatal("did not reserve JSON budget", err)
	}

}
func TestFullImportReportsMissingAudioTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := ImportAudiobook(t.Context(), "missing.cmmpack", "", t.TempDir(), "")
	if !errors.Is(err, mediaformat.ErrUnsupported) || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatal("misreported missing tools", err)
	}
}
