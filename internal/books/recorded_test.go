package books

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func recordedFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	requireAudio(t, "ffmpeg", "espeak-ng")
	file := filepath.Join(t.TempDir(), "sample.opus")
	if err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-ac", "1", "-ar", "16000", "-c:a", "libopus", file).Run(); err != nil {
		t.Fatal(err)
	}
	audio, _ := os.ReadFile(file)
	manifest := strings.ReplaceAll(string(previewManifest("recorded-book")), `"segments":[`, `"segments":[`)
	data := previewArchive(t, map[string][]byte{"pack.json": []byte(manifest), "audio/01/0001.opus": audio})
	return data, fmt.Sprintf("%x", sha256.Sum256(data))
}
func TestRecordedImportAndCompressedPlayback(t *testing.T) {
	data, hash := recordedFixture(t)
	file := filepath.Join(t.TempDir(), "book.cmmpack")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := ImportRecordedWithMenus(t.Context(), file, hash, root, ""); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil || len(catalog.Books) != 1 {
		t.Fatal(err)
	}
	b := catalog.Books[0]
	b, err = hydrateRecorded(root, b)
	if err != nil {
		t.Fatal(err)
	}
	if b.Preview || !ready(root, b) || len(b.Chapters[0].Segments) != 1 {
		t.Fatal("recording not ready", b)
	}
	if _, err := os.Stat(filepath.Join(bookDir(root, b), "chapter-001.sln16")); !os.IsNotExist(err) {
		t.Fatal("expanded recording to PCM")
	}
	if err := ImportRecordedWithMenus(t.Context(), file, hash, root, ""); err != nil {
		t.Fatal("idempotent import", err)
	}
	stream, err := startStream(t.Context(), t.TempDir(), filepath.Join(bookDir(root, b), b.Chapters[0].Audio), 0.1, 15)
	if err != nil {
		t.Fatal(err)
	}
	count := int64(0)
	for w := range stream.windows {
		if w.err != nil {
			t.Fatal(w.err)
		}
		count += w.samples
	}
	stream.close()
	if count < 3000 || count > 6000 {
		t.Fatal("compressed seek/speed output", count)
	}
	seg := b.Chapters[0].Segments[0]
	if err := os.Remove(filepath.Join(bookDir(root, b), seg.Audio)); err != nil {
		t.Fatal(err)
	}
	if ready(root, b) {
		t.Fatal("missing compressed segment remained playable")
	}
}
func TestRecordedImportRejectsCorruptionWithoutChangingCatalog(t *testing.T) {
	data, hash := recordedFixture(t)
	file := filepath.Join(t.TempDir(), "book.cmmpack")
	os.WriteFile(file, data, 0600)
	root := t.TempDir()
	if err := ImportRecordedWithMenus(t.Context(), file, strings.Repeat("0", 64), root, ""); err == nil {
		t.Fatal("accepted archive hash mismatch")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ImportRecordedWithMenus(ctx, file, hash, root, ""); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := Load(root); err == nil {
		t.Fatal("published cancelled import")
	}
	unsafe := previewArchive(t, map[string][]byte{"pack.json": previewManifest("x"), "../escape.opus": []byte("bad")})
	os.WriteFile(file, unsafe, 0600)
	if err := ImportRecordedWithMenus(t.Context(), file, fmt.Sprintf("%x", sha256.Sum256(unsafe)), root, ""); err == nil {
		t.Fatal("accepted unsafe ZIP path")
	}
}
func TestRecordedPreservesPreparedVoice(t *testing.T) {
	data, hash := recordedFixture(t)
	file := filepath.Join(t.TempDir(), "book.cmmpack")
	os.WriteFile(file, data, 0600)
	root := t.TempDir()
	fixtureBook(t, root, 0.1)
	before, _ := Load(root)
	render := func(_ string, path string) error { return os.WriteFile(path, []byte{0, 0}, 0644) }
	if err := buildMenus(root, before, "Zoe (Premium)", render); err != nil {
		t.Fatal(err)
	}
	if err := ImportRecordedWithMenus(t.Context(), file, hash, root, ""); err != ErrMenuRefresh {
		t.Fatal(err)
	}
	current, _ := Load(root)
	if digest(current) != digest(before) {
		t.Fatal("changed current catalog before menus ready")
	}
	var next Catalog
	if err := readJSON(filepath.Join(root, "preview-catalog.pending.json"), &next); err != nil {
		t.Fatal(err)
	}
	menus := t.TempDir()
	if err := buildMenus(menus, next, "Zoe (Premium)", render); err != nil {
		t.Fatal(err)
	}
	if err := ImportRecordedWithMenus(t.Context(), file, hash, root, menus); err != nil {
		t.Fatal(err)
	}
	installed, _ := Load(root)
	if len(installed.Books) != 2 {
		t.Fatal("lost existing library")
	}
}

func TestLocalRecordedImportAndInvalidMetadata(t *testing.T) {
	data, hash := recordedFixture(t)
	file := filepath.Join(t.TempDir(), "book.cmmpack")
	os.WriteFile(file, data, 0600)
	root := t.TempDir()
	if err := ImportLocalRecordedWithMenus(t.Context(), file, hash, root, ""); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil || catalog.Books[0].Origin != "cli" {
		t.Fatal("local recording mislabelled", err)
	}
	b := catalog.Books[0]
	b, err = hydrateRecorded(root, b)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(bookDir(root, b), b.Chapters[0].Audio), []byte("unsafe playlist"), 0644)
	if ready(root, b) {
		t.Fatal("accepted a changed chapter playlist")
	}
	if err := ImportRecordedWithMenus(t.Context(), file, hash, root, ""); err == nil {
		t.Fatal("silently reused damaged revision")
	}
	files, err := previewFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(files["pack.json"])
	for _, replacement := range [][2]string{{`"kind":"audiobook"`, `"kind":"story"`}, {`"sample_rate":16000`, `"sample_rate":8000`}, {`"number":1`, `"number":2`}, {`"start_ms":0`, `"start_ms":2`}, {`"duration_ms":500`, `"duration_ms":0`}, {`"codec":"opus"`, `"codec":"mp3"`}} {
		changed := previewArchive(t, map[string][]byte{"pack.json": []byte(strings.ReplaceAll(manifest, replacement[0], replacement[1])), "audio/01/0001.opus": files["audio/01/0001.opus"]})
		os.WriteFile(file, changed, 0600)
		if err := ImportRecordedWithMenus(t.Context(), file, fmt.Sprintf("%x", sha256.Sum256(changed)), t.TempDir(), ""); err == nil {
			t.Fatal("accepted invalid metadata", replacement)
		}
	}
}

