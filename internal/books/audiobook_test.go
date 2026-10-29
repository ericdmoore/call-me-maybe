package books

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func recordedArchive(t *testing.T) ([]byte, string) {
	t.Helper()
	requireAudio(t, "ffmpeg", "espeak-ng")
	file := filepath.Join(t.TempDir(), "tone.opus")
	if err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-ac", "1", "-ar", "16000", "-c:a", "libopus", file).Run(); err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.Replace(string(previewManifest("recorded-example")), `"duration_ms":500`, `"duration_ms":1000`, 2)
	manifest = strings.Replace(manifest, `"segments":[{"file":"audio/01/0001.opus","start_ms":0,"duration_ms":500}]`, `"segments":[{"file":"audio/01/0001.opus","start_ms":0,"duration_ms":500},{"file":"audio/01/0002.opus","start_ms":500,"duration_ms":500}]`, 1)
	data := previewArchive(t, map[string][]byte{"pack.json": []byte(manifest), "audio/01/0001.opus": audio, "audio/01/0002.opus": audio})
	return data, fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestRecordedImportKeepsCompressedAudioAndBoundedPlayback(t *testing.T) {
	data, hash := recordedArchive(t)
	root := t.TempDir()
	prior := fixtureBook(t, root, 0.1)
	archive := filepath.Join(t.TempDir(), "book.cmmpack")
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ImportAudiobook(t.Context(), archive, hash, root, ""); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil || len(c.Books) != 2 {
		t.Fatal(c, err)
	}
	b := c.Books[1]
	if b.Storage != "opus" || b.Preview || !ready(root, b) || !ready(root, prior) {
		t.Fatal("compressed book not ready", b)
	}
	dir := bookDir(root, b)
	if _, err := os.Stat(filepath.Join(dir, "chapter-001.sln16")); !os.IsNotExist(err) {
		t.Fatal("novel expanded to PCM")
	}
	stream, err := startStream(t.Context(), t.TempDir(), filepath.Join(dir, b.Chapters[0].Audio), 0.25, 15)
	if err != nil {
		t.Fatal(err)
	}
	var samples int64
	for w := range stream.windows {
		if w.err != nil {
			t.Fatal(w.err)
		}
		samples += w.samples
		os.Remove(w.path)
	}
	stream.close()
	if math.Abs(float64(samples)/sampleRate-0.5) > 0.12 {
		t.Fatalf("compressed seek/speed duration: %.3f", float64(samples)/sampleRate)
	}
	if err := ImportAudiobook(t.Context(), archive, hash, root, ""); err != nil {
		t.Fatal("retry", err)
	}
	c, _ = Load(root)
	if len(c.Books) != 2 {
		t.Fatal("retry duplicated book")
	}
	before, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	if ImportAudiobook(t.Context(), archive, strings.Repeat("0", 64), root, "") == nil {
		t.Fatal("accepted changed archive")
	}
	after, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed import changed library")
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-000000.opus"), []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	if ImportAudiobook(t.Context(), archive, hash, root, "") == nil {
		t.Fatal("reused corrupt immutable recording")
	}
}

func TestRecordedArchiveGuardsBeforePublication(t *testing.T) {
	for _, files := range []map[string][]byte{
		{"pack.json": []byte("{}"), "audio/a.opus": []byte("x")},
		{"pack.json": previewManifest("test"), "../escape": []byte("x")},
		{"pack.json": previewManifest("test"), "audio/01/0001.opus": []byte("not ogg")},
		{"pack.json": previewManifest("test"), "audio/01/0001.opus": []byte("x"), "SHA256SUMS": []byte(strings.Repeat("0", 64) + "  pack.json")},
		{"pack.json": bytes.ReplaceAll(previewManifest("test"), []byte(`"start_ms":0`), []byte(`"start_ms":200`)), "audio/01/0001.opus": []byte("x")},
		{"pack.json": previewManifest("test"), "audio/01/0001.opus": []byte("x"), "unused": []byte("x")},
	} {
		data := previewArchive(t, files)
		archive := filepath.Join(t.TempDir(), "bad.cmmpack")
		os.WriteFile(archive, data, 0600)
		root := filepath.Join(t.TempDir(), "library")
		if ImportAudiobook(t.Context(), archive, fmt.Sprintf("%x", sha256.Sum256(data)), root, "") == nil {
			t.Fatal("accepted invalid archive")
		}
		if _, err := os.Stat(filepath.Join(root, "catalog.json")); !os.IsNotExist(err) {
			t.Fatal("invalid input published")
		}
	}
	root := t.TempDir()
	if ImportAudiobook(t.Context(), "absent", "bad-hash", root, "") == nil {
		t.Fatal("accepted invalid hash")
	}
	if ImportAudiobook(t.Context(), "absent", strings.Repeat("0", 64), root, "") == nil {
		t.Fatal("accepted absent archive")
	}
	file := filepath.Join(t.TempDir(), "large")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(MaxAudiobookBytes + 1)
	f.Close()
	if ImportAudiobook(t.Context(), file, strings.Repeat("0", 64), root, "") == nil {
		t.Fatal("accepted oversized archive")
	}
	os.WriteFile(file, []byte("not a zip"), 0600)
	if ImportAudiobook(t.Context(), file, fmt.Sprintf("%x", sha256.Sum256([]byte("not a zip"))), root, "") == nil {
		t.Fatal("accepted non archive")
	}
}

