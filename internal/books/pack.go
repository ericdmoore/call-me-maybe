package books

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"callmemaybe/internal/mediaformat"
)

var errPackMembers = errors.New("books: invalid archive members or checksums")
var errRecordingRepair = errors.New("books: existing recording revision requires repair")
var ErrPreviewDowngrade = errors.New("books: a preview cannot replace a full recording")

// CheckAudiobookTools checks local import dependencies without reading a purchase.
func CheckAudiobookTools() error {
	for _, tool := range []string{"ffmpeg", "espeak-ng"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("books: audiobook import requires ffmpeg and espeak-ng: %w", mediaformat.ErrUnsupported)
		}
	}
	return nil
}

func packMembers(z *zip.Reader, maxFiles int, memberMax, manifestMax, totalMax uint64) (map[string]*zip.File, error) {
	if len(z.File) < 3 || len(z.File) > maxFiles {
		return nil, errPackMembers
	}
	files := map[string]*zip.File{}
	var total uint64
	for _, f := range z.File {
		name := f.Name
		limit := memberMax
		if name == "pack.json" {
			limit = manifestMax
		}
		if name == "" || name == "." || name == ".." || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00\r\n") || !f.Mode().IsRegular() || files[name] != nil || f.UncompressedSize64 > limit {
			return nil, errPackMembers
		}
		total += f.UncompressedSize64
		if total > totalMax {
			return nil, errPackMembers
		}
		files[name] = f
	}
	if files["pack.json"] == nil || files["SHA256SUMS"] == nil {
		return nil, errPackMembers
	}
	return files, nil
}
func readPackMember(files map[string]*zip.File, name string) ([]byte, error) {
	f := files[name]
	if f == nil {
		return nil, errPackMembers
	}
	r, err := f.Open()
	if err != nil {
		return nil, errPackMembers
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, int64(f.UncompressedSize64)+1))
	if err != nil || uint64(len(data)) != f.UncompressedSize64 {
		return nil, errPackMembers
	}
	return data, nil
}
func packChecksums(files map[string]*zip.File) (map[string]string, error) {
	data, err := readPackMember(files, "SHA256SUMS")
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		hash, name, ok := strings.Cut(line, "  ")
		if !ok || !revisionPattern.MatchString(hash) || files[name] == nil || name == "SHA256SUMS" || sums[name] != "" {
			return nil, errPackMembers
		}
		sums[name] = hash
	}
	if len(sums) != len(files)-1 {
		return nil, errPackMembers
	}
	return sums, nil
}
func verifiedPackMember(files map[string]*zip.File, sums map[string]string, name string) ([]byte, error) {
	data, err := readPackMember(files, name)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != sums[name] {
		return nil, errPackMembers
	}
	return data, nil
}

// Only the shared writer lock permits sweeping abandoned private import stages.
func cleanImportStages(root string) error {
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, readErr := dir.ReadDir(64)
		for _, entry := range entries {
			if entry.IsDir() && (strings.HasPrefix(entry.Name(), ".audiobook-") || strings.HasPrefix(entry.Name(), ".preview-")) {
				if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
func importedCatalog(c Catalog, b Book) (Catalog, error) {
	c.Books = append([]Book(nil), c.Books...)
	for i, old := range c.Books {
		if old.ID == b.ID {
			if b.Preview && !old.Preview {
				return Catalog{}, ErrPreviewDowngrade
			}
			c.Books[i] = b
			return c, nil
		}
	}
	c.Books = append(c.Books, b)
	return c, nil
}

func preflightRecordedCatalog(c Catalog, b Book) error {
	candidate, err := importedCatalog(c, b)
	if err != nil {
		return err
	}
	if err := validate(candidate, true); err != nil {
		return err
	}
	// Bound the final encoding even if decoded durations use more digits.
	sized := b
	sized.Chapters = append([]Chapter(nil), b.Chapters...)
	for i := range sized.Chapters {
		sized.Chapters[i].Seconds = math.MaxFloat64
	}
	candidate, err = importedCatalog(c, sized)
	if err != nil {
		return err
	}
	_, err = encodeJSON(candidate)
	return err
}