func TestOperatorCompleteRecordingPlayback(t *testing.T) {
	root := os.Getenv("CMM_RECORDED_LIBRARY")
	if root == "" {
		t.Skip("optional full-recording operator rehearsal")
	}
	requireAudio(t, "ffmpeg")
	c, err := Load(root)
	if err != nil || len(c.Books) == 0 {
		t.Fatal(err)
	}
	b := c.Books[len(c.Books)-1]
	if !ready(root, b) {
		t.Fatal("full recording unavailable")
	}
	for _, chapter := range []int{0, len(b.Chapters) / 2, len(b.Chapters) - 1} {
		for _, speed := range []int{5, 10, 15, 20} {
			stream, err := startStream(t.Context(), t.TempDir(), filepath.Join(bookDir(root, b), b.Chapters[chapter].Audio), b.Chapters[chapter].Seconds/2, speed)
			if err != nil {
				t.Fatal(err)
			}
			first, ok := <-stream.windows
			stream.close()
			if !ok || first.err != nil || first.samples <= 0 || first.samples > 30*sampleRate {
				t.Fatalf("chapter %d speed %d: %v", chapter+1, speed, first.err)
			}
		}
	}
}

func TestRecordedDurationToleranceAndCatalogScale(t *testing.T) {
	ch := Chapter{Audio: "chapter-001.ffconcat", Seconds: 45.0004, SegmentCount: 1, Segments: []Segment{{Audio: "audio/01/0001.opus", Bytes: 100, SHA256: strings.Repeat("a", 64), Seconds: 45.0004}}}
	if !validChapterAudio(ch, 0) {
		t.Fatal("rejected valid decoder padding")
	}
	ch.Seconds = 45.121
	ch.Segments[0].Seconds = ch.Seconds
	if validChapterAudio(ch, 0) {
		t.Fatal("accepted excess padding")
	}
	catalog := Catalog{Schema: 1}
	for i := range 128 {
		b := Book{ID: fmt.Sprintf("book-%d", i), Title: "A complete recording", Language: "en", Origin: "store", Revision: strings.Repeat("a", 64), IndexSHA256: strings.Repeat("b", 64)}
		for j := range 61 {
			b.Chapters = append(b.Chapters, Chapter{Title: fmt.Sprintf("Chapter %d", j+1), Audio: chapterName(j) + ".ffconcat", Seconds: 720, SegmentCount: 24})
		}
		catalog.Books = append(catalog.Books, b)
	}
	if err := validate(catalog, true); err != nil {
		t.Fatal(err)
	}
	data, err := encodeJSON(catalog)
	if err != nil {
		t.Fatal("128 full books overflow catalog", err)
	}
	if strings.Contains(string(data), `"segments"`) {
		t.Fatal("embedded segment details in shared catalog")
	}
}