func TestCompressedReadinessRejectsUnsafePlaylists(t *testing.T) {
	dir := t.TempDir()
	ch := Chapter{Audio: "chapter-001.ffconcat", Seconds: 0.5}
	for _, text := range []string{"", "ffconcat version 1.0\nfile ../private\nduration 0.5\n", "ffconcat version 1.0\nfile segment-000000.opus\nduration NaN\n", "ffconcat version 1.0\nfile segment-000000.opus\nduration 99\n", "ffconcat version 1.0\nfile segment-000000.opus\nduration 0.5\n"} {
		os.WriteFile(filepath.Join(dir, ch.Audio), []byte(text), 0644)
		if compressedChapterReady(dir, ch) {
			t.Fatal("accepted unsafe/incomplete playlist")
		}
	}
	os.WriteFile(filepath.Join(dir, "segment-000000.opus"), []byte("placeholder"), 0644)
	if !compressedChapterReady(dir, ch) {
		t.Fatal("expected generated playlist shape")
	}
	os.Remove(filepath.Join(dir, "segment-000000.opus"))
	os.Symlink("/etc/hosts", filepath.Join(dir, "segment-000000.opus"))
	if compressedChapterReady(dir, ch) {
		t.Fatal("accepted symlink")
	}
}

func TestOperatorFullAudiobookFixture(t *testing.T) {
	archive := os.Getenv("CMM_AUDIOBOOK_FIXTURE")
	if archive == "" {
		t.Skip("optional private operator fixture")
	}
	requireAudio(t, "ffmpeg", "espeak-ng")
	root := t.TempDir()
	if err := ImportAudiobook(t.Context(), archive, "c84bc545175390404ddd72981cb009c7448f0683167738a2850029a124bf0611", root, ""); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil || len(c.Books) != 1 || len(c.Books[0].Chapters) != 61 || !ready(root, c.Books[0]) {
		t.Fatal("full recording not ready", err)
	}
	var bytes int64
	filepath.Walk(bookDir(root, c.Books[0]), func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
		}
		return nil
	})
	if bytes > 140<<20 {
		t.Fatalf("unexpected expanded storage: %d", bytes)
	}
	t.Logf("61 chapters installed as compressed audio; stored bytes=%d", bytes)
}

func TestZipMetadataIsBoundedBeforeReaderAllocation(t *testing.T) {
	data := previewArchive(t, map[string][]byte{"pack.json": []byte("{}"), "audio/1.opus": []byte("x")})
	if !boundedZipDirectory(bytes.NewReader(data), int64(len(data))) {
		t.Fatal("ordinary pack directory refused")
	}
	for _, mode := range []string{"count", "size", "header", "length", "trailing", "short"} {
		t.Run(mode, func(t *testing.T) {
			changed := append([]byte(nil), data...)
			end := len(changed) - 22
			switch mode {
			case "count":
				binary.LittleEndian.PutUint16(changed[end+10:end+12], 4097)
			case "size":
				binary.LittleEndian.PutUint32(changed[end+12:end+16], 0xffffffff)
			case "header":
				offset := binary.LittleEndian.Uint32(changed[end+16 : end+20])
				changed[offset] = 0
			case "length":
				offset := binary.LittleEndian.Uint32(changed[end+16 : end+20])
				binary.LittleEndian.PutUint16(changed[offset+28:offset+30], 65535)
			case "trailing":
				changed = append(changed, 0)
			case "short":
				changed = []byte("not zip")
			}
			if boundedZipDirectory(bytes.NewReader(changed), int64(len(changed))) {
				t.Fatal("accepted unbounded/inconsistent central directory")
			}
		})
	}
}

func TestFullBookRequiresMatchingPreparedVoiceBeforeActivation(t *testing.T) {
	data, hash := recordedArchive(t)
	root := t.TempDir()
	prior := fixtureBook(t, root, 0.1)
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	render := func(_ string, file string) error { return os.WriteFile(file, []byte{0, 0}, 0644) }
	if err := buildMenus(root, catalog, "Zoe (Premium)", render); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	voice, _ := os.ReadFile(filepath.Join(root, "menus.json"))
	archive := filepath.Join(t.TempDir(), "full.cmmpack")
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ImportAudiobook(t.Context(), archive, hash, root, ""); err != ErrMenuRefresh {
		t.Fatal("activated without prepared voice", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	nextVoice, _ := os.ReadFile(filepath.Join(root, "menus.json"))
	if !bytes.Equal(before, after) || !bytes.Equal(voice, nextVoice) || !ready(root, prior) {
		t.Fatal("changed active library/voice before preparation")
	}
	var candidate Catalog
	if err := readJSON(filepath.Join(root, "preview-catalog.pending.json"), &candidate); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	if err := buildMenus(bundle, candidate, "Zoe (Premium)", render); err != nil {
		t.Fatal(err)
	}
	if err := ImportAudiobook(t.Context(), archive, hash, root, bundle); err != nil {
		t.Fatal("matching voice not activated", err)
	}
	var active menuBundle
	if err := readJSON(filepath.Join(root, "menus.json"), &active); err != nil || active.Voice != "Zoe (Premium)" {
		t.Fatal("voice changed", err)
	}
}
