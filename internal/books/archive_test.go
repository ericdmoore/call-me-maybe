package books

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func previewArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var sums strings.Builder
	for name, data := range files {
		if name != "SHA256SUMS" {
			fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), name)
		}
	}
	if files["SHA256SUMS"] == nil {
		files["SHA256SUMS"] = []byte(sums.String())
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, data := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if z.Close() != nil {
		t.Fatal("zip close")
	}
	return out.Bytes()
}
func previewManifest(id string) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"name":"Example preview","kind":"audiobook","audiobook":{"work":{"language":"en"},"recording":{"duration_ms":500},"audio":{"codec":"opus","container":"ogg","sample_rate":16000,"channels":1},"chapters":[{"number":1,"title":"An excerpt","duration_ms":500,"segments":[{"file":"audio/01/0001.opus","start_ms":0,"duration_ms":500}]}]}}`, id))
}
func TestPreviewArchiveRejectsTamperingAndUnsafeEntries(t *testing.T) {
	for _, files := range []map[string][]byte{
		{"pack.json": []byte("{}"), "../audio.opus": []byte("x")},
		{"pack.json": []byte("{}"), "audio/a.opus": []byte("x"), "SHA256SUMS": []byte(strings.Repeat("0", 64) + "  pack.json")},
		{"pack.json": []byte("{}"), "/absolute.opus": []byte("x")},
	} {
		data := previewArchive(t, files)
		if _, err := previewFiles(data); err == nil {
			t.Fatal("accepted unsafe archive")
		}
	}
	if _, err := previewFiles([]byte("not zip")); err == nil {
		t.Fatal("accepted non archive")
	}
}
func TestImportPreviewPreservesLibraryAndIsIdempotent(t *testing.T) {
	requireAudio(t, "ffmpeg", "espeak-ng")
	root := t.TempDir()
	prior := fixtureBook(t, root, 0.1)
	path := filepath.Join(t.TempDir(), "segment.opus")
	if err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-ac", "1", "-ar", "16000", "-c:a", "libopus", path).Run(); err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data := previewArchive(t, map[string][]byte{"pack.json": previewManifest(strings.Repeat("preview-", 10)), "audio/01/0001.opus": audio})
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := ImportPreview(t.Context(), data, hash, root); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil || len(c.Books) != 2 || !ready(root, prior) || !ready(root, c.Books[1]) {
		t.Fatal("library lost readiness", err)
	}
	if c.Books[1].Origin != "store" || !c.Books[1].Preview || c.Books[1].Revision != hash {
		t.Fatal(c.Books[1])
	}
	if err := ImportPreview(t.Context(), data, hash, root); err != nil {
		t.Fatal("retry failed", err)
	}
	c, err = Load(root)
	if err != nil || len(c.Books) != 2 {
		t.Fatal("retry duplicated book")
	}
	before, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	if ImportPreview(t.Context(), data, strings.Repeat("0", 64), root) == nil {
		t.Fatal("accepted wrong artifact hash")
	}
	bad := previewArchive(t, map[string][]byte{"pack.json": previewManifest("bad"), "audio/01/0001.opus": []byte("not audio")})
	if ImportPreview(t.Context(), bad, fmt.Sprintf("%x", sha256.Sum256(bad)), root) == nil {
		t.Fatal("accepted invalid Opus")
	}
	after, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	if !bytes.Equal(before, after) || !ready(root, prior) {
		t.Fatal("failed import changed library")
	}
	// A lost catalog publication can reuse validated audio after restart.
	c.Books = c.Books[:1]
	if err := publishJSON(filepath.Join(root, "catalog.json"), c, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ImportPreview(t.Context(), data, hash, root); err != nil {
		t.Fatal("cannot recover prepared revision", err)
	}
	// Same-sized corruption must not be mistaken for an installed revision.
	c, _ = Load(root)
	chapter := filepath.Join(bookDir(root, c.Books[1]), c.Books[1].Chapters[0].Audio)
	st, _ := os.Stat(chapter)
	if err := os.WriteFile(chapter, make([]byte, st.Size()), 0644); err != nil {
		t.Fatal(err)
	}
	if ImportPreview(t.Context(), data, hash, root) == nil {
		t.Fatal("accepted corrupted cached PCM")
	}
}
func TestPreviewBoundsBeforeFilesystemChanges(t *testing.T) {
	for _, manifest := range [][]byte{[]byte("invalid"), []byte(`{"kind":"story"}`), bytes.ReplaceAll(previewManifest("preview"), []byte(`"duration_ms":500`), []byte(`"duration_ms":99999999`))} {
		data := previewArchive(t, map[string][]byte{"pack.json": manifest, "audio/01/0001.opus": []byte("audio")})
		root := filepath.Join(t.TempDir(), "absent")
		if ImportPreview(t.Context(), data, fmt.Sprintf("%x", sha256.Sum256(data)), root) == nil {
			t.Fatal("accepted invalid preview")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid manifest changed library")
		}
	}
	// Bound PCM even when an external decoder ignores the advertised duration.
	w := &previewPCM{dst: &bytes.Buffer{}, limit: 2}
	if _, err := w.Write([]byte{1, 2, 3}); err == nil {
		t.Fatal("unbounded PCM")
	}
}
func TestOperatorPreviewFixture(t *testing.T) {
	path := os.Getenv("CMM_PREVIEW_FIXTURE")
	if path == "" {
		t.Skip("optional operator rehearsal fixture")
	}
	requireAudio(t, "ffmpeg", "espeak-ng")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	files, err := previewFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(files["pack.json"], &manifest) != nil {
		t.Fatal("manifest")
	}
	if err := ImportPreview(t.Context(), data, fmt.Sprintf("%x", sha256.Sum256(data)), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewImportPreservesPreparedVoiceUntilMatchingBundleReady(t *testing.T) {
	requireAudio(t, "ffmpeg", "espeak-ng")
	root := t.TempDir()
	fixtureBook(t, root, 0.1)
	beforeCatalog, _ := Load(root)
	render := func(_ string, path string) error { return os.WriteFile(path, []byte{0, 0}, 0644) }
	if err := buildMenus(root, beforeCatalog, "Zoe (Premium)", render); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	beforeMenu, _ := os.ReadFile(filepath.Join(root, "menus.json"))
	path := filepath.Join(t.TempDir(), "segment.opus")
	if err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-ac", "1", "-ar", "16000", "-c:a", "libopus", path).Run(); err != nil {
		t.Fatal(err)
	}
	audio, _ := os.ReadFile(path)
	data := previewArchive(t, map[string][]byte{"pack.json": previewManifest("new-preview"), "audio/01/0001.opus": audio})
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := ImportPreview(t.Context(), data, hash, root); err != ErrMenuRefresh {
		t.Fatal("silently downgraded prepared voice", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "catalog.json"))
	afterMenu, _ := os.ReadFile(filepath.Join(root, "menus.json"))
	if !bytes.Equal(before, after) || !bytes.Equal(beforeMenu, afterMenu) {
		t.Fatal("failed preparation replaced active library")
	}
	var candidate Catalog
	if err := readJSON(filepath.Join(root, "preview-catalog.pending.json"), &candidate); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	if err := buildMenus(bundle, candidate, "Zoe (Premium)", render); err != nil {
		t.Fatal(err)
	}
	if err := ImportPreviewWithMenus(t.Context(), data, hash, root, bundle); err != nil {
		t.Fatal(err)
	}
	active, err := Load(root)
	if err != nil || len(active.Books) != 2 {
		t.Fatal("prepared import failed", err)
	}
	var current menuBundle
	if readJSON(filepath.Join(root, "menus.json"), &current) != nil || current.Voice != "Zoe (Premium)" {
		t.Fatal("voice changed")
	}
	if clips, err := loadMenus(root, active); err != nil || len(clips) == 0 {
		t.Fatal("menus are not ready", err)
	}
	if _, err := os.Stat(filepath.Join(root, "preview-catalog.pending.json")); !os.IsNotExist(err) {
		t.Fatal("stale pending catalog")
	}
}