func TestMediaImporterUsesManifestForSmallCompleteBook(t *testing.T) {
	data, hash := recordedFixture(t)
	// This complete-book fixture is far below 1 MiB; it must keep full-book
	// identity, storage and preview=false regardless of compressed byte count.
	file := filepath.Join(t.TempDir(), "small.cmmpack")
	os.WriteFile(file, data, 0600)
	root := t.TempDir()
	if err := ImportMediaWithMenus(t.Context(), file, hash, root, ""); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil || c.Books[0].Preview || c.Books[0].IndexSHA256 == "" {
		t.Fatal("selected excerpt importer", err)
	}
	b := c.Books[0]
	os.WriteFile(filepath.Join(bookDir(root, b), "recording.json"), []byte(`{"chapters":[]}`), 0644)
	if ready(root, b) {
		t.Fatal("changed segment index remained playable")
	}
}

func TestMediaImporterExcerptAndMalformedManifest(t *testing.T) {
	data, _ := recordedFixture(t)
	files, err := previewFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.Replace(string(files["pack.json"]), `"audiobook":{`, `"audiobook":{"provenance":{"excerpt":{"source_pack_id":"source"}},`, 1)
	excerpt := previewArchive(t, map[string][]byte{"pack.json": []byte(manifest), "audio/01/0001.opus": files["audio/01/0001.opus"]})
	file := filepath.Join(t.TempDir(), "excerpt.cmmpack")
	os.WriteFile(file, excerpt, 0600)
	root := t.TempDir()
	if err := ImportMediaWithMenus(t.Context(), file, fmt.Sprintf("%x", sha256.Sum256(excerpt)), root, ""); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil || !c.Books[0].Preview || c.Books[0].IndexSHA256 != "" {
		t.Fatal("lost excerpt identity", err)
	}
	for _, broken := range [][]byte{[]byte("not a zip"), previewArchive(t, map[string][]byte{"other": []byte("missing manifest")}), previewArchive(t, map[string][]byte{"pack.json": []byte("invalid json")})} {
		os.WriteFile(file, broken, 0600)
		if err := ImportMediaWithMenus(t.Context(), file, fmt.Sprintf("%x", sha256.Sum256(broken)), t.TempDir(), ""); err == nil {
			t.Fatal("accepted malformed manifest")
		}
	}
	os.Remove(file)
	if err := ImportMediaWithMenus(t.Context(), file, "", t.TempDir(), ""); err == nil {
		t.Fatal("accepted missing archive")
	}
	os.WriteFile(file, data, 0600)
	root = t.TempDir()
	if err := ImportLocalRecordedWithMenus(t.Context(), file, "", root, ""); err != nil {
		t.Fatal("local importer did not derive revision", err)
	}
	c, err = Load(root)
	if err != nil || c.Books[0].Revision != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatal("wrong local revision", err)
	}
}
