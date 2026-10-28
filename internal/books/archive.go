package books

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var archiveID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

var ErrMenuRefresh = errors.New("books: matching prepared menu bundle required; render preview-catalog.pending.json before activation")

var ErrPreviewArchive = errors.New("books: invalid or unsupported preview archive")

// ImportPreview is deliberately bounded to a two-minute preview. Full books
// need compressed playback storage; do not expand a full novel into PCM here.
// The ordinary handset player remains account-free and never checks ownership.
func ImportPreview(ctx context.Context, data []byte, hash, root string) error {
	return ImportPreviewWithMenus(ctx, data, hash, root, "")
}

func ImportPreviewWithMenus(ctx context.Context, data []byte, hash, root, menuSource string) error {
	sum := sha256.Sum256(data)
	if len(data) == 0 || len(data) > 1<<20 || hex.EncodeToString(sum[:]) != hash {
		return ErrPreviewArchive
	}
	files, err := previewFiles(data)
	if err != nil {
		return err
	}
	var m recordedManifest
	if json.Unmarshal(files["pack.json"], &m) != nil || m.Kind != "audiobook" || !archiveID.MatchString(m.ID) || !validText(m.Name) || !idPattern.MatchString(m.Audiobook.Work.Language) || m.Audiobook.Audio.Codec != "opus" || m.Audiobook.Audio.Container != "ogg" || m.Audiobook.Audio.SampleRate != 16000 || m.Audiobook.Audio.Channels != 1 || len(m.Audiobook.Chapters) == 0 || len(m.Audiobook.Chapters) > 8 || m.Audiobook.Recording.DurationMS <= 0 || m.Audiobook.Recording.DurationMS > 120000 {
		return ErrPreviewArchive
	}
	seen := map[string]bool{}
	total := int64(0)
	for i, ch := range m.Audiobook.Chapters {
		if ch.Number != i+1 || !validText(ch.Title) || ch.DurationMS <= 0 || len(ch.Segments) == 0 || len(ch.Segments) > 16 {
			return ErrPreviewArchive
		}
		duration := int64(0)
		for _, seg := range ch.Segments {
			if seen[seg.File] || files[seg.File] == nil || !strings.HasPrefix(seg.File, "audio/") || !strings.HasSuffix(seg.File, ".opus") || seg.StartMS != duration || seg.DurationMS <= 0 || seg.DurationMS > 45000 {
				return ErrPreviewArchive
			}
			seen[seg.File] = true
			duration += seg.DurationMS
		}
		if duration != ch.DurationMS {
			return ErrPreviewArchive
		}
		total += duration
	}
	if total != m.Audiobook.Recording.DurationMS {
		return ErrPreviewArchive
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	unlock, err := lockLibrary(root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := cleanImportStages(root); err != nil {
		return err
	}
	catalog, err := Load(root)
	if os.IsNotExist(err) {
		catalog = Catalog{Schema: 1}
	} else if err != nil {
		return err
	}
	bookID := m.ID
	if len(bookID) > 64 {
		identity := sha256.Sum256([]byte(bookID))
		bookID = "preview-" + hex.EncodeToString(identity[:16])
	}
	b := Book{ID: bookID, Title: m.Name, Language: m.Audiobook.Work.Language, Origin: "store", Preview: true, Revision: hash}
	for i, ch := range m.Audiobook.Chapters {
		b.Chapters = append(b.Chapters, Chapter{Title: ch.Title, Audio: chapterName(i) + ".sln16", Seconds: float64(ch.DurationMS) / 1000})
	}
	stage, err := os.MkdirTemp(root, ".preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for i, ch := range m.Audiobook.Chapters {
		output, err := os.OpenFile(filepath.Join(stage, chapterName(i)+".sln16"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		count := int64(0)
		for _, seg := range ch.Segments {
			job, cancel := context.WithTimeout(ctx, 30*time.Second)
			cmd := exec.CommandContext(job, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "pipe", "-i", "pipe:0", "-map", "0:a:0", "-vn", "-t", "46", "-ar", "16000", "-ac", "1", "-f", "s16le", "pipe:1")
			cmd.Stdin = bytes.NewReader(files[seg.File])
			bounded := &previewPCM{dst: output, limit: 46 * sampleRate * 2}
			cmd.Stdout = bounded
			err = cmd.Run()
			cancel()
			ms := bounded.n * 1000 / (sampleRate * 2)
			if err != nil || bounded.n%2 != 0 || ms < seg.DurationMS-120 || ms > seg.DurationMS+120 {
				output.Close()
				return ErrPreviewArchive
			}
			count += bounded.n
		}
		syncErr := output.Sync()
		closeErr := output.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		b.Chapters[i].Seconds = float64(count) / (sampleRate * 2)
		if err := speak(ctx, filepath.Join(stage, chapterName(i)+"-title.sln16"), ch.Title); err != nil {
			return err
		}
	}
	if err := speak(ctx, filepath.Join(stage, "title.sln16"), b.Title+". Preview recording."); err != nil {
		return err
	}
	if err := os.Chmod(stage, 0755); err != nil {
		return err
	}
	return publishImported(ctx, root, b, stage, menuSource, catalog)
}

func publishImported(ctx context.Context, root string, b Book, stage, menuSource string, catalog Catalog) error {
	next, err := importedCatalog(catalog, b)
	if err != nil {
		return err
	}
	catalog = next
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		file, err := os.Open(filepath.Join(stage, entry.Name()))
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	directory, err := os.Open(stage)
	if err != nil {
		return err
	}
	err = directory.Sync()
	directory.Close()
	if err != nil {
		return err
	}
	target := bookDir(root, b)
	if info, statErr := os.Lstat(target); os.IsNotExist(statErr) {
		if err := os.Rename(stage, target); err != nil {
			return err
		}
	} else if statErr != nil || !info.IsDir() {
		return errRecordingRepair
	} else {
		// A prior attempt may have published audio but lost its receipt/index.
		// Reuse only identical validated bytes, never a same-sized corrupt cache.
		entries, err := os.ReadDir(stage)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			expected, err := os.ReadFile(filepath.Join(stage, entry.Name()))
			if err != nil {
				return err
			}
			info, err := os.Lstat(filepath.Join(target, entry.Name()))
			if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(expected)) {
				return errRecordingRepair
			}
			actual, err := os.ReadFile(filepath.Join(target, entry.Name()))
			if err != nil || !bytes.Equal(actual, expected) {
				return errRecordingRepair
			}
		}
	}
	parent, err := os.Open(root)
	if err != nil {
		return err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return err
	}
	if err := validate(catalog, true); err != nil {
		return err
	}
	// A catalog change must never silently downgrade a prepared voice bundle.
	// Export the candidate for offline rendering without activating it.
	previous, _ := Load(root)
	if menuSource == "" && digest(previous) != digest(catalog) {
		if _, err := os.Lstat(filepath.Join(root, "menus.json")); err == nil {
			if err := publishJSON(filepath.Join(root, "preview-catalog.pending.json"), catalog, 0644); err != nil {
				return err
			}
			return ErrMenuRefresh
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	var replacement *menuBundle
	if menuSource != "" {
		bundle, err := copyPreviewMenus(menuSource, root, catalog)
		if err != nil {
			return err
		}
		replacement = &bundle
	}
	// Common prompts are prepared before publishing the catalog. Existing calls
	// pin their old catalog/revision and can continue through this atomic rename.
	promptRoot := filepath.Join(root, "prompts-v1")
	if _, err := os.Stat(promptRoot); os.IsNotExist(err) {
		dir, err := os.MkdirTemp(root, ".prompts-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		for name, words := range prompts {
			if err := speak(ctx, filepath.Join(dir, name+".sln16"), words); err != nil {
				return err
			}
		}
		if err := os.Chmod(dir, 0755); err != nil {
			return err
		}
		if err := os.Rename(dir, promptRoot); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	oldMenus, oldErr := os.ReadFile(filepath.Join(root, "menus.json"))
	if replacement != nil {
		if err := publishJSON(filepath.Join(root, "menus.json"), replacement, 0644); err != nil {
			return err
		}
	}
	if err := publishJSON(filepath.Join(root, "catalog.json"), catalog, 0644); err != nil {
		if replacement != nil {
			if oldErr == nil {
				_ = os.WriteFile(filepath.Join(root, "menus.json"), oldMenus, 0644)
			} else {
				_ = os.Remove(filepath.Join(root, "menus.json"))
			}
		}
		return err
	}
	_ = os.Remove(filepath.Join(root, "preview-catalog.pending.json"))

	return retireStaleMenus(root)
}

type previewPCM struct {
	dst      io.Writer
	n, limit int64
}

func (w *previewPCM) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.n {
		return 0, ErrPreviewArchive
	}
	n, err := w.dst.Write(p)
	w.n += int64(n)
	return n, err
}

func previewFiles(data []byte) (map[string][]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrPreviewArchive
	}
	members, err := packMembers(z, 128, 1<<20, 1<<20, 2<<20)
	if err != nil {
		return nil, ErrPreviewArchive
	}
	sums, err := packChecksums(members)
	if err != nil {
		return nil, ErrPreviewArchive
	}
	files := map[string][]byte{}
	for name := range members {
		if name == "SHA256SUMS" {
			continue
		}
		data, err := verifiedPackMember(members, sums, name)
		if err != nil {
			return nil, ErrPreviewArchive
		}
		files[name] = data
	}
	return files, nil
}

// All library writers share this lock; readers pin immutable revisions and
// need no lock. A second import never overwrites a concurrent catalog update.
func lockLibrary(root string) (func(), error) {
	lock, err := os.OpenFile(filepath.Join(root, ".import.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, errors.New("books: library import busy")
	}
	return func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }, nil
}

// A prepared bundle is trusted operator input, checked against the exact next
// catalog. Copy immutable clips before publishing either index; never run TTS
// on the appliance or require a cloud synthesizer to receive a purchase.
func copyPreviewMenus(source, root string, c Catalog) (menuBundle, error) {
	var bundle menuBundle
	if _, err := loadMenus(source, c); err != nil {
		return bundle, ErrMenuRefresh
	}
	if err := readJSON(filepath.Join(source, "menus.json"), &bundle); err != nil {
		return bundle, ErrMenuRefresh
	}
	if err := os.MkdirAll(filepath.Join(root, "menu-audio"), 0755); err != nil {
		return bundle, err
	}
	total := int64(0)
	for _, hash := range bundle.Clips {
		name := hash + ".sln16"
		from := filepath.Join(source, "menu-audio", name)
		info, err := os.Lstat(from)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return bundle, ErrMenuRefresh
		}
		total += info.Size()
		if total > 64<<20 {
			return bundle, ErrMenuRefresh
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return bundle, err
		}
		target := filepath.Join(root, "menu-audio", name)
		if info, err := os.Lstat(target); err == nil {
			if !info.Mode().IsRegular() {
				return bundle, ErrMenuRefresh
			}
			old, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(old, data) {
				return bundle, ErrMenuRefresh
			}
			continue
		} else if !os.IsNotExist(err) {
			return bundle, err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return bundle, err
		}
	}
	return bundle, nil
}
